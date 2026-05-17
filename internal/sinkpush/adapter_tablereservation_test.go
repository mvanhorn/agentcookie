package sinkpush

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// newTRAdapter returns a TableReservationAdapter pointing at a temp
// dir with the binary placed so IsInstalled returns true.
func newTRAdapter(t *testing.T) *TableReservationAdapter {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "table-reservation-goat-pp-cli")
	if err := os.WriteFile(bin, []byte("#!/bin/bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	return &TableReservationAdapter{binary: bin, configDir: filepath.Join(dir, "config")}
}

func TestTableReservation_Identity(t *testing.T) {
	a := NewTableReservation()
	if a.Name() != "table-reservation-goat-pp-cli" {
		t.Errorf("Name: got %q", a.Name())
	}
	patterns := a.CookieHostPatterns()
	if len(patterns) != 2 {
		t.Fatalf("expected 2 host patterns, got %d", len(patterns))
	}
	want := map[string]bool{"%opentable.com": true, "%exploretock.com": true}
	for _, p := range patterns {
		if !want[p] {
			t.Errorf("unexpected pattern: %q", p)
		}
		delete(want, p)
	}
	if len(want) != 0 {
		t.Errorf("missing patterns: %v", want)
	}
}

func TestTableReservation_PushSplitsByNetwork(t *testing.T) {
	a := newTRAdapter(t)
	cookies := []chrome.Cookie{
		{HostKey: ".opentable.com", Name: "ot_a", Value: "ot1", Path: "/"},
		{HostKey: "www.opentable.com", Name: "ot_b", Value: "ot2", Path: "/"},
		{HostKey: ".exploretock.com", Name: "tock_a", Value: "t1", Path: "/"},
		{HostKey: "www.exploretock.com", Name: "tock_b", Value: "t2", Path: "/"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}

	body, _ := os.ReadFile(filepath.Join(a.configDir, "session.json"))
	var env sessionEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("parse session.json: %v", err)
	}
	if env.Version != 1 {
		t.Errorf("version: got %d, want 1", env.Version)
	}
	if len(env.OpentableCookies) != 2 {
		t.Errorf("opentable_cookies: got %d, want 2", len(env.OpentableCookies))
	}
	if len(env.TockCookies) != 2 {
		t.Errorf("tock_cookies: got %d, want 2", len(env.TockCookies))
	}
	for _, c := range env.OpentableCookies {
		if !strings.HasSuffix(c.Domain, "opentable.com") {
			t.Errorf("opentable cookie domain wrong: %q", c.Domain)
		}
	}
	for _, c := range env.TockCookies {
		if !strings.HasSuffix(c.Domain, "exploretock.com") {
			t.Errorf("tock cookie domain wrong: %q", c.Domain)
		}
	}
}

func TestTableReservation_PushOnlyOpentable(t *testing.T) {
	a := newTRAdapter(t)
	cookies := []chrome.Cookie{
		{HostKey: ".opentable.com", Name: "ot", Value: "v"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	var env sessionEnvelope
	b, _ := os.ReadFile(filepath.Join(a.configDir, "session.json"))
	_ = json.Unmarshal(b, &env)
	if len(env.OpentableCookies) != 1 {
		t.Errorf("opentable_cookies: got %d, want 1", len(env.OpentableCookies))
	}
	if env.TockCookies == nil {
		t.Errorf("tock_cookies should be empty array, not nil (schema requires presence)")
	}
}

func TestTableReservation_PushOnlyTock(t *testing.T) {
	a := newTRAdapter(t)
	cookies := []chrome.Cookie{
		{HostKey: ".exploretock.com", Name: "tk", Value: "v"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	var env sessionEnvelope
	b, _ := os.ReadFile(filepath.Join(a.configDir, "session.json"))
	_ = json.Unmarshal(b, &env)
	if len(env.TockCookies) != 1 {
		t.Errorf("tock_cookies: got %d, want 1", len(env.TockCookies))
	}
	if env.OpentableCookies == nil {
		t.Errorf("opentable_cookies should be empty array, not nil")
	}
}

func TestTableReservation_PushFiltersEmptyValueCookies(t *testing.T) {
	a := newTRAdapter(t)
	cookies := []chrome.Cookie{
		{HostKey: ".opentable.com", Name: "ot_real", Value: "actual"},
		{HostKey: ".opentable.com", Name: "ot_consent", Value: ""}, // dropped
		{HostKey: ".exploretock.com", Name: "tk_real", Value: "actual"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	var env sessionEnvelope
	b, _ := os.ReadFile(filepath.Join(a.configDir, "session.json"))
	_ = json.Unmarshal(b, &env)
	if len(env.OpentableCookies) != 1 || env.OpentableCookies[0].Name != "ot_real" {
		t.Errorf("opentable should only have ot_real, got %v", env.OpentableCookies)
	}
}

func TestTableReservation_PushNoMatchingCookies_NoOp(t *testing.T) {
	a := newTRAdapter(t)
	cookies := []chrome.Cookie{
		{HostKey: ".github.com", Name: "x", Value: "1"}, // wrong domain
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.configDir, "session.json")); err == nil {
		t.Errorf("session.json should NOT exist when no cookies match either network")
	}
}

func TestTableReservation_PushNoCookies_NoOp(t *testing.T) {
	a := newTRAdapter(t)
	if err := a.Push([]chrome.Cookie{}); err != nil {
		t.Fatalf("Push empty: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.configDir, "session.json")); err == nil {
		t.Errorf("session.json should NOT exist when cookies slice is empty")
	}
}

func TestTableReservation_PushFileMode(t *testing.T) {
	a := newTRAdapter(t)
	if err := a.Push([]chrome.Cookie{
		{HostKey: ".opentable.com", Name: "n", Value: "v"},
	}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(a.configDir, "session.json"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("session.json mode: got %o, want 0600", info.Mode().Perm())
	}
}

func TestTableReservation_AtomicWriteNoLeftoverTmp(t *testing.T) {
	a := newTRAdapter(t)
	_ = a.Push([]chrome.Cookie{
		{HostKey: ".opentable.com", Name: "n", Value: "v"},
	})
	sessionPath := filepath.Join(a.configDir, "session.json")
	if _, err := os.Stat(sessionPath + ".agentcookie.tmp"); err == nil {
		t.Errorf("leftover .tmp file after Push")
	}
}

func TestChromeExpiresToRFC3339_SessionCookieGetsFarFuture(t *testing.T) {
	got := chromeExpiresToRFC3339(0)
	// Schema requires expires to be present and parseable as a date.
	// Far-future is the design choice that satisfies "always-present"
	// without misrepresenting the cookie as already-expired.
	if !strings.HasPrefix(got, "2099-") {
		t.Errorf("session cookie expires: got %q, want 2099-* prefix", got)
	}
}

func TestChromeExpiresToRFC3339_PersistentCookieConverts(t *testing.T) {
	// Chrome epoch microseconds for 2026-01-01T00:00:00Z.
	// 2026-01-01 in Unix micros = 1767225600 * 1_000_000
	unixMicros := int64(1767225600) * 1_000_000
	chromeUTC := unixMicros + chromeEpochDeltaMicros
	got := chromeExpiresToRFC3339(chromeUTC)
	if !strings.HasPrefix(got, "2026-01-01") {
		t.Errorf("expires conversion: got %q, want 2026-01-01 prefix", got)
	}
}

func TestTableReservation_PushIsIdempotent(t *testing.T) {
	a := newTRAdapter(t)
	cookies := []chrome.Cookie{
		{HostKey: ".opentable.com", Name: "n", Value: "v", Path: "/", ExpiresUTC: 0},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(a.configDir, "session.json"))
	// Sleep is undesirable; instead, parse and compare ignoring
	// updated_at which advances per call.
	if err := a.Push(cookies); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(a.configDir, "session.json"))

	var e1, e2 sessionEnvelope
	_ = json.Unmarshal(first, &e1)
	_ = json.Unmarshal(second, &e2)
	// updated_at changes; everything else should match.
	e1.UpdatedAt = ""
	e2.UpdatedAt = ""
	b1, _ := json.Marshal(e1)
	b2, _ := json.Marshal(e2)
	if string(b1) != string(b2) {
		t.Errorf("second push changed content beyond updated_at\n  first:  %s\n  second: %s", b1, b2)
	}
}
