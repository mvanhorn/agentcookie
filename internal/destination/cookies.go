package destination

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

// ChromeApplier connects at the browser level without creating a visible page.
// The DevTools browser UUID is a process incarnation, never a reusable port.
type ChromeApplier struct {
	mu          sync.Mutex
	Endpoint    string
	browser     *chromedp.Browser
	cancel      context.CancelFunc
	incarnation string
	cache       map[string]map[string]*network.Cookie
}

func NewChromeApplier(endpoint string) *ChromeApplier {
	return &ChromeApplier{Endpoint: strings.TrimSuffix(endpoint, "/")}
}
func (a *ChromeApplier) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
	a.browser = nil
}
func (a *ChromeApplier) connect(ctx context.Context) (*chromedp.Browser, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.browser != nil {
		select {
		case <-a.browser.LostConnection:
			a.cancel()
			a.browser = nil
		default:
			return a.browser, a.incarnation, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Endpoint+"/json/version", nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("CDP discovery status %d", resp.StatusCode)
	}
	var v struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&v); err != nil {
		return nil, "", err
	}
	endpointURL, _ := url.Parse(a.Endpoint)
	socketURL, parseErr := url.Parse(v.URL)
	if parseErr != nil || socketURL.Scheme != "ws" || socketURL.User != nil || socketURL.Host != endpointURL.Host || !strings.HasPrefix(socketURL.Path, "/devtools/browser/") {
		return nil, "", fmt.Errorf("CDP discovery returned a different endpoint")
	}
	if v.URL == "" {
		return nil, "", fmt.Errorf("CDP browser URL missing")
	}
	// Connection lifetime is explicit, not the HTTP /sync request lifetime.
	life, cancel := context.WithCancel(context.Background())
	b, err := chromedp.NewBrowser(life, v.URL)
	if err != nil {
		cancel()
		return nil, "", err
	}
	a.browser = b
	a.cancel = cancel
	a.incarnation = digest([]byte(v.URL))
	return b, a.incarnation, nil
}
func (a *ChromeApplier) Contexts(ctx context.Context) ([]Context, error) {
	b, inc, err := a.connect(ctx)
	if err != nil {
		return nil, err
	}
	ids, _, err := target.GetBrowserContexts().Do(cdp.WithExecutor(ctx, b))
	if err != nil {
		return nil, err
	}
	out := []Context{{ID: "default", Incarnation: inc, Default: true}}
	for _, id := range ids {
		out = append(out, Context{ID: string(id), Incarnation: inc})
	}
	return out, nil
}
func (a *ChromeApplier) executor(ctx context.Context, c Context) (context.Context, error) {
	b, inc, err := a.connect(ctx)
	if err != nil {
		return nil, err
	}
	if c.Incarnation != inc {
		return nil, fmt.Errorf("chrome process changed during apply")
	}
	return cdp.WithExecutor(ctx, b), nil
}
func cookieItem(c chrome.Cookie) (Item, error) {
	p := livecdp.BuildCookieParams([]chrome.Cookie{c})
	if len(p) != 1 {
		return Item{}, fmt.Errorf("cookie cannot be represented or is expired")
	}
	param := p[0]
	if c.TopFrameSiteKey != "" {
		param.PartitionKey = &network.CookiePartitionKey{TopLevelSite: c.TopFrameSiteKey, HasCrossSiteAncestor: c.HasCrossSiteAncestor != 0}
	}
	host := c.HostKey
	if strings.HasPrefix(c.Name, "__Host-") {
		host = strings.TrimPrefix(host, ".")
	}
	key, _ := json.Marshal([]any{param.Name, param.Path, param.PartitionKey})
	value, _ := json.Marshal(param)
	i := NewItem("cookies", host, string(key), value)
	expires := float64(-1)
	if param.Expires != nil {
		expires = float64(param.Expires.Time().Unix())
	}
	i.Hash = cookieHash(param.Value, param.Secure, param.HTTPOnly, param.SameSite, expires, param.Expires == nil)
	return i, nil
}
func cookieHash(value string, secure, httpOnly bool, sameSite network.CookieSameSite, expires float64, session bool) string {
	if session {
		expires = -1
	}
	b, _ := json.Marshal([]any{value, secure, httpOnly, string(sameSite), int64(expires), session})
	return Hash(b)
}

