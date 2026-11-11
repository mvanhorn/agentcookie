//go:build linux

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/credentialguard"
	"github.com/mvanhorn/agentcookie/internal/source"
)

func TestCDPReaderDoesNotOpenBrowserDatabases(t *testing.T) {
	h := New(t)
	_ = h.Browser()
	tracer, err := exec.LookPath("strace")
	if err != nil {
		if os.Getenv("AGENTCOOKIE_E2E_REQUIRED") == "1" {
			t.Fatal("strace is required for database read guard")
		}
		t.Skip("strace unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(h.Home, "reader-openat.log")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tracer, "-f", "-e", "trace=openat,openat2", "-o", trace, executable, "-test.run=^TestCDPTraceReaderChild$")
	cmd.Env = append(os.Environ(), "AGENTCOOKIE_E2E_TRACE_CDP="+h.Endpoint)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("trace isolated reader: %v: %s", err, out)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		// The CDP-only subprocess must never open any running fixture profile file.
		// This is stronger than filtering just Cookies/SQLite/LevelDB database names.
		if strings.Contains(line, h.ProfileDir) {
			t.Fatalf("CDP source opened running browser profile: %s", line)
		}
	}
}
func TestCDPTraceReaderChild(t *testing.T) {
	endpoint := os.Getenv("AGENTCOOKIE_E2E_TRACE_CDP")
	if endpoint == "" {
		t.Skip("isolated reader subprocess only")
	}
	defer credentialguard.ForbidRealProviders()()
	r, err := source.NewCDP(endpoint, source.Options{}, source.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := r.Read(ctx, &config.Blocklist{Version: 1}); err != nil {
		t.Fatal(err)
	}
}
