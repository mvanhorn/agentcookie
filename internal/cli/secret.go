package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/envfile"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/secretsbus"
)

var secretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Manage per-CLI secrets in the agentcookie secrets bus",
	Long: `agentcookie secret manages per-CLI secrets/auth-tokens that sync from
the laptop to the sink alongside cookies. Each CLI gets its own
secrets.env under ~/.agentcookie/secrets/<cli>/.

Friend-facing workflow:

  agentcookie secret set tesla-pp-cli TESLA_OAUTH_BEARER
  agentcookie secret list
  agentcookie secret get tesla-pp-cli TESLA_OAUTH_BEARER
  agentcookie secret import-from ~/.config/tesla-pp-cli/auth.json --as tesla-pp-cli
  agentcookie secret rm tesla-pp-cli TESLA_OAUTH_BEARER

See docs/spec-agentcookie-secrets-bus-v1.md for the format.`,
}

var secretListCmd = &cobra.Command{
	Use:   "list",
	Short: "List CLIs registered in the bus and their key names (no values)",
	RunE:  runSecretList,
}

var secretGetCmd = &cobra.Command{
	Use:   "get <cli-name> <key>",
	Short: "Print the value of a single key to stdout",
	Args:  cobra.ExactArgs(2),
	RunE:  runSecretGet,
}

var secretSetCmd = &cobra.Command{
	Use:   "set <cli-name> <key>",
	Short: "Set a key; reads the value from stdin (pipe) or prompts (TTY)",
	Args:  cobra.ExactArgs(2),
	RunE:  runSecretSet,
}

var secretRmCmd = &cobra.Command{
	Use:   "rm <cli-name> [<key>]",
	Short: "Remove a key or the entire CLI directory",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runSecretRm,
}

var (
	secretImportAs string
)

var secretImportFromCmd = &cobra.Command{
	Use:   "import-from <path>",
	Short: "Ingest an existing config file (JSON, TOML, env) into the standard layout",
	Args:  cobra.ExactArgs(1),
	RunE:  runSecretImportFrom,
}

var secretEnvNoAliases bool

var secretEnvCmd = &cobra.Command{
	Use:   "env <cli-name>",
	Short: "Print all keys for a CLI as shell-friendly export lines (for `eval $(...)`)",
	Args:  cobra.ExactArgs(1),
	RunE:  runSecretEnv,
}

var secretLinkConfigsApply bool

var secretLinkConfigsCmd = &cobra.Command{
	Use:   "link-configs",
	Short: "Link carried CLI configs from ~/.agentcookie/ into ~/.config/ (dry run by default)",
	Long: `Carried configs materialize under ~/.agentcookie/<cli>/config.toml, but a
PP CLI reads ~/.config/<cli>/config.toml. This links the second to the first.

The bus deliberately never writes outside ~/.agentcookie/, so this is a
separate, explicit step rather than something a manifest can trigger.

It prints what it would do and changes nothing unless --apply is passed. An
existing config file is never replaced, and a symlink pointing anywhere other
than ~/.agentcookie/ is refused rather than written through. That applies to
the whole destination path: a symlinked ~/.config/<cli> directory would carry
the link out of ~/.config, so it is refused too.`,
	Args: cobra.NoArgs,
	RunE: runSecretLinkConfigs,
}

func init() {
	secretCmd.AddCommand(secretListCmd, secretGetCmd, secretSetCmd, secretRmCmd, secretImportFromCmd, secretEnvCmd, secretAliasCmd, secretLinkConfigsCmd)
	secretImportFromCmd.Flags().StringVar(&secretImportAs, "as", "", "cli-name to file the imported secrets under (required)")
	secretLinkConfigsCmd.Flags().BoolVar(&secretLinkConfigsApply, "apply", false, "actually create the links (default: dry run)")
	secretEnvCmd.Flags().BoolVar(&secretEnvNoAliases, "no-aliases", false, "print only the keys stored in the bus, without alias names (what the reader libraries return)")
}

