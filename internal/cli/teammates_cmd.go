package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/tincan"
)

var teammatesCmd = &cobra.Command{
	Use:   "teammates",
	Short: "List, approve, remove, and set up teammates (Tincan agents that pull cookies from this laptop)",
}

var teammatesApproveNode string

// teammateApproveWait is how long `teammates approve` waits for the
// running daemon to answer.
var teammateApproveWait = 20 * time.Second

func init() {
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "Show each teammate's state, machine, and last pull",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			rows, err := TeammateStatuses(common.ConfigDir, home)
			if err != nil {
				return err
			}
			if common.JSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			printTeammateStatuses(cmd.OutOrStdout(), rows, time.Now())
			return nil
		},
	}
	approveCmd := &cobra.Command{
		Use:   "approve <name> --node <stableid>",
		Short: "Approve the machine a teammate appeared on (same as Approve in the dialog)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			return runTeammatesApprove(cmd.Context(), cmd.OutOrStdout(), home, args[0], teammatesApproveNode)
		},
	}
	approveCmd.Flags().StringVar(&teammatesApproveNode, "node", "", "the Tailscale node StableID to approve, exactly as shown in the dialog or `teammates list`")
	_ = approveCmd.MarkFlagRequired("node")
	removeCmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a teammate from teammates.yaml and delete its key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTeammatesRemove(cmd.OutOrStdout(), common.ConfigDir, args[0])
		},
	}
	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "Find tincan, check the relay shows Tailscale nodes, and print the one-time owner steps",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			return runTeammatesSetup(cmd.Context(), cmd.OutOrStdout(), common.ConfigDir, home)
		},
	}
	teammatesCmd.AddCommand(listCmd, approveCmd, removeCmd, setupCmd)
}

// TeammateStatus is one teammate's state for `teammates list`, status and
// doctor.
type TeammateStatus struct {
	Name   string `json:"name"`
	Listed bool   `json:"listed"`
	// State is the lifecycle phase (state.Phase*), or "suspended" while
	// teammates.yaml does not load.
	State      string         `json:"state"`
	StateLabel string         `json:"state_label"`
	Cadence    config.Cadence `json:"cadence,omitempty"`
	Kind       string         `json:"kind,omitempty"`

	NodeID          string `json:"node_id,omitempty"`
	NodeName        string `json:"node_name,omitempty"`
	PendingNodeID   string `json:"pending_node_id,omitempty"`
	PendingNodeName string `json:"pending_node_name,omitempty"`
	PendingNodeUser string `json:"pending_node_user,omitempty"`
	Replaces        string `json:"replaces,omitempty"`

	ApprovedAt    time.Time `json:"approved_at,omitzero"`
	PairedAt      time.Time `json:"paired_at,omitzero"`
	LastPulledAt  time.Time `json:"last_pulled_at,omitzero"`
	LastPullError string    `json:"last_pull_error,omitempty"`
	RelaySeenAt   time.Time `json:"relay_seen_at,omitzero"`

	NotifyID      string    `json:"notify_id,omitempty"`
	NotifyPurpose string    `json:"notify_purpose,omitempty"`
	NotifyStatus  string    `json:"notify_status,omitempty"`
	NotifySentAt  time.Time `json:"notify_sent_at,omitzero"`
	NotifyError   string    `json:"notify_error,omitempty"`
	CodeExpiresAt time.Time `json:"code_expires_at,omitzero"`

	LastPairAttemptAt time.Time `json:"last_pair_attempt_at,omitzero"`
	PairCodeMissedAt  time.Time `json:"pair_code_missed_at,omitzero"`
}

