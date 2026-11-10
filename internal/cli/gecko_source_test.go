package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko/profiles"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPersistedGeckoSelectorDoesNotAcceptRecreatedProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldDir := common.ConfigDir
	common.ConfigDir = filepath.Join(home, "config")
	defer func() { common.ConfigDir = oldDir }()
	p := filepath.Join(home, "profile")
	os.MkdirAll(p, 0700)
	os.WriteFile(filepath.Join(p, "cookies.sqlite"), nil, 0600)
	os.WriteFile(filepath.Join(p, "times.json"), []byte(`{"created":1}`), 0600)
	cfg := &config.SourceConfig{Browser: config.BrowserRef{Name: "firefox", Profile: p, Reader: "file"}}
	r, e := openGeckoReader(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
	os.WriteFile(filepath.Join(p, "times.json"), []byte(`{"created":2}`), 0600)
	if r, e = openGeckoReader(context.Background(), cfg); e == nil {
		r.Close()
		t.Fatal("persisted selector bypassed identity guard")
	}
	ctx := context.WithValue(context.Background(), geckoExplicitProfileKey{}, true)
	r, e = openGeckoReader(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
}
func TestCachedGeckoReaderRevalidatesIdentityEveryCycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldDir := common.ConfigDir
	common.ConfigDir = filepath.Join(home, "config")
	defer func() { common.ConfigDir = oldDir }()
	p := filepath.Join(home, "profile")
	os.MkdirAll(p, 0700)
	db, e := sql.Open("sqlite3", filepath.Join(p, "cookies.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`PRAGMA user_version=17;CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,path TEXT,expiry INTEGER,isSecure INTEGER,isHttpOnly INTEGER,sameSite INTEGER,originAttributes TEXT)`)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	os.WriteFile(filepath.Join(p, "times.json"), []byte(`{"created":1}`), 0600)
	cfg := &config.SourceConfig{Browser: config.BrowserRef{Name: "firefox", Profile: p, Reader: "file"}}
	ctx, closeReaders := withGeckoSession(context.WithValue(context.Background(), geckoExplicitProfileKey{}, true))
	defer closeReaders()
	snap, e := readGeckoSnapshot(ctx, cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	capture, ok := snap.RoutingData.(*geckoreader.Capture)
	if !ok || capture.Profile.Created != 1 || capture.Profile.Dir != p {
		t.Fatalf("missing pinned snapshot identity: %+v", capture)
	}
	os.WriteFile(filepath.Join(p, "times.json"), []byte(`{"created":2}`), 0600)
	for range 2 {
		if _, e = readGeckoSnapshot(ctx, cfg, nil); e == nil {
			t.Fatal("cached daemon accepted replacement with startup-only explicit selector")
		}
	}
	raw, e := os.ReadFile(filepath.Join(common.ConfigDir, "state", "gecko-profile-firefox.json"))
	if e != nil {
		t.Fatal(e)
	}
	var pinned profiles.ProfileIdentity
	if e = json.Unmarshal(raw, &pinned); e != nil || pinned.Created != 1 {
		t.Fatal("failed read committed new identity", string(raw), e)
	}
}
func TestFailedGeckoOpenDoesNotCommitAcceptance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldDir := common.ConfigDir
	common.ConfigDir = filepath.Join(home, "config")
	defer func() { common.ConfigDir = oldDir }()
	p := filepath.Join(home, "profile")
	os.MkdirAll(p, 0700)
	os.WriteFile(filepath.Join(p, "cookies.sqlite"), nil, 0600)
	cfg := &config.SourceConfig{Browser: config.BrowserRef{Name: "firefox", Profile: p, Reader: "invalid"}}
	ctx := context.WithValue(context.Background(), geckoExplicitProfileKey{}, true)
	if r, e := openGeckoReader(ctx, cfg); e == nil {
		r.Close()
		t.Fatal("invalid mode opened")
	}
	if _, e := os.Stat(filepath.Join(common.ConfigDir, "state", "gecko-profile-firefox.json")); !os.IsNotExist(e) {
		t.Fatal("failed open persisted accepted identity", e)
	}
}
func TestCachedGeckoDefaultReassignmentRefusesBeforeDatabaseOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldDir := common.ConfigDir
	common.ConfigDir = filepath.Join(home, "config")
	defer func() { common.ConfigDir = oldDir }()
	root := filepath.Join(home, ".mozilla", "firefox")
	if runtime.GOOS == "darwin" {
		root = filepath.Join(home, "Library", "Application Support", "Firefox")
	}
	for _, name := range []string{"a", "b"} {
		os.MkdirAll(filepath.Join(root, name), 0700)
		os.WriteFile(filepath.Join(root, name, "times.json"), []byte(`{"created":1}`), 0600)
	}
	db, e := sql.Open("sqlite3", filepath.Join(root, "a", "cookies.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`PRAGMA user_version=17;CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,path TEXT,expiry INTEGER,isSecure INTEGER,isHttpOnly INTEGER,sameSite INTEGER,originAttributes TEXT)`)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	os.WriteFile(filepath.Join(root, "b", "cookies.sqlite"), []byte("must never open this replacement"), 0600)
	app := filepath.Join(home, "fixture-install")
	os.MkdirAll(app, 0700)
	hash := profiles.InstallHash(app)
	writeINI := func(selected string) {
		ini := fmt.Sprintf("[Profile0]\nName=First\nIsRelative=1\nPath=a\n[Profile1]\nName=Second\nIsRelative=1\nPath=b\n[Install%s]\nDefault=%s\nLocked=1\n", hash, selected)
		if e := os.WriteFile(filepath.Join(root, "profiles.ini"), []byte(ini), 0600); e != nil {
			t.Fatal(e)
		}
	}
	writeINI("a")
	cfg := &config.SourceConfig{Browser: config.BrowserRef{Name: "firefox", AppPath: app, Reader: "file"}}
	ctx, closeReaders := withGeckoSession(context.Background())
	defer closeReaders()
	if _, e = readGeckoSnapshot(ctx, cfg, nil); e != nil {
		t.Fatal(e)
	}
	writeINI("b")
	if _, e = readGeckoSnapshot(ctx, cfg, nil); e == nil || !strings.Contains(e.Error(), "profile identity changed") {
		t.Fatalf("default replacement not rejected by identity guard: %v", e)
	}
	if _, e = os.Stat(filepath.Join(root, "b", ".parentlock")); !os.IsNotExist(e) {
		t.Fatal("replacement profile was opened", e)
	}
}

func TestGeckoDBSCDiagnosticsAreNotApplicable(t *testing.T) {
	c := checkDBSCForSource(&config.SourceConfig{Browser: config.BrowserRef{Name: "firefox"}}, nil)
	if c.Severity != SeveritySkipped || !strings.Contains(c.Detail, "not applicable") {
		t.Fatalf("%+v", c)
	}
}
