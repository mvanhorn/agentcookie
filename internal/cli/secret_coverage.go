package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mvanhorn/agentcookie/internal/secretsbus"
)

// secretCoverage reports whether a CLI's synced secret store satisfies the
// auth env vars the CLI declares. It only flags a problem when an explicit
// secret store EXISTS but does not cover the declared keys -- the exact
// Tesla case (store has OAUTH_BEARER, CLI declares TESLA_AUTH_TOKEN). A CLI
// with no explicit store reads its value in place (pp-cli-derived) and is
// not this command's concern, so it returns "in-place".
//
// An alias (see `agentcookie secret alias`) counts as satisfying a declared
// key when the stored key it points at is present.
//
// status is one of: "in-place", "ok", "MISMATCH". detail is human-readable
// remediation for MISMATCH, empty otherwise.
func secretCoverage(cliName string, declared []string) (status, detail string) {
	storePath := filepath.Join(secretsRoot(), cliName, "secrets.env")
	storeKeys, err := readEnvKeysOnly(storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "in-place", ""
		}
		return "in-place", ""
	}
	if len(storeKeys) == 0 {
		return "in-place", ""
	}
	if len(declared) == 0 {
		return "ok", ""
	}
	have := make(map[string]bool, len(storeKeys))
	for _, k := range storeKeys {
		have[k] = true
	}
	aliases, _ := readAliases(cliName)

	var missing []string
	for _, d := range declared {
		if have[d] {
			continue
		}
		if stored, ok := aliases[d]; ok && have[stored] {
			continue
		}
		missing = append(missing, d)
	}
	if len(missing) == 0 {
		return "ok", ""
	}
	sort.Strings(missing)
	return "MISMATCH", "needs " + strings.Join(missing, ",") + "; synced keys are [" +
		strings.Join(sortedCopy(storeKeys), ",") + "] -- add `agentcookie secret alias " + cliName + " " +
		missing[0] + " <one-of-those>`"
}

// declaredKeysOf returns the sorted auth env var names a project declares.
func declaredKeysOf(rp *secretsbus.RegisteredProject) []string {
	if rp == nil || rp.Manifest == nil {
		return nil
	}
	keys := make([]string, 0, len(rp.Manifest.Sync.Keys))
	for k := range rp.Manifest.Sync.Keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
