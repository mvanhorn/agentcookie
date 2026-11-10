// Package broker owns the single direct BiDi session in a separate process.
// The daemon may crash or exit without closing this process's WebSocket.
package broker

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/gecko/bidi"
	"github.com/mvanhorn/agentcookie/internal/gecko/companion"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var ErrLostSession = errors.New("storage reads paused: BiDi slot may be held by previous broker; use storage_read_mode: request or restart browser")

type Options struct {
	Profile       string            `json:"profile"`
	Endpoint      string            `json:"endpoint"`
	StateDir      string            `json:"state_dir"`
	CompanionPort int               `json:"companion_port"`
	Executable    string            `json:"-"`
	ExtraAssets   map[string]string `json:"-"`
	AssetsPath    string            `json:"-"`
}
type State struct {
	PID        int              `json:"pid"`
	BrowserPID int              `json:"browser_pid"`
	Profile    string           `json:"profile"`
	Endpoint   string           `json:"endpoint"`
	Socket     string           `json:"socket"`
	TokenHash  string           `json:"token_hash"`
	Config     companion.Config `json:"-"`
	Installed  bool             `json:"installed"`
}
type Client struct {
	http  *http.Client
	state State
	cfg   companion.Config
}

func (o Options) paths() (string, string, string) {
	key := companion.ProfileKey(o.Profile)
	dir := filepath.Join(o.StateDir, "gecko-broker")
	return filepath.Join(dir, key+".json"), filepath.Join(dir, key+".sock"), filepath.Join(o.StateDir, "companion", key)
}
func tokenHash(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}
func connect(o Options) (*Client, error) {
	statePath, _, extension := o.paths()
	b, e := os.ReadFile(statePath)
	if e != nil {
		return nil, e
	}
	var s State
	if e = json.Unmarshal(b, &s); e != nil {
		return nil, e
	}
	if s.Profile != o.Profile || s.Endpoint != o.Endpoint || s.PID <= 0 {
		return nil, fmt.Errorf("broker identity mismatch")
	}
	if e = syscall.Kill(s.PID, 0); e != nil {
		return nil, ErrLostSession
	}
	b, e = os.ReadFile(filepath.Join(extension, "config.json"))
	if e != nil {
		return nil, e
	}
	var c companion.Config
	if e = json.Unmarshal(b, &c); e != nil {
		return nil, e
	}
	if tokenHash(c.Token) != s.TokenHash {
		return nil, fmt.Errorf("broker token mismatch")
	}
	_, expectedSocket, _ := o.paths()
	if s.Socket != expectedSocket {
		return nil, fmt.Errorf("broker socket mismatch")
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", s.Socket)
	}}, Timeout: 35 * time.Second}
	client := &Client{http: hc, state: s, cfg: c}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e = client.request(ctx, "status", nil, &s); e != nil {
		return nil, e
	}
	client.state = s
	return client, nil
}

// Ensure reconnects before attempting to create a process. A missing/dead broker
// never justifies ending a foreign or unreconnectable browser session.
func Ensure(ctx context.Context, o Options) (*Client, error) {
	if c, e := connect(o); e == nil {
		return c, nil
	}
	statePath, _, _ := o.paths()
	if e := os.MkdirAll(filepath.Dir(statePath), 0700); e != nil {
		return nil, e
	}
	if o.Executable == "" {
		var e error
		o.Executable, e = os.Executable()
		if e != nil {
			return nil, e
		}
	}
	if len(o.ExtraAssets) > 0 {
		o.AssetsPath = statePath + ".assets.json"
		raw, e := json.Marshal(o.ExtraAssets)
		if e != nil {
			return nil, e
		}
		if e = os.WriteFile(o.AssetsPath, raw, 0600); e != nil {
			return nil, e
		}
	}
	cmd := exec.Command(o.Executable, "gecko-broker", "--profile", o.Profile, "--endpoint", o.Endpoint, "--state-dir", o.StateDir, "--companion-port", fmt.Sprint(o.CompanionPort), "--assets-file", o.AssetsPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	log, e := os.OpenFile(statePath+".log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Start(); e != nil {
		return nil, e
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case e := <-done:
			return nil, fmt.Errorf("%w (%v); inspect broker diagnostics", ErrLostSession, e)
		case <-ticker.C:
			if c, e := connect(o); e == nil {
				return c, nil
			}
		}
	}
}
func (c *Client) Config() companion.Config { return c.cfg }
func (c *Client) Installed() bool          { return c.state.Installed }
func (c *Client) request(ctx context.Context, method string, params, out any) error {
	r, w := io.Pipe()
	go func() { e := json.NewEncoder(w).Encode(params); w.CloseWithError(e) }()
	req, e := http.NewRequestWithContext(ctx, "POST", "http://localhost/"+method, r)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	res, e := c.http.Do(req)
	if e != nil {
		r.Close()
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("broker %s failed (%d)", method, res.StatusCode)
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(res.Body, 32<<20))
		return e
	}
	return json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(out)
}
func (c *Client) Install(ctx context.Context) error {
	if e := c.request(ctx, "install", nil, nil); e != nil {
		return e
	}
	c.state.Installed = true
	return nil
}
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	return c.request(ctx, "call", map[string]any{"method": method, "params": params}, out)
}
func (c *Client) End(ctx context.Context) error { return c.request(ctx, "end", nil, nil) }
func (c *Client) Close() error                  { c.http.CloseIdleConnections(); return nil }

