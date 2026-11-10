package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mvanhorn/agentcookie/internal/cli/httpserver"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/transport"
	"github.com/spf13/cobra"
)

var destinationConfirm bool
var destinationCmd = &cobra.Command{Use: "destination", Short: "Open or repair a durable live destination"}

func init() {
	open := &cobra.Command{Use: "open <name>", Short: "Restore desired state before opening a Chrome window", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return requestDestinationAction(cmd, args[0], "open", false)
	}}
	repair := &cobra.Command{Use: "repair <name>", Short: "Reapply desired state and acknowledge lost ownership history", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !destinationConfirm {
			return fmt.Errorf("repair cannot identify previously revoked records whose ownership history was lost; review this consequence and pass --yes to reapply desired state")
		}
		return requestDestinationAction(cmd, args[0], "repair", true)
	}}
	repair.Flags().BoolVar(&destinationConfirm, "yes", false, "acknowledge that unidentified previously revoked records cannot be removed")
	destinationCmd.AddCommand(open, repair)
}
func requestDestinationAction(cmd *cobra.Command, name, action string, confirmed bool) error {
	cfg, err := config.LoadSink(common.ConfigDir)
	if err != nil {
		return err
	}
	if cfg.Listen.Addr == "" {
		return fmt.Errorf("destination control requires a running sink listener")
	}
	secret, err := resolveTransportSecret(common.ConfigDir, cfg.Peer.Hostname, cfg.Security.SharedSecret)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(cmd.Context(), "POST", "http://"+cfg.Listen.Addr+"/destination", nil)
	if err != nil {
		return err
	}
	if err = transport.SignRequest(req, secret, time.Now()); err != nil {
		return err
	}
	// Encrypt the fresh signed request nonce with the action so an earlier
	// ciphertext cannot be substituted into a new authenticated request.
	data, err := json.Marshal(destinationAction{Name: name, Action: action, Confirmed: confirmed, Nonce: req.Header.Get(transport.HeaderNonce)})
	if err != nil {
		return err
	}
	sealed, err := transport.SealWithSecret(data, secret)
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(sealed))
	req.ContentLength = int64(len(sealed))
	resp, err := httpserver.Client(httpserver.SyncClient).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("destination %s: %s", action, strings.TrimSpace(string(body)))
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(body))
	return err
}

type destinationAction struct {
	Nonce     string `json:"nonce"`
	Name      string `json:"name"`
	Action    string `json:"action"`
	Confirmed bool   `json:"confirmed"`
}
