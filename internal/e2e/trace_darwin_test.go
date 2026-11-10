//go:build darwin

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/credentialguard"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
)

type geckoTraceInput struct {
	Profile, StateDir, Executable string
	BiDiPort, CompanionPort       int
}

// TestGeckoReaderDoesNotOpenLiveDatabases is the macOS fs_usage gate. It traces
// only the fixture reader and fixture broker PIDs, never the user's browsers or
// a system-wide process set. A positive sentinel proves tracing was active
// before the actual read. Missing authorization/tooling fails the requested gate.
func TestGeckoReaderDoesNotOpenLiveDatabases(t *testing.T) {
	g := NewGecko(t)
	seedGecko(t, g)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	options := g.ReaderOptions()
	options.Containers = []string{"default"}
	// Establish the persistent broker first, so both exact PIDs can be traced.
	reader, err := geckoreader.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Read(ctx, &config.Blocklist{Version: 1}); err != nil {
		reader.Close()
		t.Fatal(err)
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	brokerPID := g.BrokerState().PID
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(geckoTraceInput{Profile: options.Profile, StateDir: options.StateDir, Executable: options.Executable, BiDiPort: options.BiDiPort, CompanionPort: options.CompanionPort})
	if err != nil {
		t.Fatal(err)
	}
	start := filepath.Join(g.Home, "trace-start")
	probe := filepath.Join(g.Home, "trace-positive-probe.txt")
	if err = os.WriteFile(probe, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, testBinary, "-test.run=^TestGeckoTraceReaderChild$")
	child.Env = append(os.Environ(), "AGENTCOOKIE_GECKO_TRACE_OPTIONS="+string(encoded), "AGENTCOOKIE_GECKO_TRACE_START="+start, "AGENTCOOKIE_GECKO_TRACE_PROBE="+probe)
	childLog, err := os.Create(filepath.Join(g.Home, "trace-reader.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer childLog.Close()
	child.Stdout = childLog
	child.Stderr = childLog
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	childExited := false
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	defer func() {
		if childExited {
			return
		}
		_ = child.Process.Kill()
		select {
		case <-childDone:
		case <-time.After(3 * time.Second):
		}
	}()
	tracePath := filepath.Join(g.Home, "reader-fs-usage.log")
	traceLog, err := os.Create(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	defer traceLog.Close()
	// sudo -n never prompts, changes permissions, or weakens tracing restrictions.
	// fs_usage's own -t bounds the privileged process even if the test is canceled.
	tracer := exec.Command("/usr/bin/sudo", "-n", "/usr/bin/fs_usage", "-w", "-f", "filesys", "-t", "20", strconv.Itoa(child.Process.Pid), strconv.Itoa(brokerPID))
	tracer.Stdout = traceLog
	tracer.Stderr = traceLog
	if err = tracer.Start(); err != nil {
		t.Fatalf("start fixture fs_usage: %v", err)
	}
	traceExited := false
	traceDone := make(chan error, 1)
	go func() { traceDone <- tracer.Wait() }()
	defer func() {
		if traceExited {
			return
		}
		_ = tracer.Process.Signal(syscall.SIGINT)
		select {
		case <-traceDone:
		case <-time.After(22 * time.Second):
			t.Error("fixture fs_usage did not exit within its configured deadline")
		}
	}()
	active := false
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(tracePath)
		if strings.Contains(string(data), "trace-positive-probe.txt") {
			active = true
			break
		}
		select {
		case err := <-traceDone:
			traceExited = true
			t.Fatalf("fs_usage unavailable before positive probe: %v: %s", err, data)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !active {
		t.Fatal("fs_usage captured no positive fixture probe; refusing an unobserved pass")
	}
	if err = os.WriteFile(start, []byte("start"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-childDone:
		childExited = true
		if err != nil {
			data, _ := os.ReadFile(filepath.Join(g.Home, "trace-reader.log"))
			t.Fatalf("traced reader: %v: %s", err, data)
		}
	case err = <-traceDone:
		traceExited = true
		t.Fatalf("fs_usage ended before the live read completed; refusing partial coverage: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Allow fs_usage's output buffer to flush, then wait for its bounded capture.
	select {
	case err = <-traceDone:
		traceExited = true
		if err != nil {
			t.Fatalf("fs_usage failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if forbiddenGeckoDatabaseTrace(line, g.Profile) {
			t.Fatalf("source opened a running Gecko database: %s", line)
		}
	}
	if g.BrokerState().PID != brokerPID {
		t.Fatal("broker changed during tracing; new process was not observed")
	}
}

func TestGeckoTraceReaderChild(t *testing.T) {
	raw := os.Getenv("AGENTCOOKIE_GECKO_TRACE_OPTIONS")
	if raw == "" {
		t.Skip("isolated fs_usage reader only")
	}
	defer credentialguard.ForbidRealProviders()()
	var input geckoTraceInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	options := geckoreader.Options{Name: "firefox", Profile: input.Profile, Mode: "live", StateDir: input.StateDir, Executable: input.Executable, BiDiPort: input.BiDiPort, CompanionPort: input.CompanionPort, Containers: []string{"default"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start, probe := os.Getenv("AGENTCOOKIE_GECKO_TRACE_START"), os.Getenv("AGENTCOOKIE_GECKO_TRACE_PROBE")
	for {
		if _, err := os.Stat(start); err == nil {
			break
		}
		if _, err := os.ReadFile(probe); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	reader, err := geckoreader.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	snap, err := reader.Read(ctx, &config.Blocklist{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if snap.ReaderMode != "live" {
		t.Fatalf("expected actual live read, got %s", snap.ReaderMode)
	}
	fmt.Printf("fixture live read complete: %d cookies\n", len(snap.Cookies))
}
