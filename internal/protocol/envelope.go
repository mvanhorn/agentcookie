// Package protocol defines the wire format the source uses to send sync
// payloads to the sink. The envelope carries a protocol version, the source's
// announced hostname, a monotonic sequence number for replay defense, and
// the cookie batch itself. Future versions may add diffs and signed
// allowlists; today's envelope is full-set semantics (every paired domain's
// current cookies in one batch).
//
// See docs/protocol.md for the spec.
package protocol

import (
	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// Version is the current wire protocol version. Sinks reject envelopes whose
// version does not match.
const Version = 1

// SyncEnvelope is the JSON shape sent inside the AES-GCM seal.
type SyncEnvelope struct {
	ProtocolVersion int             `json:"protocol_version"`
	SourceHostname  string          `json:"source_hostname"`
	Sequence        int64           `json:"sequence"`
	Cookies         []chrome.Cookie `json:"cookies"`
}
