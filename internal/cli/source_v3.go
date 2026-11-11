package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/tsclient"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mvanhorn/agentcookie/internal/cli/httpserver"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/pairing"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/storagecodec"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

type capabilityEntry struct {
	caps    sinkCapabilities
	expires time.Time
}

var capabilityCache = struct {
	sync.Mutex
	entries map[string]capabilityEntry
}{entries: map[string]capabilityEntry{}}
var destinationSendLocks sync.Map

func sinkSendLock(rawURL string) *sync.Mutex {
	u, err := url.Parse(rawURL)
	key := rawURL
	if err == nil {
		key = u.Scheme + "://" + u.Host
	}
	v, _ := destinationSendLocks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func sinkEndpoint(ctx context.Context, raw, path string) (string, error) {
	resolved, err := resolveSinkURL(ctx, raw)
	if errors.Is(err, tsclient.ErrAmbiguousPeer) {
		return "", fmt.Errorf("resolve sink URL: %w", err)
	}
	if errors.Is(err, tsclient.ErrPeerOffline) {
		return "", fmt.Errorf("%w: %w", errSinkAsleep, err)
	}
	if err == nil {
		raw = resolved
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Path = path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
func forgetCapabilities(raw string) {
	capabilityCache.Lock()
	delete(capabilityCache.entries, raw)
	capabilityCache.Unlock()
}
func discoverCapabilities(ctx context.Context, raw, secret string) (sinkCapabilities, error) {
	capabilityCache.Lock()
	cached, ok := capabilityCache.entries[raw]
	capabilityCache.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.caps, nil
	}
	endpoint, err := sinkEndpoint(ctx, raw, "/capabilities")
	if err != nil {
		return sinkCapabilities{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return sinkCapabilities{}, err
	}
	if err = transport.SignRequest(req, secret, time.Now()); err != nil {
		return sinkCapabilities{}, err
	}
	resp, err := httpserver.Client(httpserver.SyncClient).Do(req)
	if err != nil {
		return sinkCapabilities{}, err
	}
	defer resp.Body.Close()
	caps := sinkCapabilities{ProtocolVersions: []int{1, 2}}
	if resp.StatusCode == 200 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return caps, readErr
		}
		if !transport.VerifyResponseMAC(secret, req.Header.Get(transport.HeaderNonce), body, resp.Header.Get(transport.HeaderResponseMAC)) {
			return caps, fmt.Errorf("capabilities response authentication failed")
		}
		if err = json.Unmarshal(body, &caps); err != nil {
			return caps, err
		}
	} else if resp.StatusCode != 404 && resp.StatusCode != 405 && resp.StatusCode != 401 && resp.StatusCode != 403 {
		return caps, fmt.Errorf("capabilities returned %d", resp.StatusCode)
	}
	capabilityCache.Lock()
	capabilityCache.entries[raw] = capabilityEntry{caps: caps, expires: time.Now().Add(time.Hour)}
	capabilityCache.Unlock()
	return caps, nil
}
func supportsV3(c sinkCapabilities) bool {
	return slices.Contains(c.ProtocolVersions, 3)
}
func getSinkStatus(ctx context.Context, raw, secret, hostname string) (syncReply, error) {
	endpoint, err := sinkEndpoint(ctx, raw, "/status")
	if err != nil {
		return syncReply{}, err
	}
	endpoint += "?source=" + url.QueryEscape(hostname)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return syncReply{}, err
	}
	if err = transport.SignRequest(req, secret, time.Now()); err != nil {
		return syncReply{}, err
	}
	resp, err := httpserver.Client(httpserver.SyncClient).Do(req)
	if err != nil {
		return syncReply{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		forgetCapabilities(raw)
		return syncReply{}, fmt.Errorf("status returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return syncReply{}, err
	}
	if !transport.VerifyResponseMAC(secret, req.Header.Get(transport.HeaderNonce), body, resp.Header.Get(transport.HeaderResponseMAC)) {
		return syncReply{}, fmt.Errorf("status response authentication failed")
	}
	var out syncReply
	if err = json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	if out.SourceHostname != hostname {
		return syncReply{}, fmt.Errorf("status source authentication failed")
	}
	return out, nil
}

// recoverGeneration discovers prior accepted generations before the first
// cycle after local state loss. All destinations then share the same new G.
func recoverGeneration(ctx context.Context, cfg *config.SourceConfig, counters source.Counters, hostname string, g uint64) (uint64, error) {
	if g != 1 {
		return g, nil
	}
	maximum := uint64(0)
	for _, sink := range cfg.ResolvedSinks() {
		secret, err := resolveSinkSecret(common.ConfigDir, sink, cfg.Security.SharedSecret)
		if err != nil {
			continue
		}
		caps, err := discoverCapabilities(ctx, sink.URL, secret)
		if err != nil || !supportsV3(caps) {
			continue
		}
		status, err := getSinkStatus(ctx, sink.URL, secret, hostname)
		if err != nil {
			return 0, fmt.Errorf("recover source generation: %w", err)
		}
		if status.Generation > maximum {
			maximum = status.Generation
		}
	}
	if maximum >= g {
		if maximum == ^uint64(0) {
			return 0, fmt.Errorf("generation exhausted")
		}
		return counters.ReseedGeneration(maximum + 1)
	}
	return g, nil
}

// sendNegotiated serializes sequence allocation AND posting per sink. Separate
// destinations may arrive in either order, but cannot reuse transport sequence.
func sendNegotiated(ctx context.Context, sink config.SinkTarget, secret string, base protocol.SyncEnvelope, counters source.Counters, verbose bool) (string, int, error) {
	lock := sinkSendLock(sink.URL)
	lock.Lock()
	defer lock.Unlock()
	caps, err := discoverCapabilities(ctx, sink.URL, secret)
	if err != nil {
		return "", 0, err
	}
	env := base
	if supportsV3(caps) {
		env.ProtocolVersion = 3
		env.Destination = sink.Destination
		env.LocalStorageTarball = nil
		env.IndexedDBTarball = nil
		env.IndexedDBSkipped = nil
	} else {
		if sink.Destination != "" || len(sink.Containers) > 0 {
			return "", 2, fmt.Errorf("named destination %q requires a v3 sink; refusing account-unsafe fallback", sink.Destination)
		}
		env.ProtocolVersion = 2
		env.Destination = ""
		env.Snapshot = nil
		env.Storage = nil
		env.SourceBrowser = ""
		env.ReaderMode = ""
	}
	if env.ProtocolVersion == 2 && len(env.Cookies) == 0 && len(env.LocalStorageTarball) == 0 && len(env.IndexedDBTarball) == 0 && len(env.Secrets) == 0 {
		return "", 2, nil
	}
	for attempt := range 2 {
		seq, err := counters.NextSequence()
		if err != nil {
			return "", env.ProtocolVersion, err
		}
		env.Sequence = seq
		payload, err := marshalBoundedEnvelope(env)
		if err != nil {
			return "", env.ProtocolVersion, err
		}
		sealed, err := transport.SealWithSecret(payload, secret)
		if err != nil {
			return "", env.ProtocolVersion, err
		}
		reply, err := postToSink(ctx, sink.URL, sealed, verbose)
		if err == nil {
			return reply, env.ProtocolVersion, nil
		}
		forgetCapabilities(sink.URL)
		if attempt == 0 && strings.Contains(err.Error(), "sink returned 409") {
			// A later source cycle gets a generation above the sink's authenticated
			// accepted mark. Reusing the same semantic generation here is unsafe.
			status, statusErr := getSinkStatus(ctx, sink.URL, secret, env.SourceHostname)
			routeStatus := destinationReply(status, sink.Destination)
			if statusErr == nil && env.Snapshot != nil && routeStatus.Generation >= env.Snapshot.Generation {
				if routeStatus.Generation == ^uint64(0) {
					return "", env.ProtocolVersion, fmt.Errorf("generation exhausted")
				}
				_, _ = counters.ReseedGeneration(routeStatus.Generation + 1)
				return "", env.ProtocolVersion, fmt.Errorf("source state recovered above generation %d; next cycle will resnapshot", routeStatus.Generation)
			}
			minimum := time.Now().Add(time.Second).UnixNano()
			if statusErr == nil && status.Sequence >= minimum {
				if status.Sequence == int64(^uint64(0)>>1) {
					return "", env.ProtocolVersion, fmt.Errorf("sequence exhausted")
				}
				minimum = status.Sequence + 1
			}
			if _, seedErr := counters.ReseedSequence(minimum); seedErr != nil {
				return "", env.ProtocolVersion, seedErr
			}
			continue
		}
		return "", env.ProtocolVersion, err
	}
	return "", env.ProtocolVersion, fmt.Errorf("sink replay rejection after sequence recovery")
}

// pollSinkApplied stops after ten minutes or caller cancellation. Pending is a
// truthful result, not a successful login claim.
func pollSinkApplied(ctx context.Context, raw, secret, hostname string, initial syncReply, names ...string) (syncReply, error) {
	name := ""
	if len(names) > 0 {
		name = names[0]
	}
	initial = destinationReply(initial, name)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	last := initial
	for {
		ready := name == "" || len(last.Destinations) > 0
		for _, d := range last.Destinations {
			if !d.Ready {
				ready = false
			}
		}
		if ready {
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-tick.C:
			next, err := getSinkStatus(ctx, raw, secret, hostname)
			if err != nil {
				return last, err
			}
			last = destinationReply(next, name)
		}
	}
}

var activeStatusPolls sync.Map

// pollPendingSinks persists the eventual outcome rather than treating a 200
// accepted/queued response as applied. A newer envelope's status is never
// overwritten by a poll for an older generation.
func pollPendingSinks(ctx context.Context, cfg *config.SourceConfig, st *state.SourceState, writer *state.Writer) {
	if st == nil {
		return
	}
	var wg sync.WaitGroup
	for _, sink := range cfg.ResolvedSinks() {
		sourceStateMu.Lock()
		rec := st.SinkFor(sink.Peer, sink.URL, sink.Destination)
		initial := syncReply{Status: rec.LastStatus, Destinations: append([]destination.Status(nil), rec.Destinations...)}
		sourceStateMu.Unlock()
		pending := false
		for _, d := range initial.Destinations {
			if !d.Ready {
				pending = true
			}
			if d.Accepted > initial.Generation {
				initial.Generation = d.Accepted
			}
		}
		if !pending {
			continue
		}
		key := sink.Peer + "\x00" + sink.URL + "\x00" + sink.Destination
		if _, active := activeStatusPolls.LoadOrStore(key, true); active {
			continue
		}
		wg.Add(1)
		go func(sink config.SinkTarget, key string, initial syncReply) {
			defer wg.Done()
			defer activeStatusPolls.Delete(key)
			secret, err := resolveSinkSecret(common.ConfigDir, sink, cfg.Security.SharedSecret)
			if err != nil {
				return
			}
			out, pollErr := pollSinkApplied(ctx, sink.URL, secret, pairing.LocalHostname(), initial, sink.Destination)
			sourceStateMu.Lock()
			defer sourceStateMu.Unlock()
			rec := st.SinkFor(sink.Peer, sink.URL, sink.Destination)
			for _, current := range rec.Destinations {
				if current.Accepted > out.Generation {
					return
				}
			}
			rec.Destinations = out.Destinations
			rec.LastStatus = out.Status
			if pollErr != nil {
				rec.LastError = "destination apply remains pending: " + pollErr.Error()
			}
			if writer != nil {
				_ = writer.Save(st)
			}
		}(sink, key, initial)
	}
	wg.Wait()
}

// Leave room for the AEAD nonce and authentication tag inside the 256 MiB HTTP limit.
func marshalBoundedEnvelope(env protocol.SyncEnvelope) ([]byte, error) {
	const limit = (256 << 20) - 28
	if _, err := storagecodec.JSONSize(env, limit); err != nil {
		return nil, fmt.Errorf("sync envelope exceeds transport budget: %w", err)
	}
	payload, err := json.Marshal(env)
	if len(payload) > limit {
		return nil, errors.New("sync envelope exceeds transport budget")
	}
	return payload, err
}

// A failed sibling destination must not block this route's readiness or leak
// its account status into this destination's durable push record.
func destinationReply(reply syncReply, name string) syncReply {
	if name == "" {
		return reply
	}
	result := syncReply{Status: "degraded", Sequence: reply.Sequence}
	for _, d := range reply.Destinations {
		if d.Name == name {
			result.Destinations = []destination.Status{d}
			result.Generation = d.Accepted
			if d.Ready {
				result.Status = "ok"
			}
			return result
		}
	}
	return result
}
