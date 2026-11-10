// Package socket provides bounded JSON RPC over WebSocket. It never allocates
// according to an unchecked remote length, including continuation frames.
package socket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"io"
	"net"
	"sync"
	"time"
)

const MaxMessage = 32 << 20

type Message struct {
	ID      uint64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
	Type    string          `json:"type,omitempty"`
	Message string          `json:"message,omitempty"`
	Chunk   *Chunk          `json:"chunk,omitempty"`
}

func ReadBounded(conn net.Conn, state ws.State) ([]byte, error) {
	return readBounded(conn, state, func(op ws.OpCode, b []byte) error {
		if e := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); e != nil {
			return e
		}
		return wsutil.WriteMessage(conn, state, op, b)
	})
}
func readBounded(conn net.Conn, state ws.State, write func(ws.OpCode, []byte) error) ([]byte, error) {
	control := func(h ws.Header, src io.Reader) error {
		b, e := io.ReadAll(io.LimitReader(src, 126))
		if e != nil {
			return e
		}
		if len(b) > 125 {
			return fmt.Errorf("control frame exceeds cap")
		}
		switch h.OpCode {
		case ws.OpPing:
			return write(ws.OpPong, b)
		case ws.OpPong:
			return nil
		case ws.OpClose:
			if len(b) == 1 {
				return fmt.Errorf("invalid close payload")
			}
			if len(b) > 1 {
				code, reason := ws.ParseCloseFrameData(b)
				if e = ws.CheckCloseFrameData(code, reason); e != nil {
					return e
				}
			}
			_ = write(ws.OpClose, b)
			return io.EOF
		default:
			return fmt.Errorf("unknown control opcode")
		}
	}
	r := wsutil.Reader{Source: conn, State: state, CheckUTF8: true, MaxFrameSize: MaxMessage, OnIntermediate: control}
	for {
		h, e := r.NextFrame()
		if e != nil {
			return nil, e
		}
		if h.OpCode.IsControl() {
			if e = control(h, &r); e != nil {
				return nil, e
			}
			continue
		}
		if h.OpCode != ws.OpText {
			return nil, fmt.Errorf("expected text frame")
		}
		b, e := io.ReadAll(io.LimitReader(&r, MaxMessage+1))
		if e != nil {
			return nil, e
		}
		if len(b) > MaxMessage {
			return nil, fmt.Errorf("message exceeds cap")
		}
		return b, nil
	}
}

type Peer struct {
	conn    net.Conn
	state   ws.State
	mu      sync.Mutex
	write   sync.Mutex
	next    uint64
	pending map[uint64]chan Message
	events  chan Message
	done    chan struct{}
	once    sync.Once
	chunks  *chunkAssembler
}

func New(conn net.Conn, state ws.State) *Peer {
	p := &Peer{conn: conn, state: state, pending: map[uint64]chan Message{}, events: make(chan Message, 256), done: make(chan struct{}), chunks: newChunkAssembler(MaxReassembly)}
	go p.read()
	go p.expireChunks()
	return p
}
func (p *Peer) read() {
	defer p.Close()
	for {
		b, e := readBounded(p.conn, p.state, p.writeFrame)
		if e != nil {
			return
		}
		var m Message
		if json.Unmarshal(b, &m) != nil {
			return
		}
		if m.ID != 0 {
			p.mu.Lock()
			ch := p.pending[m.ID]
			if m.Chunk != nil {
				if ch == nil {
					unknown := m.ID > p.next
					p.mu.Unlock()
					if unknown {
						return
					}
					continue
				}
				if len(m.Result) != 0 || len(m.Error) != 0 || m.Method != "" {
					p.mu.Unlock()
					return
				}
				result, complete, err := p.chunks.add(m.ID, m.Chunk, time.Now())
				if err != nil {
					p.mu.Unlock()
					return
				}
				if !complete {
					p.mu.Unlock()
					continue
				}
				m.Result = result
				m.Chunk = nil
			} else if stream := p.chunks.streams[m.ID]; stream != nil {
				p.mu.Unlock()
				return
			}
			if ch != nil && p.chunks.streams[m.ID] == nil {
				size := len(m.Result) + len(m.Error)
				if size > p.chunks.limit-p.chunks.reserved {
					p.mu.Unlock()
					return
				}
				p.chunks.streams[m.ID] = &assembly{total: size, complete: true, started: time.Now()}
				p.chunks.reserved += size
			}
			p.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default:
				}
			}
		} else {
			if len(b) > 64<<10 {
				return
			}
			select {
			case p.events <- m:
			default:
				return
			}
		}
	}
}
func (p *Peer) Call(ctx context.Context, method string, params any, out any) error {
	b, e := json.Marshal(params)
	if e != nil {
		return e
	}
	p.mu.Lock()
	if len(p.pending) >= 64 {
		p.mu.Unlock()
		return errors.New("too many pending calls")
	}
	p.next++
	id := p.next
	ch := make(chan Message, 1)
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, id); p.chunks.remove(id); p.mu.Unlock() }()
	if e = p.Send(Message{ID: id, Method: method, Params: b}); e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return io.EOF
	case m := <-ch:
		if len(m.Error) > 0 && string(m.Error) != "null" {
			return fmt.Errorf("%s: %s", method, m.Error)
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	}
}
func (p *Peer) Send(m any) error {
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	if len(b) > MaxMessage {
		return errors.New("message exceeds cap")
	}
	return p.writeFrame(ws.OpText, b)
}
func (p *Peer) Events() <-chan Message { return p.events }
func (p *Peer) Done() <-chan struct{}  { return p.done }
func (p *Peer) Close() error {
	var e error
	p.once.Do(func() {
		close(p.done)
		e = p.conn.Close()
		p.mu.Lock()
		p.chunks = newChunkAssembler(MaxReassembly)
		p.mu.Unlock()
	})
	return e
}

func (p *Peer) writeFrame(op ws.OpCode, b []byte) error {
	p.write.Lock()
	defer p.write.Unlock()
	if e := p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); e != nil {
		return e
	}
	return wsutil.WriteMessage(p.conn, p.state, op, b)
}

func (p *Peer) expireChunks() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-p.done:
			return
		case now := <-tick.C:
			p.mu.Lock()
			expired := false
			for _, stream := range p.chunks.streams {
				if !stream.complete && now.Sub(stream.started) > ChunkTimeout {
					expired = true
					break
				}
			}
			p.mu.Unlock()
			if expired {
				_ = p.Close()
				return
			}
		}
	}
}
