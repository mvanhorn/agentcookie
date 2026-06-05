package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/mvanhorn/agentcookie/internal/agentattach"
	"github.com/mvanhorn/agentcookie/internal/agentbrowser"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
)

// fallbackAttach implements `attach --fallback`: seed a dedicated debug
// Chrome profile from this machine's default Chrome (cookies + localStorage),
// launch it on a loopback debug port, and wire the agent browsers to that
// port. Used when the real default profile cannot be CDP-attached (older
// Chrome, or the user prefers an isolated profile).
//
// This is one-shot: it leaves the debug Chrome running for the agent to
// attach to and returns. Continuous re-sync (refresh on every Chrome
// cookie change) is a deliberate follow-up -- doing it safely against a
// live debug Chrome means injecting over CDP rather than the spawn-based
// seed path, to avoid a profile-dir lock conflict.
func fallbackAttach(ctx context.Context, out io.Writer, wirers []agentbrowser.Wirer, port int) error {
	// If a browser is already serving CDP on this port, do NOT spawn a
	// second Chrome on the same profile dir. The file-copy seed path cannot
	// run against a live profile (it would corrupt the LevelDB under a held
	// lock), and a blind launch would either fail to bind the port or
	// false-pass against the existing instance. Wire to what is already
	// there and tell the user how to refresh. This is the guard the absence
	// of which let repeated `attach --fallback` runs double-launch Chrome.
	if d := agentattach.Discover(ctx, port); d.Reachable {
		fmt.Fprintf(out, "A browser is already serving CDP on 127.0.0.1:%d; not launching a second debug Chrome.\n", port)
		fmt.Fprintln(out, "If that is a stale agentcookie debug Chrome and you want fresh cookies, quit it and re-run.")
		target := agentbrowser.AttachTarget{Port: port}
		return wireAttach(out, agentattach.Discovery{Reachable: true, Tier: agentattach.TierLegacy}, wirers, target, common.JSON)
	}

	cfg, err := config.LoadSourceLocal(common.ConfigDir)
	if err != nil {
		return err
	}
	browser, err := chrome.LookupBrowser(cfg.Browser.Name)
	if err != nil {
		return err
	}
	password, err := chrome.SafeStoragePasswordFor(browser)
	if err != nil {
		return err
	}
	key, err := chrome.DeriveAESKey(password)
	if err != nil {
		return err
	}
	blocklist, err := loadFreshBlocklist()
	if err != nil {
		return err
	}

	cookies, st, err := readFilteredCookies(cfg.Chrome.DBPath, blocklist, key, false, time.Now().UTC())
	if err != nil {
		return err
	}

	dp := agentattach.NewDebugProfile(port)
	if err := dp.SeedCookies(ctx, cookies); err != nil {
		return fmt.Errorf("seed cookies into debug profile: %w", err)
	}
	lsFiles, err := dp.CopyLocalStorage(chromepaths.DefaultProfileDir())
	if err != nil {
		return fmt.Errorf("copy localStorage into debug profile: %w", err)
	}

	if _, err := dp.Launch(ctx); err != nil {
		return err
	}

	fmt.Fprintf(out, "Debug Chrome running on 127.0.0.1:%d (profile %s).\n", port, dp.Dir)
	fmt.Fprintf(out, "Seeded %d cookies + %d localStorage files from your default Chrome.\n", len(cookies), lsFiles)
	// DBSC-suspect cookies are read with skipDBSC=false, so they land in
	// the "warned" bucket (seeded, not dropped). They are seeded into the
	// debug profile but the device binding makes them unusable on a separate
	// profile, so surface the warned count, not the (always-zero) skipped one.
	if st.dbsc.warned > 0 {
		fmt.Fprintf(out, "Note: %d device-bound (DBSC) cookies were seeded but may not work on a separate profile (the binding is device/browser-specific), so a few sites may still read as logged-out.\n", st.dbsc.warned)
	}

	// Wire to the live debug port. Discovery is synthetic-reachable: the
	// debug Chrome we just launched is the endpoint.
	d := agentattach.Discovery{Reachable: true, Tier: agentattach.TierLegacy}
	target := agentbrowser.AttachTarget{Port: port}
	return wireAttach(out, d, wirers, target, common.JSON)
}
