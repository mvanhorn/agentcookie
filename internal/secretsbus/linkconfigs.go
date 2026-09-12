package secretsbus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config linking bridges a gap between where the bus is allowed to write and
// where a PP CLI actually reads.
//
// The bus materializes carried files strictly under ~/.agentcookie/ --
// validateMaterializeTarget enforces that, and the sink re-applies it, so a
// manifest can never name an arbitrary write path. But a PP CLI reads its
// config from ~/.config/<slug>/config.toml, and only binaries built after
// roughly 2026-07 honor XDG_CONFIG_HOME or <API>_CONFIG_DIR, so an env pointer
// reaches almost none of the installed fleet.
//
// Rather than widen the bus's write authority, linking is a separate, explicit,
// opt-in step: it plans in read-only mode, and only acts when the caller asks.
// It is the one place in the system that writes outside ~/.agentcookie/, so it
// refuses anything it does not positively recognize as safe.
//
// "Safe" is a statement about the whole destination path, not just its last
// component. Inspecting only ~/.config/<slug>/config.toml is not enough: that
// leaf reads as absent just as readily when ~/.config/<slug> is a symlink into
// some other tree, and the mkdir and symlink that follow would walk that
// symlink and leave the config there while the plan claimed ~/.config. So
// every lookup and every write goes through an os.Root anchored at ~/.config,
// which refuses to follow a symlink whose target leaves that tree instead of
// quietly redirecting the write. ~/.config is itself opened through a root
// anchored at the home directory, so the same holds one level up.

// LinkAction is what a plan entry proposes to do about one destination.
type LinkAction string

const (
	// LinkActionLink means the destination is absent and can be created.
	LinkActionLink LinkAction = "link"
	// LinkActionAlreadyLinked means the destination is already a symlink into
	// ~/.agentcookie/, so it is ours and already correct.
	LinkActionAlreadyLinked LinkAction = "already-linked"
	// LinkActionRefuse means the destination is occupied by something we did
	// not create. Never overwritten.
	LinkActionRefuse LinkAction = "refuse"
)

// configDirName is the home-relative directory PP CLIs read their configs
// from, and configFileName the file within a CLI's directory there.
const (
	configDirName  = ".config"
	configFileName = "config.toml"
)

// reservedBusDirs are ~/.agentcookie/ subdirectories owned by the bus itself
// rather than by a carried CLI config.
var reservedBusDirs = map[string]bool{
	"secrets":     true,
	"manifests":   true,
	"file-optin":  true,
	"cookies":     true,
	"state":       true,
	"logs":        true,
	"tmp":         true,
	"credentials": true,
}

// LinkPlanEntry is one proposed link, fully resolved and classified.
type LinkPlanEntry struct {
	// Slug is the CLI directory name (also the ~/.config/ directory name).
	Slug string
	// Materialized is the absolute path of the carried config under
	// ~/.agentcookie/.
	Materialized string
	// Destination is the absolute path the CLI reads.
	Destination string
	// Action is what would happen when applied.
	Action LinkAction
	// Reason explains a refusal, or is empty when there is nothing to explain.
	Reason string
}

// PlanConfigLinks scans materialized carried configs under ~/.agentcookie/ and
// classifies what linking each into ~/.config/<slug>/config.toml would do.
//
// It is strictly read-only: it creates no directories and no links, which is
// what makes it safe to run as the default dry run.
func PlanConfigLinks(homeDir string) ([]LinkPlanEntry, error) {
	root := agentcookieRoot(homeDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // nothing materialized yet
		}
		return nil, fmt.Errorf("read %s: %w", root, err)
	}

	// Opening the config tree read-only: a nil configRoot with no refusal
	// means ~/.config does not exist yet, so every destination under it is
	// absent and linkable. Planning must not create it -- that is the dry-run
	// guarantee -- so the directory is only made later, by ApplyConfigLinks.
	configRoot, refusal := openConfigRoot(homeDir)
	if configRoot != nil {
		defer configRoot.Close()
	}

	var plan []LinkPlanEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		if reservedBusDirs[slug] {
			continue
		}
		// Re-validate the slug before it is used to compose a destination
		// path, so a malformed directory name cannot produce a surprising
		// write location.
		if !validCLIName(slug) {
			continue
		}
		src := filepath.Join(root, slug, configFileName)
		info, err := os.Lstat(src)
		if err != nil || !info.Mode().IsRegular() {
			continue // only a real materialized config is a candidate
		}

		dst := filepath.Join(homeDir, configDirName, slug, configFileName)
		action, reason := LinkActionLink, ""
		switch {
		case refusal != "":
			action, reason = LinkActionRefuse, refusal
		case configRoot != nil:
			action, reason = classifyDestination(configRoot, slug, root)
		}
		plan = append(plan, LinkPlanEntry{
			Slug:         slug,
			Materialized: src,
			Destination:  dst,
			Action:       action,
			Reason:       reason,
		})
	}
	return plan, nil
}

