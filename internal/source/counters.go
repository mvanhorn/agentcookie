package source

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"time"

	"github.com/mvanhorn/agentcookie/internal/state"
)

// Counters separates semantic snapshot generation from the transport's
// nanosecond replay counter. All allocation persists before returning, uses
// a per-file process lock, and retains other source-state fields.
type Counters struct {
	Dir string
	Now func() time.Time
}

func (c Counters) NextGeneration() (uint64, error) { return c.generation(0) }

// ReseedGeneration allocates at least minimum after authenticated sink status
// reports a higher accepted generation. It never lowers existing local state.
// Callers must supply the server high-water mark plus one, checking overflow.
func (c Counters) ReseedGeneration(minimum uint64) (uint64, error) { return c.generation(minimum) }
func (c Counters) generation(minimum uint64) (uint64, error) {
	var next uint64
	err := state.UpdateJSON(filepath.Join(c.Dir, "source-generation.json"), func(fields map[string]json.RawMessage) error {
		var last uint64
		if raw := fields["generation"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &last); err != nil {
				return err
			}
		}
		if last == math.MaxUint64 {
			return fmt.Errorf("source generation exhausted")
		}
		next = max(last+1, minimum)
		fields["generation"], _ = json.Marshal(next)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return next, nil
}
func (c Counters) NextSequence() (int64, error) { return c.sequence(0) }

// ReseedSequence is used after a replay rejection following state loss.
// It does not reset generation or silently lower an existing high-water mark.
func (c Counters) ReseedSequence(minimum int64) (int64, error) { return c.sequence(minimum) }
func (c Counters) sequence(minimum int64) (int64, error) {
	var next int64
	err := state.UpdateJSON(filepath.Join(c.Dir, "source-state.json"), func(fields map[string]json.RawMessage) error {
		var last int64
		if raw := fields["last_sequence"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &last); err != nil {
				return err
			}
		}
		if last < 0 || last == math.MaxInt64 {
			return fmt.Errorf("invalid or exhausted source sequence")
		}
		now := time.Now()
		if c.Now != nil {
			now = c.Now()
		}
		next = max(now.UnixNano(), last+1, minimum)
		if next <= 0 {
			return fmt.Errorf("invalid source sequence clock")
		}
		fields["last_sequence"], _ = json.Marshal(next)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return next, nil
}
