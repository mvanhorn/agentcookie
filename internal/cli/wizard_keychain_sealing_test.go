package cli

import (
	"strings"
	"sync/atomic"
	"testing"
)

// TestPrimaryStrategy_OffByDefault_DoesNotCreateMasterKey proves the
// v0.12-with-deferred-U12 default: a wizard install with no sealing
// flag does NOT call keystore.CreateMasterKey, even after the Chrome
// Safe Storage delete-and-recreate step succeeds. The detail message
// surfaces the "sealing not enabled" line so log readers know why no
// master key was installed.
func TestPrimaryStrategy_OffByDefault_DoesNotCreateMasterKey(t *testing.T) {
	origSec := execSecurityFunc
	origMaster := createMasterKeyFunc
	t.Cleanup(func() {
		execSecurityFunc = origSec
		createMasterKeyFunc = origMaster
	})

	execSecurityFunc = func(args ...string) (string, error) {
		return "ok", nil
	}
	var masterCalls atomic.Int32
	createMasterKeyFunc = func(bin string, extras []string) error {
		masterCalls.Add(1)
		return nil
	}

	strategies := buildStrategies(nil, false, false)
	if len(strategies) < 1 {
		t.Fatal("expected at least one strategy")
	}
	if strategies[0].name != "delete-and-recreate-with-T" {
		t.Fatalf("primary strategy name: got %q, want delete-and-recreate-with-T", strategies[0].name)
	}
	detail, err := strategies[0].apply()
	if err != nil {
		t.Fatalf("primary strategy apply: %v", err)
	}
	if !strings.Contains(detail, "sealing not enabled") {
		t.Errorf("expected sealing-disabled message in detail, got %q", detail)
	}
	if got := masterCalls.Load(); got != 0 {
		t.Errorf("CreateMasterKey called %d times; want 0 in default-off mode", got)
	}
}

// TestPrimaryStrategy_WithSealingFlag_CreatesMasterKey: with the
// hidden --enable-sealing flag wired through, the strategy DOES call
// keystore.CreateMasterKey exactly once.
func TestPrimaryStrategy_WithSealingFlag_CreatesMasterKey(t *testing.T) {
	origSec := execSecurityFunc
	origMaster := createMasterKeyFunc
	t.Cleanup(func() {
		execSecurityFunc = origSec
		createMasterKeyFunc = origMaster
	})

	execSecurityFunc = func(args ...string) (string, error) {
		return "ok", nil
	}
	var masterCalls atomic.Int32
	var masterBinary string
	var masterExtras []string
	createMasterKeyFunc = func(bin string, extras []string) error {
		masterCalls.Add(1)
		masterBinary = bin
		masterExtras = extras
		return nil
	}

	extras := []string{"/Users/me/go/bin/instacart-pp-cli"}
	strategies := buildStrategies(extras, true, false)
	if _, err := strategies[0].apply(); err != nil {
		t.Fatalf("primary strategy apply: %v", err)
	}
	if got := masterCalls.Load(); got != 1 {
		t.Errorf("CreateMasterKey called %d times; want 1 in sealing-on mode", got)
	}
	if masterBinary == "" {
		t.Errorf("CreateMasterKey called with empty agentcookie binary path")
	}
	if len(masterExtras) != len(extras) || masterExtras[0] != extras[0] {
		t.Errorf("CreateMasterKey extras: got %v, want %v", masterExtras, extras)
	}
}

// TestBuildStrategies_ChainShapeUnchangedByFlag proves the strategy
// chain length and ordering do not change when --enable-sealing is
// toggled. Only the primary strategy's apply behavior differs.
func TestBuildStrategies_ChainShapeUnchangedByFlag(t *testing.T) {
	off := buildStrategies(nil, false, false)
	on := buildStrategies(nil, true, false)
	if len(off) != len(on) {
		t.Errorf("strategy chain length differs by flag: off=%d on=%d", len(off), len(on))
	}
	for i := range off {
		if off[i].name != on[i].name {
			t.Errorf("strategy[%d] name differs by flag: off=%q on=%q", i, off[i].name, on[i].name)
		}
	}
}