func runSecretLinkConfigs(cmd *cobra.Command, _ []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home: %w", err)
	}
	plan, err := secretsbus.PlanConfigLinks(home)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(plan) == 0 {
		fmt.Fprintln(out, "no carried configs materialized yet; nothing to link")
		return nil
	}

	for _, e := range plan {
		switch e.Action {
		case secretsbus.LinkActionLink:
			verb := "would link"
			if secretLinkConfigsApply {
				verb = "linking"
			}
			fmt.Fprintf(out, "  %s %s -> %s\n", verb, e.Destination, e.Materialized)
		case secretsbus.LinkActionAlreadyLinked:
			fmt.Fprintf(out, "  ok      %s (already linked)\n", e.Destination)
		case secretsbus.LinkActionRefuse:
			fmt.Fprintf(out, "  SKIP    %s: %s\n", e.Destination, e.Reason)
		}
	}

	if !secretLinkConfigsApply {
		fmt.Fprintln(out, "\ndry run; re-run with --apply to create the links")
		return nil
	}

	applied, errs := secretsbus.ApplyConfigLinks(home, plan)
	fmt.Fprintf(out, "\nlinked %d config(s)\n", applied)
	for _, e := range errs {
		fmt.Fprintf(cmd.ErrOrStderr(), "  skipped: %v\n", e)
	}
	return nil
}

// secretsRoot resolves to the v1 standard path. Kept as a helper for tests.
func secretsRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentcookie", "secrets")
}

func runSecretList(cmd *cobra.Command, _ []string) error {
	root := secretsRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(cmd.OutOrStdout(), "secrets bus is empty (no CLIs registered)")
			return nil
		}
		return fmt.Errorf("read secrets root %s: %w", root, err)
	}
	type entry struct {
		Name string   `json:"name"`
		Keys []string `json:"keys"`
	}
	var out []entry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		envPath := filepath.Join(root, e.Name(), "secrets.env")
		kv, err := readEnvKeysOnly(envPath)
		if err != nil {
			continue
		}
		sort.Strings(kv)
		out = append(out, entry{Name: e.Name(), Keys: kv})
	}
	if common.JSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
	if len(out) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "secrets bus is empty (no CLIs registered)")
		return nil
	}
	for _, e := range out {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", e.Name)
		for _, k := range e.Keys {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", k)
		}
	}
	return nil
}

func runSecretGet(cmd *cobra.Command, args []string) error {
	cliName, key := args[0], args[1]
	if !validBusName(cliName) {
		return fmt.Errorf("invalid cli-name %q", cliName)
	}
	// First check sealed, then plaintext.
	sealedPath := filepath.Join(secretsRoot(), cliName, "secrets.env.sealed")
	plainPath := filepath.Join(secretsRoot(), cliName, "secrets.env")

	if _, err := os.Stat(sealedPath); err == nil {
		mk, err := keystore.ReadMasterKey()
		if err != nil {
			return fmt.Errorf("read master key for sealed file: %w", err)
		}
		raw, err := os.ReadFile(sealedPath)
		if err != nil {
			return err
		}
		plain, err := keystore.Unseal(mk, raw)
		if err != nil {
			return fmt.Errorf("unseal: %w", err)
		}
		kv, err := envfile.ParseBytes(plain)
		if err != nil {
			return fmt.Errorf("parse unsealed %s: %w", sealedPath, err)
		}
		if v, ok := kv[key]; ok {
			fmt.Fprint(cmd.OutOrStdout(), v)
			return nil
		}
		return fmt.Errorf("key %q not found in sealed bus for %s", key, cliName)
	}

	kv, err := envfile.ReadFile(plainPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", plainPath, err)
	}
	if v, ok := kv[key]; ok {
		fmt.Fprint(cmd.OutOrStdout(), v)
		return nil
	}
	return fmt.Errorf("key %q not found for %s", key, cliName)
}