// TeammateStatuses reads teammates.yaml and teammates-state.json (read
// only) and returns one row per listed or remembered teammate, listed
// names first in file order.
func TeammateStatuses(configDir, home string) ([]TeammateStatus, error) {
	cfg, cfgErr := config.LoadTeammates(configDir)
	store, err := state.OpenTeammateStore(state.TeammatesPath(home))
	if err != nil {
		return nil, err
	}
	snap := store.Snapshot()
	var rows []TeammateStatus
	seen := map[string]bool{}
	if cfg != nil {
		for _, tm := range cfg.Teammates {
			rec := TeammateRecordOrZero(snap, tm.Name)
			row := teammateStatusRow(tm.Name, true, rec)
			row.Cadence = tm.Cadence
			if row.Cadence == "" {
				row.Cadence = defaultCadence(rec.Kind)
			}
			rows = append(rows, row)
			seen[tm.Name] = true
		}
	}
	var rest []string
	for name := range snap.Teammates {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	for _, name := range rest {
		rec := TeammateRecordOrZero(snap, name)
		rows = append(rows, teammateStatusRow(name, cfgErr != nil && rec.RemovedAt.IsZero(), rec))
	}
	if cfgErr != nil {
		for i := range rows {
			rows[i].State, rows[i].StateLabel = "suspended", "suspended: teammates.yaml does not load ("+cfgErr.Error()+")"
		}
	}
	return rows, nil
}

// TeammateRecordOrZero returns name's record from snap, or a zero record.
func TeammateRecordOrZero(snap state.TeammatesState, name string) state.TeammateRecord {
	if r, ok := snap.Teammates[name]; ok && r != nil {
		return *r
	}
	return state.TeammateRecord{}
}

func teammateStatusRow(name string, listed bool, rec state.TeammateRecord) TeammateStatus {
	phase := rec.Phase
	if !listed && !rec.RemovedAt.IsZero() {
		phase = state.PhaseRemoved
	}
	return TeammateStatus{
		Name: name, Listed: listed, State: phase, StateLabel: phaseLabel(phase), Kind: rec.Kind,
		NodeID: rec.BoundNodeID, NodeName: rec.BoundNodeName,
		PendingNodeID: rec.PendingNodeID, PendingNodeName: rec.PendingNodeName, PendingNodeUser: rec.PendingNodeUser,
		Replaces:   rec.ReplacesNodeName,
		ApprovedAt: rec.ApprovedAt, PairedAt: rec.PairedAt, LastPulledAt: rec.LastPulledAt, LastPullError: rec.LastPullError,
		RelaySeenAt: rec.RelaySeenAt,
		NotifyID:    rec.NotifyID, NotifyPurpose: rec.NotifyPurpose, NotifyStatus: rec.NotifyStatus,
		NotifySentAt: rec.NotifySentAt, NotifyError: rec.NotifyError, CodeExpiresAt: rec.CodeExpiresAt,
		LastPairAttemptAt: rec.LastPairAttemptAt, PairCodeMissedAt: rec.PairCodeMissedAt,
	}
}

func printTeammateStatuses(w io.Writer, rows []TeammateStatus, now time.Time) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No teammates. List Tincan agent names in teammates.yaml (see examples/teammates.yaml).")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tMACHINE\tCADENCE\tLAST PULL\tNOTE")
	for _, r := range rows {
		machine := r.NodeName
		if machine == "" {
			machine = "-"
		}
		last := "never"
		if !r.LastPulledAt.IsZero() {
			last = humanAgo(now.Sub(r.LastPulledAt)) + " ago"
		}
		note := ""
		switch {
		case r.State == state.PhaseAwaitingApproval && r.PendingNodeID != "":
			note = fmt.Sprintf("%s (%s); approve: agentcookie teammates approve %s --node %s", r.PendingNodeName, r.PendingNodeUser, r.Name, r.PendingNodeID)
		case r.State == state.PhaseRemoved:
			note = "cookies it already received stay valid on that machine"
		case r.NotifyStatus == state.NotifyHeld:
			note = "add agentcookie to its unless list in the relay's approval.json"
		case r.NotifyError != "":
			note = r.NotifyError
		case r.LastPullError != "":
			note = r.LastPullError
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.StateLabel, machine, orDash(string(r.Cadence)), last, note)
	}
	_ = tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// queueTeammateApproval hands an approval to the daemon, which owns the
