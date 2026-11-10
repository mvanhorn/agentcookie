package socket

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"io"
	"net"
	"testing"
	"time"
)

func TestFrameCapBeforeAllocation(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { _ = ws.WriteHeader(b, ws.Header{Fin: true, OpCode: ws.OpText, Length: MaxMessage + 1}) }()
	if _, e := ReadBounded(a, ws.StateClientSide); e == nil {
		t.Fatal("oversized frame accepted")
	}
}
func TestRPCAndCancellation(t *testing.T) {
	a, b := net.Pipe()
	p := New(a, ws.StateClientSide)
	defer p.Close()
	defer b.Close()
	go func() {
		raw, _ := ReadBounded(b, ws.StateServerSide)
		var m Message
		json.Unmarshal(raw, &m)
		reply, _ := json.Marshal(Message{ID: m.ID, Result: json.RawMessage(`{"ok":true}`)})
		wsutil.WriteServerText(b, reply)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out struct {
		OK bool `json:"ok"`
	}
	if e := p.Call(ctx, "test", map[string]any{}, &out); e != nil || !out.OK {
		t.Fatalf("%+v %v", out, e)
	}
}
func TestPingInsideFragmentedMessage(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	errors := make(chan error, 1)
	go func() {
		if e := ws.WriteFrame(b, ws.Frame{Header: ws.Header{Fin: false, OpCode: ws.OpText, Length: 2}, Payload: []byte("he")}); e != nil {
			errors <- e
			return
		}
		if e := ws.WriteFrame(b, ws.NewPingFrame([]byte("alive"))); e != nil {
			errors <- e
			return
		}
		h, e := ws.ReadHeader(b)
		if e != nil {
			errors <- e
			return
		}
		payload := make([]byte, h.Length)
		_, e = io.ReadFull(b, payload)
		if h.Masked {
			ws.Cipher(payload, h.Mask, 0)
		}
		if e != nil || h.OpCode != ws.OpPong || string(payload) != "alive" {
			errors <- fmt.Errorf("bad pong %v %q %v", h.OpCode, payload, e)
			return
		}
		errors <- ws.WriteFrame(b, ws.Frame{Header: ws.Header{Fin: true, OpCode: ws.OpContinuation, Length: 3}, Payload: []byte("llo")})
	}()
	out, e := ReadBounded(a, ws.StateClientSide)
	if e != nil || string(out) != "hello" {
		t.Fatalf("%q %v", out, e)
	}
	if e = <-errors; e != nil {
		t.Fatal(e)
	}
}
func TestInvalidTextRejected(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go wsutil.WriteServerText(b, []byte{0xff})
	if _, e := ReadBounded(a, ws.StateClientSide); e == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}
func TestCloseHandshakeAndMalformedClose(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			done := make(chan error, 1)
			go func() {
				payload := []byte{3, 232}
				if invalid {
					payload = []byte{1}
				}
				if e := wsutil.WriteServerMessage(b, ws.OpClose, payload); e != nil {
					done <- e
					return
				}
				if invalid {
					done <- nil
					return
				}
				frame, e := ws.ReadFrame(b)
				if e == nil && frame.Header.OpCode != ws.OpClose {
					e = fmt.Errorf("missing close reply")
				}
				done <- e
			}()
			_, e := ReadBounded(a, ws.StateClientSide)
			if invalid && e == nil {
				t.Fatal("malformed close accepted")
			}
			if !invalid && e != io.EOF {
				t.Fatal(e)
			}
			if e = <-done; e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestUnmaskedClientFrameRejected(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go wsutil.WriteServerText(b, []byte(`{}`))
	if _, e := ReadBounded(a, ws.StateServerSide); e == nil {
		t.Fatal("unmasked client frame accepted")
	}
}