// maxConfigLinkHops bounds the symlink rewriting in openConfigDir so a cycle
// of absolute symlinks cannot spin.
const maxConfigLinkHops = 8

// openConfigDir opens ~/.config through homeRoot, so the entire walk is
// confined to the home directory and a symlink leading out of it fails rather
// than redirecting the open. A nil root with an empty refusal means ~/.config
// does not exist.
//
// The alternative -- resolving ~/.config by name and then opening the result
// -- cannot be made safe, because the resolve and the open are separate
// lookups that a symlink planted between them makes disagree. Anchoring at the
// home directory instead means the containment decision and the open are the
// same operation.
//
// os.Root rejects an absolute symlink even when its target is inside the root,
// so `ln -s ~/dotfiles/config ~/.config` -- the normal way to write that
// arrangement, and one the CLI itself reads through -- would otherwise be
// refused. Such a target is rewritten as a home-relative path and the open
// retried, which keeps the resolution inside the same confined walk.
func openConfigDir(homeRoot *os.Root, homeDir string) (*os.Root, string) {
	name := configDirName
	for range maxConfigLinkHops {
		root, err := homeRoot.OpenRoot(name)
		if err == nil {
			return root, ""
		}
		if os.IsNotExist(err) {
			return nil, ""
		}
		target, rerr := homeRoot.Readlink(name)
		if rerr != nil {
			return nil, fmt.Sprintf("cannot open %s: %v", filepath.Join(homeDir, name), err)
		}
		rel, ok := homeRelative(homeDir, target)
		if !ok {
			return nil, fmt.Sprintf("%s is a symlink to %s, outside the home directory; not writing through it", filepath.Join(homeDir, name), target)
		}
		name = rel
	}
	return nil, fmt.Sprintf("%s resolves through too many symlinks to classify", filepath.Join(homeDir, configDirName))
}

// homeRelative rewrites a path inside the home directory as a home-relative
// one. The home directory's own symlinks are resolved as well, because a
// target may be written either way -- on macOS ~ is reached through /var while
// the real path is /private/var.
func homeRelative(homeDir, target string) (string, bool) {
	target = filepath.Clean(target)
	bases := []string{filepath.Clean(homeDir)}
	if resolved, err := filepath.EvalSymlinks(homeDir); err == nil && resolved != bases[0] {
		bases = append(bases, resolved)
	}
	for _, base := range bases {
		if target == base || !underRoot(target, base) {
			continue
		}
		rel, err := filepath.Rel(base, target)
		if err != nil {
			continue
		}
		return rel, true
	}
	return "", false
}

// openConfigRoot opens ~/.config for read-only classification, creating
// nothing. A nil root with an empty refusal means ~/.config is absent, so
// every destination under it is absent too.
func openConfigRoot(homeDir string) (*os.Root, string) {
	homeRoot, err := os.OpenRoot(homeDir)
	if err != nil {
		return nil, fmt.Sprintf("cannot open %s: %v", homeDir, err)
	}
	defer homeRoot.Close()
	return openConfigDir(homeRoot, homeDir)
}

// openConfigRootForWrite is openConfigRoot plus creating ~/.config when it is
// missing. The mkdir goes through the home root too, so that one directory
// cannot be placed elsewhere either.
//
// Creating is not allowed to become a way around the classification. A mkdir
// that reports the directory already exists means something appeared in the
// window since it was found absent, so the loop classifies ~/.config again
// rather than opening it by name -- opening by name is what would follow a
// symlink planted in exactly that window.
func openConfigRootForWrite(homeDir string) (*os.Root, error) {
	homeRoot, err := os.OpenRoot(homeDir)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", homeDir, err)
	}
	defer homeRoot.Close()

	nominal := filepath.Join(homeDir, configDirName)
	for created := false; ; created = true {
		root, refusal := openConfigDir(homeRoot, homeDir)
		if refusal != "" {
			return nil, errors.New(refusal)
		}
		if root != nil {
			return root, nil
		}
		if created {
			// Still absent after a mkdir that either succeeded or found
			// something there: a dangling symlink, or a racing writer. Refuse
			// rather than try harder.
			return nil, fmt.Errorf("%s does not resolve to a directory; not linking", nominal)
		}
		if err := homeRoot.Mkdir(configDirName, 0o700); err != nil && !os.IsExist(err) {
			return nil, fmt.Errorf("create %s: %w", nominal, err)
		}
	}
}

