package socket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func chunkFor(data []byte, index int) *Chunk {
	hash := sha256.Sum256(data)
	end := min((index+1)*ChunkBytes, len(data))
	return &Chunk{Index: index, Count: (len(data) + ChunkBytes - 1) / ChunkBytes, Total: len(data), SHA256: hex.EncodeToString(hash[:]), Data: base64.StdEncoding.EncodeToString(data[index*ChunkBytes : end])}
}
func TestChunkAggregateReservationExactBoundary(t *testing.T) {
	a := newChunkAssembler(10)
	if _, ok, err := a.add(1, chunkFor([]byte("null  "), 0), time.Now()); err != nil || !ok {
		t.Fatal(err)
	}
	if _, ok, err := a.add(2, chunkFor([]byte("null"), 0), time.Now()); err != nil || !ok || a.reserved != 10 {
		t.Fatal(err, a.reserved)
	}
	if _, _, err := a.add(3, chunkFor([]byte("0"), 0), time.Now()); err == nil {
		t.Fatal("aggregate cap bypass")
	}
	a.remove(1)
	if a.reserved != 4 {
		t.Fatal("reservation leaked")
	}
	if _, _, err := a.add(3, chunkFor([]byte("null  "), 0), time.Now()); err != nil {
		t.Fatal(err)
	}
}
func TestChunksRejectMalformedDuplicateHashAndOrder(t *testing.T) {
	data := []byte(`{"ok":true}`)
	for _, mutate := range []func(*Chunk){func(c *Chunk) { c.Index = 1 }, func(c *Chunk) { c.Count++ }, func(c *Chunk) { c.Total = -1 }, func(c *Chunk) { c.SHA256 = strings.Repeat("0", 64) }, func(c *Chunk) { c.Data = "invalid" }} {
		a := newChunkAssembler(100)
		c := chunkFor(data, 0)
		mutate(c)
		if _, _, err := a.add(1, c, time.Now()); err == nil {
			t.Fatal("malformed accepted", c)
		}
	}
	a := newChunkAssembler(100)
	c := chunkFor(data, 0)
	_, _, _ = a.add(1, c, time.Now())
	if _, _, err := a.add(1, c, time.Now()); err == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestChunkedRPCAboveFrameCap(t *testing.T) {
	if testing.Short() {
		t.Skip("large bounded transport allocation")
	}
	data := append([]byte{'"'}, bytes.Repeat([]byte{'x'}, 33<<20)...)
	data = append(data, '"')
	left, right := net.Pipe()
	peer := New(left, ws.StateClientSide)
	defer peer.Close()
	defer right.Close()
	serverErr := make(chan error, 1)
	go func() {
		request, err := ReadBounded(right, ws.StateServerSide)
		if err != nil {
			serverErr <- err
			return
		}
		var m Message
		if err = json.Unmarshal(request, &m); err != nil {
			serverErr <- err
			return
		}
		for n := 0; n < (len(data)+ChunkBytes-1)/ChunkBytes; n++ {
			frame, err := json.Marshal(Message{ID: m.ID, Chunk: chunkFor(data, n)})
			if err != nil {
				serverErr <- err
				return
			}
			if len(frame) > MaxMessage {
				serverErr <- context.DeadlineExceeded
				return
			}
			if err = wsutil.WriteServerText(right, frame); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out string
	if err := peer.Call(ctx, "readStorage", map[string]any{"chunked": true}, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 33<<20 || out[0] != 'x' || out[len(out)-1] != 'x' {
		t.Fatal("reassembled value differs")
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if peer.chunks.reserved != 0 {
		t.Fatal("completed call retained bytes")
	}
}
func TestCancelledChunkCallReleasesReservation(t *testing.T) {
	left, right := net.Pipe()
	peer := New(left, ws.StateClientSide)
	defer peer.Close()
	defer right.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- peer.Call(ctx, "readStorage", nil, nil) }()
	request, err := ReadBounded(right, ws.StateServerSide)
	if err != nil {
		t.Fatal(err)
	}
	var m Message
	_ = json.Unmarshal(request, &m)
	peer.mu.Lock()
	peer.chunks.streams[m.ID] = &assembly{total: 100, started: time.Now()}
	peer.chunks.reserved = 100
	peer.mu.Unlock()
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal(err)
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if peer.chunks.reserved != 0 || len(peer.chunks.streams) != 0 {
		t.Fatal("cancel leaked reassembly")
	}
}
