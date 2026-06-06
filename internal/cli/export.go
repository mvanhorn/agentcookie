package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
)

var (
	exportDomains  []string
	exportSkipDBSC bool
	exportBrowser  string
)

// exportEpochOffsetSec converts Chrome's microseconds-since-1601 cookie expiry
// to the Unix seconds a Chromium consumer (orca's cookie importer) expects.
const exportEpochOffsetSec = 11644473600

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Emit this machine's current plaintext cookie set as JSON for a consumer to import",
	Long: `export runs the live read pipeline -- read this Mac's Chrome cookies,
decrypt them, apply the blocklist, and drop device-bound (DBSC) cookies --
and prints the surviving set to stdout as a JSON array in the shape a Chromium
consumer accepts (e.g. ` + "`orca cookie import`" + `):

  agentcookie export | orca cookie import

It is a live read (the same pipeline source and agent-sync use), so it does
not depend on the sink or the sidecar and works on a purely-local machine.
Each object carries name, value, domain, path, secure, httpOnly, sameSite, and
(for persistent cookies) expirationDate in Unix seconds.

  agentcookie export                         emit the full set as JSON
  agentcookie export --domain %github.com    limit to matching hosts

stdout is a clean JSON document; the count of skipped device-bound cookies is
reported on stderr so it never corrupts the JSON. Device-bound (DBSC) sites --
Google/Workspace account cookies -- cannot transfer to another browser and are
excluded, not faked.`,
	RunE: runExport,
}

func init() {
	exportCmd.Flags().StringSliceVar(&exportDomains, "domain", nil, "limit to these host_key LIKE patterns (repeatable), e.g. --domain %github.com")
	exportCmd.Flags().BoolVar(&exportSkipDBSC, "skip-dbsc-suspect", false, "drop cookies that look device-bound (DBSC); also honored via AGENTCOOKIE_SKIP_DBSC_SUSPECT=1")
	exportCmd.Flags().StringVar(&exportBrowser, "browser", "", "source browser name (default: source.yaml browser, then Chrome)")
}

func runExport(cmd *cobra.Command, args []string) error {
	// LoadSourceLocal, not LoadSource: export has no push target, so it must
	// not require sink.url or a peer/secret. A missing source.yaml is fine
	// (defaults: default Chrome path, no blocklist).
	cfg, err := config.LoadSourceLocal(common.ConfigDir)
	if err != nil {
		return err
	}
	// Load the blocklist once, up front: fails fast on a broken file before any
	// keychain prompt, and is reused for the read below.
	blocklist, err := loadFreshBlocklist()
	if err != nil {
		return err
	}

	browserName := exportBrowser
	if browserName == "" {
		browserName = cfg.Browser.Name
	}
	sourceBrowser, err := chrome.LookupBrowser(browserName)
	if err != nil {
		return err
	}
	password, err := chrome.SafeStoragePasswordFor(sourceBrowser)
	if err != nil {
		return err
	}
	key, err := chrome.DeriveAESKey(password)
	if err != nil {
		return err
	}

	skipDBSC := exportSkipDBSC || os.Getenv("AGENTCOOKIE_SKIP_DBSC_SUSPECT") == "1"

	cookies, st, err := readFilteredCookies(cfg.Chrome.DBPath, blocklist, key, skipDBSC, time.Now().UTC())
	if err != nil {
		return err
	}
	cookies = sinkpush.FilterByHostPatterns(cookies, exportDomains)

	enc := json.NewEncoder(cmd.OutOrStdout())
	if err := enc.Encode(toExportCookies(cookies)); err != nil {
		return fmt.Errorf("export: encode cookies: %w", err)
	}

	// Report the DBSC-skipped count on stderr (never stdout) so a device-bound
	// cookie that was dropped is explainable rather than a mysterious
	// logged-out site, without corrupting the JSON document on stdout.
	if st.dbsc.skipped > 0 {
		fmt.Fprintf(os.Stderr, "agentcookie export: skipped %d device-bound (DBSC) cookies -- those sessions cannot transfer and will read as logged-out in the consumer\n", st.dbsc.skipped)
	}
	return nil
}

// exportCookie is the per-cookie JSON shape a Chromium consumer's importer
// accepts (orca's RawCookieEntry): domain, name, value, path, secure, httpOnly,
// sameSite, and an optional expirationDate in Unix seconds. Field names and
// JSON tags match so the output imports with no mapping step on the consumer.
type exportCookie struct {
	Domain         string `json:"domain"`
	Name           string `json:"name"`
	Value          string `json:"value"`
	Path           string `json:"path"`
	Secure         bool   `json:"secure"`
	HTTPOnly       bool   `json:"httpOnly"`
	SameSite       string `json:"sameSite"`
	ExpirationDate *int64 `json:"expirationDate,omitempty"`
}

// toExportCookies maps decrypted chrome.Cookie rows into the consumer import
// shape. Pure (no I/O) so it is unit-testable without a Chrome DB. Domain and
// value pass through verbatim: the value is already App-Bound-stripped by the
// source read pipeline.
func toExportCookies(cookies []chrome.Cookie) []exportCookie {
	out := make([]exportCookie, 0, len(cookies))
	for _, c := range cookies {
		ec := exportCookie{
			Domain:   c.HostKey,
			Name:     c.Name,
			Value:    c.Value,
			Path:     c.Path,
			Secure:   c.IsSecure == 1,
			HTTPOnly: c.IsHTTPOnly == 1,
			SameSite: exportSameSite(c.SameSite),
		}
		// Persistent cookies carry an expiry; session cookies (HasExpires == 0)
		// omit it so the consumer treats them as session cookies rather than
		// expiring them against a bogus 1601-epoch value.
		if c.HasExpires != 0 && c.ExpiresUTC != 0 {
			exp := c.ExpiresUTC/1_000_000 - exportEpochOffsetSec
			ec.ExpirationDate = &exp
		}
		out = append(out, ec)
	}
	return out
}

// exportSameSite maps Chrome's numeric SameSite (-1 unspecified, 0 None, 1 Lax,
// 2 Strict) to the string a Chromium importer normalizes.
func exportSameSite(s int) string {
	switch s {
	case 0:
		return "no_restriction"
	case 1:
		return "lax"
	case 2:
		return "strict"
	default:
		return "unspecified"
	}
}
