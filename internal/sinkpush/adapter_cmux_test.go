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
	openErr  error
	setErrs  []error // consumed in order, one per browser.cookies.set call
	setCalls int
}

func (f *fakeCmux) run(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if len(args) >= 2 && args[0] == "browser" && args[1] == "open" {
		if f.openErr != nil {
			return "", f.openErr
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
	raw, err := cmuxCookieJSON("surface:9", c)
	if err != nil {
		t.Fatalf("cmuxCookieJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["surface_id"] != "surface:9" {
		t.Errorf("surface_id: got %v", m["surface_id"])
	}
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
}

func TestCmuxCookieJSON_SessionCookieOmitsExpires(t *testing.T) {
	c := chrome.Cookie{HostKey: "example.com", Name: "s", Value: "v", ExpiresUTC: 0}
	raw, _ := cmuxCookieJSON("surface:1", c)
	var m map[string]any
	_ = json.Unmarshal([]byte(raw), &m)
	if _, ok := m["expires"]; ok {
		t.Errorf("session cookie (ExpiresUTC==0) must omit expires, got %v", m["expires"])
	}
	if m["path"] != "/" {
		t.Errorf("empty path should default to /, got %v", m["path"])
	}
}

func TestCmuxCookieJSON_ValuePassthroughNoSecondStrip(t *testing.T) {
	// Regression guard: the App-Bound prefix is stripped once on the
	// source side. A second strip here lopped 32 bytes off every cookie
	// longer than the prefix (the v0.12.0-beta.3 64% drop). The value
	// must arrive byte-identical.
	val := strings.Repeat("A", 32) + "REAL_SESSION_PAYLOAD"
	c := chrome.Cookie{HostKey: ".x.com", Name: "k", Value: val}
	raw, _ := cmuxCookieJSON("surface:1", c)
	var m map[string]any
	_ = json.Unmarshal([]byte(raw), &m)
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

func TestCmuxPush_OpensSurfaceThenSetsEach(t *testing.T) {
	f := &fakeCmux{}
	a := newTestCmux(f, nil)
	cookies := []chrome.Cookie{
		{HostKey: ".github.com", Name: "a", Value: "1"},
		{HostKey: ".github.com", Name: "b", Value: "2"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// One open, two sets.
	if f.calls[0][0] != "browser" || f.calls[0][1] != "open" {
		t.Errorf("first call should open a surface, got %v", f.calls[0])
	}
	if f.setCalls != 2 {
		t.Errorf("expected 2 cookies.set calls, got %d", f.setCalls)
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
