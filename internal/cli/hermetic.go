package cli

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
	"github.com/mvanhorn/agentcookie/internal/state"
)

// HermeticPipelineForTesting assembles the real source push and sink HTTP
// handler with explicit fixture credentials, no adapters/discovery, and no
// master key. The caller supplies a temporary HOME and configDir and must call
// restore only after all requests complete. Global CLI seams prohibit parallel
// harness instances within the same test process.
func HermeticPipelineForTesting(configDir, secret string, password func(chrome.Browser) (string, error), apply func(context.Context, []chrome.Cookie) error) (http.Handler, func(context.Context, *config.SourceConfig) error, func(), error) {
	if !filepath.IsAbs(configDir) || password == nil || apply == nil {
		return nil, nil, nil, fmt.Errorf("hermetic pipeline requires absolute config directory and injected providers")
	}
	previousCommon := common
	previousSkipSecrets, previousSkipStorage := sourceSkipSecrets, sourceSkipLocalStorage
	previousMaster, previousInject, previousResolve := sinkSidecarMasterKey, liveCDPInject, resolveSinkURL
	previousDeliveries := sinkDeliveries
	common.ConfigDir = configDir
	common.JSON = true
	sourceSkipSecrets = true
	sourceSkipLocalStorage = true
	sinkSidecarMasterKey = func() []byte { return nil }
	liveCDPInject = func(ctx context.Context, _ string, cookies []chrome.Cookie) (int, error) {
		return 1, apply(ctx, cookies)
	}
	resolveSinkURL = func(_ context.Context, url string) (string, error) { return url, nil }
	sinkDeliveries = newSinkDeliveryLog()
	restoreAdapters := sinkpush.SetRegistryForTesting(nil)
	restore := func() {
		common = previousCommon
		sourceSkipSecrets = previousSkipSecrets
		sourceSkipLocalStorage = previousSkipStorage
		sinkSidecarMasterKey = previousMaster
		liveCDPInject = previousInject
		resolveSinkURL = previousResolve
		sinkDeliveries = previousDeliveries
		restoreAdapters()
	}
	sinkConfig := &config.SinkConfig{SkipChromeSQLite: true, LiveCDP: config.LiveCDPRef{Enabled: true, Endpoint: "http://127.0.0.1:1"}}
	sinkState := &state.SinkState{}
	writer := state.NewWriter(filepath.Join(configDir, "sink-state.json"))
	mu := &sync.Mutex{}
	reinject := newLiveCDPReinjector(sinkConfig, writer, sinkState, mu)
	mux := newSinkMux(sinkConfig, secret, nil, protocol.NewSequenceTracker(), writer, sinkState, mu, reinject)
	push := func(ctx context.Context, cfg *config.SourceConfig) error {
		effective, err := config.EffectiveSource(cfg, config.SourceOverrides{})
		if err != nil {
			return err
		}
		var key []byte
		if !effective.CDPSource.Enabled {
			browser, err := chrome.LookupBrowser(effective.Browser.Name)
			if err != nil {
				return err
			}
			pw, err := password(browser)
			if err != nil {
				return err
			}
			key, err = chrome.DeriveAESKey(pw)
			if err != nil {
				return err
			}
		}
		policy, err := loadFreshBlocklist()
		if err != nil {
			return err
		}
		results, _, _, err := pushOnce(ctx, effective, policy, key, false, false, false, 0, &state.SourceState{})
		if err != nil {
			return err
		}
		for _, result := range results {
			if result.Err != nil {
				return result.Err
			}
		}
		return nil
	}
	return mux, push, restore, nil
}