func cookieParam(i Item) (*network.CookieParam, error) {
	if i.Surface != "cookies" {
		return nil, fmt.Errorf("unsupported surface %q", i.Surface)
	}
	var p network.CookieParam
	if err := json.Unmarshal(i.Value, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
func (a *ChromeApplier) Read(ctx context.Context, c Context, i Item) (string, bool, error) {
	p, err := cookieParam(i)
	if err != nil {
		return "", false, err
	}
	a.mu.Lock()
	cookies, cached := a.cache[c.Key()]
	a.mu.Unlock()
	if !cached || cookies == nil {
		cookies, err = a.loadCookies(ctx, c)
		if err != nil {
			return "", false, err
		}
		if cached {
			a.mu.Lock()
			a.cache[c.Key()] = cookies
			a.mu.Unlock()
		}
	}
	if v, ok := cookies[cookieLookupKey(i.Scope, p.Name, p.Path, p.PartitionKey)]; ok {
		return cookieHash(v.Value, v.Secure, v.HTTPOnly, v.SameSite, v.Expires, v.Session), true, nil
	}

	return "", false, nil
}
func (a *ChromeApplier) Set(ctx context.Context, c Context, i Item) error {
	p, err := cookieParam(i)
	if err != nil {
		return err
	}
	exec, err := a.executor(ctx, c)
	if err != nil {
		return err
	}
	set := storage.SetCookies([]*network.CookieParam{p})
	if !c.Default {
		set = set.WithBrowserContextID(cdp.BrowserContextID(c.ID))
	}
	defer a.invalidate(c)
	return set.Do(exec)
}
func (a *ChromeApplier) Delete(ctx context.Context, c Context, i Item) error {
	defer a.invalidate(c)
	p, err := cookieParam(i)
	if err != nil {
		return err
	}
	exec, err := a.executor(ctx, c)
	if err != nil {
		return err
	}
	// Network.deleteCookies with no partition key can remove partitioned siblings.
	// Expiring the exact unpartitioned key is unambiguous and context-addressable.
	if p.PartitionKey == nil {
		expired := cdp.TimeSinceEpoch(time.Unix(1, 0))
		p.Expires = &expired
		set := storage.SetCookies([]*network.CookieParam{p})
		if !c.Default {
			set = set.WithBrowserContextID(cdp.BrowserContextID(c.ID))
		}
		return set.Do(exec)
	}
	create := target.CreateTarget("about:blank").WithHidden(true)
	if !c.Default {
		create = create.WithBrowserContextID(cdp.BrowserContextID(c.ID))
	}
	id, err := create.Do(exec)
	if err != nil {
		return err
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		b, _, e := a.connect(clean)
		if e == nil {
			_ = target.CloseTarget(id).Do(cdp.WithExecutor(clean, b))
		}
	}()
	alloc, ac := chromedp.NewRemoteAllocator(ctx, a.Endpoint)
	defer ac()
	tab, tc := chromedp.NewContext(alloc, chromedp.WithTargetID(id))
	defer tc()
	return chromedp.Run(tab, network.DeleteCookies(p.Name).WithDomain(i.Scope).WithPath(p.Path).WithPartitionKey(p.PartitionKey))
}

// CookieSnapshot converts wire completeness metadata to the reconciliation IR.
// Cookie-only v1/v2 envelopes are complete snapshots as in the old sender.
func CookieSnapshot(env protocol.SyncEnvelope) (Snapshot, int, error) {
	s := Snapshot{Generation: uint64(env.Sequence), Surfaces: map[string]string{"cookies": "complete"}, Scopes: map[string]string{}, Sites: map[string]string{}}
	if env.Snapshot != nil {
		s.Surfaces = map[string]string{}
		s.Generation = env.Snapshot.Generation
		s.AuthoritativeEmpty = env.Snapshot.AuthoritativeEmpty
		for surface, status := range env.Snapshot.Surfaces {
			s.Surfaces[string(surface)] = status.State
		}
		for site, status := range env.Snapshot.Sites {
			s.Sites[site] = status.State
		}
	}
	dropped := 0
	ordered := slices.Clone(env.Cookies)
	slices.SortStableFunc(ordered, func(a, b chrome.Cookie) int { return cmp.Compare(a.LastAccessUTC, b.LastAccessUTC) })
	for _, c := range ordered {
		i, err := cookieItem(c)
		if err != nil {
			dropped++
			continue
		}
		s.Items = append(s.Items, i)
	}
	if len(s.Items) > livecdp.MaxInjectCookies {
		dropped += len(s.Items) - livecdp.MaxInjectCookies
		s.Items = s.Items[len(s.Items)-livecdp.MaxInjectCookies:]
	}
	s.CapExcluded = dropped
	return s, dropped, nil
}

func (a *ChromeApplier) loadCookies(ctx context.Context, c Context) (map[string]*network.Cookie, error) {
	exec, err := a.executor(ctx, c)
	if err != nil {
		return nil, err
	}
	get := storage.GetCookies()
	if !c.Default {
		get = get.WithBrowserContextID(cdp.BrowserContextID(c.ID))
	}
	cookies, err := get.Do(exec)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*network.Cookie, len(cookies))
	for _, v := range cookies {
		out[cookieLookupKey(v.Domain, v.Name, v.Path, v.PartitionKey)] = v
	}
	return out, nil
}
func cookieLookupKey(domain, name, path string, p *network.CookiePartitionKey) string {
	b, _ := json.Marshal([]any{domain, name, path, p})
	return string(b)
}
func (a *ChromeApplier) BeginReconcile(ctx context.Context, c Context) error {
	cookies, err := a.loadCookies(ctx, c)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache == nil {
		a.cache = map[string]map[string]*network.Cookie{}
	}
	a.cache[c.Key()] = cookies
	return nil
}
func (a *ChromeApplier) EndReconcile(c Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.cache, c.Key())
}
func (a *ChromeApplier) invalidate(c Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.cache[c.Key()]; ok {
		a.cache[c.Key()] = nil
	}
}
func (a *ChromeApplier) BatchKey(i Item) string {
	if i.Surface == "cookies" {
		return "cookies"
	}
	return ""
}
func (a *ChromeApplier) ApplyBatch(ctx context.Context, c Context, items []Item) error {
	params := make([]*network.CookieParam, 0, len(items))
	for _, i := range items {
		p, err := cookieParam(i)
		if err != nil {
			return err
		}
		if i.Deleted {
			expired := cdp.TimeSinceEpoch(time.Unix(1, 0))
			p.Expires = &expired
		}
		params = append(params, p)
	}
	exec, err := a.executor(ctx, c)
	if err != nil {
		return err
	}
	set := storage.SetCookies(params)
	if !c.Default {
		set = set.WithBrowserContextID(cdp.BrowserContextID(c.ID))
	}
	err = set.Do(exec)
	a.invalidate(c)
	return err
}
