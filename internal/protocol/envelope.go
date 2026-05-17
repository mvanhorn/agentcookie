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

// Version is the current wire protocol version. v2 added LocalStorage and
// IndexedDB payloads alongside cookies (v0.7). Sinks accept any version
// they understand; sources always emit the highest version they support.
const Version = 2

// MinVersion is the oldest envelope version the sink still accepts. v1
// envelopes (cookies only, pre-v0.7) decode through the same struct with
// LocalStorageTarball and IndexedDBTarball left nil.
const MinVersion = 1

// SyncEnvelope is the JSON shape sent inside the AES-GCM seal.
//
// LocalStorageTarball and IndexedDBTarball are byte slices produced by
// internal/chromedirsync.Pack. The sink calls chromedirsync.Unpack +
// AtomicReplaceDir to install them into Chrome's Default profile.
//
// On v2 envelopes, all three payload fields are independently optional:
// a source can sync cookies only, localStorage only, both, or all three
// in a single envelope. The sink applies whatever is present.
type SyncEnvelope struct {
	ProtocolVersion     int             `json:"protocol_version"`
	SourceHostname      string          `json:"source_hostname"`
	Sequence            int64           `json:"sequence"`
	Cookies             []chrome.Cookie `json:"cookies"`
	LocalStorageTarball []byte          `json:"local_storage_tarball,omitempty"`
	IndexedDBTarball    []byte          `json:"indexed_db_tarball,omitempty"`
	IndexedDBSkipped    []string        `json:"indexed_db_skipped,omitempty"`
}
