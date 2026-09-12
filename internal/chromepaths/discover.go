package chromepaths

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

// Store represents a discovered Chrome cookie store that may be readable.
type Store struct {
	// Root is the user-data-dir containing this profile (e.g.
	// ~/Library/Application Support/Google/Chrome).
	Root string

	// Profile is the profile directory name (e.g. "Default", "Profile 1").
	Profile string

	// CookiesPath is the resolved path to the Cookies SQLite file.
	// Either Root/Profile/Cookies or Root/Profile/Network/Cookies.
	CookiesPath string

	// IsDefault is true when this is the historical Default profile in
	// the OS-standard Chrome root. Distinguishes "the" profile agentcookie
	// v0.7 read from extra profiles.
	IsDefault bool

	// Browser identifies which browser this store belongs to (e.g.
	// "chrome", "chromium", "brave", "edge").
	Browser string
}

// SkippedStore is a profile directory that was found but cannot be used,
// along with the reason it was skipped.
type SkippedStore struct {
	Root    string
	Profile string
	Reason  string
}

// DiscoverResult holds the outcome of a discovery pass.
type DiscoverResult struct {
	// Stores is the list of usable cookie stores found.
	Stores []Store

	// Skipped is the list of profile directories found but not usable.
	Skipped []SkippedStore
}

// profileNameAllowlist matches profile directory names that are actual
// Chrome profiles, not cache/crash dirs. Patterns:
//   - Default
//   - Profile N (where N is a number)
//   - Guest Profile
//   - System Profile
var profileNameAllowlist = regexp.MustCompile(`^(Default|Profile \d+|Guest Profile|System Profile)$`)

// skipDirs are directory names that should never be treated as profiles.
var skipDirs = map[string]bool{
	"Crashpad":              true,
	"ShaderCache":           true,
	"GrShaderCache":         true,
	"GPUCache":              true,
	"Cache":                 true,
	"Code Cache":            true,
	"component_crx_cache":   true,
	"Safe Browsing":         true,
	"Crowd Deny":            true,
	"MEIPreload":            true,
	"FileTypePolicies":      true,
	"hyphen-data":           true,
	"OptimizationHints":     true,
	"OriginTrials":          true,
	"SSLErrorAssistant":     true,
	"Subresource Filter":    true,
	"ZxcvbnData":            true,
	"BrowserMetrics":        true,
	"extensions_crx_cache":  true,
	"pnacl":                 true,
	"PnaclTranslationCache": true,
}

// chromeRoots returns the list of Chrome/Chromium user-data-dir roots to scan.
// Includes the OS-standard locations plus well-known agent-specific directories.
func chromeRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	var roots []string

	switch runtime.GOOS {
	case "darwin":
		appSupport := filepath.Join(home, "Library", "Application Support")
		roots = append(roots,
			filepath.Join(appSupport, "Google", "Chrome"),
			filepath.Join(appSupport, "Chromium"),
			filepath.Join(appSupport, "BraveSoftware", "Brave-Browser"),
			filepath.Join(appSupport, "Microsoft Edge"),
			// Dia uses Arc's User Data layout. Scan the user-data-dir itself
			// (profiles live under Dia/User Data/<profile>/), not the Dia
			// support dir, or Default/Profile N would not be found.
			filepath.Join(appSupport, "Dia", "User Data"),
		)
	case "linux":
		configDir := filepath.Join(home, ".config")
		roots = append(roots,
			filepath.Join(configDir, "google-chrome"),
			filepath.Join(configDir, "chromium"),
			filepath.Join(configDir, "BraveSoftware", "Brave-Browser"),
			filepath.Join(configDir, "microsoft-edge"),
		)
	}

	// Well-known agent-specific user-data-dirs that often have Cookies
	// but no Local State (CDP-launched Chromes, sandbox Chromes).
	roots = append(roots,
		filepath.Join(home, "chrome-profile"),
		filepath.Join(home, ".agentcookie", "chrome-profile"),
	)

	// Honor CHROME_USER_DATA_DIR environment variable if set.
	if envDir := os.Getenv("CHROME_USER_DATA_DIR"); envDir != "" {
		roots = append(roots, envDir)
	}

	return roots
}

// osDefaultChromeRoot returns the path to the OS-standard Chrome user-data-dir.
func osDefaultChromeRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	}
	if runtime.GOOS == "linux" {
		return filepath.Join(home, ".config", "google-chrome")
	}
	return ""
}

// browserForRoot returns a browser identifier based on the root path.
func browserForRoot(root string) string {
	lower := strings.ToLower(filepath.ToSlash(root))
	switch {
	case strings.Contains(lower, "brave"):
		return "brave"
	case strings.Contains(lower, "edge"):
		return "edge"
	case strings.Contains(lower, "chromium"):
		return "chromium"
	case pathHasComponent(lower, "dia"):
		// Path-component match: strings.Contains("media", "dia") is true, so
		// a substring check would mislabel any root whose path includes
		// "media" as Dia and then look up Dia Safe Storage.
		return "dia"
	default:
		return "chrome"
	}
}

// pathHasComponent reports whether name is a path element of slashPath.
// slashPath must already use forward slashes (filepath.ToSlash).
func pathHasComponent(slashPath, name string) bool {
	return slices.Contains(strings.Split(slashPath, "/"), name)
}

