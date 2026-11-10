// Package profiles resolves Gecko profiles by explicit selection or an install
// hash. Legacy profile defaults are deliberately never a source of authority.
package profiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/mvanhorn/agentcookie/internal/browsers"
)

// ProfileIdentity pins the selected profile across unattended runs.
type ProfileIdentity struct {
	Dir         string `json:"dir"`
	Created     int64  `json:"created"`
	Name        string `json:"name"`
	How         string `json:"how"`
	InstallHash string `json:"install_hash,omitempty"`
}

// ValidateIdentity refuses an identity switch until the user makes a fresh
// explicit selection or accepts the new identity. explicitSelection must mean a
// user selection in this invocation (for example an explicit CLI flag), never
// merely a nonempty persisted browser.profile setting. A missing prior identity
// is first use.
func ValidateIdentity(previous, current ProfileIdentity, explicitSelection bool, acceptChange bool) error {
	if previous.Dir == "" || acceptChange || explicitSelection {
		return nil
	}
	if previous.Dir != current.Dir || previous.Created != current.Created {
		return fmt.Errorf("gecko profile identity changed from %q (created %d) to %q (created %d); pass --profile explicitly or use --accept-profile-change", previous.Dir, previous.Created, current.Dir, current.Created)
	}
	return nil
}

// Candidate contains diagnostic metadata only; LastAppDir is never authoritative.
type Candidate struct {
	Dir        string
	Path       string
	Name       string
	LastAppDir string
	Sections   []string
}
type install struct{ root, section, hash, path string }
type inventory struct {
	profiles []Candidate
	installs []install
}

// Resolver makes platform/home choices explicit for callers and isolated tests.
// Nonempty AppPaths override, rather than supplement, registry install paths.
type Resolver struct {
	HomeDir  string
	GOOS     string
	AppPaths []string
}

func (r Resolver) platform() string {
	if r.GOOS != "" {
		return r.GOOS
	}
	return runtime.GOOS
}
func (r Resolver) home() (string, error) {
	if r.HomeDir != "" {
		return r.HomeDir, nil
	}
	return os.UserHomeDir()
}
func gecko(browser string) (browsers.Browser, error) {
	b, err := browsers.Lookup(browser)
	if err != nil {
		return b, err
	}
	if b.Family != browsers.Gecko {
		return b, fmt.Errorf("browser %q is not Gecko", browser)
	}
	return b, nil
}
func (r Resolver) roots(browser string) ([]string, error) {
	b, err := gecko(browser)
	if err != nil {
		return nil, err
	}
	home, err := r.home()
	if err != nil {
		return nil, err
	}
	switch r.platform() {
	case "darwin":
		return []string{filepath.Join(home, "Library/Application Support", b.MacRoot)}, nil
	case "linux":
		roots := []string{filepath.Join(home, b.LinuxRoot)}
		for _, rel := range b.LinuxProfileRoots {
			roots = append(roots, filepath.Join(home, rel))
		}
		return roots, nil
	default:
		return nil, fmt.Errorf("gecko profile resolution unsupported on %s", r.platform())
	}
}

// InstallDirectories returns paths whose hashes may match an Install section.
// Existing symlinks (notably snap/current and executable launchers) are resolved.
func (r Resolver) InstallDirectories(browser string) ([]string, error) {
	b, err := gecko(browser)
	if err != nil {
		return nil, err
	}
	var apps []string
	for _, path := range r.AppPaths {
		if path != "" {
			apps = append(apps, path)
		}
	}
	if len(apps) == 0 {
		switch r.platform() {
		case "darwin":
			apps = []string{b.AppBundle}
		case "linux":
			apps = b.LinuxInstallDirs
		default:
			return nil, fmt.Errorf("gecko profile resolution unsupported on %s", r.platform())
		}
	}
	var dirs []string
	add := func(path string) {
		path = filepath.Clean(path)
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		if !slices.Contains(dirs, path) {
			dirs = append(dirs, path)
		}
	}
	for _, app := range apps {
		if strings.HasPrefix(app, "~/") {
			home, e := r.home()
			if e != nil {
				return nil, e
			}
			app = filepath.Join(home, app[2:])
		}
		if !filepath.IsAbs(app) {
			return nil, fmt.Errorf("browser.app_path must be absolute: %q", app)
		}
		if r.platform() == "darwin" && strings.HasSuffix(filepath.Clean(app), ".app") {
			add(filepath.Join(app, "Contents/MacOS"))
			add(filepath.Join(app, "Contents/Resources"))
			continue
		}
		if real, e := filepath.EvalSymlinks(app); e == nil {
			app = real
		}
		if st, e := os.Stat(app); e == nil && !st.IsDir() {
			app = filepath.Dir(app)
		}
		add(app)
	}
	return dirs, nil
}

