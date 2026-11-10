// Package launchd generates and installs LaunchAgent plists for the
// agentcookie source and sink daemons. LaunchAgents run inside the user's GUI
// login session, so they have the Keychain access and Chrome lifecycle
// privileges that SSH-launched processes lack.
package launchd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/mvanhorn/agentcookie/internal/macapp"
)

// Role identifies which side the plist drives.
type Role string

const (
	RoleSource   Role = "source"
	RoleSink     Role = "sink"
	RoleCmuxSync Role = "cmux-sync"
	// RoleProfileKeepalive keeps each Chrome profile paired with a
	// different Claude account loaded, so Claude in Chrome stays connected.
	RoleProfileKeepalive Role = "profile-keepalive"
)

// Spec is the input to plist generation. All paths must be absolute.
type Spec struct {
	Role       Role
	BinaryPath string
	LogDir     string
	ExtraArgs  []string
	// Launcher, when set, is the agentcookie.app launcher the job starts
	// through: it goes first in ProgramArguments, ahead of BinaryPath, and
	// the plist names the app in AssociatedBundleIdentifiers so macOS
	// lists the job as "agentcookie". Choose it with LauncherFor.
	Launcher string
}

// Label returns the launchd Label corresponding to the spec's role. Labels
// follow `dev.agentcookie.<role>`. Since LaunchAgents live in per-user
// ~/Library/LaunchAgents/ there is no risk of cross-user label collision.
func (s Spec) Label() string {
	return "dev.agentcookie." + string(s.Role)
}

// Render produces the plist XML for the spec.
func (s Spec) Render() ([]byte, error) {
	if s.BinaryPath == "" {
		return nil, fmt.Errorf("BinaryPath is required")
	}
	if s.LogDir == "" {
		return nil, fmt.Errorf("LogDir is required")
	}
	if s.Role != RoleSource && s.Role != RoleSink && s.Role != RoleCmuxSync && s.Role != RoleProfileKeepalive {
		return nil, fmt.Errorf("invalid Role %q", s.Role)
	}

	args := []string{string(s.Role)}
	args = append(args, s.ExtraArgs...)

	data := plistData{
		Label:         s.Label(),
		Launcher:      s.Launcher,
		BinaryPath:    s.BinaryPath,
		Args:          args,
		StandardOut:   filepath.Join(s.LogDir, string(s.Role)+".out.log"),
		StandardError: filepath.Join(s.LogDir, string(s.Role)+".err.log"),
	}

	var buf bytes.Buffer
	if err := plistTmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render plist: %w", err)
	}
	return buf.Bytes(), nil
}

// Path returns the file path the plist installs to under
// ~/Library/LaunchAgents/.
func (s Spec) Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return s.PathIn(home), nil
}

// PathIn returns the plist's path under home's ~/Library/LaunchAgents/.
func (s Spec) PathIn(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", s.Label()+".plist")
}

// Install writes the plist to disk and bootstraps it via launchctl. Idempotent:
// running Install on an already-installed spec produces a refresh, not a
// duplicate. Returns the installed plist path.
func Install(s Spec) (string, error) {
	xml, err := s.Render()
	if err != nil {
		return "", err
	}
	path, err := s.Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("mkdir LaunchAgents: %w", err)
	}
	if err := os.MkdirAll(s.LogDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir log dir: %w", err)
	}
	if err := os.WriteFile(path, xml, 0o644); err != nil {
		return "", fmt.Errorf("write plist: %w", err)
	}
	uid := strconv.Itoa(os.Getuid())
	domain := "gui/" + uid

	// bootout existing job (idempotent: ignore non-zero if not loaded).
	_ = exec.Command("launchctl", "bootout", domain+"/"+s.Label()).Run()
	// bootstrap fresh.
	cmd := exec.Command("launchctl", "bootstrap", domain, path)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return path, fmt.Errorf("launchctl bootstrap: %w", err)
	}
	return path, nil
}

// Uninstall is the inverse of Install. Removes the launchctl job and deletes
// the plist file. Idempotent.
func Uninstall(s Spec) error {
	uid := strconv.Itoa(os.Getuid())
	_ = exec.Command("launchctl", "bootout", "gui/"+uid+"/"+s.Label()).Run()
	path, err := s.Path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

// IsInstalled returns true when launchctl reports the spec's label as loaded.
func IsInstalled(s Spec) bool {
	uid := strconv.Itoa(os.Getuid())
	out, err := exec.Command("launchctl", "print", "gui/"+uid+"/"+s.Label()).Output()
	if err != nil {
		return false
	}
	return bytes.Contains(out, []byte(s.Label()))
}

type plistData struct {
	Label         string
	Launcher      string
	BinaryPath    string
	Args          []string
	StandardOut   string
	StandardError string
}

func (plistData) BundleID() string { return macapp.BundleID }

var xmlEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

var plistTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{"x": xmlEscape}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{x .Label}}</string>

  <key>ProgramArguments</key>
  <array>
{{if .Launcher}}    <string>{{x .Launcher}}</string>
{{end}}    <string>{{x .BinaryPath}}</string>
{{range .Args}}    <string>{{x .}}</string>
{{end}}  </array>
{{if .Launcher}}
  <key>AssociatedBundleIdentifiers</key>
  <array>
    <string>{{x .BundleID}}</string>
  </array>
{{end}}
  <key>RunAtLoad</key>
  <true/>

  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
    <key>Crashed</key>
    <true/>
  </dict>

  <key>ThrottleInterval</key>
  <integer>10</integer>

  <key>ProcessType</key>
  <string>Background</string>

  <key>StandardOutPath</key>
  <string>{{x .StandardOut}}</string>

  <key>StandardErrorPath</key>
  <string>{{x .StandardError}}</string>
</dict>
</plist>
`))
