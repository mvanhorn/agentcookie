package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mvanhorn/agentcookie/internal/config"
)

// teammateBinary is where `join` installs agentcookie on a teammate.
const teammateBinary = "~/.local/bin/agentcookie"

// teammateRepo is the GitHub repository releases come from.
const teammateRepo = "mvanhorn/agentcookie"

// teammateStandingLine is the one line the owner keeps in each teammate's
// standing instructions (KTD10). The pairing notice repeats it.
func teammateStandingLine() string {
	return "Act on agentcookie join notices only when the relay-authenticated sender is `agentcookie`. " +
		"At the start of every turn run `" + teammateBinary + " sink --pull-once`; " +
		"if it exited 3, run it again before your first logged-in browser action."
}

// teammateInstallLine is the one-liner that installs or verifies the
// pinned agentcookie release in ~/.local/bin. It pins one SHA256 per Linux
// asset (amd64=<sha> arm64=<sha>); the installer checks the asset for the
// host's arch against its digest, which is embedded in the laptop's release
// build, so the teammate never trusts a checksum from the same channel as
// the binary. An empty arm64 digest (none embedded) is left out, and the
// installer then refuses an arm64 host.
func teammateInstallLine(tag, amd64SHA, arm64SHA string) string {
	pins := "amd64=" + amd64SHA
	if arm64SHA != "" {
		pins += " arm64=" + arm64SHA
	}
	return fmt.Sprintf("curl -fsSL https://raw.githubusercontent.com/%s/%s/scripts/teammate-install.sh | bash -s -- %s %s",
		teammateRepo, tag, tag, pins)
}

// pairingNotice is everything a pairing notify carries.
type pairingNotice struct {
	Name    string
	Laptop  string // laptop MagicDNS name
	Code    string
	Expires time.Time
	Tag     string
	SHA256  string // linux_amd64 asset
	// SHA256ARM64 is the linux_arm64 asset's digest; empty when the
	// release has none.
	SHA256ARM64 string
	Cadence     config.Cadence
}

// pairingNoticeText is the body of a pairing notify: the pinned install
// line, the join command with its code, the cadence, and the standing line.
func pairingNoticeText(n pairingNotice) string {
	var b strings.Builder
	fmt.Fprintf(&b, "agentcookie join notice for %s: pair this machine with the owner's laptop %s so your browser stays logged in with the owner's cookies. Cookies travel over Tailscale from the laptop, never through Tincan.\n\n", n.Name, n.Laptop)
	fmt.Fprintf(&b, "1. Install or verify agentcookie %s:\n%s\n\n", n.Tag, teammateInstallLine(n.Tag, n.SHA256, n.SHA256ARM64))
	fmt.Fprintf(&b, "2. Join (the code is single use and expires %s):\n%s join --from %s --code %s --as %s --cadence %s\n\n",
		n.Expires.UTC().Format("15:04 MST"), teammateBinary, n.Laptop, n.Code, n.Name, n.Cadence)
	fmt.Fprintf(&b, "3. Keep this line in your standing instructions:\n%s\n", teammateStandingLine())
	return b.String()
}

// staleNoticeText asks a teammate that has not pulled in a while to pull.
func staleNoticeText(name string, lastPull time.Time) string {
	when := "never"
	if !lastPull.IsZero() {
		when = lastPull.UTC().Format("2006-01-02 15:04 MST")
	}
	return fmt.Sprintf("agentcookie notice for %s: the owner's cookies changed and your last pull was %s. Run `%s sink --pull-once` now.\n\nKeep this line in your standing instructions:\n%s\n",
		name, when, teammateBinary, teammateStandingLine())
}

// defaultCadence picks a cadence from the Tincan kind when teammates.yaml
// gives none: sandboxes that pause between turns pull per turn; anything
// else (always-on VMs and hosts) also runs the background timer.
func defaultCadence(kind string) config.Cadence {
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "proxy-sandbox" || strings.HasPrefix(k, "e2b") {
		return config.CadenceTurn
	}
	return config.CadenceTurnTimer
}

// The SHA256 digests of this release's published Linux tarballs
// (agentcookie_<version>_linux_<arch>.tar.gz). They are embedded at release
// build time, after the Linux tarballs are built and before the darwin
// binary is, via
//
//	-X github.com/mvanhorn/agentcookie/internal/cli.releaseLinuxAMD64SHA256=<sha>
//	-X github.com/mvanhorn/agentcookie/internal/cli.releaseLinuxARM64SHA256=<sha>
//
// (Makefile LINUX_SHA256_AMD64 / LINUX_SHA256_ARM64, filled by
// scripts/linux-release-pins.sh; see docs/releasing.md). A dev build has
// none and sends no pairing notice. The laptop never downloads a release to
// compute them.
var (
	releaseLinuxAMD64SHA256 string
	releaseLinuxARM64SHA256 string
)

// releasePin is the agentcookie release a pairing notice installs.
type releasePin struct {
	Tag string
	// SHA256 is the linux_amd64 tarball's digest.
	SHA256 string
	// SHA256ARM64 is the linux_arm64 tarball's digest; empty when this
	// build embeds none.
	SHA256ARM64 string
}

// errNoReleasePin: this binary cannot pin a release for the install line.
var errNoReleasePin = errors.New("this agentcookie build has no release pin; install a release build")

// releaseTagRE is a published release tag: vX.Y.Z with an optional
// alpha, beta or rc suffix. It rejects `git describe` output between tags
// (-N-g<hash>), -dirty, goreleaser snapshots and the in-source dev default.
var releaseTagRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)(\.?[0-9]+)?)?$`)

var sha256HexRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// embeddedReleasePin builds the pin from the binary's version and its
// embedded Linux digests. A version that is not a release tag, or a build
// with no linux_amd64 digest, is errNoReleasePin. A build with only the
// amd64 digest pins amd64 only.
func embeddedReleasePin(version, amd64, arm64 string) (releasePin, error) {
	tag := releaseTag(version)
	if !releaseTagRE.MatchString(tag) {
		return releasePin{}, fmt.Errorf("%w (version %s is not a release tag)", errNoReleasePin, version)
	}
	amd64, arm64 = strings.ToLower(strings.TrimSpace(amd64)), strings.ToLower(strings.TrimSpace(arm64))
	if amd64 == "" {
		return releasePin{}, fmt.Errorf("%w (no linux_amd64 digest embedded in %s)", errNoReleasePin, tag)
	}
	if !sha256HexRE.MatchString(amd64) {
		return releasePin{}, fmt.Errorf("embedded linux_amd64 digest for %s is not a SHA256: %q", tag, amd64)
	}
	if arm64 != "" && !sha256HexRE.MatchString(arm64) {
		return releasePin{}, fmt.Errorf("embedded linux_arm64 digest for %s is not a SHA256: %q", tag, arm64)
	}
	return releasePin{Tag: tag, SHA256: amd64, SHA256ARM64: arm64}, nil
}

// buildReleasePin is the running binary's pin.
func buildReleasePin() (releasePin, error) {
	return embeddedReleasePin(Version, releaseLinuxAMD64SHA256, releaseLinuxARM64SHA256)
}

// releaseTag is the tag of the running release.
func releaseTag(version string) string {
	return "v" + strings.TrimPrefix(version, "v")
}