// Run is entered only by the gecko-broker subcommand. It never signals the browser.
func Run(ctx context.Context, o Options) error {
	statePath, socketPath, extension := o.paths()
	if e := os.MkdirAll(filepath.Dir(statePath), 0700); e != nil {
		return e
	}
	// flock prevents duplicate startup races independently of daemon lifetimes.
	lock, e := os.OpenFile(statePath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return fmt.Errorf("broker already starting/running")
	}
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, e := bidi.Dial(connectCtx, o.Endpoint)
	if e != nil {
		return e
	}
	defer b.Close()
	if e = b.NewSession(connectCtx, o.Profile); e != nil {
		return e
	}
	defer func() {
		end, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = b.End(end)
	}()
	if o.AssetsPath != "" {
		raw, e := os.ReadFile(o.AssetsPath)
		if e != nil {
			return e
		}
		if len(raw) > 4<<20 {
			return fmt.Errorf("companion assets exceed cap")
		}
		if e = json.Unmarshal(raw, &o.ExtraAssets); e != nil {
			return e
		}
	}
	cfg, e := companion.Provision(extension, o.CompanionPort, companion.ProfileKey(o.Profile), true, o.ExtraAssets)
	if e != nil {
		return e
	}
	os.Remove(socketPath)
	l, e := net.Listen("unix", socketPath)
	if e != nil {
		return e
	}
	defer l.Close()
	defer os.Remove(socketPath)
	if e = os.Chmod(socketPath, 0600); e != nil {
		return e
	}
	state := State{PID: os.Getpid(), BrowserPID: b.BrowserPID, Profile: o.Profile, Endpoint: o.Endpoint, Socket: socketPath, TokenHash: tokenHash(cfg.Token)}
	save := func() error {
		raw, _ := json.Marshal(state)
		temp := statePath + ".tmp"
		if e := os.WriteFile(temp, raw, 0600); e != nil {
			return e
		}
		return os.Rename(temp, statePath)
	}
	if e = save(); e != nil {
		return e
	}
	defer os.Remove(statePath)
	table := newInterceptTable(b)
	if e = b.Call(connectCtx, "session.subscribe", map[string]any{"events": []string{"network.beforeRequestSent"}}, nil); e != nil {
		return e
	}
	var mu sync.Mutex
	stop := make(chan struct{})
	var stopOnce sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+cfg.Token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
		w.Header().Set("Content-Type", "application/json")
		var result any = map[string]any{}
		var err error
		switch r.URL.Path {
		case "/status":
			mu.Lock()
			result = state
			mu.Unlock()
		case "/install", "/reinstall":
			mu.Lock()
			if r.URL.Path == "/reinstall" {
				state.Installed = false
			}
			if !state.Installed {
				err = b.Install(r.Context(), extension)
				if err == nil {
					state.Installed = true
					err = save()
				}
			}
			mu.Unlock()
		case "/read-intercept", "/remove-read-intercept":
			var call struct {
				Origin string `json:"origin"`
				Nonce  string `json:"nonce"`
			}
			err = json.NewDecoder(r.Body).Decode(&call)
			if err == nil {
				if r.URL.Path == "/read-intercept" {
					err = table.add(r.Context(), call.Origin, call.Nonce)
				} else {
					err = table.remove(r.Context(), call.Nonce)
				}
			}
		case "/remove-companion":
			// Removing our own temporary extension deliberately leaves Installed true:
			// user removal is not an instruction to reinstall in the same browser session.
			err = b.Call(r.Context(), "webExtension.uninstall", map[string]string{"extension": companion.ExtensionID}, nil)
		case "/call":
			var call struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err = json.NewDecoder(r.Body).Decode(&call); err == nil {
				allowed := map[string]bool{"session.status": true, "session.subscribe": true, "network.addIntercept": true, "network.removeIntercept": true, "network.provideResponse": true, "network.failRequest": true, "browsingContext.close": true}
				if !allowed[call.Method] {
					err = fmt.Errorf("method not permitted")
				} else {
					var raw json.RawMessage
					err = b.Call(r.Context(), call.Method, call.Params, &raw)
					result = raw
				}
			}
		case "/end":
			err = b.End(r.Context())
			if err == nil {
				stopOnce.Do(func() { close(stop) })
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "broker operation failed", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 35 * time.Second, MaxHeaderBytes: 8192}
	defer srv.Close()
	go func() { _ = srv.Serve(l) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stop:
			return nil
		case <-b.Done():
			return nil
		case <-ticker.C:
			if state.BrowserPID > 0 && syscall.Kill(state.BrowserPID, 0) != nil {
				return nil
			}
		case event := <-b.Events():
			table.handle(event)
		}
	}
}

func (c *Client) Reinstall(ctx context.Context) error { return c.request(ctx, "reinstall", nil, nil) }

// Connect reconnects to an existing authenticated broker without starting one.
// It is useful for diagnostics and deterministic fixture cleanup.
func Connect(o Options) (*Client, error) { return connect(o) }

func (c *Client) Check(ctx context.Context) error {
	var current State
	if e := c.request(ctx, "status", nil, &current); e != nil {
		return e
	}
	if current.PID != c.state.PID || current.Profile != c.state.Profile || current.TokenHash != c.state.TokenHash {
		return fmt.Errorf("broker identity changed")
	}
	return nil
}

// RemoveCompanion removes only agentcookie's own temporary extension. The broker
// records that this browser session was installed and will not reinstall it.
func (c *Client) RemoveCompanion(ctx context.Context) error {
	return c.request(ctx, "remove-companion", nil, nil)
}
