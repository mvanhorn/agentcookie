package e2e

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/mvanhorn/agentcookie/internal/cli"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/destination"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"github.com/mvanhorn/agentcookie/internal/storagecodec"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

//go:embed testdata/gecko-storage-page.js
var storageFixturePage string

// TestGeckoStorageSourceSinkChrome is deliberately opt-in and hard-fails missing
// prerequisites in required mode. It uses real headed Gecko, the actual broker/
// companion source, encrypted v3 HTTP sink, and an owned throwaway Chrome.
func TestGeckoStorageSourceSinkChrome(t *testing.T) {
	if os.Getenv("AGENTCOOKIE_GECKO_STORAGE_REQUIRED") != "1" {
		t.Skip("combined storage browser gate not requested")
	}
	for _, key := range []string{"AGENTCOOKIE_GECKO_BINARY", "AGENTCOOKIE_BINARY", "AGENTCOOKIE_CHROME_BINARY"} {
		if os.Getenv(key) == "" {
			t.Fatalf("required gate missing %s", key)
		}
	}
	if os.Getenv("AGENTCOOKIE_GECKO_HEADED") != "1" {
		t.Fatal("combined source gate requires headed Gecko")
	}
	t.Setenv("AGENTCOOKIE_GECKO_REQUIRED", "1")
	t.Setenv("AGENTCOOKIE_E2E_REQUIRED", "1")
	g := NewGecko(t)
	var mu sync.Mutex
	seeded := map[uint32]bool{}
	hostile := map[uint32]bool{}
	loggedOut := false
	command := ""
	failure := ""
	var containers []uint32
	var paths []string
	activeReads := map[int]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/report":
			var report struct {
				Kind       string
				Container  uint32
				Containers []uint32
				Error      string
				ID         int
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&report); err != nil {
				failure = err.Error()
				return
			}
			switch report.Kind {
			case "seed":
				seeded[report.Container] = true
			case "ready":
				containers = report.Containers
			case "hostile":
				hostile[report.Container] = true
			case "logout":
				loggedOut = true
			case "error":
				failure = report.Error
			case "created":
				activeReads[report.ID] = true
			case "removed":
				delete(activeReads, report.ID)
			}
		case "/state":
			_ = json.NewEncoder(w).Encode(map[string]any{"seeded": seeded, "command": command})
		case "/sw.js":
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprint(w, `self.addEventListener('install',e=>e.waitUntil(self.skipWaiting()));self.addEventListener('activate',e=>e.waitUntil(self.clients.claim()));self.addEventListener('fetch',e=>{if(e.request.url.includes('/.agentcookie-'))e.respondWith(new Response('<title>hostile worker</title>',{headers:{'Content-Type':'text/html'}}))});`)
		case "/page.js":
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprint(w, storageFixturePage)
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<!doctype html><title>storage fixture</title><script src="/page.js"></script>`)
		}
	}))
	defer server.Close()
	origin, _ := json.Marshal(server.URL)
	g.InstallFixtureExtension(`(async()=>{const origin=` + string(origin) + `;let reports=Promise.resolve();const report=o=>reports=reports.then(()=>fetch(origin+'/report',{method:'POST',body:JSON.stringify(o)}));try{
 const ids=[],tabs=[];for(let n=0;n<2;n++){const identity=await browser.contextualIdentities.create({name:'Storage '+n,color:n?'red':'blue',icon:'circle'});const id=Number(identity.cookieStoreId.split('-').pop());ids.push(id);const tab=await browser.tabs.create({url:origin+'/seed?container='+id,active:false,cookieStoreId:identity.cookieStoreId});for(let i=0;i<200;i++){const state=await(await fetch(origin+'/state')).json();if(state.seeded[id])break;if(i===199)throw Error('seed timeout');await new Promise(r=>setTimeout(r,100))}await browser.tabs.remove(tab.id);tabs.push(await browser.tabs.create({url:origin+'/hostile?container='+id,active:false,cookieStoreId:identity.cookieStoreId}))}
 const tracked=new Set();browser.tabs.onCreated.addListener(tab=>{tracked.add(tab.id);report({kind:'created',id:tab.id})});browser.tabs.onRemoved.addListener(id=>{if(tracked.delete(id))report({kind:'removed',id})});
 let done=false;setInterval(async()=>{try{const state=await(await fetch(origin+'/state')).json();if(state.command==='logout'&&!done){done=true;await browser.tabs.update(tabs[0].id,{url:origin+'/logout?container='+ids[0]})}}catch(error){report({kind:'error',error:String(error)})}},200);
 await report({kind:'ready',containers:ids});}catch(error){await report({kind:'error',error:String(error)})}})();`)
	wait := func(label string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			err := failure
			ok := condition()
			mu.Unlock()
			if err != "" {
				t.Fatal(label, err)
			}
			if ok {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal(label + " timed out")
	}
	wait("seed two containers and close seed tabs", func() bool { return len(containers) == 2 && hostile[containers[0]] && hostile[containers[1]] })
	mu.Lock()
	first, second := containers[0], containers[1]
	paths = nil
	mu.Unlock()
	opts := g.ReaderOptions()
	opts.Containers = []string{strconv.Itoa(int(first))}
	opts.StoragePreferences = [][]string{nil, {strconv.Itoa(int(second))}}
	opts.StorageSurfaces = &storagecodec.ReadSurfaces{LocalStorage: true, IndexedDB: true}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reader, err := geckoreader.Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	policy := &config.Blocklist{Policy: "blocklist"}
	snapshot, err := reader.Read(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	union := snapshot.Storage
	if capture, ok := snapshot.RoutingData.(*geckoreader.Capture); ok && capture.Storage != nil {
		union = capture.Storage
	}
	if union == nil || len(union.Origins) != 2 {
		t.Fatalf("two-container storage capture missing: %+v", union)
	}
	var wanted protocol.StorageOrigin
	for _, o := range union.Origins {
		if o.LocalStorageStatus.State != protocol.Complete || o.IndexedDBStatus.State != protocol.Complete {
			t.Fatalf("combined read incomplete: %+v", o)
		}
		if o.Container == first {
			wanted = o
		}
	}
	if len(snapshot.Cookies) != 0 || len(wanted.LocalStorage) != 2 || len(wanted.IndexedDB) != 1 {
		t.Fatalf("fixture is not storage-only or data missing: %+v", snapshot)
	}
	mu.Lock()
	for _, path := range paths {
		if strings.Contains(path, "/.agentcookie-read/") {
			t.Errorf("intercepted source request escaped to server: %s", path)
		}
	}
	mu.Unlock()
	wait("hidden source tabs removed", func() bool { return len(activeReads) == 0 })
	h := New(t)
	chrome := h.Browser()
	if err := chromedp.Run(chrome, chromedp.Navigate(server.URL+"/hostile"), chromedp.Poll(`globalThis.fixtureReady===true`, nil)); err != nil {
		t.Fatal(err)
	}
	before, err := chromedp.Targets(chrome)
	if err != nil {
		t.Fatal(err)
	}
	base := destination.NewChromeApplier(h.Endpoint)
	defer base.Close()
	adapter, err := storagecodec.NewAdapter(h.Endpoint, base)
	if err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(h.Home, "config")
	if err := os.WriteFile(filepath.Join(configDir, "blocklist.yaml"), []byte("version: 1\npolicy: blocklist\ndomains: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const secret = "synthetic-storage-transport-secret"
	mux, restore, err := cli.HermeticStorageSinkForTesting(configDir, secret, adapter)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	sink := httptest.NewServer(mux)
	defer sink.Close()
	send := func(snap *source.Snapshot, generation uint64) {
		t.Helper()
		meta := snap.Meta
		meta.Generation = generation
		storage := &protocol.StorageRecords{Version: 1}
		for _, o := range snap.Storage.Origins {
			if o.Container == first {
				storage.Origins = append(storage.Origins, o)
			}
		}
		env := protocol.SyncEnvelope{ProtocolVersion: 3, SourceHostname: "fixture-source", SourceBrowser: "firefox", ReaderMode: "live", Destination: "fixture", Sequence: int64(generation), Snapshot: &meta, Storage: storage}
		body, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		body, err = transport.SealWithSecret(body, secret)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(sink.URL+"/sync", "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		result, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		var reply struct {
			Status       string
			Destinations []destination.Status
		}
		if err := json.Unmarshal(result, &reply); err != nil || response.StatusCode != http.StatusOK || len(reply.Destinations) != 1 || !reply.Destinations[0].Ready {
			t.Fatalf("actual sink not ready: %d %s (%v)", response.StatusCode, result, err)
		}
	}
	// Explicitly select one account from the source's authorized capture.
	snapshot.Storage = &protocol.StorageRecords{Version: 1, Origins: []protocol.StorageOrigin{wanted}}
	send(snapshot, 1)
	var actual struct {
		LS      string
		IDB     string
		Rich    json.RawMessage
		Version uint64
	}
	script := storagecodec.CodecJS + `; (async()=>{const db=await new Promise((resolve,reject)=>{const r=indexedDB.open('fixture-auth');r.onsuccess=()=>resolve(r.result);r.onerror=()=>reject(r.error)});const get=k=>new Promise((resolve,reject)=>{const r=db.transaction('tokens').objectStore('tokens').get(k);r.onsuccess=()=>resolve(r.result);r.onerror=()=>reject(r.error)});const out={LS:localStorage.getItem('auth'),IDB:await get('auth'),Rich:AgentCookieCodec.encode(await get('rich')),Version:db.version};db.close();return out})()`
	if err := chromedp.Run(chrome, chromedp.Evaluate(script, &actual, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true).WithReturnByValue(true)
	})); err != nil {
		t.Fatal(err)
	}
	if actual.LS != "token-"+strconv.Itoa(int(first)) || actual.IDB != actual.LS || actual.Version != 1 {
		t.Fatalf("storage-only fresh-v1 login failed: %+v", actual)
	}
	var expected json.RawMessage
	for _, store := range wanted.IndexedDB[0].Stores {
		for _, record := range store.Records {
			if string(record.Key) == `{"t":"str","v":"rich"}` {
				expected = record.Value
			}
		}
	}
	if len(expected) == 0 || destination.Hash(actual.Rich) != destination.Hash(expected) {
		t.Fatal("full structured-clone record did not round trip")
	}
	if err := chromedp.Run(chrome, chromedp.Evaluate(`localStorage.setItem('unowned','keep')`, nil)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	command = "logout"
	mu.Unlock()
	wait("source logout", func() bool { return loggedOut })
	reader.Catalog().Observe("127.0.0.1", first)
	next, err := reader.Read(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	send(next, 2)
	var remaining []string
	if err := chromedp.Run(chrome, chromedp.Evaluate(`(async()=>{const db=await new Promise(r=>{const q=indexedDB.open('fixture-auth');q.onsuccess=()=>r(q.result)});const count=await new Promise(r=>{const q=db.transaction('tokens').objectStore('tokens').count();q.onsuccess=()=>r(q.result)});db.close();return [localStorage.getItem('auth')||'',localStorage.getItem('unowned'),String(count)]})()`, &remaining, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true).WithReturnByValue(true)
	})); err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 3 || remaining[0] != "" || remaining[1] != "keep" || remaining[2] != "0" {
		t.Fatalf("logout failed or unowned LS removed: %v", remaining)
	}
	after, err := chromedp.Targets(chrome)
	if err != nil || len(before) != len(after) {
		t.Fatal("destination helper target leak", len(before), len(after), err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, path := range paths {
		if strings.Contains(path, "/.agentcookie-") {
			t.Errorf("helper navigation reached server: %s", path)
		}
	}
}
