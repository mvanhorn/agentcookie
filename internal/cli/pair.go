package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/pairing"
)

var (
	pairRole       string
	pairListenAddr string
	pairLocalName  string
	pairPeerURL    string
	pairCode       string
	pairPeerHost   string
)

var pairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Pair source and sink machines with a one-time code over X25519 + HKDF",
	Long: `Run on the source machine first:

  agentcookie pair --as source

That prints a one-time pairing code and the source hostname + URL. Within
ten minutes, run on the sink machine:

  agentcookie pair --as sink --peer <source-hostname> \\
    --pair-url http://<source-hostname>:9998/pair --code <code>

Both sides derive a 32-byte symmetric key from an X25519 exchange salted
with the pairing code (HKDF-SHA256, info "agentcookie-pair-v1"). The
derived key is written to ~/.config/agentcookie/keys/<peer>.json with
mode 0600. macOS Keychain storage is a planned follow-up.

After pairing, 'agentcookie source --once' and 'agentcookie sink' look up
the key by the peer hostname configured in source.yaml / sink.yaml rather
than reading 'security.shared_secret' from those files.`,
	RunE: runPair,
}

func init() {
	pairCmd.Flags().StringVar(&pairRole, "as", "", "role: source | sink (required)")
	pairCmd.Flags().StringVar(&pairListenAddr, "listen", "0.0.0.0:9998", "[source] address to listen on for the sink handshake")
	pairCmd.Flags().StringVar(&pairLocalName, "local-name", "", "hostname identifier announced to the peer (defaults to os.Hostname)")
	pairCmd.Flags().StringVar(&pairPeerURL, "pair-url", "", "[sink] full URL of the source's /pair endpoint")
	pairCmd.Flags().StringVar(&pairCode, "code", "", "[sink] pairing code printed by the source")
	pairCmd.Flags().StringVar(&pairPeerHost, "peer", "", "[sink] source machine's hostname (also used as filename for the derived key)")
}

func runPair(cmd *cobra.Command, args []string) error {
	if pairLocalName == "" {
		pairLocalName = pairing.LocalHostname()
	}
	switch strings.ToLower(pairRole) {
	case "source":
		return runPairAsSource(cmd.Context())
	case "sink":
		return runPairAsSink(cmd.Context())
	default:
		return fmt.Errorf("--as is required and must be 'source' or 'sink'")
	}
}

func runPairAsSource(ctx context.Context) error {
	res, _, err := pairing.RunSource(ctx, pairListenAddr, pairLocalName, os.Stderr)
	if err != nil {
		return err
	}
	pk := &keystore.PeerKey{
		Peer:        res.RemotePeer,
		Key:         res.Key,
		PairedAt:    res.PairedAt,
		Fingerprint: res.Fingerprint,
		ProtocolVer: pairing.ProtocolVersion,
	}
	if err := keystore.Save(common.ConfigDir, pk); err != nil {
		return fmt.Errorf("save key: %w", err)
	}
	fmt.Fprintf(os.Stderr, "\nagentcookie pair: paired with sink %q (fingerprint %s)\n", res.RemotePeer, res.Fingerprint)
	fmt.Fprintf(os.Stderr, "  key saved to %s/keys/%s.json (mode 0600)\n", common.ConfigDir, res.RemotePeer)
	return nil
}

func runPairAsSink(ctx context.Context) error {
	if pairPeerURL == "" {
		return fmt.Errorf("--pair-url is required when --as sink")
	}
	if pairCode == "" {
		return fmt.Errorf("--code is required when --as sink")
	}
	if pairPeerHost == "" {
		return fmt.Errorf("--peer is required when --as sink (the source machine's hostname)")
	}
	res, err := pairing.RunSink(ctx, pairPeerURL, pairing.Code(pairCode), pairLocalName)
	if err != nil {
		return err
	}
	pk := &keystore.PeerKey{
		Peer:        pairPeerHost,
		Key:         res.Key,
		PairedAt:    res.PairedAt,
		Fingerprint: res.Fingerprint,
		ProtocolVer: pairing.ProtocolVersion,
	}
	if err := keystore.Save(common.ConfigDir, pk); err != nil {
		return fmt.Errorf("save key: %w", err)
	}
	fmt.Fprintf(os.Stderr, "agentcookie pair: paired with source %q (fingerprint %s)\n", pairPeerHost, res.Fingerprint)
	fmt.Fprintf(os.Stderr, "  key saved to %s/keys/%s.json (mode 0600)\n", common.ConfigDir, pairPeerHost)
	return nil
}
