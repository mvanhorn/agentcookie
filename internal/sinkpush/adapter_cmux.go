package sinkpush

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// cmuxAppCLI is the canonical cmux CLI path inside a macOS cmux.app
// install. A standalone install may drop `cmux` on PATH, which NewCmux
// probes first (mirrors internal/tsclient.FindCLI).
const cmuxAppCLI = "/Applications/cmux.app/Contents/Resources/bin/cmux"

// chromeEpochOffsetSec is the gap between Chrome's 1601-01-01 epoch and
// the Unix 1970-01-01 epoch, in seconds. Chrome stores cookie expiry as
// microseconds since 1601; cmux's RPC wants Unix seconds.
const chromeEpochOffsetSec = 11644473600

// surfaceRefRE extracts the `surface:N` ref from `cmux new-surface`
// output, e.g. "OK surface:29 pane:7 workspace:5".
var surfaceRefRE = regexp.MustCompile(`surface:\d+`)

// CmuxAdapter delivers synced cookies into cmux's embedded WebKit
// browser via the cmux control socket, so an agent driving cmux's
// browser pane wakes up authenticated. It is the fourth delivery
// surface (alongside Chrome SQLite, the sidecar, and the per-CLI
// adapters) and fires after every sync through sinkpush.RunAll.
//
// Unlike the per-CLI adapters, this surface is opt-in: the sink only
// registers it when sink.yaml sets `cmux.enabled: true`. It is wired in
// internal/cli/sink.go at sink startup rather than in init.go, because
// it carries config (binary path, host filter) that init() cannot see.
//
// Injection surface (why a hidden background tab, not a workspace):
// browser.cookies.set requires a browser surface_id -- there is no
// profile-level set call. But WebKit cookies persist at the
// WKWebsiteDataStore (profile) level, shared across every browser
// surface and surviving pane close (confirmed empirically: a set on one
// surface is visible to cookies.get on any other). So the surface only
// has to exist; it never has to be seen. The adapter adds ONE unfocused
// (`--focus false`) about:blank browser tab to an existing workspace's
// active pane and caches it. A non-selected browser tab is a hidden
// webview -- cookies.set still lands on it -- so the only UI artifact is
// a small tab chip; nothing opens in the user's view and nothing appears
// in the workspace sidebar. An earlier design parked the surface in a
// dedicated "agentcookie" workspace, which showed in the sidebar and
// (because cmux short refs renumber across restarts) could alias a live
// user workspace and pop the pane into focus; the hidden-tab approach
// removes the workspace entirely.
//
// WebKit cookie semantics (confirmed empirically, differ from Chrome's
// CDP path):
//   - Cookie scoping is prefix/host-aware (see cmuxCookieParam). __Host--
//     prefixed and host-only cookies are scoped by url with NO Domain;
//     domain cookies (leading-dot host_key) keep Domain verbatim
//     (".example.com" is accepted; the leading dot is not stripped). A
//     __Host- cookie carrying ANY Domain attribute is hard-rejected by
//     WebKit, which silently dropped GitHub's __Host-user_session_same_site
//     on this lane until the url/no-Domain shaping was added.
//   - expires is Unix seconds; omit it for session cookies. WebKit
//     clamps far-future expiries to its ~400-day max (expected).
//   - Cookie values are passed through VERBATIM. The App-Bound (Chrome
//     127+) 32-byte prefix is already stripped once on the source side
//     (internal/chrome/read.go); a second strip silently dropped 64% of
//     cookies in the v0.12.0-beta.3 dry-run. Never strip again here.
type CmuxAdapter struct {
	binary       string
	domainFilter []string

	mu        sync.Mutex
	surfaceID string // cached hidden background tab ref, added lazily, reused

	// run executes a cmux subcommand and returns stdout. Swappable in
	// tests; defaults to execCmux.
	run func(args ...string) (string, error)
}

// NewCmux returns a cmux delivery adapter. cmuxPath overrides the binary
// location; empty resolves the canonical app path with a PATH fallback.
// domainFilter is the set of SQLite-LIKE host_key patterns to deliver
// (nil/empty = the full synced set).
func NewCmux(cmuxPath string, domainFilter []string) *CmuxAdapter {
	a := &CmuxAdapter{binary: ResolveCmuxBinary(cmuxPath), domainFilter: domainFilter}
	a.run = a.execCmux
	return a
}