// Candidates lists every profile mentioned by profiles.ini, including stale
// entries, with the profile/install sections that refer to it.
func (r Resolver) Candidates(browser string) ([]Candidate, error) {
	inv, err := r.read(browser)
	return inv.profiles, err
}
func (r Resolver) read(browser string) (inventory, error) {
	var inv inventory
	roots, err := r.roots(browser)
	if err != nil {
		return inv, err
	}
	for _, root := range roots {
		sections, err := readINI(filepath.Join(root, "profiles.ini"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return inv, err
		}
		for _, sec := range sections {
			if number, ok := strings.CutPrefix(sec.name, "Profile"); ok {
				if _, err := strconv.ParseUint(number, 10, 32); err != nil {
					continue
				}
				path := sec.values["Path"]
				if path == "" {
					return inv, fmt.Errorf("%s [%s]: missing Path", root, sec.name)
				}
				dir := path
				if sec.values["IsRelative"] == "1" {
					dir = filepath.Join(root, path)
				} else if !filepath.IsAbs(dir) {
					return inv, fmt.Errorf("%s [%s]: nonabsolute Path without IsRelative=1", root, sec.name)
				}
				candidate := Candidate{Dir: canonical(dir), Path: path, Name: sec.values["Name"], Sections: []string{sec.name}}
				compat, e := readINI(filepath.Join(dir, "compatibility.ini"))
				if e == nil {
					for _, c := range compat {
						if c.name == "Compatibility" {
							candidate.LastAppDir = c.values["LastAppDir"]
						}
					}
				}
				inv.profiles = append(inv.profiles, candidate)
			} else if hash, ok := strings.CutPrefix(sec.name, "Install"); ok && hash != "" {
				inv.installs = append(inv.installs, install{root, sec.name, hash, sec.values["Default"]})
			}
		}
	}
	for _, inst := range inv.installs {
		if inst.path == "" {
			continue
		}
		dir := inst.path
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(inst.root, dir)
		}
		dir = canonical(dir)
		for i := range inv.profiles {
			if inv.profiles[i].Dir == dir {
				inv.profiles[i].Sections = append(inv.profiles[i].Sections, inst.section)
			}
		}
	}
	return inv, nil
}
func canonical(path string) string {
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// Resolve validates profile storage and records the canonical identity. An
// ambiguous or stale matching install is an error, never a fallback selection.
func (r Resolver) Resolve(browser, selector string) (ProfileIdentity, error) {
	if _, err := gecko(browser); err != nil {
		return ProfileIdentity{}, err
	}
	if filepath.IsAbs(selector) || strings.HasPrefix(selector, "./") || strings.HasPrefix(selector, "../") || strings.HasPrefix(selector, "~/") {
		path := selector
		if strings.HasPrefix(path, "~/") {
			home, err := r.home()
			if err != nil {
				return ProfileIdentity{}, err
			}
			path = filepath.Join(home, path[2:])
		}
		return identity(Candidate{Dir: path}, "path", "")
	}
	inv, err := r.read(browser)
	if err != nil {
		return ProfileIdentity{}, err
	}
	fail := func(reason string) (ProfileIdentity, error) {
		return ProfileIdentity{}, fmt.Errorf("%s; set browser.profile to an explicit path or unique name. Candidates:%s", reason, describe(inv.profiles))
	}
	if selector != "" {
		var matches []Candidate
		how := "name"
		for _, p := range inv.profiles {
			if p.Name == selector {
				matches = append(matches, p)
			}
		}
		if len(matches) == 0 {
			how = "basename"
			for _, p := range inv.profiles {
				if filepath.Base(p.Path) == selector {
					matches = append(matches, p)
				}
			}
		}
		if len(matches) == 0 && strings.ContainsRune(selector, filepath.Separator) {
			roots, e := r.roots(browser)
			if e != nil {
				return ProfileIdentity{}, e
			}
			for _, root := range roots {
				path := filepath.Join(root, selector)
				if st, e := os.Stat(path); e == nil && st.IsDir() {
					matches = append(matches, Candidate{Dir: path, Path: selector})
				}
			}
			how = "path"
		}
		if len(matches) != 1 {
			return fail(fmt.Sprintf("profile selector %q matches %d profiles", selector, len(matches)))
		}
		return identity(matches[0], how, "")
	}
	dirs, err := r.InstallDirectories(browser)
	if err != nil {
		return ProfileIdentity{}, err
	}
	var matches []ProfileIdentity
	for _, inst := range inv.installs {
		matched := false
		for _, dir := range dirs {
			if inst.hash == InstallHash(dir) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if inst.path == "" {
			return fail("matching " + inst.section + " has no Default")
		}
		path := inst.path
		if !filepath.IsAbs(path) {
			path = filepath.Join(inst.root, path)
		}
		candidate := Candidate{Dir: canonical(path), Path: inst.path}
		for _, p := range inv.profiles {
			if p.Dir == candidate.Dir {
				candidate = p
				break
			}
		}
		id, e := identity(candidate, "install", inst.hash)
		if e != nil {
			return fail(fmt.Sprintf("stale matching %s: %v", inst.section, e))
		}
		if !slices.ContainsFunc(matches, func(p ProfileIdentity) bool { return p.Dir == id.Dir }) {
			matches = append(matches, id)
		}
	}
	if len(matches) != 1 {
		return fail(fmt.Sprintf("installation candidates %q match %d profiles", dirs, len(matches)))
	}
	return matches[0], nil
}
func identity(p Candidate, how, hash string) (ProfileIdentity, error) {
	dir, err := filepath.EvalSymlinks(p.Dir)
	if err != nil {
		return ProfileIdentity{}, fmt.Errorf("profile %q: %w", p.Dir, err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return ProfileIdentity{}, err
	}
	cookies, e := os.Stat(filepath.Join(dir, "cookies.sqlite"))
	storage, se := os.Stat(filepath.Join(dir, "storage/default"))
	if (e != nil || !cookies.Mode().IsRegular()) && (se != nil || !storage.IsDir()) {
		return ProfileIdentity{}, fmt.Errorf("profile %q has neither cookies.sqlite nor storage/default", dir)
	}
	id := ProfileIdentity{Dir: dir, Name: p.Name, How: how, InstallHash: hash}
	data, err := os.ReadFile(filepath.Join(dir, "times.json"))
	if errors.Is(err, os.ErrNotExist) {
		return id, nil
	}
	if err != nil {
		return ProfileIdentity{}, err
	}
	var times struct {
		Created int64 `json:"created"`
	}
	if err := json.Unmarshal(data, &times); err != nil {
		return ProfileIdentity{}, fmt.Errorf("profile %q times.json: %w", dir, err)
	}
	if times.Created < 0 {
		return ProfileIdentity{}, fmt.Errorf("profile %q has negative creation time", dir)
	}
	id.Created = times.Created
	return id, nil
}
func describe(profiles []Candidate) string {
	if len(profiles) == 0 {
		return " none"
	}
	var b strings.Builder
	for _, p := range profiles {
		fmt.Fprintf(&b, "\n  Path=%q Name=%q Dir=%q LastAppDir=%q sections=%v", p.Path, p.Name, p.Dir, p.LastAppDir, p.Sections)
	}
	return b.String()
}
