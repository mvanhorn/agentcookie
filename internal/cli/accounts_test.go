package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

func TestNormalizeAccountDomain(t *testing.T) {
	cases := map[string]string{
		"x.com":                    "x.com",
		".x.com":                   "x.com",
		"*.x.com":                  "x.com",
		"https://www.amazon.com/a": "www.amazon.com",
		"example.com:443":          "example.com",
		"Example.COM.":             "example.com",
	}
	for in, want := range cases {
		got, err := normalizeAccountDomain(in)
		if err != nil {
			t.Fatalf("normalizeAccountDomain(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("normalizeAccountDomain(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"", "https://", "bad domain.com", "-x.com", "x..com", "x.com?bad=1"} {
		if _, err := normalizeAccountDomain(in); err == nil {
			t.Errorf("normalizeAccountDomain(%q) should fail", in)
		}
	}
}

func TestAccountBlockPatternsAvoidSiblingDomains(t *testing.T) {
	bl := &config.Blocklist{Version: 1}
	if !addAccountBlock(bl, "amazon.com") {
		t.Fatal("addAccountBlock should report changed on first add")
	}
	matcher := protocol.NewBlocklistMatcher(bl)
	for _, host := range []string{"amazon.com", ".amazon.com", "www.amazon.com", "sellercentral.amazon.com"} {
		if !matcher.MatchesHost(host) {
			t.Errorf("%q should be blocked", host)
		}
	}
	for _, host := range []string{"evilamazon.com", "amazon-adsystem.com", "amazon.co.uk"} {
		if matcher.MatchesHost(host) {
			t.Errorf("%q should not be blocked", host)
		}
	}
}

func TestAccountsToggleWritesBlocklist(t *testing.T) {
	dir := t.TempDir()
	oldDir := common.ConfigDir
	oldJSON := common.JSON
	common.ConfigDir = dir
	common.JSON = false
	t.Cleanup(func() {
		common.ConfigDir = oldDir
		common.JSON = oldJSON
	})

	offOut := &bytes.Buffer{}
	if err := runAccountsToggle(commandWithOutput(offOut), "x.com", false); err != nil {
		t.Fatalf("accounts off: %v", err)
	}
	if !strings.Contains(offOut.String(), "x.com disabled") {
		t.Errorf("unexpected off output: %q", offOut.String())
	}
	bl, err := config.LoadBlocklist(dir)
	if err != nil {
		t.Fatalf("LoadBlocklist: %v", err)
	}
	if got := patternsFromBlocklist(bl); strings.Join(got, ",") != "%.x.com,x.com" {
		t.Fatalf("patterns after off = %v", got)
	}

	offAgain := &bytes.Buffer{}
	if err := runAccountsToggle(commandWithOutput(offAgain), "x.com", false); err != nil {
		t.Fatalf("accounts off again: %v", err)
	}
	if !strings.Contains(offAgain.String(), "already disabled") {
		t.Errorf("unexpected off-again output: %q", offAgain.String())
	}

	onOut := &bytes.Buffer{}
	if err := runAccountsToggle(commandWithOutput(onOut), "x.com", true); err != nil {
		t.Fatalf("accounts on: %v", err)
	}
	if !strings.Contains(onOut.String(), "x.com enabled") {
		t.Errorf("unexpected on output: %q", onOut.String())
	}
	bl, err = config.LoadBlocklist(dir)
	if err != nil {
		t.Fatalf("LoadBlocklist after on: %v", err)
	}
	if len(bl.Domains) != 0 {
		t.Fatalf("patterns after on = %v", patternsFromBlocklist(bl))
	}

	info, err := os.Stat(filepath.Join(dir, "blocklist.yaml"))
	if err != nil {
		t.Fatalf("stat blocklist: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("blocklist mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestAccountsOnRemovesLegacyBroadPattern(t *testing.T) {
	bl := &config.Blocklist{Version: 1, Domains: []config.BlocklistEntry{
		{Pattern: "%x.com"},
		{Pattern: "%.keep.com"},
	}}
	if !removeAccountBlock(bl, "x.com") {
		t.Fatal("removeAccountBlock should remove legacy broad pattern")
	}
	if got := patternsFromBlocklist(bl); strings.Join(got, ",") != "%.keep.com" {
		t.Fatalf("patterns after remove = %v", got)
	}
}

func TestAccountsToggleJSON(t *testing.T) {
	dir := t.TempDir()
	oldDir := common.ConfigDir
	oldJSON := common.JSON
	common.ConfigDir = dir
	common.JSON = true
	t.Cleanup(func() {
		common.ConfigDir = oldDir
		common.JSON = oldJSON
	})

	out := &bytes.Buffer{}
	if err := runAccountsToggle(commandWithOutput(out), "x.com", false); err != nil {
		t.Fatalf("accounts off json: %v", err)
	}
	var got accountsToggleResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v (%s)", err, out.String())
	}
	if got.Action != "off" || got.Domain != "x.com" || !got.Changed || len(got.Patterns) != 2 {
		t.Fatalf("unexpected JSON result: %+v", got)
	}
}

func TestAccountsList(t *testing.T) {
	oldJSON := common.JSON
	common.JSON = false
	t.Cleanup(func() { common.JSON = oldJSON })

	bl := &config.Blocklist{Version: 1, Domains: []config.BlocklistEntry{
		{Pattern: "%.x.com"},
		{Pattern: "x.com"},
		{Pattern: "%custom.test"},
	}}
	out := &bytes.Buffer{}
	if err := emitAccountsList(commandWithOutput(out), bl); err != nil {
		t.Fatalf("emitAccountsList: %v", err)
	}
	if got := out.String(); got != "x.com\npatterns:\n  %custom.test\n" {
		t.Errorf("list output = %q", got)
	}
	common.JSON = true
	out.Reset()
	if err := emitAccountsList(commandWithOutput(out), bl); err != nil {
		t.Fatalf("emitAccountsList json: %v", err)
	}
	if !strings.Contains(out.String(), "\"domains\"") || !strings.Contains(out.String(), "\"patterns\"") {
		t.Errorf("expected JSON list, got %q", out.String())
	}
}

func TestAccountsCommandHonorsConfigDirFlag(t *testing.T) {
	dir := t.TempDir()
	oldDir := common.ConfigDir
	oldJSON := common.JSON
	common.JSON = false
	t.Cleanup(func() {
		common.ConfigDir = oldDir
		common.JSON = oldJSON
	})

	root := &cobra.Command{Use: "agentcookie", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&common.ConfigDir, "config-dir", defaultConfigDir(), "config dir")
	root.PersistentFlags().BoolVar(&common.JSON, "json", false, "json")
	root.AddCommand(accountsCmd)
	t.Cleanup(func() { root.RemoveCommand(accountsCmd) })

	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetArgs([]string{"--config-dir", dir, "accounts", "off", "x.com"})
	if err := root.Execute(); err != nil {
		t.Fatalf("root accounts off: %v", err)
	}
	bl, err := config.LoadBlocklist(dir)
	if err != nil {
		t.Fatalf("LoadBlocklist: %v", err)
	}
	if got := patternsFromBlocklist(bl); strings.Join(got, ",") != "%.x.com,x.com" {
		t.Fatalf("patterns after root command = %v", got)
	}
}

func commandWithOutput(out *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	return cmd
}

func patternsFromBlocklist(bl *config.Blocklist) []string {
	patterns := make([]string, 0, len(bl.Domains))
	for _, d := range bl.Domains {
		patterns = append(patterns, d.Pattern)
	}
	sort.Strings(patterns)
	return patterns
}