func runSecretSet(cmd *cobra.Command, args []string) error {
	cliName, key := args[0], args[1]
	if err := checkNewCLIName(cliName); err != nil {
		return err
	}
	if !envfile.ValidKey(key) {
		return fmt.Errorf("invalid key name %q", key)
	}

	var value string
	if isTerminal(os.Stdin) {
		fmt.Fprintf(cmd.ErrOrStderr(), "value for %s/%s: ", cliName, key)
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		value = strings.TrimRight(line, "\r\n")
	} else {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		value = strings.TrimRight(string(data), "\r\n")
	}
	// The value is never echoed, only the reason it cannot be stored.
	if _, err := envfile.RenderLine(key, value); err != nil {
		return fmt.Errorf("refusing to set %s/%s: %w", cliName, key, err)
	}

	envPath := filepath.Join(secretsRoot(), cliName, "secrets.env")
	existing, err := readEnvForRewrite(envPath, busRecovery(cliName))
	if err != nil {
		return err
	}
	existing[key] = value
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		return err
	}
	if err := writeEnvAtomic(envPath, existing); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "set %s/%s\n", cliName, key)
	return nil
}

func runSecretRm(cmd *cobra.Command, args []string) error {
	cliName := args[0]
	if !validBusName(cliName) {
		return fmt.Errorf("invalid cli-name %q", cliName)
	}
	cliDir := filepath.Join(secretsRoot(), cliName)

	if len(args) == 1 {
		// Whole-CLI removal.
		if err := os.RemoveAll(cliDir); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed %s/\n", cliName)
		return nil
	}
	key := args[1]
	envPath := filepath.Join(cliDir, "secrets.env")
	existing, err := readEnvForRewrite(envPath, busRecovery(cliName))
	if err != nil {
		return err
	}
	if _, ok := existing[key]; !ok {
		return fmt.Errorf("key %q not found for %s", key, cliName)
	}
	delete(existing, key)
	if len(existing) == 0 {
		// Remove the file entirely rather than leave an empty stub.
		_ = os.Remove(envPath)
	} else if err := writeEnvAtomic(envPath, existing); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed %s/%s\n", cliName, key)
	return nil
}

func runSecretImportFrom(cmd *cobra.Command, args []string) error {
	if secretImportAs == "" {
		return fmt.Errorf("--as <cli-name> is required")
	}
	if err := checkNewCLIName(secretImportAs); err != nil {
		return fmt.Errorf("--as: %w", err)
	}
	src := args[0]
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}

	// Detect format by extension and parse into a flat map. JSON is the
	// most common shape in the U1 audit (tesla auth.json, superhuman
	// tokens.json). TOML appears in many configs but the existing fields
	// are usually under [auth] or top-level. Env files are already in
	// shape.
	flat, mappingNotes, err := importParse(src, data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", src, err)
	}
	if len(flat) == 0 {
		return fmt.Errorf("no recognizable keys in %s", src)
	}
	// Skip values the bus cannot carry and keep the rest. The note names
	// the stored key, which is always a valid name, never the value.
	for _, k := range slices.Sorted(maps.Keys(flat)) {
		if _, err := envfile.RenderLine(k, flat[k]); err != nil {
			delete(flat, k)
			mappingNotes = append(mappingNotes, fmt.Sprintf("skipped %s: %v", k, err))
		}
	}
	if len(flat) == 0 {
		for _, note := range mappingNotes {
			fmt.Fprintf(cmd.ErrOrStderr(), "  note: %s\n", note)
		}
		return fmt.Errorf("no importable keys in %s", src)
	}

	envPath := filepath.Join(secretsRoot(), secretImportAs, "secrets.env")
	existing, err := readEnvForRewrite(envPath, busRecovery(secretImportAs))
	if err != nil {
		return err
	}
	maps.Copy(existing, flat)
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		return err
	}
	if err := writeEnvAtomic(envPath, existing); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "imported %d keys from %s into %s/secrets.env\n", len(flat), src, secretImportAs)
	for _, note := range mappingNotes {
		fmt.Fprintf(cmd.ErrOrStderr(), "  note: %s\n", note)
	}
	return nil
}

