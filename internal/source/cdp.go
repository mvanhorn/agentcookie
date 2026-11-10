package source

import (
	"context"
	"fmt"

	"github.com/mvanhorn/agentcookie/internal/browsers"
	"github.com/mvanhorn/agentcookie/internal/cdpsource"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

type cdpReader struct {
	endpoint string
	opts     Options
	deps     Dependencies
}

func NewCDP(endpoint string, opts Options, deps Dependencies) (Reader, error) {
	if err := cdpsource.ValidateEndpoint(endpoint); err != nil {
		return nil, err
	}
	if deps.ReadCDP == nil {
		deps.ReadCDP = cdpsource.Read
	}
	return &cdpReader{endpoint: endpoint, opts: opts, deps: deps}, nil
}
func (r *cdpReader) Label() string           { return "Chromium (CDP)" }
func (r *cdpReader) Family() browsers.Family { return browsers.Chromium }
func (r *cdpReader) Capabilities() Caps {
	return Caps{Live: true, Surfaces: []protocol.Surface{protocol.Cookies}}
}
func (r *cdpReader) WatchPaths() WatchPaths { return WatchPaths{} }
func (r *cdpReader) Close() error           { return nil }
func (r *cdpReader) Diagnose(ctx context.Context) []Check {
	_, err := r.Read(ctx, &config.Blocklist{Version: 1})
	if err != nil {
		return []Check{{Name: "Source adapter", Severity: "fail", Detail: err.Error()}}
	}
	return []Check{{Name: "Source adapter", Severity: "ok", Detail: r.Label()}}
}
func (r *cdpReader) Read(ctx context.Context, policy *config.Blocklist) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cookies, err := r.deps.ReadCDP(ctx, r.endpoint)
	if err != nil {
		return nil, fmt.Errorf("read cookies from cdp source: %w", err)
	}
	return newSnapshot(cookies, policy, r.opts), nil
}
