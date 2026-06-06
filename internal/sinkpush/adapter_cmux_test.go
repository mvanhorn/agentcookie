package sinkpush

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// fakeCmux records the cmux subcommands invoked and returns scripted
// responses, so adapter tests never shell out to a real cmux.
type fakeCmux struct {
	calls    [][]string
	openOut  string
	openErrs []error // consumed in order, one per browser open call
	openIdx  int
	listOut  string // workspace list --json response ("" = empty list)
	listErr  error
	wsOut    string // workspace create response
	wsErr    error
	setErrs  []error // consumed in order, one per browser.cookies.set call
	setCalls int
}

func (f *fakeCmux) run(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if len(args) >= 2 && args[0] == "workspace" && args[1] == "list" {
		if f.listErr != nil {
			return "", f.listErr
		}
		out := f.listOut
		if out == "" {
			out = `{"workspaces":[]}`
		}
		return out, nil
	}
	if len(args) >= 2 && args[0] == "workspace" && args[1] == "create" {
		if f.wsErr != nil {
			return "", f.wsErr
		}
		out := f.wsOut
		if out == "" {
			out = "OK workspace:7"
		}
		return out, nil
	}
	if len(args) >= 2 && args[0] == "browser" && args[1] == "open" {
		i := f.openIdx
		f.openIdx++
		if i < len(f.openErrs) && f.openErrs[i] != nil {
			return "", f.openErrs[i]
		}
		out := f.openOut
		if out == "" {
			out = "OK surface=surface:9 pane=pane:1 placement=split"
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
	return "", nil
}

func newTestCmux(f *fakeCmux, filter []string) *CmuxAdapter {
	a := &CmuxAdapter{binary: "/fake/cmux", domainFilter: filter}
	a.run = f.run
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

func TestCmuxPush_OpensSurfaceThenBatchSets(t *testing.T) {
	f := &fakeCmux{}
	a := newTestCmux(f, nil)
	cookies := []chrome.Cookie{
		{HostKey: ".github.com", Name: "a", Value: "1"},
		{HostKey: ".github.com", Name: "b", Value: "2"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// One open (inside the background workspace), and both cookies in a
	// single batched set call.
	open := findCall(f.calls, "browser", "open")
	if open == nil {
		t.Fatalf("no browser open call, got %v", f.calls)
	}
	if !hasFlag(open, "--workspace", "workspace:7") {
		t.Errorf("open must target the background workspace, got %v", open)
	}
	if !hasFlag(open, "--focus", "false") {
		t.Errorf("open must be unfocused, got %v", open)
	}
	if f.setCalls != 1 {
		t.Errorf("expected 1 batched cookies.set call for 2 cookies, got %d", f.setCalls)
	}
	// Surface is cached: a second Push reuses it (no new open).
	opensBefore := countOpens(f.calls)
	if err := a.Push(cookies[:1]); err != nil {
		t.Fatalf("second Push: %v", err)
	}
	if countOpens(f.calls) != opensBefore {
		t.Errorf("second Push should reuse cached surface, opened again")
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

func TestCmuxPush_ReopensOnSurfaceError(t *testing.T) {
	f := &fakeCmux{
		// First set fails with a surface error; after reopen the retry
		// (and subsequent sets) succeed.
		setErrs: []error{errors.New("invalid_params: Surface is not a browser")},
	}
	a := newTestCmux(f, nil)
	// Prime a stale cached surface so Push starts with one and the first
	// set hits the stale-surface path.
	a.surfaceID = "surface:stale"
	cookies := []chrome.Cookie{{HostKey: ".x.com", Name: "a", Value: "1"}}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push should recover by reopening, got %v", err)
	}
	if countOpens(f.calls) != 1 {
		t.Errorf("expected exactly one reopen, got %d", countOpens(f.calls))
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

func countOpens(calls [][]string) int {
	n := 0
	for _, c := range calls {
		if len(c) >= 2 && c[0] == "browser" && c[1] == "open" {
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

func TestCmuxPush_ReusesExistingNamedWorkspace(t *testing.T) {
	// A workspace named "agentcookie" from a previous run is reused (cmux
	// prefixes active titles with a status glyph), so restarts don't
	// accumulate one background workspace each.
	f := &fakeCmux{listOut: `{"workspaces":[
		{"ref":"workspace:2","title":"✳ Claude Code"},
		{"ref":"workspace:5","title":"⠂ agentcookie"}
	]}`}
	a := newTestCmux(f, nil)
	if err := a.Push([]chrome.Cookie{{HostKey: ".x.com", Name: "a", Value: "1"}}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if c := findCall(f.calls, "workspace", "create"); c != nil {
		t.Errorf("existing workspace must be reused, but create was called: %v", c)
	}
	open := findCall(f.calls, "browser", "open")
	if open == nil || !hasFlag(open, "--workspace", "workspace:5") {
		t.Errorf("open should target the existing workspace:5, got %v", open)
	}
}

func TestCmuxPush_RecreatesClosedWorkspace(t *testing.T) {
	// The cached background workspace was closed by the user between
	// syncs: browser open fails with a workspace error, and the adapter
	// recreates the workspace once and retries the open.
	f := &fakeCmux{openErrs: []error{errors.New("not_found: Workspace not found")}}
	a := newTestCmux(f, nil)
	a.workspaceRef = "workspace:stale"
	if err := a.Push([]chrome.Cookie{{HostKey: ".x.com", Name: "a", Value: "1"}}); err != nil {
		t.Fatalf("Push should recover by recreating the workspace, got %v", err)
	}
	if findCall(f.calls, "workspace", "create") == nil {
		t.Errorf("expected a workspace create after the stale-workspace open failure, calls: %v", f.calls)
	}
	if countOpens(f.calls) != 2 {
		t.Errorf("expected 2 opens (stale fail + retry), got %d", countOpens(f.calls))
	}
	if a.workspaceRef != "workspace:7" {
		t.Errorf("workspaceRef should be the recreated workspace, got %q", a.workspaceRef)
	}
}

func TestFindWorkspaceRef(t *testing.T) {
	list := `{"workspaces":[
		{"ref":"workspace:1","title":"✳ Claude Code"},
		{"ref":"workspace:3","title":"agentcookie"},
		{"ref":"workspace:4","title":"not-agentcookie"}
	]}`
	if got := findWorkspaceRef(list, "agentcookie"); got != "workspace:3" {
		t.Errorf("exact title: got %q, want workspace:3", got)
	}
	glyph := `{"workspaces":[{"ref":"workspace:9","title":"⠐ agentcookie"}]}`
	if got := findWorkspaceRef(glyph, "agentcookie"); got != "workspace:9" {
		t.Errorf("glyph-prefixed title: got %q, want workspace:9", got)
	}
	// Greptile P2: only a single leading glyph is accepted -- a user
	// workspace that merely ends with the name must not be hijacked.
	words := `{"workspaces":[
		{"ref":"workspace:2","title":"dev agentcookie"},
		{"ref":"workspace:6","title":"my old agentcookie"}
	]}`
	if got := findWorkspaceRef(words, "agentcookie"); got != "" {
		t.Errorf("word-prefixed titles must not match, got %q", got)
	}
	if got := findWorkspaceRef(list, "missing"); got != "" {
		t.Errorf("missing name should return empty, got %q", got)
	}
	if got := findWorkspaceRef("not json", "agentcookie"); got != "" {
		t.Errorf("bad json should return empty, got %q", got)
	}
}

func TestIsWorkspaceError_OnlyMissingWorkspace(t *testing.T) {
	// Greptile P1: only the closed/missing-ref signature triggers the
	// one-shot recreate. Quota or permission failures would recur on a
	// fresh workspace, so recreating for them just leaks workspaces.
	if !isWorkspaceError(errors.New("not_found: Workspace not found")) {
		t.Error("missing-workspace error must trigger recreate")
	}
	for _, msg := range []string{
		"workspace quota exceeded",
		"workspace permission denied",
		"connection refused",
	} {
		if isWorkspaceError(errors.New(msg)) {
			t.Errorf("%q must not trigger a workspace recreate", msg)
		}
	}
	if isWorkspaceError(nil) {
		t.Error("nil error must not trigger recreate")
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
