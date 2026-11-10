package socket

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"time"
)

const ChunkBytes = 16 << 20

// Two 128 MiB surfaces plus bounded envelope/schema overhead, shared across ALL
// pending calls. Frame/base64 temporary buffers remain bounded by MaxMessage.
const MaxReassembly = 272 << 20
const ChunkTimeout = 30 * time.Second

type Chunk struct {
	Index  int    `json:"index"`
	Count  int    `json:"count"`
	Total  int    `json:"total"`
	SHA256 string `json:"sha256"`
	Data   string `json:"data"`
}
type assembly struct {
	next, count, total int
	digest             string
	sum                hash.Hash
	data               []byte
	started            time.Time
	complete           bool
}
type chunkAssembler struct {
	streams         map[uint64]*assembly
	reserved, limit int
}

func newChunkAssembler(limit int) *chunkAssembler {
	if limit < 0 || limit > MaxReassembly {
		limit = MaxReassembly
	}
	return &chunkAssembler{streams: map[uint64]*assembly{}, limit: limit}
}
func (a *chunkAssembler) remove(id uint64) {
	if stream := a.streams[id]; stream != nil {
		a.reserved -= stream.total
		delete(a.streams, id)
	}
}
func (a *chunkAssembler) add(id uint64, c *Chunk, now time.Time) (json.RawMessage, bool, error) {
	if id == 0 || c == nil || c.Total <= 0 || c.Total > a.limit || c.Count != (c.Total+ChunkBytes-1)/ChunkBytes || c.Index < 0 || c.Index >= c.Count || len(c.SHA256) != 64 {
		return nil, false, errors.New("invalid chunk metadata")
	}
	if digest, err := hex.DecodeString(c.SHA256); err != nil || len(digest) != 32 {
		return nil, false, errors.New("invalid chunk digest")
	}
	stream := a.streams[id]
	if stream == nil {
		if c.Index != 0 {
			return nil, false, errors.New("chunk starts out of order")
		}
		if c.Total > a.limit-a.reserved {
			return nil, false, errors.New("peer reassembly bytes limit")
		}
		stream = &assembly{count: c.Count, total: c.Total, digest: c.SHA256, sum: sha256.New(), data: make([]byte, 0, c.Total), started: now}
		a.streams[id] = stream
		a.reserved += c.Total
	}
	if stream.complete || stream.next != c.Index || stream.count != c.Count || stream.total != c.Total || stream.digest != c.SHA256 || now.Sub(stream.started) > ChunkTimeout {
		return nil, false, errors.New("duplicate, changed, expired or out-of-order chunk")
	}
	expected := ChunkBytes
	if remaining := c.Total - c.Index*ChunkBytes; remaining < expected {
		expected = remaining
	}
	if len(c.Data) != base64.StdEncoding.EncodedLen(expected) {
		return nil, false, errors.New("chunk length mismatch")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(c.Data)
	if err != nil || len(decoded) != expected {
		return nil, false, errors.New("invalid chunk data")
	}
	stream.data = append(stream.data, decoded...)
	_, _ = stream.sum.Write(decoded)
	stream.next++
	if stream.next != stream.count {
		return nil, false, nil
	}
	if len(stream.data) != stream.total || hex.EncodeToString(stream.sum.Sum(nil)) != stream.digest || !json.Valid(stream.data) {
		return nil, false, errors.New("chunk content hash or JSON mismatch")
	}
	stream.complete = true
	return stream.data, true, nil
}
