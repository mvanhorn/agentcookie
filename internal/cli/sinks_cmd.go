package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/state"
)

var sinksCmd = &cobra.Command{
	Use:   "sinks",
	Short: "List and remove the sinks this source pushes to",
}

func init() {
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "Show each sink in source.yaml, its key, and its last push",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			rows, err := SinkStatuses(common.ConfigDir, home)
			if err != nil {
				return err
			}
			if common.JSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			printSinkStatuses(cmd.OutOrStdout(), rows, time.Now())
			return nil
		},
	}
	var removeOpts sinksRemoveOptions
	removeCmd := &cobra.Command{
		Use:   "remove <peer|url>",
		Short: "Stop pushing to a sink: remove it from source.yaml and delete its key",
		Long: `remove takes a sink out of source.yaml and deletes its paired key, so
the sink stops receiving cookies and secrets. Name the sink by its peer, or
by its URL for a sink without one (a legacy shared-secret sink).

Deleting the key is what cuts a paired sink off: a running source reads
each sink's key on every push, so it stops sealing for the removed sink at
its next push, and GET /pull refuses that key right away. The running
source keeps the sink in its list, and reports it failing, until it
restarts.

The key is kept when another sink, a teammate in teammates.yaml, or this
machine's sink.yaml still uses that peer name. Then the removed sink can
keep pulling with that key, even after a restart, until the key is rotated
and every legitimate user of it is re-paired.

security.shared_secret stays unless you pass --revoke-shared-secret,
because pull-only sinks that are not listed in source.yaml may use it too.
While it stays, a removed machine that has it can keep pulling with it
until you rotate it. --revoke-shared-secret removes it, for every machine
that uses it, and refuses when a remaining sink still needs it (a sink
without a peer, or a paired sink whose key is missing here). A running
source still accepts it on GET /pull until it restarts, so the restart is
what cuts those machines off.

Removing the last entry of a sinks: list also removes a legacy sink:/peer:
it was shadowing, and that peer's key unless something else uses it.

The only sink cannot be removed unless teammates.yaml lists a teammate,
because a source with nothing to deliver to does not start. Pair another
sink first, or stop the source with agentcookie wizard uninstall.

Cookies and secrets the sink already received stay valid on that machine
until they expire or you sign out of those sites.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSinksRemoveWith(cmd.OutOrStdout(), common.ConfigDir, args[0], removeOpts)
		},
	}
	removeCmd.Flags().BoolVar(&removeOpts.RevokeSharedSecret, "revoke-shared-secret", false,
		"also remove security.shared_secret, cutting off every machine that pulls with it")
	sinksCmd.AddCommand(listCmd, removeCmd)
}

// Sink key states for `sinks list`.
const (
	sinkKeyPaired       = "paired"
	sinkKeyMissing      = "missing"
	sinkKeySharedSecret = "shared secret"
)

// sourceRestartHint says how to restart the source LaunchAgent the wizard
// installs.
const sourceRestartHint = "on macOS: launchctl kickstart -k gui/$(id -u)/dev.agentcookie.source"

// SinkStatus is one configured sink's row for `sinks list`.
type SinkStatus struct {
	Peer string `json:"peer,omitempty"`
	URL  string `json:"url"`
	// Key is how the source seals for this sink: sinkKeyPaired,
	// sinkKeyMissing, or sinkKeySharedSecret for a sink without a peer.
	Key string `json:"key"`

	LastPush      time.Time `json:"last_push,omitzero"`
	TotalPushes   int       `json:"total_pushes"`
	TotalFailures int       `json:"total_failures"`
	LastError     string    `json:"last_error,omitempty"`
}

// SinkStatuses returns one row per sink in source.yaml, in file order,
// with each sink's push record from the source state file when it has one.
func SinkStatuses(configDir, home string) ([]SinkStatus, error) {
	cfg, err := loadSourceForSinks(configDir)
	if err != nil {
		return nil, err
	}
	// The state file is only for display; a missing or unreadable one
	// leaves the push columns empty.
	st, _ := state.LoadSource(sourceStatePathForConfig(cfg, configDir, home))
	// An empty slice, not nil, so --json prints [] for a source that
	// delivers to teammates only.
	rows := []SinkStatus{}
	for _, s := range cfg.ResolvedSinks() {
		row := SinkStatus{Peer: s.Peer, URL: s.URL, Key: sinkKeyState(configDir, s)}
		if st != nil {
			for _, rec := range st.Sinks {
				if sinkConfigured([]config.SinkTarget{s}, rec) {
					row.LastPush, row.TotalPushes, row.TotalFailures, row.LastError = rec.LastPush, rec.TotalPushes, rec.TotalFailures, rec.LastError
					break
				}
			}
			// A single legacy sink, or a state file from before multi-sink,
			// keeps its history only in the top-level fields.
			if len(st.Sinks) == 0 && st.SinkURL != "" && st.SinkURL == s.URL {
				row.LastPush, row.TotalPushes, row.TotalFailures, row.LastError = st.LastPush, st.TotalPushes, st.TotalFailures, st.LastError
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func sinkKeyState(configDir string, s config.SinkTarget) string {
	if s.Peer == "" {
		return sinkKeySharedSecret
	}
	if !hasPeerKeyFile(configDir, s.Peer) {
		return sinkKeyMissing
	}
	return sinkKeyPaired
}

func printSinkStatuses(w io.Writer, rows []SinkStatus, now time.Time) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No sinks. This source delivers to teammates only (see `agentcookie teammates list`).")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PEER\tURL\tKEY\tLAST PUSH\tNOTE")
	for _, r := range rows {
		last := "never"
		if !r.LastPush.IsZero() {
			last = humanAgo(now.Sub(r.LastPush)) + " ago"
		}
		note := r.LastError
		if r.Key == sinkKeyMissing {
			note = "no paired key; run agentcookie pair --as source here, then on that machine agentcookie pair --as sink --local-name " + r.Peer + " (with the --peer, --pair-url and --code it prints)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", orDash(r.Peer), r.URL, r.Key, last, note)
	}
	_ = tw.Flush()
}

// loadSourceForSinks loads source.yaml, saying plainly when this machine is
// not a source at all.
func loadSourceForSinks(configDir string) (*config.SourceConfig, error) {
	path := filepath.Join(configDir, "source.yaml")
	if !fileExists(path) {
		return nil, fmt.Errorf("no %s: this machine is not set up as a source", path)
	}
	cfg, err := config.LoadSource(configDir)
	if err != nil {
		return nil, fmt.Errorf("load source config: %w", err)
	}
	return cfg, nil
}

// sinksRemoveOptions are the flags of `sinks remove`.
type sinksRemoveOptions struct {
	// RevokeSharedSecret also removes security.shared_secret. It is never
	// removed otherwise: pull-only sinks not listed in source.yaml may use
	// it, and this command cannot see them.
	RevokeSharedSecret bool
}

func runSinksRemove(out io.Writer, configDir, target string) error {
	return runSinksRemoveWith(out, configDir, target, sinksRemoveOptions{})
}

func runSinksRemoveWith(out io.Writer, configDir, target string, opts sinksRemoveOptions) error {
	cfg, err := loadSourceForSinks(configDir)
	if err != nil {
		return err
	}
	path := filepath.Join(configDir, "source.yaml")
	sinks := cfg.ResolvedSinks()
	idx := findSink(sinks, target)
	if idx < 0 {
		var names []string
		for _, s := range sinks {
			names = append(names, sinkLabel(s))
		}
		if len(names) == 0 {
			return fmt.Errorf("%s lists no sinks", path)
		}
		return fmt.Errorf("no sink %q in %s (configured: %s)", target, path, strings.Join(names, ", "))
	}
	removed, label := sinks[idx], sinkLabel(sinks[idx])
	remaining := slices.Delete(slices.Clone(sinks), idx, idx+1)

	teammates, err := config.LoadTeammates(configDir)
	if err != nil {
		return fmt.Errorf("%s does not load, so it is not safe to remove a sink (fix it first): %w", config.TeammatesPath(configDir), err)
	}
	if len(remaining) == 0 && (teammates == nil || len(teammates.Teammates) == 0) {
		return fmt.Errorf("%s is the only sink in %s, and a source with no sink and no teammates does not start; pair another sink first (agentcookie wizard install --as source --add-sink --peer <host>), or stop the source with agentcookie wizard uninstall", label, path)
	}

	// When the last sinks: entry goes, a legacy peer: it was shadowing goes
	// too, and so does that peer's key unless something else uses it.
	legacyPeer := ""
	if len(cfg.Sinks) > 0 && len(remaining) == 0 && cfg.Peer.Hostname != "" && cfg.Peer.Hostname != removed.Peer {
		legacyPeer = cfg.Peer.Hostname
	}

	// A sink.yaml that does not load cannot prove it does not use the key,
	// so refuse before changing anything rather than delete a key it needs.
	if (removed.Peer != "" || legacyPeer != "") && fileExists(filepath.Join(configDir, "sink.yaml")) {
		if _, err := config.LoadSink(configDir); err != nil {
			return fmt.Errorf("%s does not load, so it is not safe to remove a sink (fix it first): %w", filepath.Join(configDir, "sink.yaml"), err)
		}
	}

	// Deleting a node that holds a YAML anchor would leave its aliases
	// dangling and source.yaml unloadable, so refuse before changing
	// anything when the file uses anchors or aliases.
	if usesAnchors, err := yamlFileUsesAnchors(path); err != nil {
		return err
	} else if usesAnchors {
		return fmt.Errorf("%s uses YAML anchors or aliases, so it is not safe to edit automatically; remove %s by hand", path, label)
	}

	// While teammates.yaml lists anyone, GET /pull refuses the legacy
	// shared secret (authorizePull), whatever this command does with it.
	teamActive := teammates != nil && len(teammates.Teammates) > 0

	secret := cfg.Security.SharedSecret
	secretUser := ""
	if secret != "" {
		secretUser = sharedSecretUser(configDir, remaining)
	}
	dropSecret := opts.RevokeSharedSecret && secret != ""
	if dropSecret && secretUser != "" {
		return fmt.Errorf("not removing %s: --revoke-shared-secret would break it, because %s; pair that sink first, or remove without the flag", label, secretUser)
	}

	// Delete keys before editing source.yaml: if a delete fails the sink
	// is still listed and the command can be retried, and if the edit
	// fails the sink is already cut off.
	var deleted []string // key files deleted so far, for error messages
	deletedNote := func() string {
		if len(deleted) == 0 {
			return ""
		}
		return " (already deleted: " + strings.Join(deleted, ", ") + ")"
	}
	keyUser, keyForTeammate := "", false
	if removed.Peer != "" {
		keyUser, keyForTeammate = peerKeyUser(configDir, removed.Peer, remaining, teammates)
		if keyUser == "" {
			if err := keystore.Delete(configDir, removed.Peer); err != nil {
				return fmt.Errorf("could not delete the key for %s, so %s is unchanged (fix the error and retry): %w", label, path, err)
			}
			deleted = append(deleted, keyFileName(removed.Peer))
		}
	}
	legacyKeyUser, legacyKeyDeleted := "", false
	if legacyPeer != "" {
		legacyKeyUser, _ = peerKeyUser(configDir, legacyPeer, remaining, teammates)
		if legacyKeyUser == "" && hasPeerKeyFile(configDir, legacyPeer) {
			if err := keystore.Delete(configDir, legacyPeer); err != nil {
				return fmt.Errorf("could not delete the key for legacy peer %s, so %s is unchanged%s (fix the error and retry): %w", legacyPeer, path, deletedNote(), err)
			}
			legacyKeyDeleted = true
			deleted = append(deleted, keyFileName(legacyPeer))
		}
	}

	changed, err := editYAMLFile(path, func(doc *yaml.Node) (bool, error) {
		return removeSinkNode(doc, cfg, removed, len(remaining) == 0, dropSecret), nil
	})
	if err != nil {
		if len(deleted) > 0 {
			return fmt.Errorf("could not update %s, so remove %s from it by hand%s: %w", path, label, deletedNote(), err)
		}
		return err
	}
	if !changed {
		if len(deleted) > 0 {
			return fmt.Errorf("could not find %s in %s to remove it, so remove it by hand%s", label, path, deletedNote())
		}
		return fmt.Errorf("could not find %s in %s to remove it", label, path)
	}

	// pushContinues: a running source still has a credential to seal for
	// the removed sink, so it keeps pushing to it until it restarts.
	// pullContinues: the removed machine may still pass GET /pull with a
	// credential this command kept, and a restart does not change that.
	// A teammate's key is kept, but a running source already skips legacy
	// pushes to any peer teammates.yaml lists (SuppressPush).
	pushContinues := removed.Peer == "" || (keyUser != "" && !keyForTeammate)
	pullContinues := keyUser != "" || legacyKeyUser != "" || (secret != "" && !dropSecret && !teamActive)
	// secretLive: the running source still accepts the dropped shared
	// secret on GET /pull until it restarts.
	secretLive := dropSecret && !teamActive

	switch {
	case removed.Peer == "":
		fmt.Fprintf(out, "Removed sink %s from %s.\n", label, path)
	case keyUser != "":
		fmt.Fprintf(out, "Removed sink %s from %s. Its key stays, because %s still uses it, so %s can keep pulling with that key on GET /pull until the shared key is rotated and every legitimate user of it is re-paired.\n", label, path, keyUser, removed.Peer)
	default:
		fmt.Fprintf(out, "Removed sink %s from %s and deleted its key. A running source stops sealing for it at its next push, and GET /pull refuses that key now.\n", label, path)
	}
	secretWhy := ""
	if secretUser != "" {
		secretWhy = ", because " + secretUser
	}
	switch {
	case opts.RevokeSharedSecret && secret == "":
		fmt.Fprintf(out, "%s has no security.shared_secret, so there was none to revoke.\n", path)
	case secretLive:
		fmt.Fprintf(out, "Also removed security.shared_secret. A running source still accepts it on GET /pull until it restarts, so the restart is what cuts off every machine that pulls with it, including pull-only sinks not listed in %s.\n", path)
	case dropSecret:
		fmt.Fprintf(out, "Also removed security.shared_secret. GET /pull already refuses it while %s lists teammates.\n", config.TeammatesFile)
	case secret != "" && teamActive:
		fmt.Fprintf(out, "security.shared_secret stays%s. GET /pull refuses it while %s lists teammates; rotate it if the removed machine has it, in case the teammates are removed later.\n", secretWhy, config.TeammatesFile)
	case secret != "":
		fmt.Fprintf(out, "security.shared_secret stays%s. The removed machine can keep pulling with it, if it has it, until the secret is rotated. Pull-only sinks not listed in %s may use it too, so it is removed only with --revoke-shared-secret.\n", secretWhy, path)
	}
	if legacyPeer != "" {
		switch {
		case legacyKeyDeleted:
			fmt.Fprintf(out, "Also removed the legacy sink:/peer: %s that the sinks: list was shadowing, and deleted keys/%s.json, so GET /pull refuses that key now.\n", legacyPeer, legacyPeer)
		case legacyKeyUser != "":
			fmt.Fprintf(out, "Also removed the legacy sink:/peer: %s that the sinks: list was shadowing. keys/%s.json stays, because %s still uses it, so %s can keep pulling with that key until it is rotated.\n", legacyPeer, legacyPeer, legacyKeyUser, legacyPeer)
		default:
			fmt.Fprintf(out, "Also removed the legacy sink:/peer: %s that the sinks: list was shadowing.\n", legacyPeer)
		}
	}
	switch {
	case pushContinues && pullContinues:
		fmt.Fprintf(out, "A running source keeps pushing to it until you restart the source (%s). That stops the pushes only, not pulls with the kept credential.\n", sourceRestartHint)
	case secretLive && pushContinues:
		fmt.Fprintf(out, "Restart the source now (%s): until it restarts it keeps pushing to the sink and accepting the shared secret on GET /pull.\n", sourceRestartHint)
	case secretLive:
		fmt.Fprintf(out, "Restart the source now (%s): until it restarts it accepts the shared secret on GET /pull and lists the sink in its push list, status and doctor.\n", sourceRestartHint)
	case pushContinues:
		fmt.Fprintf(out, "A running source keeps pushing to it until it restarts, so restart the source now (%s).\n", sourceRestartHint)
	case keyForTeammate:
		fmt.Fprintf(out, "A running source already skips pushes to a listed teammate. Restart the source so it drops the sink from its push list, status and doctor (%s).\n", sourceRestartHint)
	default:
		fmt.Fprintf(out, "Restart the source so it drops the sink from its push list, status and doctor (%s).\n", sourceRestartHint)
	}
	fmt.Fprintf(out, "Cookies and secrets %s already received stay valid on that machine until they expire or you sign out of those sites.\n", label)
	return nil
}

// findSink returns the index of the sink named by target: a peer name
// first, then a URL. It returns -1 when no sink matches.
func findSink(sinks []config.SinkTarget, target string) int {
	for i, s := range sinks {
		if s.Peer != "" && s.Peer == target {
			return i
		}
	}
	for i, s := range sinks {
		if s.URL == target {
			return i
		}
	}
	return -1
}

func sinkLabel(s config.SinkTarget) string {
	if s.Peer == "" {
		return s.URL
	}
	return fmt.Sprintf("%s (%s)", s.Peer, s.URL)
}

// sharedSecretUser names what may still use security.shared_secret after
// the removal, or returns "" when it can be dropped. A sink without a peer
// seals with it. A peered sink whose key file is missing here may have
// fallen back to it on its own side, so dropping it could break that sink.
func sharedSecretUser(configDir string, remaining []config.SinkTarget) string {
	for _, s := range remaining {
		if s.Peer == "" {
			return "sink " + s.URL + " has no peer and uses it"
		}
	}
	for _, s := range remaining {
		if !hasPeerKeyFile(configDir, s.Peer) {
			return "sink " + sinkLabel(s) + " has no paired key here and may still use it"
		}
	}
	return ""
}

func hasPeerKeyFile(configDir, peer string) bool {
	_, err := keystore.Load(configDir, peer)
	return err == nil
}

// peerKeyUser names what else still seals or authenticates with peer's key,
// or returns "" when nothing does and the key can be deleted. Names are
// compared by key file (sameKeyFile), because keystore.Path sanitizes
// them, so "a/b" and "a_b" share a file. teammate reports whether a
// teammate in teammates.yaml has exactly peer's name, so a running source
// already skips pushes to it.
func peerKeyUser(configDir, peer string, remaining []config.SinkTarget, teammates *config.TeammatesConfig) (user string, teammate bool) {
	for _, s := range remaining {
		if s.Peer != "" && sameKeyFile(configDir, s.Peer, peer) {
			return "sink " + sinkLabel(s), false
		}
	}
	if teammates != nil {
		for _, tm := range teammates.Teammates {
			if sameKeyFile(configDir, tm.Name, peer) {
				// The source skips pushes only to a peer whose name a
				// teammate matches exactly (SuppressPush), not to one
				// that merely shares the key file.
				return "teammate " + tm.Name + " in " + config.TeammatesFile, tm.Name == peer
			}
		}
	}
	if fileExists(filepath.Join(configDir, "sink.yaml")) {
		if sink, err := config.LoadSink(configDir); err == nil && sink.Peer.Hostname != "" && sameKeyFile(configDir, sink.Peer.Hostname, peer) {
			return "this machine's sink.yaml", false
		}
	}
	// GET /pull accepts every key file, so another file holding the same
	// key bytes keeps the credential valid after this one is deleted.
	if other := duplicateKeyFile(configDir, peer); other != "" {
		return other + ", which holds the same key,", false
	}
	return "", false
}

// sameKeyFile reports whether two peer names map to the same key file on
// disk: the same sanitized path, or two paths the filesystem resolves to
// one file (a case-insensitive disk treats keys/Bot.json and keys/bot.json
// as one file; a case-sensitive one does not).
func sameKeyFile(configDir, a, b string) bool {
	pa, errA := keystore.Path(configDir, a)
	pb, errB := keystore.Path(configDir, b)
	if errA != nil || errB != nil {
		return false
	}
	if pa == pb {
		return true
	}
	fa, errA := os.Stat(pa)
	fb, errB := os.Stat(pb)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// keyFileName is the keys/ path, relative to the config dir, that peer's
// key is stored at.
func keyFileName(peer string) string {
	p, err := keystore.Path("", peer)
	if err != nil {
		return ""
	}
	return p
}

// duplicateKeyFile returns another key file (keys/<name>.json) holding
// the same key bytes as peer's, or "".
func duplicateKeyFile(configDir, peer string) string {
	k, err := keystore.Load(configDir, peer)
	if err != nil {
		return ""
	}
	names, err := keystore.List(configDir)
	if err != nil {
		return ""
	}
	for _, name := range names {
		if sameKeyFile(configDir, name, peer) {
			continue
		}
		if o, err := keystore.Load(configDir, name); err == nil && bytes.Equal(o.Key, k.Key) {
			return keyFileName(name)
		}
	}
	return ""
}

// yamlFileUsesAnchors reports whether the YAML file at path defines an
// anchor or uses an alias anywhere.
func yamlFileUsesAnchors(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	var walk func(n *yaml.Node) bool
	walk = func(n *yaml.Node) bool {
		if n.Anchor != "" || n.Kind == yaml.AliasNode {
			return true
		}
		return slices.ContainsFunc(n.Content, walk)
	}
	return walk(&doc), nil
}

// removeSinkNode removes removed from source.yaml's node tree, keeping
// every comment and other key. A sinks: list loses the matching entry; a
// legacy single-sink config loses its sink: and peer: keys. When last is
// set nothing is left to deliver to, so a now-empty sinks: key and any
// legacy sink:/peer: it was shadowing go too, so neither resurfaces.
// dropSecret also removes security.shared_secret.
func removeSinkNode(doc *yaml.Node, cfg *config.SourceConfig, removed config.SinkTarget, last, dropSecret bool) bool {
	changed := false
	if len(cfg.Sinks) > 0 {
		seq := mappingValue(doc, "sinks")
		if seq == nil || seq.Kind != yaml.SequenceNode {
			return false
		}
		for i, item := range seq.Content {
			if scalarValue(item, "url") == removed.URL && scalarValue(item, "peer") == removed.Peer {
				seq.Content = slices.Delete(seq.Content, i, i+1)
				changed = true
				break
			}
		}
		if !changed {
			return false
		}
		if len(seq.Content) == 0 {
			deleteMappingKey(doc, "sinks")
		}
	}
	if len(cfg.Sinks) == 0 || last {
		if deleteMappingKey(doc, "sink") {
			changed = true
		}
		deleteMappingKey(doc, "peer")
	}
	if dropSecret {
		if sec := mappingValue(doc, "security"); sec != nil {
			deleteMappingKey(sec, "shared_secret")
			if len(sec.Content) == 0 {
				deleteMappingKey(doc, "security")
			}
		}
	}
	return changed
}

// scalarValue returns key's scalar value in a mapping node, or "".
func scalarValue(n *yaml.Node, key string) string {
	if v := mappingValue(n, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

// deleteMappingKey removes key and its value from a mapping node (or a
// document whose root is one) and reports whether it was there. yaml.v3
// attaches a file's leading comments to the first key, so deleting the
// first key of a document moves its head comment onto the next key (or
// the document) rather than dropping it.
func deleteMappingKey(n *yaml.Node, key string) bool {
	if n == nil {
		return false
	}
	doc := (*yaml.Node)(nil)
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		doc, n = n, n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != key {
			continue
		}
		if head := n.Content[i].HeadComment; doc != nil && i == 0 && head != "" {
			if len(n.Content) > 2 {
				n.Content[2].HeadComment = joinComments(head, n.Content[2].HeadComment)
			} else {
				doc.HeadComment = joinComments(doc.HeadComment, head)
			}
		}
		n.Content = slices.Delete(n.Content, i, i+2)
		return true
	}
	return false
}

func joinComments(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n" + b
}
