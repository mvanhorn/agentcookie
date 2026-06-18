package sinkpush

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// fakeCmux records the cmux subcommands invoked and returns scripted
// responses, so adapter tests never shell out to a real cmux.
type fakeCmux struct {
	calls    [][]string
	addOut   string  // new-surface response ("" = a default surface in the targeted workspace)
	addErrs  []error // consumed in order, one per new-surface call
	addIdx   int
	listOut  string // workspace list --json response ("" = one default workspace)
	listErr  error
	setErrs  []error // consumed in order, one per browser.cookies.set call
	setCalls int

	// Auth-verify (verify.go) scripting.
	evalOuts   []string // browser.eval responses, consumed in order; last repeats
	evalErrs   []error  // browser.eval errors, consumed in order; last repeats
	evalIdx    int
	closeCalls int // count of rpc surface.close calls
}

func (f *fakeCmux) run(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if len(args) >= 2 && args[0] == "workspace" && args[1] == "list" {
		if f.listErr != nil {
			return "", f.listErr
		}
		out := f.listOut
		if out == "" {
			out = `{"workspaces":[{"ref":"workspace:2","title":"✳ Claude Code"}]}`
		}
		return out, nil
	}
	if len(args) >= 1 && args[0] == "new-surface" {
		i := f.addIdx
		f.addIdx++
		if i < len(f.addErrs) && f.addErrs[i] != nil {
			return "", f.addErrs[i]
		}
		out := f.addOut
		if out == "" {
			// Echo the targeted workspace so tests can assert placement.
			ws := flagValue(args, "--workspace")
			out = "OK surface:9 pane:1 " + ws
		}
		return out, nil
	}
	if len(args) >= 2 && args[0] == "rpc" && args[1] == "browser.cookies.set" {
		i := f.setCalls
		f.setCalls++
		if i < len(f.setErrs) && f.setErrs[i] != nil {
			return "", f.setErrs[i]
		}
		return `{"set":1}`, nil
	}
	if len(args) >= 2 && args[0] == "rpc" && args[1] == "browser.eval" {
		i := f.evalIdx
		f.evalIdx++
		if e := pickAt(f.evalErrs, i); e != nil {
			return "", e
		}
		return pickStrAt(f.evalOuts, i), nil
	}
	if len(args) >= 2 && args[0] == "rpc" && args[1] == "surface.close" {
		f.closeCalls++
		return `{"closed":1}`, nil
	}
	return "", nil
}

// pickAt returns errs[i], or the last element when i is past the end, or nil
// when empty. Lets a fake script "then this forever" with one trailing entry.
func pickAt(errs []error, i int) error {
	if len(errs) == 0 {
		return nil
	}
	if i >= len(errs) {
		i = len(errs) - 1
	}
	return errs[i]
}

// pickStrAt is pickAt for strings (returns "" when empty).
func pickStrAt(s []string, i int) string {
	if len(s) == 0 {
		return ""
	}
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}

// flagValue returns the value following flag in args, or "".
func flagValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func newTestCmux(f *fakeCmux, filter []string) *CmuxAdapter {
	a := &CmuxAdapter{binary: "/fake/cmux", domainFilter: filter}
	a.run = f.run
	a.sleep = func(time.Duration) {} // no real waits in tests
	return a
}

func TestCmuxAdapter_Identity(t *testing.T) {
	a := newTestCmux(&fakeCmux{}, []string{"%github.com"})
	if a.Name() != "cmux" {
		t.Errorf("Name: got %q, want cmux", a.Name())
	}
	if got := a.CookieHostPatterns(); len(got) != 1 || got[0] != "%github.com" {
		t.Errorf("CookieHostPatterns: got %v", got)
	}
}

