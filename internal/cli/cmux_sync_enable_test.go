package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/cmuxconfig"
	"github.com/mvanhorn/agentcookie/internal/launchd"
)

func fakeCmuxBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func stubEnableSeams(t *testing.T) (setCalls *[]string, installed *[]launchd.Spec) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // keep logDir + os.Executable side effects in tmp
	sc := []string{}
	ins := []launchd.Spec{}
	origSet, origInstall := cmuxSyncSetMode, cmuxSyncInstallAgent
	cmuxSyncSetMode = func(path, mode, password string, now time.Time) (string, error) {
		sc = append(sc, mode)
		return path + ".bak", nil
	}
	cmuxSyncInstallAgent = func(spec launchd.Spec) error {
		ins = append(ins, spec)
		return nil
	}
	t.Cleanup(func() { cmuxSyncSetMode, cmuxSyncInstallAgent = origSet, origInstall })
	return &sc, &ins
}

func TestEnableCmuxLoop_NoOpWhenCmuxAbsent(t *testing.T) {
	setCalls, installed := stubEnableSeams(t)
	missing := filepath.Join(t.TempDir(), "no-cmux")
	if err := enableCmuxLoop(missing, true); err != nil {
		t.Fatalf("enable should be a clean no-op, got %v", err)
	}
	if len(*setCalls) != 0 || len(*installed) != 0 {
		t.Errorf("cmux absent should touch nothing: set=%v install=%v", *setCalls, *installed)
	}
}

func TestEnableCmuxLoop_WiresModeAndAgent(t *testing.T) {
	setCalls, installed := stubEnableSeams(t)
	if err := enableCmuxLoop(fakeCmuxBinary(t), true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(*setCalls) != 1 || (*setCalls)[0] != "allowAll" {
		t.Errorf("expected socketControlMode=allowAll set once, got %v", *setCalls)
	}
	if len(*installed) != 1 {
		t.Fatalf("expected one agent install, got %d", len(*installed))
	}
	spec := (*installed)[0]
	if spec.Role != launchd.RoleCmuxSync {
		t.Errorf("agent role = %q, want cmux-sync", spec.Role)
	}
	if len(spec.ExtraArgs) != 1 || spec.ExtraArgs[0] != "--watch" {
		t.Errorf("agent args = %v, want [--watch]", spec.ExtraArgs)
	}
}

func TestEnableCmuxLoop_NoAgentWhenCmuxConfigMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installed := []launchd.Spec{}
	origSet, origInstall := cmuxSyncSetMode, cmuxSyncInstallAgent
	cmuxSyncSetMode = func(string, string, string, time.Time) (string, error) {
		return "", cmuxconfig.ErrNotFound
	}
	cmuxSyncInstallAgent = func(spec launchd.Spec) error {
		installed = append(installed, spec)
		return nil
	}
	t.Cleanup(func() { cmuxSyncSetMode, cmuxSyncInstallAgent = origSet, origInstall })

	if err := enableCmuxLoop(fakeCmuxBinary(t), true); err != nil {
		t.Fatalf("missing cmux.json should be a clean no-op, got %v", err)
	}
	if len(installed) != 0 {
		t.Errorf("no agent should be installed when cmux.json is missing, got %d", len(installed))
	}
}

func TestCmuxSyncDisable_UninstallsAgent(t *testing.T) {
	var got launchd.Spec
	orig := cmuxSyncUninstallAgent
	cmuxSyncUninstallAgent = func(spec launchd.Spec) error { got = spec; return nil }
	t.Cleanup(func() { cmuxSyncUninstallAgent = orig })

	if err := cmuxSyncDisableCmd.RunE(cmuxSyncDisableCmd, nil); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got.Role != launchd.RoleCmuxSync {
		t.Errorf("disable should uninstall the cmux-sync agent, got role %q", got.Role)
	}
}

func TestMaybeAutoEnableCmux_GatedByNoCmuxFlag(t *testing.T) {
	calls := 0
	orig := cmuxAutoEnable
	cmuxAutoEnable = func(cmuxPath string, quiet bool) error { calls++; return nil }
	t.Cleanup(func() { cmuxAutoEnable = orig })

	origFlag := wizardNoCmux
	t.Cleanup(func() { wizardNoCmux = origFlag })

	wizardNoCmux = false
	maybeAutoEnableCmux()
	if calls != 1 {
		t.Errorf("expected auto-enable to fire when --no-cmux is off, got %d calls", calls)
	}

	wizardNoCmux = true
	maybeAutoEnableCmux()
	if calls != 1 {
		t.Errorf("--no-cmux should skip auto-enable; calls went to %d", calls)
	}
}
