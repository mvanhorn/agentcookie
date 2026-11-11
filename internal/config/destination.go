package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// DestinationRef selects durable live delivery or the explicit legacy ppcli lane.
type DestinationRef struct {
	Name       string            `yaml:"name,omitempty" json:"name,omitempty"`
	Kind       string            `yaml:"kind" json:"kind"`
	Required   bool              `yaml:"required" json:"required"`
	Chrome     DestinationChrome `yaml:"chrome,omitempty" json:"chrome,omitempty"`
	Endpoint   string            `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	ProbeHosts []string          `yaml:"probe_hosts,omitempty" json:"probe_hosts,omitempty"`
}
type DestinationChrome struct {
	UserDataDir string `yaml:"user_data_dir,omitempty" json:"user_data_dir,omitempty"`
	Port        int    `yaml:"port,omitempty" json:"port,omitempty"`
	Headed      *bool  `yaml:"headed,omitempty" json:"headed,omitempty"`
	Start       string `yaml:"start,omitempty" json:"start,omitempty"`
	Relaunch    string `yaml:"relaunch,omitempty" json:"relaunch,omitempty"`
	Path        string `yaml:"path,omitempty" json:"path,omitempty"`
}

func (c *SinkConfig) HasDestinations() bool { return c.Destination != nil || len(c.Destinations) > 0 }
func (c *SinkConfig) ResolveDestinations() ([]DestinationRef, error) {
	if c.Destination != nil && len(c.Destinations) > 0 {
		return nil, fmt.Errorf("use destination or destinations, not both")
	}
	ds := append([]DestinationRef(nil), c.Destinations...)
	if c.Destination != nil {
		ds = append(ds, *c.Destination)
	}
	if len(ds) == 0 {
		kind := "ppcli"
		if c.LiveCDP.Enabled {
			kind = "attach"
		}
		ds = []DestinationRef{{Kind: kind, Endpoint: c.LiveCDP.Endpoint}}
	}
	names := map[string]bool{}
	ports := map[int]bool{}
	profiles := map[string]bool{}
	endpoints := map[string]bool{}
	// Reserve explicit ports before assigning defaults, independent of list order.
	for _, d := range ds {
		if d.Kind == "live_chrome" && d.Chrome.Port != 0 {
			if d.Chrome.Port < 1024 || d.Chrome.Port > 65535 || ports[d.Chrome.Port] {
				return nil, fmt.Errorf("destination ports must be distinct, 1024..65535")
			}
			ports[d.Chrome.Port] = true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	for i := range ds {
		d := &ds[i]
		if d.Name == "" {
			d.Name = d.Kind
		}
		if !ValidDestinationName(d.Name) {
			return nil, fmt.Errorf("invalid destination name")
		}
		if names[d.Name] {
			return nil, fmt.Errorf("duplicate destination %q", d.Name)
		}
		names[d.Name] = true
		switch d.Kind {
		case "ppcli":
		case "attach":
			if d.Endpoint == "" {
				d.Endpoint = "http://127.0.0.1:9223"
			}
			u, e := url.Parse(d.Endpoint)
			if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" || u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
				return nil, fmt.Errorf("destination attach endpoint must be loopback HTTP")
			}
			port := u.Port()
			if port == "" {
				port = "80"
			}
			portNumber, e := strconv.Atoi(port)
			if e != nil || portNumber < 1 || portNumber > 65535 {
				return nil, fmt.Errorf("invalid attach port")
			}
			endpoint := "loopback:" + strconv.Itoa(portNumber)
			if endpoints[endpoint] {
				return nil, fmt.Errorf("destination attach endpoints must be distinct")
			}
			endpoints[endpoint] = true
		case "live_chrome":
			if d.Chrome.UserDataDir == "" {
				d.Chrome.UserDataDir = filepath.Join(home, ".agentcookie", "synced-chrome-"+d.Name)
			}
			d.Chrome.UserDataDir = ExpandTilde(d.Chrome.UserDataDir)
			abs, e := canonicalProfilePath(d.Chrome.UserDataDir)
			if e != nil {
				return nil, e
			}
			d.Chrome.UserDataDir = abs
			if DefaultProfileOverlap(abs, home) {
				return nil, fmt.Errorf("default browser profiles and their ancestors are not valid destinations")
			}
			for profile := range profiles {
				if pathsOverlap(profile, abs) {
					return nil, fmt.Errorf("destination profiles must be distinct and non-overlapping")
				}
			}
			profiles[abs] = true
			if d.Chrome.Port == 0 {
				d.Chrome.Port = 9224
				for ports[d.Chrome.Port] {
					d.Chrome.Port++
				}
				if d.Chrome.Port > 65535 {
					return nil, fmt.Errorf("no available destination ports")
				}
				ports[d.Chrome.Port] = true
			}
			endpoint := "loopback:" + strconv.Itoa(d.Chrome.Port)
			if endpoints[endpoint] {
				return nil, fmt.Errorf("destination endpoints must be distinct")
			}
			endpoints[endpoint] = true
			if d.Chrome.Start == "" {
				d.Chrome.Start = "on_boot"
			}
			if d.Chrome.Start != "on_boot" && d.Chrome.Start != "on_demand" {
				return nil, fmt.Errorf("invalid chrome.start")
			}
			if d.Chrome.Relaunch == "" {
				d.Chrome.Relaunch = "after_crash"
			}
			if d.Chrome.Relaunch != "after_crash" && d.Chrome.Relaunch != "always" && d.Chrome.Relaunch != "never" {
				return nil, fmt.Errorf("invalid chrome.relaunch")
			}
		default:
			return nil, fmt.Errorf("unknown destination kind %q", d.Kind)
		}
	}
	return ds, nil
}

// LiveDestinationMode preserves the legacy live_cdp -> attach mapping while
// leaving explicit ppcli on its existing sidecar/SQLite credential path.
func (c *SinkConfig) LiveDestinationMode() bool {
	if !c.HasDestinations() {
		return c.LiveCDP.Enabled
	}
	if c.Destination != nil {
		return c.Destination.Kind != "ppcli"
	}
	for _, d := range c.Destinations {
		if d.Kind != "ppcli" {
			return true
		}
	}
	return false
}

// Destination names are also filesystem and replay namespace components.
// Lowercase names avoid aliases on case-insensitive destination filesystems.
var destinationNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func ValidDestinationName(name string) bool { return destinationNamePattern.MatchString(name) }

func pathsOverlap(a, b string) bool {
	return profilePathsOverlap(a, b, runtime.GOOS)
}

// Resolve existing ancestors too: a fresh profile underneath a symlink must
// not alias another destination or any real browser's default profile.
func canonicalProfilePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(abs)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", err
		}
		missing = append(missing, filepath.Base(abs))
		abs = parent
	}
}

// ValidateSinkTargets prevents ambiguous account routes from silently replacing
// one another. An explicit container selection always needs a named target.
func ValidateSinkTargets(sinks []SinkTarget) error {
	seen := map[string]bool{}
	for _, sink := range sinks {
		if sink.Destination != "" && !ValidDestinationName(sink.Destination) {
			return fmt.Errorf("invalid destination name %q", sink.Destination)
		}
		if len(sink.Containers) > 0 && sink.Destination == "" {
			return fmt.Errorf("sinks[].containers requires a named destination")
		}
		for _, container := range sink.Containers {
			if strings.TrimSpace(container) == "" {
				return fmt.Errorf("container preference cannot be empty")
			}
		}
		key := sink.Peer + "\x00" + sink.URL + "\x00" + sink.Destination
		if seen[key] {
			return fmt.Errorf("duplicate sink destination %q", sink.Destination)
		}
		seen[key] = true
	}
	return nil
}

func profilePathsOverlap(a, b, goos string) bool {
	if goos == "darwin" {
		a = strings.ToLower(a)
		b = strings.ToLower(b)
	}
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	return contains(a, b) || contains(b, a)
}

// DefaultProfileOverlap checks containment in both directions using filepath.Rel,
// including root and home ancestors. It resolves only filesystem symlinks/stat
// metadata, never opens browser databases.
func DefaultProfileOverlap(profile, home string) bool {
	profile = canonicalDestinationPath(profile)
	for _, p := range []string{filepath.Join(home, "Library/Application Support/Google/Chrome"), filepath.Join(home, ".config/google-chrome"), filepath.Join(home, ".config/chromium")} {
		p = canonicalDestinationPath(p)
		if pathsOverlap(p, profile) {
			return true
		}
	}
	return false
}
func canonicalDestinationPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	for parent := absolute; ; {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			suffix, _ := filepath.Rel(parent, absolute)
			return filepath.Join(resolved, suffix)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return absolute
		}
		parent = next
	}
}