// importParse handles JSON, TOML, and env-shaped input via heuristic field
// mapping. Unknown fields become _unknown_<original_name> per the spec's
// reserved-key rule so the friend can review and rename.
func importParse(path string, data []byte) (map[string]string, []string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	var notes []string

	// Common field-name heuristics observed in the U1 audit. Lowercase
	// match; we render in canonical UPPER_SNAKE_CASE on the way out.
	canonical := map[string]string{
		"access_token":  "OAUTH_BEARER",
		"accesstoken":   "OAUTH_BEARER",
		"refresh_token": "OAUTH_REFRESH",
		"refreshtoken":  "OAUTH_REFRESH",
		"api_key":       "API_KEY",
		"apikey":        "API_KEY",
		"client_id":     "OAUTH_CLIENT_ID",
		"clientid":      "OAUTH_CLIENT_ID",
		"client_secret": "OAUTH_CLIENT_SECRET",
		"clientsecret":  "OAUTH_CLIENT_SECRET",
		"token":         "TOKEN",
		"bearer":        "OAUTH_BEARER",
		"auth_header":   "AUTH_HEADER",
		"token_expiry":  "OAUTH_EXPIRES_AT",
		"expires_at":    "OAUTH_EXPIRES_AT",
		"base_url":      "BASE_URL",
	}

	flat := map[string]string{}
	mapKey := func(orig string) (string, bool) {
		lower := strings.ToLower(orig)
		if canon, ok := canonical[lower]; ok {
			return canon, true
		}
		// Underscore-and-uppercase fallback for unknown but env-shaped keys.
		if envfile.ValidKey(orig) {
			return strings.ToUpper(orig), false
		}
		// Reserved prefix for fields we can't validate as env names.
		safe := "_unknown_" + sanitizeForReserved(orig)
		return safe, false
	}

	switch ext {
	case ".env", "":
		// Treat as env-shaped already. Keys are parsed leniently so a
		// foreign .env still imports; one that is not a valid name is
		// stored under its line number, since its text can be a secret
		// pasted on its own line.
		entries, err := envfile.Parse(bytes.NewReader(data), envfile.Options{LenientKeys: true})
		if err != nil {
			return nil, nil, err
		}
		var renamedLines []string
		for _, e := range entries {
			if !envfile.ValidKey(e.Key) {
				flat[fmt.Sprintf("_unknown_line%d", e.Line)] = e.Value
				renamedLines = append(renamedLines, strconv.Itoa(e.Line))
				continue
			}
			canon, mapped := mapKey(e.Key)
			flat[canon] = e.Value
			if !mapped && canon != e.Key {
				notes = append(notes, fmt.Sprintf("renamed %q -> %q (review)", e.Key, canon))
			}
		}
		if len(renamedLines) > 0 {
			notes = append(notes, fmt.Sprintf("stored %d invalid key name(s) as _unknown_line<N> (line %s; review)", len(renamedLines), strings.Join(renamedLines, ", ")))
		}
	case ".json":
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, nil, fmt.Errorf("json: %w", err)
		}
		flattenJSON("", raw, flat, &notes, mapKey)
	case ".toml":
		// Best-effort: many config.toml files in the audit have flat
		// top-level keys OR a single [auth] table. We parse via the
		// existing TOML dep and walk it.
		var raw map[string]any
		// Re-use the same TOML lib via secretsbus indirectly.
		if err := decodeTOML(data, &raw); err != nil {
			return nil, nil, fmt.Errorf("toml: %w", err)
		}
		flattenJSON("", raw, flat, &notes, mapKey)
	default:
		return nil, nil, fmt.Errorf("unsupported extension %q (supported: .env .json .toml)", ext)
	}
	return flat, notes, nil
}