// state file.
func queueTeammateApproval(home, name, nodeID string, now time.Time) error {
	dir := teammateApprovalsDir(home)
	_ = os.Remove(filepath.Join(dir, name+".result.json"))
	return writeJSONFile(filepath.Join(dir, name+".json"), approvalIntent{NodeID: nodeID, RequestedAt: now.UTC()})
}

func runTeammatesApprove(ctx context.Context, out io.Writer, home, name, nodeID string) error {
	if name == "" || strings.ContainsAny(name, `/\.`) {
		return fmt.Errorf("invalid teammate name %q", name)
	}
	if nodeID == "" {
		return errors.New("--node is required")
	}
	if err := queueTeammateApproval(home, name, nodeID, time.Now()); err != nil {
		return fmt.Errorf("queue approval: %w", err)
	}
	resultPath := filepath.Join(teammateApprovalsDir(home), name+".result.json")
	deadline := time.Now().Add(teammateApproveWait)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(resultPath); err == nil {
			_ = os.Remove(resultPath)
			var res approvalResult
			if err := json.Unmarshal(data, &res); err != nil {
				return fmt.Errorf("read daemon answer: %w", err)
			}
			if !res.OK {
				return fmt.Errorf("not approved: %s", res.Message)
			}
			fmt.Fprintf(out, "Approved %s on node %s: %s\n", name, nodeID, res.Message)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	fmt.Fprintf(out, "Queued the approval of %s on node %s. The agentcookie daemon (source --watch) applies it when it runs; check with `agentcookie teammates list`.\n", name, nodeID)
	return nil
}

func runTeammatesRemove(out io.Writer, configDir, name string) error {
	path := config.TeammatesPath(configDir)
	removed, err := editYAMLFile(path, func(doc *yaml.Node) (bool, error) {
		seq := mappingValue(doc, "teammates")
		if seq == nil || seq.Kind != yaml.SequenceNode {
			return false, nil
		}
		for i, item := range seq.Content {
			if n := mappingValue(item, "name"); n != nil && n.Value == name {
				seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("%s is not listed in %s", name, path)
	}
	if err := keystore.Delete(configDir, name); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed %s from %s and deleted its key. Its next pull is refused, and the daemon withdraws any open notice to it.\n", name, path)
	fmt.Fprintf(out, "Cookies %s already received stay valid on that machine until they expire or you sign out of those sites.\n", name)
	return nil
}

// editYAMLFile applies edit to a config file (teammates.yaml, source.yaml)
// as a YAML node tree, so the owner's comments survive, and writes it back
// atomically, keeping its mode, when edit reports a change.
func editYAMLFile(path string, edit func(doc *yaml.Node) (bool, error)) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	changed, err := edit(&doc)
	if err != nil || !changed {
		return false, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return false, err
	}
	_ = enc.Close()
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := writeFileAtomic(path, buf.Bytes(), mode); err != nil {
		return false, err
	}
	return true, nil
}

// mappingValue returns key's value in a mapping node (or a document whose
// root is one), or nil.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func runTeammatesSetup(ctx context.Context, out io.Writer, configDir, home string) error {
	cfg, cfgErr := config.LoadTeammates(configDir)
	if cfgErr != nil {
		return cfgErr
	}
	var configured, tincanConf string
	if cfg != nil {
		configured, tincanConf = cfg.TincanPath, cfg.TincanConfig
	}
	if tincanConf == "" {
		tincanConf = tincan.DefaultConfigPath(home)
	}
	listPath := config.TeammatesPath(configDir)

	path, err := tincan.Resolve(configured, home)
	if err != nil {
		fmt.Fprintf(out, "tincan: not found (%v)\nInstall the tincan CLI, or set tincan_path in %s, then run setup again.\n", err, listPath)
		return nil
	}
	fmt.Fprintf(out, "tincan: %s\n", path)
	if configured == "" && cfg != nil {
		if _, err := editYAMLFile(listPath, func(doc *yaml.Node) (bool, error) {
			root := doc.Content[0]
			if root.Kind != yaml.MappingNode || mappingValue(doc, "tincan_path") != nil {
				return false, nil
			}
			root.Content = append(root.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: "tincan_path"},
				&yaml.Node{Kind: yaml.ScalarNode, Value: path})
			return true, nil
		}); err != nil {
			fmt.Fprintf(out, "  could not record it in %s (%v); add: tincan_path: %s\n", listPath, err, path)
		} else {
			fmt.Fprintf(out, "  recorded as tincan_path in %s (the launchd PATH does not include ~/bin)\n", listPath)
		}
	} else if cfg == nil {
		fmt.Fprintf(out, "  add `tincan_path: %s` to %s when you create it\n", path, listPath)
	}

	fmt.Fprintf(out, "TINCAN_CONFIG for the agentcookie agent: %s\n", tincanConf)
	joined := false
	if _, err := os.Stat(tincanConf); err == nil {
		joined = true
		c := &tincan.Client{Path: path, Config: tincanConf}
		roster, err := c.Roster(ctx)
		switch {
		case err != nil:
			fmt.Fprintf(out, "  roster: cannot read it (%v)\n", err)
		case roster.NodeFieldsVisible():
			fmt.Fprintln(out, "  roster: OK, this laptop sees each agent's Tailscale node (admin view)")
		default:
			fmt.Fprintln(out, "  roster: no Tailscale nodes shown. Upgrade the relay to a release with admin node fields, and add this laptop's Tailscale name to the relay's --admin list. Until then every teammate waits for its machine.")
		}
	} else {
		fmt.Fprintln(out, "  not joined yet (steps 1 and 2 below)")
	}

	laptop, _ := laptopDNSNameFunc(ctx)
	if laptop == "" {
		laptop = "<this laptop's MagicDNS name>"
	}
	var names []string
	if cfg != nil {
		names = cfg.Names()
	}
	fmt.Fprintln(out, "\nOne-time owner steps:")
	if !joined {
		fmt.Fprintln(out, "  1. On the relay host or an admin device: tincan invite agentcookie")
		fmt.Fprintf(out, "  2. On this laptop, with its own config so it is not your Claude Code identity:\n     TINCAN_CONFIG=%s %s join <invite-code> --relay <relay-url>\n", tincanConf, path)
	} else {
		fmt.Fprintln(out, "  1-2. agentcookie is already joined to Tincan.")
	}
	fmt.Fprintf(out, "  3. Tailnet ACL: let each teammate's Tailscale tag reach this laptop's port 9998, one rule per tag, for example:\n     {\"action\": \"accept\", \"src\": [\"tag:<teammate-tag>\"], \"dst\": [\"%s:9998\"]}\n", laptop)
	if len(names) > 0 {
		fmt.Fprintf(out, "     Teammates: %s\n", strings.Join(names, ", "))
	}
	fmt.Fprintln(out, "  4. Tincan approval gate: if approval.json on the relay host gates a teammate, add \"agentcookie\" to its \"unless\" list (or keep it out of its \"from\" list), so pairing notices are not held.")
	fmt.Fprintln(out, "  5. Before you add an agent to teammates.yaml, give it its standing line (its pairing notice repeats it). An agent without the line may stop at its first pairing notice to ask you; a fresh code is sent automatically the next time it checks Tincan:")
	fmt.Fprintf(out, "     %s\n", teammateStandingLine())
	fmt.Fprintln(out, "  6. Then add the agent's name to teammates.yaml and approve its machine when the dialog appears.")
	return nil
}
