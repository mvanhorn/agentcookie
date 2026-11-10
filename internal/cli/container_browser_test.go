package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/e2e"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"github.com/mvanhorn/agentcookie/internal/state"
)

// This final gate is deliberately opt-in. It seeds only extension-created
// containers in a throwaway Gecko profile and owns both Chromium profiles.
// Run after VM coding/reviews, never as an implicit local browser test.
func TestContainerScenario6bBrowsers(t *testing.T) {
	if os.Getenv("AGENTCOOKIE_CONTAINER_E2E") != "1" {
		t.Skip("final multi-container browser gate not requested")
	}
	if os.Getenv("AGENTCOOKIE_GECKO_BINARY") == "" || os.Getenv("AGENTCOOKIE_CHROME_BINARY") == "" || os.Getenv("AGENTCOOKIE_BINARY") == "" {
		t.Fatal("explicit Gecko, Chrome and agentcookie fixture binaries required")
	}
	g := e2e.NewGecko(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var phase atomic.Int64
	phase.Store(1)
	reported := make(chan string, 4)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/phase":
			fmt.Fprint(w, phase.Load())
		case "/report":
			b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			select {
			case reported <- string(b):
			default:
			}
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<!doctype html><title>account fixture</title>")
		}
	}))
	defer site.Close()
	origin := site.URL
	script := `(async()=>{const origin=` + strconv.Quote(origin) + `;try{
 const personal=await browser.contextualIdentities.create({name:'Personal',color:'red',icon:'circle'});
 const work=await browser.contextualIdentities.create({name:'Work',color:'blue',icon:'briefcase'});
 async function write(store,value){
  const tab=await browser.tabs.create({url:origin+'/fixture',cookieStoreId:store.cookieStoreId,active:false});
  try{for(let n=0;n<150;n++){if((await browser.tabs.get(tab.id)).status==='complete')break;await new Promise(r=>setTimeout(r,100))}
   const code='(async()=>{const value='+JSON.stringify(value)+';if(value===null){document.cookie="session=;Max-Age=0;Path=/";localStorage.removeItem("session")}else{document.cookie="session="+value+";Path=/";localStorage.setItem("session",value)}const db=await new Promise((ok,bad)=>{const r=indexedDB.open("auth",1);r.onupgradeneeded=()=>r.result.createObjectStore("sessions");r.onsuccess=()=>ok(r.result);r.onerror=()=>bad(r.error)});await new Promise((ok,bad)=>{const tx=db.transaction("sessions","readwrite");const s=tx.objectStore("sessions");if(value===null)s.delete("current");else s.put(value,"current");tx.oncomplete=ok;tx.onabort=()=>bad(tx.error)});db.close();return true})()';
   await browser.tabs.executeScript(tab.id,{code});
  }finally{await browser.tabs.remove(tab.id)}
 }
 await write(personal,'personal-account');await write(work,'work-account');
 await fetch(origin+'/report',{method:'POST',body:'ready'});
 let busy=false;setInterval(async()=>{if(busy)return;busy=true;try{const next=await(await fetch(origin+'/phase')).text();if(next==='2'){await write(personal,null);await fetch(origin+'/report',{method:'POST',body:'logout'});busy=true;return}}catch(e){await fetch(origin+'/report',{method:'POST',body:'error:'+e})}busy=false},200);
}catch(e){await fetch(origin+'/report',{method:'POST',body:'error:'+e})}})();`
	g.InstallFixtureExtension(script)
	await := func(want string) {
		t.Helper()
		select {
		case got := <-reported:
			if got != want {
				t.Fatalf("Gecko seed: %s, want %s", got, want)
			}
		case <-ctx.Done():
			t.Fatal("Gecko seed timed out")
		}
	}
	await("ready")
	opts := g.ReaderOptions()
	opts.StorageReadMode = "request"
	opts.StoragePreferences = [][]string{{"Personal"}, {"Work"}}
	reader, err := geckoreader.Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	dir := filepath.Join(g.Home, "container-wire")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	withConfigDir(t, dir)
	writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), "version: 1\npolicy: blocklist\ndomains: []\n")
	p := &destinationPipeline{entries: map[string]*destinationEntry{}, order: []string{"personal", "work"}}
	browsers := map[string]context.Context{}
	for _, name := range p.order {
		h := e2e.New(t)
		browser := h.Browser()
		browsers[name] = browser
		// Use the production factory and converter. The fixture owns the
		// browser process; attach mode targets only its throwaway endpoint.
		managed := destination.NewManaged(config.DestinationRef{Name: name, Kind: "attach", Endpoint: h.Endpoint})
		defer managed.Close()
		applier, surfaces, err := newDestinationApplier(managed)
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{"cookies", "local_storage", "indexed_db"} {
			found := false
			for _, surface := range surfaces {
				found = found || surface == required
			}
			if !found {
				t.Fatalf("production destination factory lacks %s", required)
			}
		}
		engine, err := destination.Open(filepath.Join(dir, name), nil, applier)
		if err != nil {
			t.Fatal(err)
		}
		p.entries[name] = &destinationEntry{engine: engine, surfaces: surfaces, config: config.DestinationRef{Name: name, Kind: "live_chrome", Required: true}}
	}
	tracker := protocol.NewSequenceTracker()
	writer := state.NewWriter(filepath.Join(dir, "sink-state.json"))
	st := &state.SinkState{}
	mu := &sync.Mutex{}
	mux := newSinkMux(&config.SinkConfig{}, "fixture", nil, tracker, writer, st, mu, nil, p)
	sink := httptest.NewServer(mux)
	defer sink.Close()
	t.Cleanup(SetResolveSinkURLForTesting(func(_ context.Context, u string) (string, error) { return u, nil }))
	counters := source.Counters{Dir: filepath.Join(dir, "source")}
	sourceCfg := &config.SourceConfig{Browser: config.BrowserRef{Name: "firefox"}}
	targets := []config.SinkTarget{{URL: sink.URL + "/sync", Destination: "personal", Containers: []string{"Personal"}}, {URL: sink.URL + "/sync", Destination: "work", Containers: []string{"Work"}}}
	send := func(generation uint64) {
		t.Helper()
		// Force fixture refresh after deliberate mutation, without waiting for TTL.
		for _, entry := range reader.Catalog().Entries {
			entry.Dirty = true
		}
		capture, err := reader.Read(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		capture.Meta.Generation = generation
		for _, target := range targets {
			env, err := envelopeForDestination(protocol.SyncEnvelope{SourceHostname: "browser-fixture"}, capture, sourceCfg, nil, target)
			if err != nil {
				t.Fatal(err)
			}
			body, _, err := sendNegotiated(ctx, target, "fixture", env, counters, false)
			if err != nil {
				t.Fatal(err)
			}
			var reply syncReply
			if err = json.Unmarshal([]byte(body), &reply); err != nil {
				t.Fatal(err)
			}
			if len(reply.Destinations) != 1 || !reply.Destinations[0].Ready {
				t.Fatalf("not ready: %s", body)
			}
		}
	}
	verify := func(name, want string) {
		t.Helper()
		var got struct {
			Cookie string  `json:"cookie"`
			LS     *string `json:"ls"`
			IDB    *string `json:"idb"`
		}
		expression := `(async()=>{const db=await new Promise((ok,bad)=>{const r=indexedDB.open('auth');r.onsuccess=()=>ok(r.result);r.onerror=()=>bad(r.error)});const idb=await new Promise((ok,bad)=>{const r=db.transaction('sessions').objectStore('sessions').get('current');r.onsuccess=()=>ok(r.result??null);r.onerror=()=>bad(r.error)});db.close();return {cookie:document.cookie,ls:localStorage.getItem('session'),idb}})()`
		if err := chromedp.Run(browsers[name], chromedp.Navigate(origin+"/verify"), chromedp.Evaluate(expression, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })); err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if got.Cookie != "" || got.LS != nil || got.IDB != nil {
				t.Fatalf("%s logout leaked: %+v", name, got)
			}
			return
		}
		if got.Cookie != "session="+want || got.LS == nil || *got.LS != want || got.IDB == nil || *got.IDB != want {
			t.Fatalf("%s account mismatch: %+v want %s", name, got, want)
		}
	}
	send(1)
	verify("personal", "personal-account")
	verify("work", "work-account")
	phase.Store(2)
	await("logout")
	send(2)
	verify("personal", "")
	verify("work", "work-account")
	targets[0].Containers = []string{"Work"}
	targets[1].Containers = []string{"Personal"}
	targets[0], targets[1] = targets[1], targets[0]
	send(3)
	verify("personal", "work-account")
	verify("work", "")
}
