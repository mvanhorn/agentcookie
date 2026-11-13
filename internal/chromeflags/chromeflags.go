// Package chromeflags holds Chrome command-line flags shared by every place
// agentcookie launches its own Chrome subprocess.
package chromeflags

import "strings"

// MacAppCodeSignClone is the Chrome feature that, on macOS, copies the app
// bundle into $TMPDIR/../X/com.google.Chrome.code_sign_clone/code_sign_clone.*
// at every launch so code-signature checks survive an in-place update. The
// clone is only removed on a clean shutdown; a Chrome that is killed (as a
// chromedp/owned headless Chrome routinely is) leaks the whole directory.
// agentcookie starts a short-lived Chrome every sync, so on a long-running
// sink these leaked clones accumulated to hundreds of directories. A
// throwaway automation Chrome does not need the protection, so disable it.
const MacAppCodeSignClone = "MacAppCodeSignClone"

// chromedpDefaultDisabled mirrors the disable-features value set by
// chromedp.DefaultExecAllocatorOptions. chromedp stores flags in a map, so a
// later Flag("disable-features", ...) replaces rather than appends; we have to
// carry chromedp's defaults forward ourselves.
const chromedpDefaultDisabled = "site-per-process,Translate,BlinkGenPropertyTrees"

// DisabledFeatures returns the comma-joined feature list agentcookie passes
// as --disable-features: base (may be empty) plus MacAppCodeSignClone, with
// duplicates removed and order preserved.
func DisabledFeatures(base string) string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.Split(base+","+MacAppCodeSignClone, ",") {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return strings.Join(out, ",")
}

// ChromedpDisabledFeatures is the --disable-features value for chromedp
// ExecAllocator launches: chromedp's defaults plus MacAppCodeSignClone.
func ChromedpDisabledFeatures() string { return DisabledFeatures(chromedpDefaultDisabled) }