// classifyDestination decides what may be done with slug's destination without
// touching it.
//
// Every lookup is made through configRoot, so a symlink anywhere in
// <slug>/config.toml that leads out of ~/.config produces a containment error
// rather than a path that reads as absent. The leaf itself is inspected with
// Lstat, so a symlink there is read and judged, never followed.
func classifyDestination(configRoot *os.Root, slug, busRoot string) (LinkAction, string) {
	rel := filepath.Join(slug, configFileName)
	info, err := configRoot.Lstat(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return LinkActionLink, ""
		}
		return LinkActionRefuse, unreachableDestinationReason(configRoot, slug, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return LinkActionRefuse, "destination is an existing file; not replacing it"
	}
	target, err := configRoot.Readlink(rel)
	if err != nil {
		return LinkActionRefuse, fmt.Sprintf("cannot read existing symlink: %v", err)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(configRoot.Name(), slug, target)
	}
	if !underRoot(target, busRoot) {
		return LinkActionRefuse, "destination is a symlink to " + target + ", which we did not create"
	}
	return LinkActionAlreadyLinked, ""
}

// unreachableDestinationReason explains a containment or lookup error in terms
// the user can act on. os.Root only reports that the path escaped, so the
// offending parent is inspected -- Readlink still works on a symlink whose
// target is outside the root, since reading a link does not follow it.
func unreachableDestinationReason(configRoot *os.Root, slug string, cause error) string {
	parent := filepath.Join(configRoot.Name(), slug)
	info, err := configRoot.Lstat(slug)
	switch {
	case err != nil:
		return fmt.Sprintf("cannot inspect %s: %v", parent, err)
	case info.Mode()&os.ModeSymlink != 0:
		target, err := configRoot.Readlink(slug)
		if err != nil {
			return fmt.Sprintf("%s is a symlink out of %s; not writing through it", parent, configRoot.Name())
		}
		return fmt.Sprintf("%s is a symlink to %s, outside %s; not writing through it", parent, target, configRoot.Name())
	case !info.IsDir():
		return fmt.Sprintf("%s is not a directory", parent)
	default:
		return fmt.Sprintf("cannot inspect destination: %v", cause)
	}
}

// underRoot reports whether p is root or lies beneath it, after cleaning.
func underRoot(p, root string) bool {
	cleanP := filepath.Clean(p)
	cleanRoot := filepath.Clean(root)
	if cleanP == cleanRoot {
		return true
	}
	return strings.HasPrefix(cleanP, cleanRoot+string(filepath.Separator))
}

// ApplyConfigLinks acts on a plan, creating only the links its entries marked
// linkable. Entries marked refuse are reported as errors and never written;
// entries already linked are skipped. Returns the number of links created.
//
// A plan entry is treated as a request, not as a decision already made: the
// slug, the materialized source and the destination are all re-checked here.
// A caller can hand over a plan it built itself, and even one produced by
// PlanConfigLinks describes a filesystem that may have changed since.
func ApplyConfigLinks(homeDir string, plan []LinkPlanEntry) (int, []error) {
	busRoot := agentcookieRoot(homeDir)
	var errs []error
	applied := 0

	// Opened on first use so a plan with nothing to link stays read-only and
	// does not create ~/.config as a side effect.
	var configRoot *os.Root
	defer func() {
		if configRoot != nil {
			configRoot.Close()
		}
	}()

	for _, e := range plan {
		switch e.Action {
		case LinkActionAlreadyLinked:
			continue
		case LinkActionRefuse:
			errs = append(errs, fmt.Errorf("%s: %s (%s)", e.Slug, e.Reason, e.Destination))
			continue
		}
		if !validCLIName(e.Slug) {
			errs = append(errs, fmt.Errorf("%q is not a valid cli name; not linking", e.Slug))
			continue
		}
		// The link target is the other half of the containment: a symlink is
		// only worth creating if it points at a config the bus materialized.
		if !underRoot(e.Materialized, busRoot) {
			errs = append(errs, fmt.Errorf("%s: source %s is outside %s; not linking", e.Slug, e.Materialized, busRoot))
			continue
		}
		if configRoot == nil {
			root, err := openConfigRootForWrite(homeDir)
			if err != nil {
				// Nothing can be linked if the config tree itself is not
				// usable, so report it once rather than per entry.
				return applied, append(errs, err)
			}
			configRoot = root
		}
		if action, reason := classifyDestination(configRoot, e.Slug, busRoot); action != LinkActionLink {
			if action == LinkActionAlreadyLinked {
				continue
			}
			errs = append(errs, fmt.Errorf("%s: %s (%s)", e.Slug, reason, e.Destination))
			continue
		}
		// Both writes are confined to ~/.config by configRoot, so the
		// classification above is a source of good error messages rather than
		// the only thing standing between a swapped-in symlink and a config
		// written outside the tree.
		if err := configRoot.MkdirAll(e.Slug, 0o700); err != nil {
			errs = append(errs, fmt.Errorf("%s: create config dir: %w", e.Slug, err))
			continue
		}
		if err := configRoot.Symlink(e.Materialized, filepath.Join(e.Slug, configFileName)); err != nil {
			errs = append(errs, fmt.Errorf("%s: link: %w", e.Slug, err))
			continue
		}
		applied++
	}
	return applied, errs
}
