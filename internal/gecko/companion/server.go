// Package companion embeds the temporary Firefox extension and authenticates
// its bounded loopback-only channel. A disconnected extension is not silently
// reinstalled: a browser restart or protocol mismatch is required.
package companion

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gobwas/ws"
	"github.com/mvanhorn/agentcookie/internal/gecko"
	"github.com/mvanhorn/agentcookie/internal/gecko/socket"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

var ErrProtocolMismatch = errors.New("companion protocol mismatch")

const ProtocolVersion = 2
const ExtensionID = "agentcookie-companion@agentcookie.dev"

//go:embed chunks.js
var chunks string

//go:embed background.js
var background string

type Config struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Port            int    `json:"wsPort"`
	Token           string `json:"token"`
	ProfileHint     string `json:"profileHint"`
}

func Provision(dir string, port int, profileHint string, rotate bool, extra map[string]string) (Config, error) {
	var c Config
	if port < 1 || port > 65535 {
		return c, fmt.Errorf("invalid companion port")
	}
	if !rotate {
		b, e := os.ReadFile(filepath.Join(dir, "config.json"))
		if e == nil && json.Unmarshal(b, &c) == nil && c.Port == port && c.ProfileHint == profileHint {
			token, e := hex.DecodeString(c.Token)
			if e == nil && len(token) == 32 {
				if c.ProtocolVersion == ProtocolVersion {
					return c, nil
				}
				// A daemon upgrade must replace assets without invalidating the
				// credentials held by the surviving browser-session broker.
				c.ProtocolVersion = ProtocolVersion
			} else {
				c = Config{}
			}
		} else {
			c = Config{}
		}
	}
	if c.Token == "" {
		b := make([]byte, 32)
		if _, e := rand.Read(b); e != nil {
			return c, e
		}
		c = Config{ProtocolVersion: ProtocolVersion, Port: port, Token: hex.EncodeToString(b), ProfileHint: profileHint}
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return c, e
	}
	if e := os.Chmod(dir, 0700); e != nil {
		return c, e
	}
	raw, _ := json.Marshal(c)
	scripts := []string{"config.js", "chunks.js"}
	for _, name := range []string{"codec.js", "reader.js", "read-storage.js"} {
		if _, ok := extra[name]; ok {
			scripts = append(scripts, name)
		}
	}
	scripts = append(scripts, "background.js")
	manifest := map[string]any{"manifest_version": 2, "name": "agentcookie companion", "version": "1.0.0", "browser_specific_settings": map[string]any{"gecko": map[string]string{"id": ExtensionID}}, "permissions": []string{"cookies", "contextualIdentities", "tabs", "tabHide", "webNavigation", "scripting", "<all_urls>"}, "background": map[string]any{"scripts": scripts}, "content_security_policy": fmt.Sprintf("script-src 'self'; object-src 'none'; connect-src ws://127.0.0.1:%d", port)}
	m, _ := json.Marshal(manifest)
	files := map[string]string{"config.json": string(raw), "config.js": "globalThis.AgentCookieConfig = " + string(raw) + ";", "manifest.json": string(m), "background.js": background, "chunks.js": chunks}
	for k, v := range extra {
		if filepath.Base(k) != k {
			return c, fmt.Errorf("invalid asset name")
		}
		files[k] = v
	}
	for name, data := range files {
		if e := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); e != nil {
			return c, e
		}
	}
	return c, nil
}

type Snapshot struct {
	Cookies    []gecko.Cookie   `json:"cookies"`
	Identities []gecko.Identity `json:"identities"`
}
type Server struct {
	cfg       Config
	mu        sync.Mutex
	peer      *socket.Peer
	http      *http.Server
	listener  net.Listener
	connected chan struct{}
	changed   chan socket.Message
	notified  chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	Mismatch  bool
	Reason    string
}

