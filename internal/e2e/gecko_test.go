package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko/companion"
	"github.com/mvanhorn/agentcookie/internal/gecko/profilelock"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/transport"
	"gopkg.in/yaml.v3"
)

type geckoSeed struct {
	Visits    atomic.Int64
	Version   atomic.Int64
	Container uint32
	Origins   []string
}

func seedGecko(t *testing.T, g *GeckoFixture) *geckoSeed {
	t.Helper()
	seed := &geckoSeed{}
	seed.Version.Store(1)
	reported := make(chan json.RawMessage, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/report":
			b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			select {
			case reported <- b:
			default:
			}
		case "/version":
			fmt.Fprint(w, seed.Version.Load())
		default:
			seed.Visits.Add(1)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<!doctype html><title>fixture</title>")
		}
	})
	one := httptest.NewServer(handler)
	two := httptest.NewServer(handler)
	t.Cleanup(one.Close)
	t.Cleanup(two.Close)
	seed.Origins = []string{one.URL, strings.Replace(two.URL, "127.0.0.1", "localhost", 1)}
	origins, _ := json.Marshal(seed.Origins)
	report, _ := json.Marshal(one.URL)
	script := `
(async()=>{const endpoint=` + string(report) + `;try{
 const work=await browser.contextualIdentities.create({name:'Fixture Work',color:'blue',icon:'briefcase'});
 await browser.cookies.set({url:'https://example.test/',name:'fixture_default',value:'token-1',secure:true});
 await browser.cookies.set({url:'https://example.test/',name:'fixture_work',value:'work',secure:true,storeId:work.cookieStoreId});
 for(const ancestor of [false,true])await browser.cookies.set({url:'https://example.test/',name:'fixture_partition_'+ancestor,value:'partition',secure:true,partitionKey:{topLevelSite:'https://top.example',hasCrossSiteAncestor:ancestor}});
 for(const origin of ` + string(origins) + `)for(const storeId of ['firefox-default',work.cookieStoreId]){
  const tab=await browser.tabs.create({url:origin+'/seed',active:false,cookieStoreId:storeId});
  try {for(let i=0;i<100;i++){if((await browser.tabs.get(tab.id)).status==='complete')break;await new Promise(r=>setTimeout(r,100));}
   await browser.tabs.executeScript(tab.id,{code:"(async()=>{localStorage.setItem('fixture_token','storage-token');const db=await new Promise((resolve,reject)=>{const r=indexedDB.open('auth',1);r.onupgradeneeded=()=>r.result.createObjectStore('kv');r.onsuccess=()=>resolve(r.result);r.onerror=()=>reject(r.error)});await new Promise((resolve,reject)=>{const tx=db.transaction('kv','readwrite');tx.objectStore('kv').put('idb-token','token');tx.oncomplete=resolve;tx.onabort=()=>reject(tx.error)});db.close();return true})()"});
  }finally{await browser.tabs.remove(tab.id)}
 }
 let version='1';setInterval(async()=>{try{const next=await(await fetch(endpoint+'/version')).text();if(next!==version){await browser.cookies.set({url:'https://example.test/',name:'fixture_default',value:'token-'+next,secure:true});version=next}}catch(_){}},250);
 await fetch(endpoint+'/report',{method:'POST',body:JSON.stringify({container:Number(work.cookieStoreId.split('-').pop())})});
}catch(error){await fetch(endpoint+'/report',{method:'POST',body:JSON.stringify({error:String(error)})})}})();`
	g.InstallFixtureExtension(script)
	select {
	case raw := <-reported:
		var result struct {
			Container uint32 `json:"container"`
			Error     string `json:"error"`
		}
		if e := json.Unmarshal(raw, &result); e != nil || result.Error != "" || result.Container == 0 {
			t.Fatalf("Gecko fixture seed failed: %s (%v)", raw, e)
		}
		seed.Container = result.Container
	case <-time.After(30 * time.Second):
		t.Fatal("Gecko fixture seeding timed out")
	}
	return seed
}
func TestGeckoLiveLifecycle(t *testing.T) {
	g := NewGecko(t)
	seed := seedGecko(t, g)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	o := g.ReaderOptions()
	o.Containers = []string{"default"}
	r, e := geckoreader.Open(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { r.Close() })
	snap, e := r.Read(ctx, &config.Blocklist{Version: 1})
	if e != nil {
		t.Fatal(e)
	}
	names := map[string]bool{}
	for _, c := range snap.Cookies {
		names[c.Name] = true
		if strings.HasPrefix(c.Name, "fixture_partition_") {
			want := 0
			if strings.HasSuffix(c.Name, "true") {
				want = 1
			}
			if c.HasCrossSiteAncestor != want || c.TopFrameSiteKey != "https://top.example" {
				t.Fatalf("partition provenance lost: %+v", c)
			}
		}
	}
	for _, name := range []string{"fixture_default", "fixture_partition_false", "fixture_partition_true"} {
		if !names[name] {
			t.Fatalf("missing live cookie %s", name)
		}
	}
	capture, ok := snap.RoutingData.(*geckoreader.Capture)
	if !ok || len(capture.Identities) == 0 {
		t.Fatal("missing container identity capture")
	}
	for _, origin := range seed.Origins {
		for _, container := range []uint32{0, seed.Container} {
			found := false
			for _, entry := range r.Catalog().Entries {
				found = found || (entry.Origin == origin && entry.Container == container)
			}
			if !found {
				t.Fatalf("closed-tab cookie-free inventory missed %s container %d", origin, container)
			}
		}
	}
	if lock, e := profilelock.Acquire(g.Profile); !errors.Is(e, profilelock.ErrBrowserRunning) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("running browser lock not refused: %v", e)
	}
	visits := seed.Visits.Load()
	state := g.BrokerState()
	r.Close()
	r, e = geckoreader.Open(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Read(ctx, &config.Blocklist{Version: 1}); e != nil {
		t.Fatal(e)
	}
	after := g.BrokerState()
	if state.PID != after.PID || state.TokenHash != after.TokenHash {
		t.Fatal("daemon reconnect replaced broker or token")
	}
	if seed.Visits.Load() != visits {
		t.Fatal("catalog-only read visited fixture sites")
	}
	r.Close()
	g.StopBroker()
	g.RestartBrowser()
	r, e = geckoreader.Open(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Read(ctx, &config.Blocklist{Version: 1}); e != nil {
		t.Fatal(e)
	}
	if g.BrokerState().TokenHash == state.TokenHash {
		t.Fatal("browser restart failed to rotate companion token")
	}
}

func TestGeckoDaemonCrash(t *testing.T) {
	g := NewGecko(t)
	seed := seedGecko(t, g)
	configDir := filepath.Join(g.Home, "daemon-config")
	g.StateDir = filepath.Join(configDir, "state")
	if e := os.MkdirAll(configDir, 0700); e != nil {
		t.Fatal(e)
	}
	const secret = "hermetic-gecko-transport-secret"
	received := make(chan string, 32)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sync" {
			http.NotFound(w, r)
			return
		}
		wire, e := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if e != nil {
			t.Error(e)
			return
		}
		plain, e := transport.OpenWithSecret(wire, secret)
		if e != nil {
			t.Error(e)
			return
		}
		var envelope protocol.SyncEnvelope
		if e = json.Unmarshal(plain, &envelope); e != nil {
			t.Error(e)
			return
		}
		for _, c := range envelope.Cookies {
			if c.Name == "fixture_default" {
				select {
				case received <- c.Value:
				default:
				}
			}
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	t.Cleanup(sink.Close)
	cfg := config.SourceConfig{Sink: config.SinkRef{URL: sink.URL + "/sync"}, Security: config.SecurityRef{SharedSecret: secret}, Browser: config.BrowserRef{Name: "firefox", Profile: g.Profile, Reader: "live", BiDiPort: g.BiDiPort, CompanionPort: g.CompanionPort, Containers: []string{"default"}}}
	raw, e := yaml.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(configDir, "source.yaml"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	start := func() (*exec.Cmd, <-chan error) {
		cmd := exec.Command(g.AgentcookieBinary, "--config-dir", configDir, "--json", "source", "--watch", "--skip-secrets", "--pull-listen", fmt.Sprintf("127.0.0.1:%d", fixturePort(t)))
		cmd.Env = os.Environ()
		log, e := os.OpenFile(filepath.Join(g.Home, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if e != nil {
			t.Fatal(e)
		}
		cmd.Stdout = log
		cmd.Stderr = log
		if e = cmd.Start(); e != nil {
			log.Close()
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait(); log.Close() }()
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		})
		return cmd, done
	}
	waitToken := func(want string) {
		timer := time.NewTimer(90 * time.Second)
		defer timer.Stop()
		for {
			select {
			case got := <-received:
				if got == want {
					return
				}
			case <-timer.C:
				t.Fatalf("daemon did not deliver %s after reconnect", want)
			}
		}
	}
	daemon, done := start()
	waitToken("token-1")
	state := g.BrokerState()
	browserPID := g.BrowserProcessID()
	if e = daemon.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	<-done
	if e = syscall.Kill(state.PID, 0); e != nil {
		t.Fatalf("broker died with daemon: %v", e)
	}
	if g.BrowserProcessID() != browserPID {
		t.Fatal("browser restarted")
	}
	seed.Version.Store(2)
	_, _ = start()
	waitToken("token-2")
	after := g.BrokerState()
	if after.PID != state.PID || after.TokenHash != state.TokenHash {
		t.Fatal("daemon crash recovery created a new broker/session")
	}
}

func TestGeckoStartsAfterSource(t *testing.T) {
	g := NewGecko(t)
	g.StopBrowser()
	o := g.ReaderOptions()
	o.Mode = "auto"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, e := geckoreader.Open(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	closed, e := r.Read(ctx, &config.Blocklist{Version: 1})
	if e != nil {
		t.Fatal(e)
	}
	if closed.ReaderMode != "file" || closed.Meta.Surfaces[protocol.Cookies].State != protocol.Incomplete {
		t.Fatal("closed browser mode was not incomplete file mode")
	}
	g.RestartBrowser()
	live, e := r.Read(ctx, &config.Blocklist{Version: 1})
	if e != nil {
		t.Fatal(e)
	}
	if live.ReaderMode != "live" {
		t.Fatal("source did not attach after browser started")
	}
}

func TestGeckoCompanionRemovalRequiresBrowserRestart(t *testing.T) {
	g := NewGecko(t)
	seed := seedGecko(t, g)
	_ = seed
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r, e := geckoreader.Open(ctx, g.ReaderOptions())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if _, e = r.Read(ctx, &config.Blocklist{Version: 1}); e != nil {
		t.Fatal(e)
	}
	before := g.BrokerState()
	if e = r.Broker().RemoveCompanion(ctx); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connected, _, _ := r.Companion().Status()
		if !connected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	short, stop := context.WithTimeout(ctx, 3*time.Second)
	_, e = r.Read(short, &config.Blocklist{Version: 1})
	stop()
	if e == nil {
		t.Fatal("removed companion was silently reinstalled")
	}
	if g.BrokerState().TokenHash != before.TokenHash {
		t.Fatal("companion removal rotated token without browser restart")
	}
	r.Close()
	g.StopBroker()
	g.RestartBrowser()
	r, e = geckoreader.Open(ctx, g.ReaderOptions())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	fresh, e := r.Read(ctx, &config.Blocklist{Version: 1})
	if e != nil {
		t.Fatal(e)
	}
	if fresh.ReaderMode != "live" || !fresh.Meta.Inventory.Listed || g.BrokerState().TokenHash == before.TokenHash {
		t.Fatal("browser restart did not reinstall and rescan")
	}
}

func TestGeckoBrokerCrashKeepsCookiesWithoutRetryStorm(t *testing.T) {
	g := NewGecko(t)
	seedGecko(t, g)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	o := g.ReaderOptions()
	o.Containers = []string{"default"}
	r, e := geckoreader.Open(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if _, e = r.Read(ctx, &config.Blocklist{Version: 1}); e != nil {
		t.Fatal(e)
	}
	state := g.BrokerState()
	if e = r.Broker().Check(ctx); e != nil {
		t.Fatal(e)
	}
	if e = syscall.Kill(state.PID, syscall.SIGKILL); e != nil {
		t.Fatal(e)
	}
	logPath := filepath.Join(g.StateDir, "gecko-broker", companion.ProfileKey(g.Profile)+".json.log")
	before, _ := os.Stat(logPath)
	for range 3 {
		snap, e := r.Read(ctx, &config.Blocklist{Version: 1})
		if e != nil {
			t.Fatal(e)
		}
		if snap.ReaderMode != "live" || len(snap.Cookies) == 0 || !strings.Contains(snap.ReaderReason, "storage reads paused") {
			t.Fatalf("broker loss not transparently degraded: %s %s", snap.ReaderMode, snap.ReaderReason)
		}
	}
	if r.Broker() != nil {
		t.Fatal("lost broker still presented as available")
	}
	after, _ := os.Stat(logPath)
	if before != nil && after != nil && before.Size() != after.Size() {
		t.Fatal("reader retried acquisition of lost BiDi slot")
	}
}
