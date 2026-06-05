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
	if st.dbsc.skipped > 0 {
		fmt.Fprintf(out, "Note: %d device-bound (DBSC) cookies were skipped -- those sessions cannot transfer to a separate profile and may read as logged-out.\n", st.dbsc.skipped)
	}

	// Wire to the live debug port. Discovery is synthetic-reachable: the
	// debug Chrome we just launched is the endpoint.
	d := agentattach.Discovery{Reachable: true, Tier: agentattach.TierLegacy}
	target := agentbrowser.AttachTarget{Port: port}
	return wireAttach(out, d, wirers, target, common.JSON)
}
