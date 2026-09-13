package cli

import (
	"context"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
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
