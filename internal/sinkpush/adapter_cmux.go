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

// workspaceRefRE extracts the `workspace:N` ref from `cmux workspace
// create` output, e.g. "OK workspace:9".
var workspaceRefRE = regexp.MustCompile(`workspace:\d+`)

// cmuxWorkspaceName is the dedicated background workspace the adapter
// parks its injection surface in. The cmux-sync LaunchAgent runs outside
// cmux, so $CMUX_WORKSPACE_ID is unset and a bare `browser open` lands
// the pane in whatever workspace the user has focused -- right in their
// face, and reopened on every sync if they close it. A named, unfocused
// workspace keeps the surface alive without ever entering the user's
// view.
const cmuxWorkspaceName = "agentcookie"

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
//     (unfocused, inside the dedicated cmuxWorkspaceName background
//     workspace) and caches it for reuse. Injected cookies persist at
//     the WKWebsiteDataStore (profile) level, surviving pane close, so
//     a single injection authenticates the agent's future panes.
//   - Surface refs are workspace-scoped: any command that targets the
//     cached surface by ref must carry the cached workspace ref too.
//   - expires is Unix seconds; omit it for session cookies. WebKit
//     clamps far-future expiries to its ~400-day max (expected).
//   - Cookie values are passed through VERBATIM. The App-Bound (Chrome
//     127+) 32-byte prefix is already stripped once on the source side
//     (internal/chrome/read.go); a second strip silently dropped 64% of
//     cookies in the v0.12.0-beta.3 dry-run. Never strip again here.
type CmuxAdapter struct {
	binary       string
	domainFilter []string

	mu           sync.Mutex
	surfaceID    string // cached browser surface ref, opened lazily, reused
	workspaceRef string // cached background workspace ref hosting the surface

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

// ensureSurfaceLocked returns the cached browser surface ref, opening an
// unfocused about:blank pane inside the dedicated background workspace
// if none is cached. If the cached workspace has been closed by the user
// since, it is recreated once. Caller must hold a.mu.
func (a *CmuxAdapter) ensureSurfaceLocked() (string, error) {
	if a.surfaceID != "" {
		return a.surfaceID, nil
	}
	ws, err := a.ensureWorkspaceLocked()
	if err != nil {
		return "", fmt.Errorf("background workspace: %w", err)
	}
	out, err := a.run("browser", "open", "about:blank", "--workspace", ws, "--focus", "false")
	if err != nil && isWorkspaceError(err) {
		// The cached workspace went away (user closed it). Recreate once.
		a.workspaceRef = ""
		ws, werr := a.ensureWorkspaceLocked()
		if werr != nil {
			return "", fmt.Errorf("recreate background workspace after %v: %w", err, werr)
		}
		out, err = a.run("browser", "open", "about:blank", "--workspace", ws, "--focus", "false")
	}
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

// ensureWorkspaceLocked returns the cached background workspace ref,
// finding an existing workspace named cmuxWorkspaceName or creating an
// unfocused one. Caller must hold a.mu.
func (a *CmuxAdapter) ensureWorkspaceLocked() (string, error) {
	if a.workspaceRef != "" {
		return a.workspaceRef, nil
	}
	// Reuse an existing workspace from a previous run so restarts don't
	// accumulate one workspace each. A list failure is non-fatal: fall
	// through to create.
	if out, err := a.run("workspace", "list", "--json"); err == nil {
		if ref := findWorkspaceRef(out, cmuxWorkspaceName); ref != "" {
			a.workspaceRef = ref
			return ref, nil
		}
	}
	out, err := a.run("workspace", "create", "--name", cmuxWorkspaceName, "--focus", "false")
	if err != nil {
		return "", err
	}
	ref := workspaceRefRE.FindString(out)
	if ref == "" {
		return "", fmt.Errorf("could not parse workspace ref from %q", strings.TrimSpace(out))
	}
	a.workspaceRef = ref
	return ref, nil
}

// findWorkspaceRef scans `workspace list --json` output for a workspace
// whose title is name. cmux prefixes active-workspace titles with a
// single status glyph ("✳ agentcookie"), so a glyph-plus-name title is
// accepted alongside the exact one -- but not arbitrary leading words
// ("dev agentcookie" is someone else's workspace).
func findWorkspaceRef(listJSON, name string) string {
	var parsed struct {
		Workspaces []struct {
			Ref   string `json:"ref"`
			Title string `json:"title"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal([]byte(listJSON), &parsed); err != nil {
		return ""
	}
	for _, w := range parsed.Workspaces {
		if w.Title == name {
			return w.Ref
		}
		if fields := strings.Fields(w.Title); len(fields) == 2 && fields[1] == name && len([]rune(fields[0])) == 1 {
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
// through verbatim (see the CmuxAdapter doc on why no App-Bound re-strip
// and no leading-dot strip). expires is omitted for session cookies
// (ExpiresUTC == 0).
func cmuxCookieParam(c chrome.Cookie) map[string]any {
	path := c.Path
	if path == "" {
		path = "/"
	}
	m := map[string]any{
		"name":      c.Name,
		"value":     c.Value,   // verbatim -- already App-Bound-stripped on the source
		"domain":    c.HostKey, // verbatim -- WebKit accepts the leading dot
		"path":      path,
		"secure":    c.IsSecure == 1,
		"http_only": c.IsHTTPOnly == 1,
	}
	if ss := cmuxSameSite(c.SameSite); ss != "" {
		m["same_site"] = ss
	}
	if c.ExpiresUTC != 0 {
		m["expires"] = chromeMicrosToUnixSec(c.ExpiresUTC)
	}
	return m
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

// isWorkspaceError reports whether err is specifically a stale/missing
// workspace (e.g. the user closed the background workspace), which
// warrants one recreate. cmux answers "not_found: Workspace not found"
// for a closed workspace ref. Deliberately NOT any error mentioning
// "workspace" -- a quota or permission failure would recur on the
// recreated workspace too, so recreating for those is a spurious
// workspace per failed sync.
func isWorkspaceError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "workspace not found")
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
