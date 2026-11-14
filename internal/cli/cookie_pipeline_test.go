package cli

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko"
)

func TestReadConfiguredCookiesUsesCDPWithoutSQLiteFallback(t *testing.T) {
	previous := readCDPSource
	readCDPSource = func(_ context.Context, endpoint string) ([]chrome.Cookie, error) {
		if endpoint != "http://127.0.0.1:9230" {
			t.Fatalf("CDP endpoint = %q", endpoint)
		}
		return []chrome.Cookie{{HostKey: "example.com", Name: "session", Value: "value", Path: "/"}}, nil
	}
	t.Cleanup(func() { readCDPSource = previous })

	cfg := &config.SourceConfig{
		Chrome:    config.ChromeRef{DBPath: "/must-not-read/Cookies"},
		CDPSource: config.CDPSourceRef{Enabled: true, Endpoint: "http://127.0.0.1:9230"},
	}
	cookies, stats, err := readConfiguredCookies(context.Background(), cfg, &config.Blocklist{Version: 1}, nil, false, time.Now().UTC())
	if err != nil {
		t.Fatalf("read configured CDP cookies: %v", err)
	}
	if len(cookies) != 1 || cookies[0].HostKey != "example.com" {
		t.Fatalf("cookies = %#v", cookies)
	}
	if stats.totalRead != 1 {
		t.Fatalf("total read = %d, want 1", stats.totalRead)
	}
}

// geckoTestCookie is one row of a fixture Gecko cookies.sqlite.
type geckoTestCookie struct {
	host, name, value string
	expiryMS          int64
}

// writeGeckoProfile creates a single-profile Gecko root for browser under
// home (the layout for this OS) holding a current-schema cookies.sqlite with
// rows, and returns the cookies.sqlite path.
func writeGeckoProfile(t *testing.T, home, browser string, rows []geckoTestCookie) string {
	t.Helper()
	b, ok := gecko.Lookup(browser)
	if !ok {
		t.Fatalf("no Gecko browser %q", browser)
	}
	root := b.Roots(home, runtime.GOOS == "linux")[0]
	profile := filepath.Join(root, "Profiles", "test.default-release")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, filepath.Join(root, "profiles.ini"), "[Profile0]\nName=default-release\nIsRelative=1\nPath=Profiles/test.default-release\n")
	path := filepath.Join(profile, gecko.CookiesFile)
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE moz_cookies (id INTEGER PRIMARY KEY, originAttributes TEXT NOT NULL DEFAULT '', name TEXT, value TEXT, host TEXT, path TEXT, expiry INTEGER, lastAccessed INTEGER, creationTime INTEGER, isSecure INTEGER, isHttpOnly INTEGER, inBrowserElement INTEGER DEFAULT 0, sameSite INTEGER DEFAULT 0, schemeMap INTEGER DEFAULT 0, isPartitionedAttributeSet INTEGER DEFAULT 0, updateTime INTEGER, CONSTRAINT moz_uniqueid UNIQUE (name, host, path, originAttributes))`,
		`PRAGMA user_version = 17`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO moz_cookies (name, value, host, path, expiry, lastAccessed, creationTime, isSecure, isHttpOnly, sameSite, schemeMap) VALUES (?,?,?,'/',?,0,0,1,1,256,2)`,
			r.name, r.value, r.host, r.expiryMS); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func futureMS() int64 { return time.Now().Add(24 * time.Hour).UnixMilli() }

func TestReadConfiguredCookiesReadsGeckoProfile(t *testing.T) {
	home := t.TempDir()
	path := writeGeckoProfile(t, home, "zen", []geckoTestCookie{
		{".example.com", "session", "zen-value", futureMS()},
	})
	cfg := &config.SourceConfig{
		Chrome:  config.ChromeRef{DBPath: path},
		Browser: config.BrowserRef{Name: "zen"},
	}
	cookies, stats, err := readConfiguredCookies(context.Background(), cfg, &config.Blocklist{Version: 1}, nil, false, time.Now().UTC())
	if err != nil {
		t.Fatalf("readConfiguredCookies: %v", err)
	}
	if len(cookies) != 1 || cookies[0].Value != "zen-value" || stats.totalRead != 1 {
		t.Fatalf("cookies = %#v, totalRead = %d", cookies, stats.totalRead)
	}
}

func TestSourceCookieKeySkipsKeychainForGecko(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	firefox := writeGeckoProfile(t, home, "firefox", nil)
	var keychainReads int
	noKeychain := func(chrome.Browser) (string, error) {
		keychainReads++
		return "", errors.New("keychain read")
	}
	t.Cleanup(func() {
		if keychainReads != 0 {
			t.Errorf("read the Keychain %d time(s); no case here may", keychainReads)
		}
	})

	t.Run("configured", func(t *testing.T) {
		cfg := &config.SourceConfig{Chrome: config.ChromeRef{DBPath: firefox}, Browser: config.BrowserRef{Name: "firefox"}}
		key, err := sourceCookieKey(cfg, "", noKeychain)
		if err != nil || key != nil {
			t.Fatalf("key = %v, err = %v; want nil, nil", key, err)
		}
	})

	t.Run("flag overrides a Chromium config", func(t *testing.T) {
		cfg := &config.SourceConfig{Chrome: config.ChromeRef{DBPath: "/chrome/Default/Cookies"}, Browser: config.BrowserRef{Name: "chrome"}}
		key, err := sourceCookieKey(cfg, "firefox", noKeychain)
		if err != nil || key != nil {
			t.Fatalf("key = %v, err = %v; want nil, nil", key, err)
		}
		if cfg.Chrome.DBPath != firefox || cfg.Browser.Name != "firefox" {
			t.Errorf("cfg points at %q (%q), want the Firefox profile %q", cfg.Chrome.DBPath, cfg.Browser.Name, firefox)
		}
	})

	t.Run("Chromium flag over a Gecko config is refused", func(t *testing.T) {
		cfg := &config.SourceConfig{Chrome: config.ChromeRef{DBPath: firefox}, Browser: config.BrowserRef{Name: "firefox"}}
		if _, err := sourceCookieKey(cfg, "chrome", noKeychain); err == nil {
			t.Fatal("expected an error, not a Chrome key for a Firefox cookie file")
		}
	})

	t.Run("unknown flag lists both families", func(t *testing.T) {
		cfg := &config.SourceConfig{}
		_, err := sourceCookieKey(cfg, "vivaldi", noKeychain)
		if err == nil || !strings.Contains(err.Error(), "chrome") || !strings.Contains(err.Error(), "zen") {
			t.Fatalf("got %v, want an error listing Chromium and Gecko names", err)
		}
	})
}
