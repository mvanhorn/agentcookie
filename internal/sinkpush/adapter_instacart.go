package sinkpush

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// InstacartAdapter pushes Instacart cookies into instacart-pp-cli's
// session cache via the CLI's `auth paste` command. Strategy:
//
//  1. Filter cookies to instacart.com hosts (sink does this; adapter
//     receives the filtered set).
//  2. Format as a Cookie header value ("name=value; name=value; ...")
//     mirroring the hack/dump-instacart reference flow that has been
//     proven end-to-end on the Mac mini sink.
//  3. exec `<cli> auth paste` with the header on stdin. instacart-pp-cli
//     parses the header, writes its own session.json, and exits 0.
//
// After Push completes, future invocations of instacart-pp-cli (over
// SSH, via Hermes, anywhere) read from the freshly-written session.json
// and never touch Chrome cookies or Keychain.
type InstacartAdapter struct {
	// binary is the resolved path. Auto-detected via NewInstacart;
	// overridable in tests by constructing the struct directly.
	binary string
}

// NewInstacart returns an adapter that finds the instacart CLI. It checks:
//  1. ~/.local/bin/instacart-pp-cli (printing-press installer path)
//  2. ~/go/bin/instacart-pp-cli (canonical go install location)
//  3. ~/bin/instacart-pp-cli (user bin directory)
//  4. `instacart-pp-cli` on PATH via exec.LookPath
//  5. `instacart` on PATH (common alias name)
func NewInstacart() *InstacartAdapter {
	return &InstacartAdapter{binary: findPPCLI("instacart-pp-cli", "instacart")}
}

func (a *InstacartAdapter) Name() string { return "instacart-pp-cli" }

// resolveBinary returns the binary path to use. If the stored binary path
// is executable (e.g., set directly in tests), use it. Otherwise, re-resolve
// via findPPCLI to handle binaries installed after init() and to avoid using
// a non-executable cached path that would shadow a valid executable elsewhere.
func (a *InstacartAdapter) resolveBinary() string {
	if a.binary != "" && isExecutableFile(a.binary) {
		return a.binary
	}
	return findPPCLI("instacart-pp-cli", "instacart")
}

func (a *InstacartAdapter) CLIBinary() string { return a.resolveBinary() }

func (a *InstacartAdapter) IsInstalled() bool {
	return isExecutableFile(a.resolveBinary())
}

func (a *InstacartAdapter) CookieHostPatterns() []string {
	// Instacart sets cookies on multiple instacart.com subdomains
	// (www.instacart.com, .instacart.com, etc.). The domain pair covers
	// them all without matching look-alike hosts (instacart.attacker.com).
	return DomainHostPatterns("instacart.com")
}

// Push shells out to `<cli> auth paste`, sending the Cookie header
// value on stdin. Returns the exec error wrapped with stderr context
// when non-zero exit.
func (a *InstacartAdapter) Push(cookies []chrome.Cookie) error {
	header := formatCookieHeader(cookies)
	if header == "" {
		// Defensive: RunAll's filter should have caught this and
		// reported Skipped, but if a caller invokes Push directly
		// with an empty/all-empty-value set, return without
		// invoking the CLI.
		return nil
	}

	binary := a.resolveBinary()
	cmd := exec.Command(binary, "auth", "paste")
	cmd.Stdin = strings.NewReader(header)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("instacart-pp-cli auth paste: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// formatCookieHeader joins cookies into a single Cookie header value
// ("name=value; name=value; ..."). Cookies whose Value is empty are
// dropped -- they carry no auth signal and instacart-pp-cli's parser
// treats empty values as cookie deletes, which would clear good
// state if any equivalent cookie already lived in the session cache.
//
// Output mirrors the format hack/dump-instacart/main.go has been
// using successfully against instacart-pp-cli auth paste.
func formatCookieHeader(cookies []chrome.Cookie) string {
	if len(cookies) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c.Value == "" {
			continue
		}
		pairs = append(pairs, c.Name+"="+c.Value)
	}
	return strings.Join(pairs, "; ")
}