// Discover scans known Chrome user-data-dirs and returns all usable
// cookie stores. A store is usable if a Cookies file exists at either
// <profile>/Cookies or <profile>/Network/Cookies. Local State is NOT
// required (matches real-world agent Chromes that lack it).
//
// The result includes both Stores (usable) and Skipped (found but not
// usable, with reasons), so doctor/status can report skip reasons.
func Discover() DiscoverResult {
	var result DiscoverResult

	defaultRoot := osDefaultChromeRoot()
	seen := make(map[string]bool)

	for _, root := range chromeRoots() {
		if root == "" {
			continue
		}
		// Normalize to catch duplicates.
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true

		entries, err := os.ReadDir(root)
		if err != nil {
			// Root doesn't exist or not readable - not an error.
			continue
		}

		browser := browserForRoot(root)
		isDefaultRoot := (abs == defaultRoot || root == defaultRoot)

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()

			// Skip non-profile directories.
			if skipDirs[name] {
				continue
			}

			// Check if this looks like a profile directory.
			if !profileNameAllowlist.MatchString(name) {
				// For well-known agent roots (chrome-profile, agentcookie/chrome-profile),
				// the root itself is the profile (not a subdirectory).
				// We'll handle these specially below.
				continue
			}

			profileDir := filepath.Join(root, name)
			store, skipReason := probeProfileDir(root, name, profileDir, browser, isDefaultRoot && name == "Default")
			if skipReason != "" {
				result.Skipped = append(result.Skipped, SkippedStore{
					Root:    root,
					Profile: name,
					Reason:  skipReason,
				})
			} else if store != nil {
				result.Stores = append(result.Stores, *store)
			}
		}

		// For well-known agent roots that are themselves profiles (the root
		// IS the profile, no subdirectory), probe the root directly.
		if strings.HasSuffix(root, "chrome-profile") {
			store, skipReason := probeProfileDir(filepath.Dir(root), filepath.Base(root), root, browser, false)
			if skipReason != "" {
				result.Skipped = append(result.Skipped, SkippedStore{
					Root:    filepath.Dir(root),
					Profile: filepath.Base(root),
					Reason:  skipReason,
				})
			} else if store != nil {
				result.Stores = append(result.Stores, *store)
			}
		}
	}

	return result
}

// probeProfileDir checks if profileDir has a usable Cookies file.
// Returns a Store if usable, or a skip reason string if not.
func probeProfileDir(root, profileName, profileDir, browser string, isDefault bool) (*Store, string) {
	// Try Network/Cookies first (Chrome 96+), then Cookies.
	networkCookies := filepath.Join(profileDir, "Network", "Cookies")
	legacyCookies := filepath.Join(profileDir, "Cookies")

	var cookiesPath string
	if fileExists(networkCookies) {
		cookiesPath = networkCookies
	} else if fileExists(legacyCookies) {
		cookiesPath = legacyCookies
	}

	if cookiesPath == "" {
		return nil, "no Cookies file"
	}

	return &Store{
		Root:        root,
		Profile:     profileName,
		CookiesPath: cookiesPath,
		IsDefault:   isDefault,
		Browser:     browser,
	}, ""
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// expandTilde turns a leading "~/" into the user's home dir. Leaves all other
// paths alone. Matches config.ExpandTilde behavior.
func expandTilde(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// DiscoverForConfig returns stores that match the optional config constraints.
// If profileDir is non-empty, it's treated as an explicit user-data-dir to
// include in discovery. If profileDir contains profile subdirectories (Default,
// Profile N), those are scanned the same way Discover scans standard roots.
// Otherwise, profileDir itself is probed as a profile.
func DiscoverForConfig(profileDir string) DiscoverResult {
	result := Discover()

	// If an explicit profile dir is given (e.g. from config cdp.profile_dir),
	// add it if not already in the result.
	if profileDir != "" {
		// Expand ~ to home directory before filepath.Abs (which treats ~ as literal).
		expanded := expandTilde(profileDir)
		abs, err := filepath.Abs(expanded)
		if err == nil {
			// Check if any discovered stores already cover this path.
			found := false
			for _, s := range result.Stores {
				if s.Root == abs || filepath.Join(s.Root, s.Profile) == abs {
					found = true
					break
				}
			}
			if !found {
				// First, try scanning profileDir as a user-data-dir with
				// profile subdirectories (Default, Profile N, etc.).
				addedFromChildren := scanUserDataDir(abs, &result)

				// If no profile subdirs were found, probe the directory
				// itself as a profile (e.g., a bare chrome-profile dir).
				if !addedFromChildren {
					store, skipReason := probeProfileDir(
						filepath.Dir(abs),
						filepath.Base(abs),
						abs,
						browserForRoot(abs),
						false,
					)
					if skipReason != "" {
						result.Skipped = append(result.Skipped, SkippedStore{
							Root:    filepath.Dir(abs),
							Profile: filepath.Base(abs),
							Reason:  skipReason,
						})
					} else if store != nil {
						result.Stores = append(result.Stores, *store)
					}
				}
			}
		}
	}

	return result
}

// scanUserDataDir scans root as a Chrome user-data-dir, looking for profile
// subdirectories (Default, Profile N, etc.). Returns true if any profiles
// were found (added or skipped), false if root doesn't look like a user-data-dir.
func scanUserDataDir(root string, result *DiscoverResult) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}

	browser := browserForRoot(root)
	foundProfiles := false

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()

		if skipDirs[name] {
			continue
		}

		if !profileNameAllowlist.MatchString(name) {
			continue
		}

		foundProfiles = true
		profileDir := filepath.Join(root, name)
		store, skipReason := probeProfileDir(root, name, profileDir, browser, name == "Default")
		if skipReason != "" {
			result.Skipped = append(result.Skipped, SkippedStore{
				Root:    root,
				Profile: name,
				Reason:  skipReason,
			})
		} else if store != nil {
			result.Stores = append(result.Stores, *store)
		}
	}

	return foundProfiles
}
