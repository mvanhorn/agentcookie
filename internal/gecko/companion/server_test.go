package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	c, e := Provision(t.TempDir(), port, "fixture", true, nil)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestTokenLifecycle(t *testing.T) {
	d := t.TempDir()
	a, e := Provision(d, 9225, "fixture", true, nil)
	if e != nil {
		t.Fatal(e)
	}
	b, e := Provision(d, 9225, "fixture", false, nil)
	if e != nil || a.Token != b.Token {
		t.Fatal("daemon restart rotated token", e)
	}
	c, e := Provision(d, 9225, "fixture", true, nil)
	if e != nil || a.Token == c.Token {
		t.Fatal("browser restart did not rotate", e)
	}
	for _, name := range []string{"config.json", "config.js", "manifest.json", "background.js"} {
		s, e := os.Stat(filepath.Join(d, name))
		if e != nil || s.Mode().Perm() != 0600 {
			t.Fatal(name, e)
		}
	}
}
func TestHandshakeRejectsPageOrigin(t *testing.T) {
	c := testConfig(t)
	s, e := Listen(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	d := ws.Dialer{Header: ws.HandshakeHeaderHTTP(http.Header{"Origin": []string{"https://evil.example"}})}
	conn, _, _, e := d.Dial(context.Background(), fmt.Sprintf("ws://127.0.0.1:%d/companion", c.Port))
	if e == nil {
		conn.Close()
		t.Fatal("page origin accepted")
	}
}
func TestHandshakeAndOneConnection(t *testing.T) {
	c := testConfig(t)
	s, e := Listen(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	d := ws.Dialer{Header: ws.HandshakeHeaderHTTP(http.Header{"Origin": []string{"moz-extension://test-uuid"}})}
	conn, _, _, e := d.Dial(context.Background(), fmt.Sprintf("ws://127.0.0.1:%d/companion", c.Port))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	hello, _ := json.Marshal(map[string]any{"hello": true, "protocolVersion": 1, "token": c.Token, "profileHint": "fixture"})
	if e = wsutil.WriteClientText(conn, hello); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = s.Wait(ctx); e != nil {
		t.Fatal(e)
	}
	second, _, _, e := d.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/companion", c.Port))
	if e == nil {
		second.Close()
		t.Fatal("second connection accepted")
	}
}
func TestBadTokenRejected(t *testing.T) {
	c := testConfig(t)
	s, e := Listen(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	d := ws.Dialer{Header: ws.HandshakeHeaderHTTP(http.Header{"Origin": []string{"moz-extension://test-uuid"}})}
	conn, _, _, e := d.Dial(context.Background(), fmt.Sprintf("ws://127.0.0.1:%d/companion", c.Port))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	_ = wsutil.WriteClientText(conn, []byte(`{"hello":true,"protocolVersion":1,"token":"wrong","profileHint":"fixture"}`))
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = wsutil.ReadServerText(conn); e == nil {
		t.Fatal("token accepted")
	}
	connected, _, _ := s.Status()
	if connected {
		t.Fatal("bad token connected")
	}
}
func TestVersionMismatchRequiresReinstall(t *testing.T) {
	c := testConfig(t)
	s, e := Listen(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	d := ws.Dialer{Header: ws.HandshakeHeaderHTTP(http.Header{"Origin": []string{"moz-extension://test-uuid"}})}
	conn, _, _, e := d.Dial(context.Background(), fmt.Sprintf("ws://127.0.0.1:%d/companion", c.Port))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	hello, _ := json.Marshal(map[string]any{"hello": true, "protocolVersion": ProtocolVersion + 1, "token": c.Token, "profileHint": "fixture"})
	wsutil.WriteClientText(conn, hello)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = s.Wait(ctx); !errors.Is(e, ErrProtocolMismatch) {
		t.Fatal(e)
	}
	connected, _, mismatch := s.Status()
	if connected || !mismatch {
		t.Fatal("mismatch accepted")
	}
}
func TestNonLoopbackHostHeaderRejected(t *testing.T) {
	c := testConfig(t)
	s, e := Listen(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	r, e := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/companion", c.Port), nil)
	if e != nil {
		t.Fatal(e)
	}
	r.Host = "evil.example"
	r.Header.Set("Origin", "moz-extension://test-uuid")
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatal(res.StatusCode)
	}
}

func TestProtocolUpgradePreservesSessionToken(t *testing.T) {
	d := t.TempDir()
	initial, err := Provision(d, 9225, "fixture", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := initial
	old.ProtocolVersion = 0
	data, _ := json.Marshal(old)
	if err = os.WriteFile(filepath.Join(d, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Provision(d, 9225, "fixture", false, map[string]string{"reader.js": "new assets"})
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Token != initial.Token || upgraded.ProtocolVersion != ProtocolVersion {
		t.Fatalf("bad upgrade: %+v", upgraded)
	}
	asset, err := os.ReadFile(filepath.Join(d, "reader.js"))
	if err != nil || string(asset) != "new assets" {
		t.Fatal("upgrade did not replace assets", err)
	}
}