func flattenJSON(prefix string, raw map[string]any, out map[string]string, notes *[]string, mapKey func(string) (string, bool)) {
	for k, v := range raw {
		fullKey := k
		if prefix != "" {
			fullKey = prefix + "_" + k
		}
		switch val := v.(type) {
		case string:
			canon, mapped := mapKey(fullKey)
			out[canon] = val
			if !mapped && canon != fullKey && strings.HasPrefix(canon, "_unknown_") {
				*notes = append(*notes, fmt.Sprintf("renamed %q -> %q (review)", fullKey, canon))
			}
		case float64:
			canon, _ := mapKey(fullKey)
			out[canon] = fmt.Sprintf("%v", val)
		case bool:
			canon, _ := mapKey(fullKey)
			out[canon] = fmt.Sprintf("%v", val)
		case map[string]any:
			flattenJSON(fullKey, val, out, notes, mapKey)
		default:
			// Drop arrays + other non-string scalars; not env-shaped.
		}
	}
}

func sanitizeForReserved(orig string) string {
	var b strings.Builder
	for _, r := range orig {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		if isLetter || isDigit {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "X"
	}
	return b.String()
}

// validBusName mirrors secretsbus.validCLIName (kept local to avoid an
// unnecessary import + dependency cycle).
func validBusName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

// checkNewCLIName applies the full spec rule to a name a command is about
// to create: validBusName plus no "--", which the Python reader refuses.
// Reads and removal stay on validBusName, so an existing "--" name keeps
// working and can be removed.
func checkNewCLIName(name string) error {
	if !validBusName(name) || strings.Contains(name, "--") {
		return fmt.Errorf("invalid cli-name %q: use 1-64 lowercase letters, digits and single hyphens, not starting or ending with a hyphen", name)
	}
	return nil
}

// readEnvKeysOnly returns the KEY names from a secrets.env file (no values).
// Used by `secret list` to print without leaking value content.
func readEnvKeysOnly(path string) ([]string, error) {
	all, err := envfile.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for k := range all {
		out = append(out, k)
	}
	return out, nil
}

// readEnvForRewrite reads a file that a command is about to rewrite. A
// missing file starts empty. A file the rewrite could not reproduce is
// refused rather than silently changed: one that does not parse, uses a
// backslash continuation, or holds a value the writer would refuse. The
// refusal ends with recovery, the way out for this particular file.
func readEnvForRewrite(path, recovery string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	entries, err := envfile.Parse(f, envfile.Options{})
	if err != nil {
		return nil, refuseRewrite(path, recovery, err.Error())
	}
	for _, e := range entries {
		if e.Continued {
			return nil, refuseRewrite(path, recovery, fmt.Sprintf("line %d: uses a backslash continuation", e.Line))
		}
		if _, err := envfile.RenderLine(e.Key, e.Value); err != nil {
			return nil, refuseRewrite(path, recovery, fmt.Sprintf("line %d: %v", e.Line, err))
		}
	}
	return envfile.ToMap(entries), nil
}

func refuseRewrite(path, recovery, detail string) error {
	return fmt.Errorf("%s %s; not rewriting it, since its contents would not survive. Edit that line, %s", path, detail, recovery)
}

// busRecovery is the refusal's way out for a CLI's secrets.env.
func busRecovery(cliName string) string {
	return fmt.Sprintf("or run `agentcookie secret rm %s` and set the keys again", cliName)
}

// writeEnvAtomic mirrors secretsbus.atomicWrite via a tiny helper. The
// secret subcommand needs a write path; we keep it next to the read path
// for symmetry.
func writeEnvAtomic(path string, kv map[string]string) error {
	body, err := renderEnvForCmd(kv)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// renderEnvForCmd renders kv with keys sorted, quoting each value that
// would not read back unchanged bare. Callers check values before they get
// here, so a refusal names only the key.
func renderEnvForCmd(kv map[string]string) ([]byte, error) {
	lines, refused := envfile.RenderSorted(kv)
	if len(refused) > 0 {
		return nil, fmt.Errorf("%s: value cannot be stored in secrets.env", refused[0])
	}
	var b strings.Builder
	b.WriteString("# Written by agentcookie secret. See docs/spec-agentcookie-secrets-bus-v1.md.\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	return []byte(b.String()), nil
}

// decodeTOML wraps the existing BurntSushi/toml dep for the import-from
// command's TOML branch.
func decodeTOML(data []byte, out *map[string]any) error {
	return toml.Unmarshal(data, out)
}

// runSecretEnv prints all keys for a CLI in `KEY=VALUE` form, one per line,
// suitable for `eval "$(agentcookie secret env <cli>)"`. Sealed twin wins over
// plaintext when both exist.
func runSecretEnv(cmd *cobra.Command, args []string) error {
	cliName := args[0]
	if !validBusName(cliName) {
		return fmt.Errorf("invalid cli-name %q", cliName)
	}
	sealedPath := filepath.Join(secretsRoot(), cliName, "secrets.env.sealed")
	plainPath := filepath.Join(secretsRoot(), cliName, "secrets.env")

	var kv map[string]string
	if _, err := os.Stat(sealedPath); err == nil {
		mk, err := keystore.ReadMasterKey()
		if err != nil {
			return fmt.Errorf("read master key for sealed file: %w", err)
		}
		raw, err := os.ReadFile(sealedPath)
		if err != nil {
			return err
		}
		plain, err := keystore.Unseal(mk, raw)
		if err != nil {
			return fmt.Errorf("unseal: %w", err)
		}
		kv, err = envfile.ParseBytes(plain)
		if err != nil {
			return fmt.Errorf("parse unsealed %s: %w", sealedPath, err)
		}
	} else {
		var err error
		kv, err = envfile.ReadFile(plainPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("read %s: %w", plainPath, err)
		}
	}
	// Apply aliases so the consumer's declared env var name (e.g.
	// TESLA_AUTH_TOKEN) is emitted carrying the live value of the synced key
	// it maps to (e.g. OAUTH_BEARER). Resolved on every call so it tracks
	// token refreshes rather than going stale. --no-aliases skips this so
	// the output is exactly the stored dataset; the Python reader unseals
	// through it and must return the same keys as the Go reader.
	if !secretEnvNoAliases {
		aliases := effectiveAliases(cliName)
		for declared, stored := range aliases {
			if v, ok := kv[stored]; ok {
				kv[declared] = v
			}
		}
	}

	// --no-aliases is the reader libraries' format: each value verbatim
	// after the first '=', which is lossless because no stored value holds
	// a newline. Released Python readers parse it that way, so it must not
	// change.
	if secretEnvNoAliases {
		for _, k := range slices.Sorted(maps.Keys(kv)) {
			fmt.Fprintf(cmd.OutOrStdout(), "%s=%s\n", k, kv[k])
		}
		return nil
	}

	// Otherwise the output is for eval: each value is quoted so the shell
	// assigns it as text. A value no quoting can carry is left out, and the
	// command fails after printing the rest so the omission is not silent.
	var leftOut []string
	for _, k := range slices.Sorted(maps.Keys(kv)) {
		v, err := envfile.RenderShellValue(kv[k])
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "agentcookie secret env: left out %s: %v\n", k, err)
			leftOut = append(leftOut, k)
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s=%s\n", k, v)
	}
	if len(leftOut) > 0 {
		return fmt.Errorf("left out %d key(s) that cannot be printed safely for eval: %s; read them with `agentcookie secret get`", len(leftOut), strings.Join(leftOut, ", "))
	}
	return nil
}

// aliasesPath is the per-CLI alias file mapping a consumer's declared env var
// name to the synced secret key that holds its value.
func aliasesPath(cliName string) string {
	return filepath.Join(secretsRoot(), cliName, "aliases.env")
}

// readAliases loads declared-env-var -> stored-key mappings for a CLI. A
// missing file is not an error (most CLIs need no alias).
func readAliases(cliName string) (map[string]string, error) {
	m, err := envfile.ReadFile(aliasesPath(cliName))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	return m, nil
}

// manifestAliases returns the [aliases] mapping declared in the CLI's
// agentcookie.toml, discovered from the v2 registry. These ship with the CLI
// so any user gets the mapping automatically, with no `secret alias` command.
// A discovery failure or absent manifest yields an empty map (not an error):
// alias resolution must never break `secret env`.
func manifestAliases(cliName string) map[string]string {
	out := map[string]string{}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	reg, _ := secretsbus.Discover(secretsbus.DiscoveryConfig{HomeDir: home})
	if reg == nil {
		return out
	}
	rp, ok := reg.Projects[cliName]
	if !ok || rp.Manifest == nil {
		return out
	}
	maps.Copy(out, rp.Manifest.Aliases)
	return out
}

// effectiveAliases merges the automatic, ship-with-the-CLI manifest aliases
// (base) with explicit local `secret alias` entries from aliases.env (override).
// A local alias for the same declared var wins over the manifest, so a user can
// always override the default mapping. This is what makes a CLI like
// tesla-pp-cli auto-connect for any user without a manual alias command.
func effectiveAliases(cliName string) map[string]string {
	merged := manifestAliases(cliName)
	local, _ := readAliases(cliName)
	maps.Copy(merged, local)
	return merged
}

var secretAliasCmd = &cobra.Command{
	Use:   "alias <cli-name> [<declared-env-var> <stored-key>]",
	Short: "Map a consumer's declared env var to a synced secret key (resolved live by `secret env`)",
	Long: `alias bridges a CLI's expected auth env var name to the key the secrets
bus actually stores, so 'agentcookie secret env' emits the name the CLI reads.

  agentcookie secret alias tesla-pp-cli TESLA_AUTH_TOKEN OAUTH_BEARER
  agentcookie secret alias tesla-pp-cli        # list current aliases

The alias is resolved against the live synced value on every 'secret env',
so it tracks token refreshes instead of going stale. Use it when a CLI reads
a different env var name than the one the secret was imported under.`,
	Args: cobra.RangeArgs(1, 3),
	RunE: runSecretAlias,
}

func runSecretAlias(cmd *cobra.Command, args []string) error {
	cliName := args[0]
	if !validBusName(cliName) {
		return fmt.Errorf("invalid cli-name %q", cliName)
	}
	if len(args) == 1 {
		aliases, err := readAliases(cliName)
		if err != nil {
			return err
		}
		declaredKeys := make([]string, 0, len(aliases))
		for d := range aliases {
			declaredKeys = append(declaredKeys, d)
		}
		sort.Strings(declaredKeys)
		for _, d := range declaredKeys {
			fmt.Fprintf(cmd.OutOrStdout(), "%s <- %s\n", d, aliases[d])
		}
		return nil
	}
	if len(args) != 3 {
		return fmt.Errorf("to set an alias pass <cli-name> <declared-env-var> <stored-key>; to list pass just <cli-name>")
	}
	if err := checkNewCLIName(cliName); err != nil {
		return err
	}
	declared, stored := args[1], args[2]
	if !envfile.ValidKey(declared) || !envfile.ValidKey(stored) {
		return fmt.Errorf("env var names must be valid identifiers (A-Z, 0-9, underscore)")
	}
	aliases, err := readEnvForRewrite(aliasesPath(cliName), "or delete that file and set the aliases again")
	if err != nil {
		return err
	}
	aliases[declared] = stored
	if err := os.MkdirAll(filepath.Dir(aliasesPath(cliName)), 0o700); err != nil {
		return fmt.Errorf("mkdir secrets dir: %w", err)
	}
	if err := writeEnvAtomic(aliasesPath(cliName), aliases); err != nil {
		return fmt.Errorf("write aliases: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "aliased %s <- %s for %s\n", declared, stored, cliName)
	return nil
}

// isTerminal reports whether stdin is a TTY. Crude check; used to decide
// between interactive prompt and pipe input in `secret set`.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