// ResolveCmuxBinary returns the cmux CLI path to use: the explicit
// cmuxPath when set, otherwise a `cmux` on PATH, otherwise the canonical
// app-bundle location. Shared by the adapter and the doctor check.
func ResolveCmuxBinary(cmuxPath string) string {
	if cmuxPath != "" {
		return cmuxPath
	}
	if p, err := exec.LookPath("cmux"); err == nil {
		return p
	}
	return cmuxAppCLI
}

func (a *CmuxAdapter) Name() string { return "cmux" }

func (a *CmuxAdapter) CLIBinary() string { return a.binary }

func (a *CmuxAdapter) IsInstalled() bool {
	info, err := os.Stat(a.binary)
	return err == nil && !info.IsDir()
}

func (a *CmuxAdapter) CookieHostPatterns() []string { return a.domainFilter }

// Push injects each cookie into cmux's WebKit browser via
// `cmux rpc browser.cookies.set`. It ensures a cached hidden background
// tab exists (adding one unfocused about:blank tab on first use), and
// re-adds once if the cached surface has gone away. Errors are returned
// for RunAll to log; a missing or cmuxOnly-gated cmux is a soft failure
// that never aborts the sync or the other surfaces.
func (a *CmuxAdapter) Push(cookies []chrome.Cookie) error {
	if len(cookies) == 0 {
		return nil
	}

	// Drop RFC-invalid cookies up front (the same gate RunAll applies before
	// an adapter's Push). cmux-sync calls Push directly, bypassing RunAll, so
	// without this a malformed cookie would fail a whole batch.
	valid := make([]chrome.Cookie, 0, len(cookies))
	for _, c := range cookies {
		if Validate(c) == nil {
			valid = append(valid, c)
		}
	}
	cookies = valid
	if len(cookies) == 0 {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, err := a.ensureSurfaceLocked(); err != nil {
		return fmt.Errorf("cmux: open browser surface: %w", err)
	}

	// Inject in batches: browser.cookies.set accepts a cookies array, so a
	// full Chrome cookie set lands in a handful of RPC calls instead of one
	// per cookie. Chunked because the JSON params ride a CLI arg bounded by
	// ARG_MAX (~1MB); cmuxCookieBatch keeps each call well under that.
	reopened := false
	for start := 0; start < len(cookies); start += cmuxCookieBatch {
		end := min(start+cmuxCookieBatch, len(cookies))
		chunk := cookies[start:end]
		err := a.setCookiesLocked(chunk)
		if err == nil {
			continue
		}
		// The cached surface may have been closed between syncs. Reopen
		// once and retry this chunk before giving up.
		if !reopened && isSurfaceError(err) {
			reopened = true
			a.surfaceID = ""
			if _, oerr := a.ensureSurfaceLocked(); oerr != nil {
				return fmt.Errorf("cmux: reopen surface after %v: %w", err, oerr)
			}
			if rerr := a.setCookiesLocked(chunk); rerr == nil {
				continue
			} else {
				err = rerr
			}
		}
		// Hard-fail (do not silently drop the chunk) when cmux is unreachable
		// (down / cmuxOnly-gated) OR the error is still surface-related after
		// the one reopen attempt -- a reopen that didn't fix it won't be fixed
		// per-cookie either, and silently skipping would lose the whole chunk.
		if isCmuxUnavailable(err) || isSurfaceError(err) {
			return fmt.Errorf("cmux: set cookies [%d:%d] of %d: %w", start, end, len(cookies), err)
		}
		// Otherwise one cookie in the chunk is rejected by cmux (e.g. a
		// payload WebKit won't accept). Fall back to per-cookie so the rest
		// of the chunk still lands; skip only the genuine individual rejects.
		// A cmux-unavailable or surface error mid-fallback still hard-fails.
		for _, c := range chunk {
			if e := a.setCookiesLocked([]chrome.Cookie{c}); e != nil {
				if isCmuxUnavailable(e) || isSurfaceError(e) {
					return fmt.Errorf("cmux: set cookie %q: %w", c.Name, e)
				}
				// individual payload reject: skip it, keep going
			}
		}
	}
	return nil
}

// ensureSurfaceLocked returns the cached injection surface ref, adding an
// unfocused about:blank browser tab to an existing workspace if none is
// cached. The tab is hidden (a non-selected webview) -- cookies.set lands
// on it without it ever being shown, and it adds no sidebar entry.
// Caller must hold a.mu.
func (a *CmuxAdapter) ensureSurfaceLocked() (string, error) {
	if a.surfaceID != "" {
		return a.surfaceID, nil
	}
	ws, err := a.hostWorkspaceLocked()
	if err != nil {
		return "", fmt.Errorf("host workspace: %w", err)
	}
	// --focus false adds the about:blank as a non-selected background tab,
	// so the user's selected surface stays put and nothing pops into view.
	// cmux picks the workspace's active pane when --pane is omitted.
	out, err := a.run("new-surface", "--type", "browser", "--workspace", ws, "--url", "about:blank", "--focus", "false")
	if err != nil {
		return "", err
	}
	sid := surfaceRefRE.FindString(out)
	if sid == "" {
		return "", fmt.Errorf("could not parse surface ref from %q", strings.TrimSpace(out))
	}
	a.surfaceID = sid
	return sid, nil
}

// hostWorkspaceLocked returns a workspace to host the hidden injection
// tab: the first workspace cmux reports. The cmux-sync LaunchAgent runs
// outside cmux ($CMUX_WORKSPACE_ID unset), so there is no caller
// workspace to default to; the first listed one is a stable, already-open
// home that costs no new workspace and no sidebar entry. Resolved fresh
// every time a surface must be (re)added -- never cached -- because short
// refs renumber across cmux restarts and this process outlives cmux.
// Caller must hold a.mu.
func (a *CmuxAdapter) hostWorkspaceLocked() (string, error) {
	out, err := a.run("workspace", "list", "--json")
	if err != nil {
		return "", err
	}
	ref := firstWorkspaceRef(out)
	if ref == "" {
		return "", fmt.Errorf("no cmux workspace to host the injection tab in %q", strings.TrimSpace(out))
	}
	return ref, nil
}

// firstWorkspaceRef returns the ref of the first workspace in `workspace
// list --json` output, or "" if there are none / the JSON is unparseable.
func firstWorkspaceRef(listJSON string) string {
	var parsed struct {
		Workspaces []struct {
			Ref string `json:"ref"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal([]byte(listJSON), &parsed); err != nil {
		return ""
	}
	for _, w := range parsed.Workspaces {
		if w.Ref != "" {
			return w.Ref
		}
	}
	return ""
}

// cmuxCookieBatch caps how many cookies ride one browser.cookies.set
// call. The params travel as a CLI arg bounded by ARG_MAX (~1MB on
// macOS); at a few hundred bytes per cookie, 200 keeps each call well
// under that with comfortable headroom.
const cmuxCookieBatch = 200

// setCookiesLocked sends a batch of cookies to the cached surface in one
// browser.cookies.set call. Caller must hold a.mu and have a non-empty
// a.surfaceID.
func (a *CmuxAdapter) setCookiesLocked(cookies []chrome.Cookie) error {
	payload, err := cmuxCookiesJSON(a.surfaceID, cookies)
	if err != nil {
		return fmt.Errorf("marshal %d cookies: %w", len(cookies), err)
	}
	_, err = a.run("rpc", "browser.cookies.set", payload)
	return err
}

// cmuxCookiesJSON builds browser.cookies.set params for a batch of
// cookies: {"surface_id":..., "cookies":[...]}.
func cmuxCookiesJSON(surfaceID string, cookies []chrome.Cookie) (string, error) {
	params := make([]map[string]any, 0, len(cookies))
	for _, c := range cookies {
		params = append(params, cmuxCookieParam(c))
	}
	m := map[string]any{
		"surface_id": surfaceID,
		"cookies":    params,
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// cmuxCookieParam builds one cookie's params. Value and domain pass
// through verbatim (see the CmuxAdapter doc on why no App-Bound re-strip).
// expires is omitted for session cookies (ExpiresUTC == 0).
//
// Cookie scoping mirrors the corrected shaping in internal/livecdp.buildParam,
// translated to cmux's browser.cookies.set fields (a "url" the handler maps to
// HTTPCookie's originURL, plus an optional "domain"):
//
//   - __Host--prefixed cookies carry NO Domain. WebKit hard-rejects a __Host-
//     cookie that has a Domain attribute, which silently dropped GitHub's
//     __Host-user_session_same_site on this lane. We send url, force Secure
//     and Path "/", and omit Domain (host-only via the url's host).
//   - Domain cookies (host_key with a leading dot) keep Domain verbatim
//     (WebKit accepts the leading dot) plus url.
//   - Host-only cookies (host_key without a leading dot) carry no Domain --
//     scoped to the exact host via url. Setting Domain would widen them to
//     all subdomains.
//
// url is required for the __Host- and host-only cases: with no Domain,
// cmux's HTTPCookie(properties:) has nothing to scope the cookie to unless
// originURL is set (the cached about:blank surface has no host to fall back
// to).
func cmuxCookieParam(c chrome.Cookie) map[string]any {
	path := c.Path
	if path == "" {
		path = "/"
	}
	secure := c.IsSecure == 1
	m := map[string]any{
		"name":      c.Name,
		"value":     c.Value, // verbatim -- already App-Bound-stripped on the source
		"path":      path,
		"http_only": c.IsHTTPOnly == 1,
	}
	if u := cmuxCookieURL(c); u != "" {
		m["url"] = u
	}
	switch {
	case strings.HasPrefix(c.Name, "__Host-"):
		// __Host- invariants: Secure, Path "/", host-only (no Domain).
		secure = true
		m["path"] = "/"
	case strings.HasPrefix(c.HostKey, "."):
		// Domain cookie: valid for subdomains. WebKit accepts the leading dot.
		m["domain"] = c.HostKey
	default:
		// Host-only cookie: scoped to the exact host via url, no Domain.
	}
	m["secure"] = secure
	if ss := cmuxSameSite(c.SameSite); ss != "" {
		// SameSite=None requires Secure -- a None cookie that isn't Secure is
		// rejected by spec. Downgrade an insecure None to Lax so it survives
		// rather than vanishing (mirrors internal/livecdp.buildParam and the
		// CDP lane). Computed after the switch so a __Host- cookie's forced
		// Secure keeps None.
		if ss == "none" && !secure {
			ss = "lax"
		}
		m["same_site"] = ss
	}
	if c.ExpiresUTC != 0 {
		m["expires"] = chromeMicrosToUnixSec(c.ExpiresUTC)
	}
	return m
}

// cmuxCookieURL synthesizes a request-URI for a cookie from its host_key,
// scheme, and path so cmux's WebKit cookie store scopes the cookie to the
// right host -- and so __Host-/host-only cookies have an origin even with no
// Domain attribute. The leading dot of a domain host_key is stripped for the
// URL hostname.
func cmuxCookieURL(c chrome.Cookie) string {
	host := strings.TrimPrefix(c.HostKey, ".")
	if host == "" {
		return ""
	}
	scheme := "https"
	if c.IsSecure == 0 {
		scheme = "http"
	}
	path := c.Path
	if path == "" {
		path = "/"
	}
	return scheme + "://" + host + path
}

// cmuxSameSite maps Chrome's numeric SameSite (cookies.samesite) to the
// string cmux expects. -1 (unspecified) returns "" so the field is
// omitted and WebKit applies its default.
func cmuxSameSite(s int) string {
	switch s {
	case 0:
		return "none"
	case 1:
		return "lax"
	case 2:
		return "strict"
	default:
		return ""
	}
}

// chromeMicrosToUnixSec converts Chrome's microseconds-since-1601 expiry
// to Unix seconds.
func chromeMicrosToUnixSec(chromeMicrosSince1601 int64) int64 {
	return chromeMicrosSince1601/1_000_000 - chromeEpochOffsetSec
}

// isSurfaceError reports whether err looks like a stale/invalid surface
// (e.g. the user closed the cached pane), which warrants one reopen.
func isSurfaceError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Match only stale/invalid-surface signatures. Deliberately NOT
	// "invalid_params" (cmux returns "invalid_params: Invalid cookie
	// payload" for a bad cookie, which is not a surface problem) and NOT
	// "not found" (Go's exec.LookPath error contains it).
	return strings.Contains(msg, "surface") ||
		strings.Contains(msg, "not a browser")
}

// isCmuxUnavailable reports whether err means cmux itself can't be
// reached (down, or socketControlMode gating a non-cmux-child caller) --
// a hard failure for the whole push, as opposed to a single rejected
// cookie which can be skipped.
func isCmuxUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "access denied") ||
		strings.Contains(msg, "permission denied") || // EACCES (unexecutable cmux mid-run)
		strings.Contains(msg, "only processes started inside cmux") ||
		strings.Contains(msg, "cmuxonly")
}

// execCmux runs a cmux subcommand, returning stdout. A non-zero exit
// folds stderr (or stdout, when cmux writes the error there) into the
// returned error so callers can match on the message.
func (a *CmuxAdapter) execCmux(args ...string) (string, error) {
	cmd := exec.Command(a.binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return stdout.String(), fmt.Errorf("%w (%s)", err, detail)
	}
	return stdout.String(), nil
}
