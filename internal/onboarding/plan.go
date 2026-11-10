// Package onboarding builds reviewable install plans without pairing, installing
// services, launching browsers, or reading credential stores.
package onboarding

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mvanhorn/agentcookie/internal/browsers"
	"gopkg.in/yaml.v3"
)

// SourceInput contains the operator's explicit choices and the dry-read result.
// HeldSites must be shown with their container names before AcceptHeldSites.
type SourceInput struct {
	Browser, Profile, Reader, StorageReadMode string
	BiDiPort, CompanionPort                   int
	Containers                                []string
	ContainerSites                            map[string][]string
	HeldSites                                 []string
	AcceptHeldSites                           bool
}

// SourcePlan emits a YAML browser block, never a guessed Chrome DB path.
func SourcePlan(in SourceInput) (string, error) {
	if in.Browser == "" {
		return "", fmt.Errorf("which browser do you log in with? --browser is required")
	}
	entry, err := browsers.Lookup(in.Browser)
	if err != nil {
		return "", err
	}
	in.Browser = entry.Name
	if len(in.HeldSites) > 0 && !in.AcceptHeldSites {
		return "", fmt.Errorf("unresolved container sites: %s; choose --containers/--container-sites or explicitly accept holding", strings.Join(in.HeldSites, "; "))
	}
	if in.Reader == "" {
		in.Reader = "auto"
	}
	if in.Reader != "auto" && in.Reader != "live" && in.Reader != "file" {
		return "", fmt.Errorf("reader must be auto, live or file")
	}
	if in.StorageReadMode == "" {
		in.StorageReadMode = "intercept"
	}
	if in.StorageReadMode != "request" && in.StorageReadMode != "intercept" {
		return "", fmt.Errorf("storage read mode must be request or intercept")
	}
	if in.BiDiPort == 0 {
		in.BiDiPort = 9225
	}
	if in.CompanionPort == 0 {
		in.CompanionPort = 9226
	}
	if in.BiDiPort < 1024 || in.BiDiPort > 65535 || in.CompanionPort < 1024 || in.CompanionPort > 65535 || in.BiDiPort == in.CompanionPort {
		return "", fmt.Errorf("BiDi and companion ports must be distinct in 1024..65535")
	}
	b := map[string]any{"name": in.Browser, "profile": in.Profile, "reader": in.Reader}
	if entry.Family == browsers.Gecko {
		b["bidi_port"] = in.BiDiPort
		b["companion_port"] = in.CompanionPort
		b["storage_read_mode"] = in.StorageReadMode
		if in.Containers == nil {
			in.Containers = []string{}
		}
		if in.ContainerSites == nil {
			in.ContainerSites = map[string][]string{}
		}
		b["containers"] = in.Containers
		b["container_sites"] = in.ContainerSites
	}
	data, err := yaml.Marshal(map[string]any{"browser": b})
	return string(data), err
}

// DefaultDestination applies only to new installs. Existing YAML is preserved.
func DefaultDestination(goos string, gui bool) string {
	if goos == "darwin" && gui {
		return "live_chrome"
	}
	return "attach"
}

// ReadyHooks makes the order of effects explicit and independently testable.
type ReadyHooks struct {
	Prepare func(context.Context) error
	Restore func(context.Context) (bool, error)
	Open    func(context.Context) error
	Install func(context.Context) error
}

func ReadyBeforeInstall(ctx context.Context, h ReadyHooks) error {
	if h.Prepare == nil || h.Restore == nil {
		return fmt.Errorf("destination readiness hooks required")
	}
	if err := h.Prepare(ctx); err != nil {
		return err
	}
	ready, err := h.Restore(ctx)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("destination not ready; window and daemon installation withheld")
	}
	if h.Open != nil {
		if err := h.Open(ctx); err != nil {
			return err
		}
	}
	if h.Install != nil {
		return h.Install(ctx)
	}
	return nil
}

// ChromeLauncher delegates to the authenticated sink. Starting Chrome directly
// would race restoration and expose a window with stale session state.
func ChromeLauncher(binary, configDir, name string) (string, error) {
	if !filepath.IsAbs(binary) || !filepath.IsAbs(configDir) {
		return "", fmt.Errorf("launcher paths must be absolute")
	}
	if name == "" || strings.ContainsAny(binary+configDir+name, "\x00\n\r") {
		return "", fmt.Errorf("invalid launcher input")
	}
	return "#!/bin/sh\nset -eu\nexec " + shellQuote(binary) + " --config-dir " + shellQuote(configDir) + " destination open " + shellQuote(name) + "\n", nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
