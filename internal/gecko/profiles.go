package gecko

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type iniProfile struct {
	name string
	dir  string
	def  bool // Default=1
}

type profilesINI struct {
	root            string
	profiles        []iniProfile
	installDefaults []string
}

// ResolveProfile returns the directory of the profile b uses under home.
//
// With profile unset it follows what the browser itself opens: the
// [Install...] section's Default= path, else the profile marked Default=1,
// else the only profile. Anything ambiguous is an error that lists the
// candidates: several profiles and no default, [Install...] sections that
// disagree (release, Developer Edition, Nightly), or more than one root with
// a profiles.ini (native plus snap or flatpak).
//
// profile, when set, names a profile by its Name=, or by its directory:
// absolute, relative to the root, or the directory's base name.
func ResolveProfile(b Browser, home string, linux bool, profile string) (string, error) {
	profile = strings.TrimSpace(profile)
	if profile != "" && filepath.IsAbs(profile) {
		return filepath.Clean(profile), nil
	}

	roots := b.Roots(home, linux)
	var found []profilesINI
	for _, root := range roots {
		data, err := os.ReadFile(filepath.Join(root, "profiles.ini"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("%s: read profiles.ini: %w", b.Name, err)
		}
		found = append(found, parseProfilesINI(root, data))
	}
	if len(found) == 0 {
		return "", fmt.Errorf("%s: no profiles.ini found under %s; open %s once to create a profile", b.Name, strings.Join(roots, ", "), b.Name)
	}

	if profile != "" {
		return pickNamed(b, found, profile)
	}

	if len(found) > 1 {
		var list []string
		for _, f := range found {
			list = append(list, f.root)
		}
		return "", fmt.Errorf("%s: profiles found under more than one root (%s); set browser.profile to the profile's directory", b.Name, strings.Join(list, ", "))
	}
	ini := found[0]

	switch len(ini.installDefaults) {
	case 0:
	case 1:
		return ini.installDefaults[0], nil
	default:
		return "", fmt.Errorf("%s: profiles.ini has more than one installed default (%s); set browser.profile to one of them", b.Name, strings.Join(ini.installDefaults, ", "))
	}

	var defaults []iniProfile
	for _, p := range ini.profiles {
		if p.def {
			defaults = append(defaults, p)
		}
	}
	if len(defaults) == 1 {
		return defaults[0].dir, nil
	}
	if len(defaults) == 0 && len(ini.profiles) == 1 {
		return ini.profiles[0].dir, nil
	}
	if len(ini.profiles) == 0 {
		return "", fmt.Errorf("%s: %s lists no profiles", b.Name, filepath.Join(ini.root, "profiles.ini"))
	}
	return "", fmt.Errorf("%s: no single default profile; set browser.profile to one of: %s", b.Name, describe(ini.profiles))
}

func pickNamed(b Browser, found []profilesINI, profile string) (string, error) {
	var matches []iniProfile
	for _, f := range found {
		rel := filepath.Join(f.root, filepath.FromSlash(profile))
		for _, p := range f.profiles {
			if p.name == profile || p.dir == rel || filepath.Base(p.dir) == profile {
				matches = append(matches, p)
			}
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].dir, nil
	case 0:
		var all []iniProfile
		for _, f := range found {
			all = append(all, f.profiles...)
		}
		return "", fmt.Errorf("%s: no profile named %q; known profiles: %s", b.Name, profile, describe(all))
	default:
		return "", fmt.Errorf("%s: browser.profile %q matches more than one profile (%s); set it to the profile's directory", b.Name, profile, describe(matches))
	}
}

func describe(ps []iniProfile) string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%s (%s)", p.name, p.dir))
	}
	return strings.Join(out, ", ")
}

func parseProfilesINI(root string, data []byte) profilesINI {
	ini := profilesINI{root: root}
	type section struct {
		name string
		keys map[string]string
	}
	var sections []section
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			sections = append(sections, section{name: line[1 : len(line)-1], keys: map[string]string{}})
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || len(sections) == 0 {
			continue
		}
		sections[len(sections)-1].keys[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}

	abs := func(path string, relative bool) string {
		if !relative && filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		return filepath.Join(root, filepath.FromSlash(path))
	}
	seen := map[string]bool{}
	for _, s := range sections {
		path := s.keys["Path"]
		switch {
		case strings.HasPrefix(s.name, "Profile") && path != "":
			ini.profiles = append(ini.profiles, iniProfile{
				name: s.keys["Name"],
				dir:  abs(path, s.keys["IsRelative"] != "0"),
				def:  s.keys["Default"] == "1",
			})
		case strings.HasPrefix(s.name, "Install") && s.keys["Default"] != "":
			dir := abs(s.keys["Default"], !filepath.IsAbs(s.keys["Default"]))
			if !seen[dir] {
				seen[dir] = true
				ini.installDefaults = append(ini.installDefaults, dir)
			}
		}
	}
	return ini
}
