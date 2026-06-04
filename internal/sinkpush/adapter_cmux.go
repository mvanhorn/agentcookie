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

// surfaceRefRE extracts the `surface:N` ref from `cmux browser open`
// output, e.g. "OK surface=surface:45 pane=pane:32 placement=split".
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
// WebKit cookie semantics (confirmed empirically, differ from Chrome's
// CDP path):
//   - Domain is stored VERBATIM. ".example.com" is accepted, so unlike
//     the CDP injector we do NOT strip the leading dot from host_key.
//   - browser.cookies.set REQUIRES a browser surface_id; there is no
//     profile-level set, and browser surfaces are not listed by
//     surface.list. So the adapter opens its own about:blank surface
//     (unfocused) and caches it for reuse. Injected cookies persist at
//     the WKWebsiteDataStore (profile) level, surviving pane close, so
//     a single injection authenticates the agent's future panes.
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
	surfaceID string // cached browser surface ref, opened lazily, reused

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
// `cmux rpc browser.cookies.set`. It ensures a cached browser surface
// exists (opening an unfocused about:blank pane on first use), and
// reopens once if the cached surface has gone away. Errors are returned
// for RunAll to log; a missing or cmuxOnly-gated cmux is a soft failure
// that never aborts the sync or the other surfaces.
func (a *CmuxAdapter) Push(cookies []chrome.Cookie) error {
	if len(cookies) == 0 {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, err := a.ensureSurfaceLocked(); err != nil {
		return fmt.Errorf("cmux: open browser surface: %w", err)
	}

	reopened := false
	for i, c := range cookies {
		err := a.setCookieLocked(c)
		if err == nil {
			continue
		}
		// The cached surface may have been closed by the user between
		// syncs. Reopen once and retry this cookie before giving up.
		if !reopened && isSurfaceError(err) {
			reopened = true
			a.surfaceID = ""
			if _, oerr := a.ensureSurfaceLocked(); oerr != nil {
				return fmt.Errorf("cmux: reopen surface after %v: %w", err, oerr)
			}
			if rerr := a.setCookieLocked(c); rerr != nil {
				return fmt.Errorf("cmux: set cookie %q after reopen: %w", c.Name, rerr)
			}
			continue
		}
		return fmt.Errorf("cmux: set cookie %q (%d/%d): %w", c.Name, i+1, len(cookies), err)
	}
	return nil
}

// ensureSurfaceLocked returns the cached browser surface ref, opening an
// unfocused about:blank pane if none is cached. Caller must hold a.mu.
func (a *CmuxAdapter) ensureSurfaceLocked() (string, error) {
	if a.surfaceID != "" {
		return a.surfaceID, nil
	}
	out, err := a.run("browser", "open", "about:blank", "--focus", "false")
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

// setCookieLocked sends one cookie to the cached surface. Caller must
// hold a.mu and have a non-empty a.surfaceID.
func (a *CmuxAdapter) setCookieLocked(c chrome.Cookie) error {
	payload, err := cmuxCookieJSON(a.surfaceID, c)
	if err != nil {
		return fmt.Errorf("marshal cookie %q: %w", c.Name, err)
	}
	_, err = a.run("rpc", "browser.cookies.set", payload)
	return err
}

// cmuxCookieJSON builds the browser.cookies.set params for one cookie.
// Value and domain pass through verbatim (see the CmuxAdapter doc on why
// no App-Bound re-strip and no leading-dot strip). expires is omitted
// for session cookies (ExpiresUTC == 0).
func cmuxCookieJSON(surfaceID string, c chrome.Cookie) (string, error) {
	path := c.Path
	if path == "" {
		path = "/"
	}
	m := map[string]any{
		"surface_id": surfaceID,
		"name":       c.Name,
		"value":      c.Value,   // verbatim -- already App-Bound-stripped on the source
		"domain":     c.HostKey, // verbatim -- WebKit accepts the leading dot
		"path":       path,
		"secure":     c.IsSecure == 1,
		"http_only":  c.IsHTTPOnly == 1,
	}
	if ss := cmuxSameSite(c.SameSite); ss != "" {
		m["same_site"] = ss
	}
	if c.ExpiresUTC != 0 {
		m["expires"] = chromeMicrosToUnixSec(c.ExpiresUTC)
	}
	b, err := json.Marshal(m)
	return string(b), err
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
	// "not found": Go's exec.LookPath error ("executable file not found
	// in $PATH") and unrelated OS errors contain that phrase, and would
	// wrongly trigger the one-time reopen on a genuinely missing cmux.
	return strings.Contains(msg, "surface") ||
		strings.Contains(msg, "invalid_params") ||
		strings.Contains(msg, "not a browser")
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
