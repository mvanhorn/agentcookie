package cli

import (
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/config"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
)

// Each target is a pure projection of one capture. Neither reading a browser
// nor allocating a semantic generation is permitted inside this fan-out step.
func envelopeForDestination(base protocol.SyncEnvelope, snapshot *source.Snapshot, cfg *config.SourceConfig, policy *config.Blocklist, sink config.SinkTarget) (protocol.SyncEnvelope, error) {
	base.Destination = sink.Destination
	if len(sink.Containers) == 0 {
		return base, nil
	}
	if sink.Destination == "" {
		return protocol.SyncEnvelope{}, fmt.Errorf("container route requires a named destination")
	}
	if !isGeckoSource(cfg) {
		return protocol.SyncEnvelope{}, fmt.Errorf("container selection requires a Gecko source")
	}
	routed, err := geckoreader.RerouteDestination(snapshot, policy, sink.Containers)
	if err != nil {
		return protocol.SyncEnvelope{}, fmt.Errorf("destination %s: %w", sink.Destination, err)
	}
	base.Cookies = routed.Cookies
	base.Snapshot = &routed.Meta
	base.Storage = routed.Storage
	base.LocalStorageTarball = nil
	base.IndexedDBTarball = nil
	base.IndexedDBSkipped = nil
	return base, nil
}

// Named-only account routes do not implicitly authorize a separate default
// pull account. --pull-listen explicitly adds that route; mixed/default sink
// configurations retain their existing pull behavior.
func defaultPullRouteEnabled(cfg *config.SourceConfig) bool {
	if !isGeckoSource(cfg) || sourcePullListen != "" {
		return true
	}
	sinks := cfg.ResolvedSinks()
	if len(sinks) == 0 {
		return true
	}
	for _, sink := range sinks {
		if len(sink.Containers) == 0 {
			return true
		}
	}
	return false
}
