package chrome

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestProbeCookiesFile_HealthyV10Bridge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)

	in := []Cookie{
		{HostKey: ".instacart.com", Name: "session", Value: "real-session-token", Path: "/"},
		{HostKey: ".github.com", Name: "user_session", Value: "github-token", Path: "/"},
		{HostKey: ".claude.ai", Name: "csrf", Value: "csrf-value", Path: "/"},
	}
	if _, err := WriteCookies(path, in, testKey); err != nil {
		t.Fatalf("WriteCookies: %v", err)
	}

	probe, err := ProbeCookiesFile(path, testKey, 3)
	if err != nil {
		t.Fatalf("ProbeCookiesFile: %v", err)
	}
	if !probe.OK() {
		t.Errorf("expected probe.OK()=true, got %#v (summary=%q)", probe, probe.Summary())
	}
	if probe.RowsChecked != 3 {
		t.Errorf("RowsChecked: got %d, want 3", probe.RowsChecked)
	}
	if probe.AppBoundLeaks != 0 {
		t.Errorf("AppBoundLeaks: got %d, want 0", probe.AppBoundLeaks)
	}
	if probe.MetaVersion != "18" {
		t.Errorf("MetaVersion: got %q, want 18", probe.MetaVersion)
	}
}

func TestProbeCookiesFile_DetectsAppBoundPrefixRegression(t *testing.T) {
	// Simulate the v0.8 (pre-v0.9) write path: encrypted_value carries the
	// App-Bound SHA256(host_key) prefix in its plaintext. The probe must
	// catch this because PP CLIs on kooky v0.2.2 would silently corrupt
	// every cookie they read.
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)

	host := ".instacart.com"
	value := "real-session-token"
	withPrefix := prependAppBoundPrefix([]byte(value), host)
	enc, err := encryptValueBytes(withPrefix, testKey)
	if err != nil {
		t.Fatalf("encryptValueBytes: %v", err)
	}

	db, _ := sql.Open("sqlite3", "file:"+path)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta(key, value) VALUES ('version', '18')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cookies (creation_utc, host_key, top_frame_site_key, name, value, encrypted_value, path, expires_utc, is_secure, is_httponly, last_access_utc, has_expires, is_persistent, priority, samesite, source_scheme, source_port, last_update_utc, source_type, has_cross_site_ancestor) VALUES (0, ?, '', 'session', '', ?, '/', 0, 1, 0, 0, 0, 0, 1, 0, 2, 443, 0, 0, 1)`, host, enc); err != nil {
		t.Fatal(err)
	}

	probe, err := ProbeCookiesFile(path, testKey, 3)
	if err != nil {
		t.Fatalf("ProbeCookiesFile: %v", err)
	}
	if probe.OK() {
		t.Errorf("expected probe.OK()=false (App-Bound regression), got %#v", probe)
	}
	if probe.AppBoundLeaks != 1 {
		t.Errorf("AppBoundLeaks: got %d, want 1", probe.AppBoundLeaks)
	}
	if probe.RowsChecked != 1 {
		t.Errorf("RowsChecked: got %d, want 1", probe.RowsChecked)
	}
}

func TestProbeCookiesFile_ReportsMissingMetaVersion(t *testing.T) {
	// File has cookies but no meta table. probe.MetaVersion should be empty
	// so the caller can distinguish "meta intentionally not set" from "we
	// did write meta.version=18".
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)

	enc, _ := encryptValueBytes([]byte("v"), testKey)
	db, _ := sql.Open("sqlite3", "file:"+path)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO cookies (creation_utc, host_key, top_frame_site_key, name, value, encrypted_value, path, expires_utc, is_secure, is_httponly, last_access_utc, has_expires, is_persistent, priority, samesite, source_scheme, source_port, last_update_utc, source_type, has_cross_site_ancestor) VALUES (0, '.a.com', '', 'n', '', ?, '/', 0, 0, 0, 0, 0, 0, 1, 0, 2, 443, 0, 0, 1)`, enc); err != nil {
		t.Fatal(err)
	}

	probe, err := ProbeCookiesFile(path, testKey, 3)
	if err != nil {
		t.Fatalf("ProbeCookiesFile: %v", err)
	}
	if probe.MetaVersion != "" {
		t.Errorf("MetaVersion with missing meta: got %q, want empty", probe.MetaVersion)
	}
	if probe.OK() {
		t.Errorf("OK() with missing meta should be false")
	}
}

func TestProbeCookiesFile_EmptyDBReturnsZeroRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)
	probe, err := ProbeCookiesFile(path, testKey, 3)
	if err != nil {
		t.Fatalf("ProbeCookiesFile: %v", err)
	}
	if probe.RowsChecked != 0 {
		t.Errorf("RowsChecked on empty db: got %d, want 0", probe.RowsChecked)
	}
	if probe.OK() {
		t.Errorf("OK() on empty db should be false")
	}
}

func TestProbeCookiesFile_FileMissingReturnsError(t *testing.T) {
	_, err := ProbeCookiesFile(filepath.Join(t.TempDir(), "does-not-exist"), testKey, 3)
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestProbeResult_SummaryShape(t *testing.T) {
	ok := ProbeResult{RowsChecked: 5, AppBoundLeaks: 0, MetaVersion: "18"}
	if got := ok.Summary(); got == "" {
		t.Error("ok Summary empty")
	}
	bad := ProbeResult{RowsChecked: 5, AppBoundLeaks: 3, MetaVersion: "24"}
	got := bad.Summary()
	if got == "" {
		t.Error("bad Summary empty")
	}
	// Should include the leak count and meta.version explicitly so a sink
	// log line is actionable without rerunning the probe.
	if !contains(got, "3") || !contains(got, "24") {
		t.Errorf("bad Summary missing details: %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
