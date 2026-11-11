package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
	"github.com/mvanhorn/agentcookie/internal/state"
)

// The freshness window distinguishes ongoing delivery from a source that has
// stopped. A stale source is diagnosed separately, not as destination failure.
const destinationStallWindow = 10 * time.Minute

func checkDoctorDestinations(cfg *config.SinkConfig, st *state.SinkState, d doctorDeps) []Check {
	refs, err := cfg.ResolveDestinations()
	if err != nil {
		return []Check{{Name: "Destinations", Severity: SeverityFail, Detail: "invalid destination configuration"}}
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	var out []Check
	for _, ref := range refs {
		if ref.Kind == "ppcli" {
			continue
		}
		var statuses []destination.Status
		if st != nil {
			for _, s := range st.Destinations {
				if s.Name == ref.Name {
					statuses = append(statuses, s)
				}
			}
		}
		check, ready := doctorDestinationStatus(ref, statuses, now)
		out = append(out, check)
		if len(ref.ProbeHosts) == 0 {
			continue
		} // Empty VerifySpecsForHosts otherwise means all hosts.
		probe := Check{Name: "Destination probe " + ref.Name, Severity: SeverityWarn}
		specs := sinkpush.VerifySpecsForHosts(ref.ProbeHosts)
		switch {
		case ref.Kind != "live_chrome":
			probe.Detail = "probes require an owned live_chrome destination"
		case !ready:
			probe.Detail = "not probed: destination is not running and ready"
		case len(specs) == 0:
			probe.Detail = "no supported VerifySpec matches probe_hosts"
		case d.DestinationProbe == nil:
			probe.Detail = "probe unavailable"
		default:
			results := d.DestinationProbe(ref, specs)
			for i, spec := range specs {
				row := Check{Name: probe.Name + " " + spec.Host, Severity: SeverityWarn, Detail: "unknown"}
				if i < len(results) && results[i].Host == spec.Host {
					row.Detail = results[i].State.String() // Never log page values or raw errors.
					if results[i].State == sinkpush.AuthYes {
						row.Severity = SeverityOK
					}
					if results[i].State == sinkpush.AuthNo {
						row.Remediation = "Authenticate directly in this destination; delivered state does not guarantee a usable session."
					}
				}
				out = append(out, row)
			}
			continue
		}
		out = append(out, probe)
	}
	return out
}
func doctorDestinationStatus(ref config.DestinationRef, statuses []destination.Status, now time.Time) (Check, bool) {
	c := Check{Name: "Destination " + ref.Name, Severity: SeverityWarn}
	if len(statuses) == 0 {
		c.Detail = "no persisted destination status"
		return c, false
	}
	running, ready := true, true
	var generations []string
	pending, failed, conflicts, excluded := 0, 0, 0, 0
	repair, stalled := false, false
	for _, s := range statuses {
		running = running && s.Running
		ready = ready && s.Ready
		applied, _ := json.Marshal(s.Applied)
		generations = append(generations, fmt.Sprintf("{accepted=%d applied=%s}", s.Accepted, applied))
		for _, n := range s.Pending {
			pending += n
		}
		failed += len(s.Failed)
		conflicts += s.Conflicts
		excluded += s.CapExcluded
		repair = repair || s.RepairNeeded != nil
		stalled = stalled || destinationStalled(ref.Required, s, now)
	}
	c.Detail = fmt.Sprintf("running=%t ready=%t generations=[%s] pending=%d failed=%d conflicts=%d cap_excluded=%d repair=%t", running, ready, strings.Join(generations, ", "), pending, failed, conflicts, excluded, repair)
	if running && ready && failed == 0 && !repair {
		c.Severity = SeverityOK
	}
	if stalled {
		c.Severity = SeverityFail
		c.Remediation = "Required destination has not applied for over 10 minutes while delivery continues. Inspect pending and failed surfaces; repair only after reviewing ownership loss."
	}
	if repair {
		c.Severity = SeverityFail
		c.Remediation = "Review destination ownership loss, then explicitly run destination repair NAME --yes; old revoked records may remain."
	}
	return c, running && ready && !repair
}
func destinationStalled(required bool, s destination.Status, now time.Time) bool {
	return required && !s.Ready && !s.PendingSince.IsZero() && now.Sub(s.PendingSince) > destinationStallWindow && !s.LastAcceptedAt.IsZero() && !s.LastAcceptedAt.After(now) && now.Sub(s.LastAcceptedAt) <= destinationStallWindow
}
func probeDoctorDestination(ref config.DestinationRef, specs []sinkpush.VerifySpec) []sinkpush.VerifyResult {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if ref.Kind != "live_chrome" {
		return nil
	}
	websocket, err := livecdp.VerifiedOwnedChromeWebSocket(ctx, ref.Chrome.UserDataDir, ref.Chrome.Port)
	if err != nil {
		return nil
	}
	applier := destination.NewChromeApplier(fmt.Sprintf("http://127.0.0.1:%d", ref.Chrome.Port))
	defer applier.Close()
	return applier.Probe(ctx, websocket, specs)
}