func Listen(c Config) (*Server, error) {
	l, e := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port)))
	if e != nil {
		return nil, e
	}
	s := &Server{cfg: c, listener: l, connected: make(chan struct{}, 1), changed: make(chan socket.Message, 256), notified: make(chan struct{}, 1), closed: make(chan struct{}), Reason: "waiting for companion"}
	mux := http.NewServeMux()
	mux.HandleFunc("/companion", s.upgrade)
	s.http = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	go func() { _ = s.http.Serve(l) }()
	return s, nil
}
func (s *Server) upgrade(w http.ResponseWriter, r *http.Request) {
	host, port, e := net.SplitHostPort(r.Host)
	origin, oe := url.Parse(r.Header.Get("Origin"))
	if e != nil || host != "127.0.0.1" || port != strconv.Itoa(s.cfg.Port) || oe != nil || origin.Scheme != "moz-extension" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || r.URL.RawQuery != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if ip, _, e := net.SplitHostPort(r.RemoteAddr); e != nil || !net.ParseIP(ip).IsLoopback() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	busy := s.peer != nil
	s.mu.Unlock()
	if busy {
		http.Error(w, "already connected", http.StatusConflict)
		return
	}
	conn, _, _, e := ws.UpgradeHTTP(r, w)
	if e != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, e := socket.ReadBounded(conn, ws.StateServerSide)
	if e != nil {
		conn.Close()
		return
	}
	var hello struct {
		Hello            bool   `json:"hello"`
		Version          int    `json:"protocolVersion"`
		ExtensionVersion string `json:"extensionVersion"`
		Token            string `json:"token"`
		ProfileHint      string `json:"profileHint"`
	}
	if json.Unmarshal(b, &hello) != nil || !hello.Hello || subtle.ConstantTimeCompare([]byte(hello.Token), []byte(s.cfg.Token)) != 1 || hello.ProfileHint != s.cfg.ProfileHint {
		conn.Close()
		return
	}
	if hello.Version != ProtocolVersion {
		s.mu.Lock()
		s.Mismatch = true
		s.Reason = "companion protocol mismatch; reinstall required"
		s.mu.Unlock()
		select {
		case s.connected <- struct{}{}:
		default:
		}
		conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	p := socket.New(conn, ws.StateServerSide)
	s.mu.Lock()
	if s.peer != nil {
		s.mu.Unlock()
		p.Close()
		return
	}
	s.peer = p
	s.Reason = ""
	s.mu.Unlock()
	select {
	case s.connected <- struct{}{}:
	default:
	}
	go func() {
		defer func() {
			s.mu.Lock()
			if s.peer == p {
				s.peer = nil
				s.Reason = "companion disabled or disconnected"
			}
			s.mu.Unlock()
		}()
		for {
			select {
			case <-p.Done():
				return
			case <-s.closed:
				return
			case m := <-p.Events():
				select {
				case s.changed <- m:
					select {
					case s.notified <- struct{}{}:
					default:
					}
				default:
					p.Close()
					return
				}
			}
		}
	}()
}
func (s *Server) Wait(ctx context.Context) error {
	for {
		s.mu.Lock()
		p := s.peer
		mismatch := s.Mismatch
		s.mu.Unlock()
		if mismatch {
			return ErrProtocolMismatch
		}
		if p != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return fmt.Errorf("companion server closed")
		case <-s.connected:
		}
	}
}
func (s *Server) Call(ctx context.Context, method string, params, out any) error {
	s.mu.Lock()
	p := s.peer
	s.mu.Unlock()
	if p == nil {
		return fmt.Errorf("companion disconnected")
	}
	return p.Call(ctx, method, params, out)
}
func (s *Server) Snapshot(ctx context.Context) (Snapshot, error) {
	var snap Snapshot
	e := s.Call(ctx, "snapshot", map[string]any{}, &snap)
	return snap, e
}
func (s *Server) Events() <-chan socket.Message { return s.changed }
func (s *Server) Status() (bool, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peer != nil, s.Reason, s.Mismatch
}
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.mu.Lock()
		if s.peer != nil {
			s.peer.Close()
		}
		s.mu.Unlock()
		s.http.Close()
	})
	return nil
}
func ProfileKey(profile string) string {
	sum := sha256.Sum256([]byte(profile))
	return hex.EncodeToString(sum[:16])
}

func (s *Server) Notifications() <-chan struct{} { return s.notified }

func (s *Server) ResetMismatch() { s.mu.Lock(); s.Mismatch = false; s.mu.Unlock() }
