package cli

import (
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
	removeCmd := &cobra.Command{
		Use:   "remove <peer|url>",
		Short: "Stop pushing to a sink: remove it from source.yaml and delete its key",
		Long: `remove takes a sink out of source.yaml and deletes its paired key, so
the sink stops receiving cookies and secrets. Name the sink by its peer, or
by its URL for a sink without one (a legacy shared-secret sink).

Deleting the key is what cuts the sink off: a running source reads each
sink's key on every push, so it stops sealing for the removed sink at its
next push, and GET /pull refuses that key right away. The running source
keeps the sink in its list, and reports it failing, until it restarts.

The key is kept when another sink, a teammate in teammates.yaml, or this
machine's sink.yaml still uses that peer name. Removing a legacy sink
without a peer also removes security.shared_secret when no other sink uses
it. In both cases the running source still holds the credential, and keeps
pushing to the sink until it restarts.

The only sink cannot be removed unless teammates.yaml lists a teammate,
because a source with nothing to deliver to does not start. Pair another
sink first, or stop the source with agentcookie wizard uninstall.

Cookies and secrets the sink already received stay valid on that machine
until they expire or you sign out of those sites.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSinksRemove(cmd.OutOrStdout(), common.ConfigDir, args[0])
		},
	}
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
	var rows []SinkStatus
	for _, s := range cfg.ResolvedSinks() {
		row := SinkStatus{Peer: s.Peer, URL: s.URL, Key: sinkKeyState(configDir, s)}
		if st != nil {
			for _, rec := range st.Sinks {
				if sinkConfigured([]config.SinkTarget{s}, rec) {
					row.LastPush, row.TotalPushes, row.TotalFailures, row.LastError = rec.LastPush, rec.TotalPushes, rec.TotalFailures, rec.LastError
					break
				}
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
	if _, err := keystore.Load(configDir, s.Peer); err != nil {
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
			note = "no paired key; pair it again with agentcookie wizard install --as source --add-sink --repair --peer " + r.Peer
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

func runSinksRemove(out io.Writer, configDir, target string) error {
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

	dropSecret := removed.Peer == "" && cfg.Security.SharedSecret != "" && !anyPeerless(remaining)
	changed, err := editYAMLFile(path, func(doc *yaml.Node) (bool, error) {
		return removeSinkNode(doc, cfg, removed, len(remaining) == 0, dropSecret), nil
	})
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("could not find %s in %s to remove it", label, path)
	}

	// cutOff is whether the running source loses the sink's credential
	// now. Otherwise it still holds the sink and its credential in memory
	// and keeps pushing to it until it restarts.
	cutOff := false
	switch {
	case removed.Peer == "" && dropSecret:
		fmt.Fprintf(out, "Removed sink %s and its credential, security.shared_secret, from %s.\n", label, path)
	case removed.Peer == "":
		fmt.Fprintf(out, "Removed sink %s from %s. security.shared_secret stays, because another sink without a peer still uses it.\n", label, path)
	default:
		if user := peerKeyUser(configDir, removed.Peer, remaining, teammates); user != "" {
			fmt.Fprintf(out, "Removed sink %s from %s. Its key stays, because %s still uses it.\n", label, path, user)
			break
		}
		if err := keystore.Delete(configDir, removed.Peer); err != nil {
			return fmt.Errorf("removed sink %s from %s, but could not delete its key: %w", label, path, err)
		}
		cutOff = true
		fmt.Fprintf(out, "Removed sink %s from %s and deleted its key. A running source stops sealing for it at its next push, and GET /pull refuses its key now.\n", label, path)
	}
	if cutOff {
		fmt.Fprintf(out, "Restart the source so it drops the sink from its push list, status and doctor (%s).\n", sourceRestartHint)
	} else {
		fmt.Fprintf(out, "A running source keeps pushing to it until it restarts, so restart the source now (%s).\n", sourceRestartHint)
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

func anyPeerless(sinks []config.SinkTarget) bool {
	for _, s := range sinks {
		if s.Peer == "" {
			return true
		}
	}
	return false
}

// peerKeyUser names what else still seals or authenticates with peer's key,
// or returns "" when nothing does and the key can be deleted.
func peerKeyUser(configDir, peer string, remaining []config.SinkTarget, teammates *config.TeammatesConfig) string {
	for _, s := range remaining {
		if s.Peer == peer {
			return "sink " + sinkLabel(s)
		}
	}
	if teammates != nil && teammates.Has(peer) {
		return "teammate " + peer + " in " + config.TeammatesFile
	}
	if fileExists(filepath.Join(configDir, "sink.yaml")) {
		if sink, err := config.LoadSink(configDir); err == nil && sink.Peer.Hostname == peer {
			return "this machine's sink.yaml"
		}
	}
	return ""
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
// document whose root is one) and reports whether it was there.
func deleteMappingKey(n *yaml.Node, key string) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = slices.Delete(n.Content, i, i+2)
			return true
		}
	}
	return false
}
