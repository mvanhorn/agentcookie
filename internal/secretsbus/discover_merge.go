package secretsbus

import (
	"fmt"
	"maps"
	"os"
)

// LoadPayloadWithDiscovery is the v0.14 push-time entry point. It runs v1
// LoadPayload (~/.agentcookie/secrets/<cli>/secrets.env) AND v2 Discover
// (well-known manifest paths + PP CLI auto-detect), reads each v2 project's
// [secrets.file] in place, applies the manifest's sync policy, and merges
// the results into a single Payload.
//
// Precedence per spec section 10.3: v1 bus wins per-key over v2 read-in-place.
// A key present in both sources keeps the v1 value; v2-only keys are included.
//
// Non-fatal errors (oversized files, missing read-in-place targets,
// individual manifest parse failures) accumulate in the returned slice and
// do not stop the push.
func LoadPayloadWithDiscovery(homeDir string) (*Payload, []error) {
	var errs []error

	// v1 bus.
	v1Payload, v1Errs := LoadPayload(homeDir)
	errs = append(errs, v1Errs...)
	if v1Payload == nil {
		v1Payload = &Payload{CLIs: map[string]map[string]string{}}
	}

	// v2 discovery.
	reg, discoverErrs := Discover(DiscoveryConfig{HomeDir: homeDir})
	errs = append(errs, discoverErrs...)

	for slug, rp := range reg.Projects {
		if rp.Kind == SourceKindLegacyV1 {
			continue // already loaded via LoadPayload above
		}
		if rp.Manifest == nil {
			continue
		}
		filtered := map[string]string{}
		// Read-in-place env source, when the manifest declares one. A
		// [[files]]-only manifest (no [secrets.file]) has an empty
		// ReadInPlacePath and ships its secrets entirely via carried files
		// below, so the env read is skipped rather than treated as missing.
		if rp.ReadInPlacePath != "" {
			kv, err := parseEnvFile(rp.ReadInPlacePath)
			if err != nil {
				if os.IsNotExist(err) {
					errs = append(errs, fmt.Errorf("discovered project %q: read-in-place file missing: %s", slug, rp.ReadInPlacePath))
				} else {
					errs = append(errs, fmt.Errorf("discovered project %q: read %s: %w", slug, rp.ReadInPlacePath, err))
				}
				continue
			}
			// Apply manifest sync policy: drop keys the manifest says no to.
			for k, v := range kv {
				if rp.Manifest.ShouldShipKey(k) {
					filtered[k] = v
				}
			}
		}

		// Carry any declared [[files]] items: read each enabled file, base64
		// it into its declared key, and add the companion target so the sink
		// materializes it. Optional items are carried only when opted in.
		if len(rp.Manifest.Files) > 0 {
			enabled := LoadEnabledFileKeys(homeDir, slug)
			carried, carryErrs := CarryFiles(rp.Manifest.Files, enabled, homeDir)
			for _, e := range carryErrs {
				errs = append(errs, fmt.Errorf("discovered project %q: %w", slug, e))
			}
			maps.Copy(filtered, carried)
		}

		if len(filtered) == 0 {
			continue
		}

		// Merge into v1Payload. v1 wins per-key (section 10.3).
		existing, hasV1 := v1Payload.CLIs[slug]
		if hasV1 {
			for k, v := range filtered {
				if _, alreadyInV1 := existing[k]; !alreadyInV1 {
					existing[k] = v
				}
			}
			v1Payload.CLIs[slug] = existing
		} else {
			v1Payload.CLIs[slug] = filtered
		}
	}

	return v1Payload, errs
}