func TestCmuxCookieJSON_FieldMapping(t *testing.T) {
	c := chrome.Cookie{
		HostKey:    ".github.com", // leading dot preserved (WebKit accepts it)
		Name:       "session",
		Value:      "abc123",
		Path:       "/api",
		IsSecure:   1,
		IsHTTPOnly: 1,
		SameSite:   2,                 // strict
		ExpiresUTC: 13390000000000000, // micros since 1601
	}
	m := cmuxCookieParam(c)
	if m["domain"] != ".github.com" {
		t.Errorf("domain should be verbatim with leading dot, got %v", m["domain"])
	}
	if m["url"] != "https://github.com/api" {
		t.Errorf("url should be synthesized from host/path, got %v", m["url"])
	}
	if m["name"] != "session" || m["value"] != "abc123" {
		t.Errorf("name/value: got %v / %v", m["name"], m["value"])
	}
	if m["path"] != "/api" {
		t.Errorf("path: got %v", m["path"])
	}
	if m["secure"] != true || m["http_only"] != true {
		t.Errorf("secure/http_only: got %v / %v", m["secure"], m["http_only"])
	}
	if m["same_site"] != "strict" {
		t.Errorf("same_site: got %v, want strict", m["same_site"])
	}
	if _, ok := m["expires"]; !ok {
		t.Errorf("expires should be present for a persistent cookie")
	}

	// The batch envelope wraps per-cookie params with the surface id.
	raw, err := cmuxCookiesJSON("surface:9", []chrome.Cookie{c, c})
	if err != nil {
		t.Fatalf("cmuxCookiesJSON: %v", err)
	}
	var env struct {
		SurfaceID string           `json:"surface_id"`
		Cookies   []map[string]any `json:"cookies"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.SurfaceID != "surface:9" || len(env.Cookies) != 2 {
		t.Errorf("envelope: surface_id=%q cookies=%d, want surface:9 / 2", env.SurfaceID, len(env.Cookies))
	}
}

func TestCmuxCookieParam_HostPrefixIsHostOnly(t *testing.T) {
	// Regression guard for the GitHub-login bug: a __Host--prefixed cookie
	// (GitHub's session) is hard-rejected by WebKit if it carries ANY Domain
	// attribute. It must be sent host-only -- url set, no domain -- with
	// Secure and Path "/" forced.
	c := chrome.Cookie{
		HostKey:  "github.com", // host-only in Chrome's store (no leading dot)
		Name:     "__Host-user_session_same_site",
		Value:    "sess",
		Path:     "/",
		IsSecure: 1,
	}
	m := cmuxCookieParam(c)
	if _, ok := m["domain"]; ok {
		t.Errorf("__Host- cookie must NOT carry a domain (WebKit rejects it), got %v", m["domain"])
	}
	if m["url"] != "https://github.com/" {
		t.Errorf("__Host- cookie must be scoped by url, got %v", m["url"])
	}
	if m["secure"] != true {
		t.Errorf("__Host- cookie must force secure=true, got %v", m["secure"])
	}
	if m["path"] != "/" {
		t.Errorf("__Host- cookie must force path=/, got %v", m["path"])
	}
}

func TestCmuxCookieParam_HostPrefixForcedHostOnlyDespiteDotHostKey(t *testing.T) {
	// Hardening for PR #103's known residual: the __Host- invariant must hold
	// regardless of upstream state. Even if a __Host- cookie arrives with a
	// leading-dot host_key (which would otherwise take the domain-cookie
	// branch), the __Host- guard must win -- no Domain emitted, url present --
	// so cmux's handler never has to fall back to a navigated surface's host
	// (which would re-add a Domain and get the cookie dropped by WebKit).
	c := chrome.Cookie{
		HostKey:  ".github.com", // leading dot would normally set Domain
		Name:     "__Host-user_session_same_site",
		Value:    "sess",
		Path:     "/",
		IsSecure: 1,
	}
	m := cmuxCookieParam(c)
	if _, ok := m["domain"]; ok {
		t.Errorf("__Host- cookie must never carry a domain even with a dot host_key, got %v", m["domain"])
	}
	if m["url"] != "https://github.com/" {
		t.Errorf("__Host- cookie must always carry a url so cmux never falls back to the surface host, got %v", m["url"])
	}
}

func TestCmuxCookieParam_HostOnlyNoDomainWidening(t *testing.T) {
	// A host-only cookie (host_key with no leading dot) must not be widened
	// to subdomains: no Domain attribute, scoped to the exact host via url.
	c := chrome.Cookie{HostKey: "github.com", Name: "user_session", Value: "v", Path: "/", IsSecure: 1}
	m := cmuxCookieParam(c)
	if _, ok := m["domain"]; ok {
		t.Errorf("host-only cookie must NOT carry a domain, got %v", m["domain"])
	}
	if m["url"] != "https://github.com/" {
		t.Errorf("host-only cookie must be scoped by url, got %v", m["url"])
	}
}

func TestCmuxCookieParam_InsecureNoneDowngradesToLax(t *testing.T) {
	// A SameSite=None cookie that is not Secure is rejected by spec; downgrade
	// to Lax so it survives. A secure None cookie keeps None.
	insecure := chrome.Cookie{HostKey: "example.com", Name: "x", Value: "v", Path: "/", IsSecure: 0, SameSite: 0}
	if m := cmuxCookieParam(insecure); m["same_site"] != "lax" {
		t.Errorf("insecure None must downgrade to lax, got %v", m["same_site"])
	}
	secure := chrome.Cookie{HostKey: "example.com", Name: "x", Value: "v", Path: "/", IsSecure: 1, SameSite: 0}
	if m := cmuxCookieParam(secure); m["same_site"] != "none" {
		t.Errorf("secure None must stay none, got %v", m["same_site"])
	}
}

func TestCmuxCookieParam_SessionCookieOmitsExpires(t *testing.T) {
	c := chrome.Cookie{HostKey: "example.com", Name: "s", Value: "v", ExpiresUTC: 0}
	m := cmuxCookieParam(c)
	if _, ok := m["expires"]; ok {
		t.Errorf("session cookie (ExpiresUTC==0) must omit expires, got %v", m["expires"])
	}
	if m["path"] != "/" {
		t.Errorf("empty path should default to /, got %v", m["path"])
	}
}

func TestCmuxCookieParam_ValuePassthroughNoSecondStrip(t *testing.T) {
	// Regression guard: the App-Bound prefix is stripped once on the
	// source side. A second strip here lopped 32 bytes off every cookie
	// longer than the prefix (the v0.12.0-beta.3 64% drop). The value
	// must arrive byte-identical.
	val := strings.Repeat("A", 32) + "REAL_SESSION_PAYLOAD"
	c := chrome.Cookie{HostKey: ".x.com", Name: "k", Value: val}
	m := cmuxCookieParam(c)
	if m["value"] != val {
		t.Errorf("value was altered.\n got: %q\nwant: %q", m["value"], val)
	}
}

func TestCmuxSameSite(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "none"},
		{1, "lax"},
		{2, "strict"},
		{-1, ""}, // unspecified -> omitted
		{99, ""}, // unknown -> omitted
	}
	for _, tc := range cases {
		if got := cmuxSameSite(tc.in); got != tc.want {
			t.Errorf("cmuxSameSite(%d): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestChromeMicrosToUnixSec(t *testing.T) {
	// 13390000000000000 micros / 1e6 = 13390000000 sec since 1601;
	// minus the 11644473600 offset = 1745526400 unix sec.
	if got := chromeMicrosToUnixSec(13390000000000000); got != 1745526400 {
		t.Errorf("chromeMicrosToUnixSec: got %d, want 1745526400", got)
	}
}

func TestCmuxPush_AddsHiddenTabThenBatchSets(t *testing.T) {
	f := &fakeCmux{}
	a := newTestCmux(f, nil)
	cookies := []chrome.Cookie{
		{HostKey: ".github.com", Name: "a", Value: "1"},
		{HostKey: ".github.com", Name: "b", Value: "2"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// One hidden browser tab added to the host workspace, and both cookies
	// in a single batched set call.
	add := findCall(f.calls, "new-surface")
	if add == nil {
		t.Fatalf("no new-surface call, got %v", f.calls)
	}
	if !hasFlag(add, "--type", "browser") {
		t.Errorf("must add a browser surface, got %v", add)
	}
	if !hasFlag(add, "--workspace", "workspace:2") {
		t.Errorf("tab must target the host workspace (first listed), got %v", add)
	}
	if !hasFlag(add, "--focus", "false") {
		t.Errorf("tab must be added unfocused (hidden), got %v", add)
	}
	if f.setCalls != 1 {
		t.Errorf("expected 1 batched cookies.set call for 2 cookies, got %d", f.setCalls)
	}
	// Surface is cached: a second Push reuses it (no new tab).
	addsBefore := countAdds(f.calls)
	if err := a.Push(cookies[:1]); err != nil {
		t.Fatalf("second Push: %v", err)
	}
	if countAdds(f.calls) != addsBefore {
		t.Errorf("second Push should reuse cached surface, added another tab")
	}
}

func TestCmuxPush_ChunksLargeSets(t *testing.T) {
	f := &fakeCmux{}
	a := newTestCmux(f, nil)
	n := cmuxCookieBatch*2 + 5 // spans 3 chunks
	cookies := make([]chrome.Cookie, n)
	for i := range cookies {
		cookies[i] = chrome.Cookie{HostKey: ".x.com", Name: "c", Value: "v"}
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if f.setCalls != 3 {
		t.Errorf("expected 3 batched set calls for %d cookies (batch=%d), got %d", n, cmuxCookieBatch, f.setCalls)
	}
}

func TestCmuxPush_BatchRejectFallsBackPerCookie(t *testing.T) {
	// The batch call rejects with a payload error (one bad cookie in the
	// chunk); the per-cookie fallback then lands the good one and skips the
	// reject. Push succeeds best-effort.
	f := &fakeCmux{setErrs: []error{
		errors.New("invalid_params: Invalid cookie payload"), // batch call
		nil, // cookie a, per-cookie
		errors.New("invalid_params: Invalid cookie payload"), // cookie b rejected -> skipped
	}}
	a := newTestCmux(f, nil)
	cookies := []chrome.Cookie{
		{HostKey: ".x.com", Name: "a", Value: "1"},
		{HostKey: ".x.com", Name: "b", Value: "2"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push should be best-effort (skip rejected cookie), got %v", err)
	}
	if f.setCalls != 3 { // 1 batch + 2 per-cookie
		t.Errorf("expected 3 set calls (batch + per-cookie fallback), got %d", f.setCalls)
	}
}

func TestCmuxPush_HardFailsWhenCmuxUnavailable(t *testing.T) {
	f := &fakeCmux{setErrs: []error{errors.New("broken pipe, errno 32")}}
	a := newTestCmux(f, nil)
	err := a.Push([]chrome.Cookie{{HostKey: ".x.com", Name: "a", Value: "1"}})
	if err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("cmux-unavailable must hard-fail (not silently skip), got %v", err)
	}
}

func TestCmuxPush_DropsInvalidCookies(t *testing.T) {
	// An RFC-invalid cookie (control char in value) is dropped before any
	// RPC, so it can't poison a batch. A valid one still lands.
	f := &fakeCmux{}
	a := newTestCmux(f, nil)
	cookies := []chrome.Cookie{
		{HostKey: ".x.com", Name: "ok", Value: "good"},
		{HostKey: ".x.com", Name: "bad", Value: "has\x00null"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if f.setCalls != 1 {
		t.Errorf("expected 1 batch call, got %d", f.setCalls)
	}
}

func TestCmuxPush_ReAddsTabOnSurfaceError(t *testing.T) {
	f := &fakeCmux{
		// First set fails with a surface error (the cached hidden tab was
		// closed); after re-adding, the retry and subsequent sets succeed.
		setErrs: []error{errors.New("invalid_params: Surface is not a browser")},
	}
	a := newTestCmux(f, nil)
	// Prime a stale cached surface so Push starts with one and the first
	// set hits the stale-surface path.
	a.surfaceID = "surface:stale"
	cookies := []chrome.Cookie{{HostKey: ".x.com", Name: "a", Value: "1"}}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push should recover by re-adding the tab, got %v", err)
	}
	if countAdds(f.calls) != 1 {
		t.Errorf("expected exactly one re-add, got %d", countAdds(f.calls))
	}
}

func TestCmuxPush_NonSurfaceErrorFailsSoft(t *testing.T) {
	// A cmuxOnly access-denied (or any non-surface error that does not
	// match the reopen heuristic) propagates so RunAll records FAIL.
	f := &fakeCmux{setErrs: []error{errors.New("connection refused")}}
	a := newTestCmux(f, nil)
	err := a.Push([]chrome.Cookie{{HostKey: ".x.com", Name: "a", Value: "1"}})
	if err == nil {
		t.Fatal("expected Push to return the underlying error")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error should carry the cause, got %v", err)
	}
}

func TestCmuxPush_EmptyIsNoop(t *testing.T) {
	f := &fakeCmux{}
	a := newTestCmux(f, nil)
	if err := a.Push(nil); err != nil {
		t.Fatalf("Push(nil): %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("empty Push should not open a surface or set cookies, got %v", f.calls)
	}
}

func TestCmuxIsInstalled(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cmux")
	a := &CmuxAdapter{binary: bin}
	if a.IsInstalled() {
		t.Error("IsInstalled should be false before the binary exists")
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.IsInstalled() {
		t.Error("IsInstalled should be true once the binary exists")
	}
}

func TestNewCmux_DefaultBinaryFallback(t *testing.T) {
	a := NewCmux("", nil)
	if a.binary == "" {
		t.Error("NewCmux with empty path should resolve a binary")
	}
	a2 := NewCmux("/custom/cmux", []string{"%x"})
	if a2.binary != "/custom/cmux" {
		t.Errorf("NewCmux should honor explicit path, got %q", a2.binary)
	}
}

func TestFilterByHostPatterns(t *testing.T) {
	cookies := []chrome.Cookie{
		{HostKey: ".github.com", Name: "a"},
		{HostKey: ".openai.com", Name: "b"},
		{HostKey: "www.example.com", Name: "c"},
	}
	if got := FilterByHostPatterns(cookies, nil); len(got) != 3 {
		t.Errorf("nil patterns should pass all, got %d", len(got))
	}
	got := FilterByHostPatterns(cookies, []string{"%github.com"})
	if len(got) != 1 || got[0].HostKey != ".github.com" {
		t.Errorf("expected only github, got %+v", got)
	}
}

// countAdds counts new-surface (hidden-tab add) calls.
func countAdds(calls [][]string) int {
	n := 0
	for _, c := range calls {
		if len(c) >= 1 && c[0] == "new-surface" {
			n++
		}
	}
	return n
}

// findCall returns the first recorded call whose leading args match.
func findCall(calls [][]string, prefix ...string) []string {
	for _, c := range calls {
		if len(c) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if c[i] != p {
				match = false
				break
			}
		}
		if match {
			return c
		}
	}
	return nil
}

// hasFlag reports whether args contains the flag followed by value.
func hasFlag(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestCmuxPush_AddsTabToFirstWorkspaceNeverCreatesOne(t *testing.T) {
	// The hidden injection tab is parked in an existing workspace -- the
	// first cmux lists -- so it adds no sidebar entry and no phantom
	// terminal. (Earlier designs created a dedicated "agentcookie"
	// workspace, which showed in the sidebar and, because short refs
	// renumber across cmux restarts, could alias a live user workspace and
	// pop into view.)
	f := &fakeCmux{listOut: `{"workspaces":[
		{"ref":"workspace:5","title":"⠂ Process incoming email task"},
		{"ref":"workspace:2","title":"✳ Claude Code"}
	]}`}
	a := newTestCmux(f, nil)
	if err := a.Push([]chrome.Cookie{{HostKey: ".x.com", Name: "a", Value: "1"}}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if c := findCall(f.calls, "workspace", "create"); c != nil {
		t.Errorf("must never create a workspace, but create was called: %v", c)
	}
	add := findCall(f.calls, "new-surface")
	if add == nil {
		t.Fatalf("no new-surface call, got %v", f.calls)
	}
	if !hasFlag(add, "--workspace", "workspace:5") {
		t.Errorf("tab should target the first listed workspace:5, got %v", add)
	}
	if !hasFlag(add, "--focus", "false") {
		t.Errorf("tab must be hidden (--focus false), got %v", add)
	}
}

func TestCmuxPush_NoWorkspaceToHostIsAnError(t *testing.T) {
	// cmux is running but reports no workspaces: there is nowhere to park
	// the tab, so Push fails (soft, logged by the caller) rather than
	// silently creating one.
	f := &fakeCmux{listOut: `{"workspaces":[]}`}
	a := newTestCmux(f, nil)
	err := a.Push([]chrome.Cookie{{HostKey: ".x.com", Name: "a", Value: "1"}})
	if err == nil {
		t.Fatal("expected an error when no workspace exists to host the tab")
	}
	if findCall(f.calls, "new-surface") != nil {
		t.Errorf("must not add a surface when there is no host workspace: %v", f.calls)
	}
}

func TestCmuxPush_ReAddResolvesHostFreshNotCached(t *testing.T) {
	// After a stale-surface error, the re-add resolves the host workspace
	// by listing again -- short refs renumber across cmux restarts, so no
	// pre-restart ref is reused. Here the host is workspace:8.
	f := &fakeCmux{
		listOut: `{"workspaces":[
			{"ref":"workspace:8","title":"⠂ Process incoming email task"}
		]}`,
		setErrs: []error{errors.New("not_found: Surface not found")},
	}
	a := newTestCmux(f, nil)
	a.surfaceID = "surface:12" // pre-restart cache; surface is gone
	if err := a.Push([]chrome.Cookie{{HostKey: ".x.com", Name: "a", Value: "1"}}); err != nil {
		t.Fatalf("Push should recover via re-add, got %v", err)
	}
	add := findCall(f.calls, "new-surface")
	if add == nil {
		t.Fatalf("expected a re-add, got %v", f.calls)
	}
	if !hasFlag(add, "--workspace", "workspace:8") {
		t.Errorf("re-add should target the freshly listed host workspace:8, got %v", add)
	}
}

func TestFirstWorkspaceRef(t *testing.T) {
	list := `{"workspaces":[
		{"ref":"workspace:3","title":"✳ Claude Code"},
		{"ref":"workspace:4","title":"other"}
	]}`
	if got := firstWorkspaceRef(list); got != "workspace:3" {
		t.Errorf("first ref: got %q, want workspace:3", got)
	}
	if got := firstWorkspaceRef(`{"workspaces":[]}`); got != "" {
		t.Errorf("empty list should return empty, got %q", got)
	}
	if got := firstWorkspaceRef("not json"); got != "" {
		t.Errorf("bad json should return empty, got %q", got)
	}
}

func TestCmuxPush_SurfaceErrorAfterReopenHardFails(t *testing.T) {
	// Greptile P1: a reopen happens but the retry still hits a surface error.
	// That must hard-fail, not fall into per-cookie fallback and silently drop
	// the whole chunk.
	f := &fakeCmux{setErrs: []error{
		errors.New("invalid_params: Surface is not a browser"), // batch
		errors.New("invalid_params: Surface is not a browser"), // retry after reopen
	}}
	a := newTestCmux(f, nil)
	a.surfaceID = "surface:stale"
	err := a.Push([]chrome.Cookie{{HostKey: ".x.com", Name: "a", Value: "1"}})
	if err == nil || !strings.Contains(err.Error(), "not a browser") {
		t.Fatalf("a persistent surface error must hard-fail (no silent chunk loss), got %v", err)
	}
}

func TestCmuxPush_PermissionDeniedHardFails(t *testing.T) {
	// Greptile P2: EACCES ("permission denied") means cmux is unusable; it must
	// hard-fail, not be mistaken for a per-cookie reject and silently skipped.
	f := &fakeCmux{setErrs: []error{errors.New("fork/exec /x/cmux: permission denied")}}
	a := newTestCmux(f, nil)
	err := a.Push([]chrome.Cookie{{HostKey: ".x.com", Name: "a", Value: "1"}})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("permission denied must hard-fail, got %v", err)
	}
}
