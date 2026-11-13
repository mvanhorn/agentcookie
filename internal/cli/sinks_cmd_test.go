package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

const testSharedSecret = "0123456789abcdef0123456789abcdef"

func writeSourceFile(t *testing.T, configDir, body string) {
	t.Helper()
	writeCLIFile(t, filepath.Join(configDir, "source.yaml"), body)
}

func readSourceFile(t *testing.T, configDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(configDir, "source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// savePeerKey saves a key for peer that differs from every other peer's,
// so a GET /pull signed with one peer's key cannot pass as another's.
func savePeerKey(t *testing.T, configDir, peer string) {
	t.Helper()
	key := sha256.Sum256([]byte(peer))
	if err := keystore.Save(configDir, &keystore.PeerKey{Peer: peer, Key: key[:], PairedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func hasPeerKey(configDir, peer string) bool {
	_, err := keystore.Load(configDir, peer)
	return err == nil
}

const multiSinkSource = `# my sinks
sinks:
  # the Mac mini in the closet
  - url: http://mac-mini.test:9999/sync
    peer: mac-mini
  - url: http://grok-bot.test:9999/sync
    peer: grok-bot
chrome:
  db_path: /tmp/Cookies
`

func TestSinksRemoveByPeerEditsListAndDeletesKey(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, multiSinkSource)
	savePeerKey(t, dir, "mac-mini")
	savePeerKey(t, dir, "grok-bot")

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "grok-bot"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadSource(dir)
	if err != nil {
		t.Fatalf("source.yaml no longer loads: %v\n%s", err, readSourceFile(t, dir))
	}
	if got := cfg.ResolvedSinks(); len(got) != 1 || got[0].Peer != "mac-mini" {
		t.Fatalf("sinks = %+v", got)
	}
	body := readSourceFile(t, dir)
	for _, want := range []string{"# my sinks", "# the Mac mini in the closet", "db_path: /tmp/Cookies"} {
		if !strings.Contains(body, want) {
			t.Fatalf("lost %q:\n%s", want, body)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, "source.yaml")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("source.yaml mode = %v (%v), want 0600", fi.Mode().Perm(), err)
	}
	if hasPeerKey(dir, "grok-bot") {
		t.Fatal("removed sink's key not deleted")
	}
	if !hasPeerKey(dir, "mac-mini") {
		t.Fatal("remaining sink's key deleted")
	}
	for _, want := range []string{"deleted its key", "stay valid", "dev.agentcookie.source"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}

	if err := runSinksRemove(&out, dir, "grok-bot"); err == nil || !strings.Contains(err.Error(), "mac-mini") {
		t.Fatalf("removing a sink that is not listed must fail and name the configured ones, got %v", err)
	}
}

func TestSinksRemoveByURL(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, multiSinkSource)
	savePeerKey(t, dir, "mac-mini")
	savePeerKey(t, dir, "grok-bot")

	if err := runSinksRemove(&bytes.Buffer{}, dir, "http://mac-mini.test:9999/sync"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolvedSinks(); len(got) != 1 || got[0].Peer != "grok-bot" {
		t.Fatalf("sinks = %+v", got)
	}
	if hasPeerKey(dir, "mac-mini") {
		t.Fatal("key not deleted")
	}
}

func TestSinksRemoveRefusesOnlySinkWithoutTeammates(t *testing.T) {
	dir := t.TempDir()
	body := "sink:\n  url: http://mac-mini.test:9999/sync\npeer:\n  hostname: mac-mini\n"
	writeSourceFile(t, dir, body)
	savePeerKey(t, dir, "mac-mini")

	err := runSinksRemove(&bytes.Buffer{}, dir, "mac-mini")
	if err == nil || !strings.Contains(err.Error(), "only sink") {
		t.Fatalf("err = %v, want an only-sink refusal", err)
	}
	if readSourceFile(t, dir) != body {
		t.Fatal("source.yaml changed on a refused remove")
	}
	if !hasPeerKey(dir, "mac-mini") {
		t.Fatal("key deleted on a refused remove")
	}
}

func TestSinksRemoveLegacyOnlySinkWithTeammates(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "sink:\n  url: http://mac-mini.test:9999/sync\nchrome:\n  db_path: /tmp/Cookies\npeer:\n  hostname: mac-mini\n")
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n")
	savePeerKey(t, dir, "mac-mini")

	if err := runSinksRemove(&bytes.Buffer{}, dir, "mac-mini"); err != nil {
		t.Fatal(err)
	}
	body := readSourceFile(t, dir)
	if strings.Contains(body, "sink:") || strings.Contains(body, "peer:") {
		t.Fatalf("legacy sink:/peer: left behind:\n%s", body)
	}
	cfg, err := config.LoadSource(dir)
	if err != nil {
		t.Fatalf("a teammates-only source must still load: %v", err)
	}
	if got := cfg.ResolvedSinks(); len(got) != 0 {
		t.Fatalf("sinks = %+v, want none", got)
	}
	if hasPeerKey(dir, "mac-mini") {
		t.Fatal("key not deleted")
	}
}

func TestSinksRemoveLastListedSinkDropsShadowedLegacySink(t *testing.T) {
	dir := t.TempDir()
	// sinks: wins over the legacy scalar sink:, which would come back into
	// effect if the list were only emptied.
	writeSourceFile(t, dir, "sinks:\n  - url: http://new.test:9999/sync\n    peer: new\nsink:\n  url: http://old.test:9999/sync\npeer:\n  hostname: old\n")
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n")
	savePeerKey(t, dir, "new")

	if err := runSinksRemove(&bytes.Buffer{}, dir, "new"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolvedSinks(); len(got) != 0 {
		t.Fatalf("sinks = %+v, want none (the shadowed legacy sink must not resurface)", got)
	}
}

func TestSinksRemoveKeepsKeyStillInUse(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "sinks:\n  - url: http://grokbot.test:9999/sync\n    peer: grokbot\n  - url: http://mac-mini.test:9999/sync\n    peer: mac-mini\n")
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n")
	savePeerKey(t, dir, "grokbot")
	savePeerKey(t, dir, "mac-mini")

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "grokbot"); err != nil {
		t.Fatal(err)
	}
	if !hasPeerKey(dir, "grokbot") {
		t.Fatal("deleted a key a listed teammate still uses")
	}
	for _, want := range []string{"teammate grokbot", "can keep pulling with that key", "already skips pushes to a listed teammate"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	// SuppressPush already skips a listed teammate, so a running source
	// is not pushing to it and the output must not say it is.
	if strings.Contains(out.String(), "keeps pushing") {
		t.Fatalf("output claims a running source keeps pushing to a listed teammate:\n%s", out.String())
	}

	// This machine's own sink.yaml uses mac-mini's key too.
	writeCLIFile(t, filepath.Join(dir, "sink.yaml"), "listen:\n  addr: 127.0.0.1:9999\npeer:\n  hostname: mac-mini\n")
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n  - name: other\n")
	out.Reset()
	if err := runSinksRemove(&out, dir, "mac-mini"); err != nil {
		t.Fatal(err)
	}
	if !hasPeerKey(dir, "mac-mini") {
		t.Fatal("deleted the key this machine's sink.yaml uses")
	}
	if !strings.Contains(out.String(), "sink.yaml") {
		t.Fatalf("output must say why the key stays:\n%s", out.String())
	}
}

func TestSinksRemovePeerlessSinkDropsSharedSecret(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "sinks:\n  - url: http://legacy.test:9999/sync\n  - url: http://mac-mini.test:9999/sync\n    peer: mac-mini\nsecurity:\n  shared_secret: "+testSharedSecret+"\n")
	savePeerKey(t, dir, "mac-mini")

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "http://legacy.test:9999/sync"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "still accepts it on GET /pull until it restarts") {
		t.Fatalf("a running source still holds the shared secret; output must say to restart:\n%s", out.String())
	}
	cfg, err := config.LoadSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.SharedSecret != "" {
		t.Fatal("shared secret kept after its only sink was removed")
	}
	if strings.Contains(readSourceFile(t, dir), "security:") {
		t.Fatalf("empty security: block left behind:\n%s", readSourceFile(t, dir))
	}
	if !hasPeerKey(dir, "mac-mini") {
		t.Fatal("unrelated key deleted")
	}
}

func TestSinksRemovePeerlessSinkKeepsSharedSecretInUse(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "sinks:\n  - url: http://a.test:9999/sync\n  - url: http://b.test:9999/sync\nsecurity:\n  shared_secret: "+testSharedSecret+"\n")

	if err := runSinksRemove(&bytes.Buffer{}, dir, "http://a.test:9999/sync"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolvedSinks(); len(got) != 1 || got[0].URL != "http://b.test:9999/sync" {
		t.Fatalf("sinks = %+v", got)
	}
	if cfg.Security.SharedSecret != testSharedSecret {
		t.Fatal("shared secret removed while another peerless sink uses it")
	}
}

func TestSinksRemoveRefusesWhenTeammatesDoNotLoad(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, multiSinkSource)
	writeTeammatesFile(t, dir, "teammates: [unclosed\n")
	savePeerKey(t, dir, "mac-mini")
	savePeerKey(t, dir, "grok-bot")

	if err := runSinksRemove(&bytes.Buffer{}, dir, "grok-bot"); err == nil {
		t.Fatal("must refuse while teammates.yaml does not load: it cannot tell whether a teammate uses the key")
	}
	if readSourceFile(t, dir) != multiSinkSource || !hasPeerKey(dir, "grok-bot") {
		t.Fatal("a refused remove changed source.yaml or the key")
	}
}

func TestSinksRemoveRefusesWhenSinkYAMLDoesNotLoad(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, multiSinkSource)
	writeCLIFile(t, filepath.Join(dir, "sink.yaml"), "listen: [unclosed\n")
	savePeerKey(t, dir, "mac-mini")
	savePeerKey(t, dir, "grok-bot")

	if err := runSinksRemove(&bytes.Buffer{}, dir, "grok-bot"); err == nil {
		t.Fatal("must refuse while sink.yaml does not load: it cannot tell whether this machine's sink uses the key")
	}
	if readSourceFile(t, dir) != multiSinkSource || !hasPeerKey(dir, "grok-bot") {
		t.Fatal("a refused remove changed source.yaml or the key")
	}
}

func TestSinksRemoveKeyDeleteFailureLeavesSinkListed(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, multiSinkSource)
	savePeerKey(t, dir, "mac-mini")
	// A non-empty directory where the key file belongs makes the delete fail.
	keyPath, err := keystore.Path(dir, "grok-bot")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(keyPath, "stuck"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := runSinksRemove(&bytes.Buffer{}, dir, "grok-bot"); err == nil {
		t.Fatal("expected the key delete failure to be reported")
	}
	if readSourceFile(t, dir) != multiSinkSource {
		t.Fatal("source.yaml changed even though the key could not be deleted, so a retry could not find the sink")
	}
}

func TestSinksRemoveWithoutSourceConfig(t *testing.T) {
	err := runSinksRemove(&bytes.Buffer{}, t.TempDir(), "mac-mini")
	if err == nil || !strings.Contains(err.Error(), "not set up as a source") {
		t.Fatalf("err = %v", err)
	}
}

func TestSinkStatusesReportKeysAndLastPush(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	writeSourceFile(t, dir, multiSinkSource)
	savePeerKey(t, dir, "mac-mini")
	pushed := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Second)
	if err := state.NewWriter(state.SourcePath(home)).Save(&state.SourceState{
		Role: "source",
		Sinks: []state.SinkPushState{
			{Peer: "mac-mini", URL: "http://mac-mini.test:9999/sync", LastPush: pushed, TotalPushes: 7},
			{Peer: "retired", URL: "http://retired.test:9999/sync", TotalPushes: 3},
		},
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := SinkStatuses(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if r := rows[0]; r.Peer != "mac-mini" || r.Key != sinkKeyPaired || !r.LastPush.Equal(pushed) || r.TotalPushes != 7 {
		t.Fatalf("mac-mini row = %+v", r)
	}
	if r := rows[1]; r.Peer != "grok-bot" || r.Key != sinkKeyMissing || !r.LastPush.IsZero() {
		t.Fatalf("grok-bot row = %+v", r)
	}

	var out bytes.Buffer
	printSinkStatuses(&out, rows, time.Now())
	for _, want := range []string{"mac-mini", "5m ago", "never", "agentcookie pair --as sink --local-name grok-bot"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "retired") {
		t.Fatalf("a sink no longer in source.yaml must not be listed:\n%s", out.String())
	}
}

func pullPeerNames(configDir, legacy string) []string {
	var names []string
	for _, p := range pullAuthPeers(configDir, legacy) {
		if !p.legacy {
			names = append(names, p.name)
		}
	}
	return names
}

// pullStatus sends one signed GET /pull to h and returns the status code.
func pullStatus(t *testing.T, h http.Handler, secret string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/pull", nil)
	if err := transport.SignRequest(req, secret, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestSinksRemoveRevokesPullForRemovedPeer(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, multiSinkSource)
	savePeerKey(t, dir, "mac-mini")
	savePeerKey(t, dir, "grok-bot")

	if err := runSinksRemove(&bytes.Buffer{}, dir, "grok-bot"); err != nil {
		t.Fatal(err)
	}
	names := pullPeerNames(dir, "")
	if slices.Contains(names, "grok-bot") {
		t.Fatalf("GET /pull still accepts the removed sink's key: %v", names)
	}
	if !slices.Contains(names, "mac-mini") {
		t.Fatalf("GET /pull lost the remaining sink's key: %v", names)
	}
}

const pairedSinksWithSecret = "sinks:\n  - url: http://a.test:9999/sync\n    peer: a\n  - url: http://b.test:9999/sync\n    peer: b\nsecurity:\n  shared_secret: " + testSharedSecret + "\n"

func TestSinksRemovePeeredSinkDropsUnusedSharedSecret(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, pairedSinksWithSecret)
	savePeerKey(t, dir, "a")
	savePeerKey(t, dir, "b")
	bKey, err := keystore.Load(dir, "b")
	if err != nil {
		t.Fatal(err)
	}
	// The handler a running `source --watch` built at startup, holding the
	// shared secret it read then.
	cache := newPullCache()
	cache.Store([]byte(`{"protocol_version":2}`))
	running := newPullMux(dir, testSharedSecret, cache, nil)

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "b"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.SharedSecret != "" {
		t.Fatalf("shared secret kept although no remaining sink needs it:\n%s", readSourceFile(t, dir))
	}
	if !strings.Contains(out.String(), "still accepts it on GET /pull until it restarts") {
		t.Fatalf("output must say a running source accepts the dropped secret until it restarts:\n%s", out.String())
	}
	for _, wrong := range []string{"already refuses it", "shared_secret stays"} {
		if strings.Contains(out.String(), wrong) {
			t.Fatalf("output claims pull access already ended (%q):\n%s", wrong, out.String())
		}
	}

	// The running source still serves the old shared secret: only a
	// restart drops it. The removed sink's own key is refused at once.
	if code := pullStatus(t, running, testSharedSecret); code != http.StatusOK {
		t.Fatalf("running source refused the old shared secret (status %d); the restart-required note would be wrong", code)
	}
	if code := pullStatus(t, running, string(bKey.Key)); code != http.StatusUnauthorized {
		t.Fatalf("running source accepted the removed sink's key: status %d", code)
	}
	if code := pullStatus(t, newPullMux(dir, cfg.Security.SharedSecret, cache, nil), testSharedSecret); code != http.StatusUnauthorized {
		t.Fatalf("a restarted source still accepts the dropped shared secret: status %d", code)
	}
}

func TestSinksRemoveKeepsSharedSecretWhenRemainingPeerKeyMissing(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, pairedSinksWithSecret)
	savePeerKey(t, dir, "b")

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "b"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.SharedSecret != testSharedSecret {
		t.Fatal("shared secret dropped while sink a has no key here and may still fall back to it")
	}
	if !strings.Contains(out.String(), "until the secret is rotated") {
		t.Fatalf("output must say the removed machine can still pull with the kept secret:\n%s", out.String())
	}
}

func TestSinksRemovePeeredSinkKeepsSharedSecretForPeerlessSink(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "sinks:\n  - url: http://legacy.test:9999/sync\n  - url: http://a.test:9999/sync\n    peer: a\n  - url: http://b.test:9999/sync\n    peer: b\nsecurity:\n  shared_secret: "+testSharedSecret+"\n")
	savePeerKey(t, dir, "a")
	savePeerKey(t, dir, "b")

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "b"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.SharedSecret != testSharedSecret {
		t.Fatal("shared secret dropped while a peerless sink still uses it")
	}
	for _, want := range []string{"http://legacy.test:9999/sync", "can keep pulling with it", "until the secret is rotated"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if hasPeerKey(dir, "b") {
		t.Fatal("removed sink's key not deleted")
	}
}

func TestSinksRemoveKeyKeptForSinkYAMLSaysPullContinues(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "sinks:\n  - url: http://a.test:9999/sync\n    peer: a\n  - url: http://b.test:9999/sync\n    peer: b\n")
	writeCLIFile(t, filepath.Join(dir, "sink.yaml"), "listen:\n  addr: 127.0.0.1:9999\npeer:\n  hostname: b\n")
	savePeerKey(t, dir, "a")
	savePeerKey(t, dir, "b")

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "b"); err != nil {
		t.Fatal(err)
	}
	if !hasPeerKey(dir, "b") {
		t.Fatal("deleted the key this machine's sink.yaml uses")
	}
	if !slices.Contains(pullPeerNames(dir, ""), "b") {
		t.Fatal("GET /pull no longer accepts b's key, but the test expects it kept")
	}
	for _, want := range []string{"can keep pulling", "rotated", "re-pair"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	// A restart does not take the kept key away, so the output must not
	// suggest one ends access.
	for _, wrong := range []string{"until it restarts", "restart the source now"} {
		if strings.Contains(strings.ToLower(out.String()), wrong) {
			t.Fatalf("output implies a restart revokes the kept key (%q):\n%s", wrong, out.String())
		}
	}
}

func TestSinksRemoveLastListedSinkDeletesShadowedLegacyPeerKey(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "sinks:\n  - url: http://new.test:9999/sync\n    peer: new\nsink:\n  url: http://c.test:9999/sync\npeer:\n  hostname: c\n")
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n")
	savePeerKey(t, dir, "new")
	savePeerKey(t, dir, "c")

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "new"); err != nil {
		t.Fatal(err)
	}
	if hasPeerKey(dir, "c") {
		t.Fatal("keys/c.json left behind after its legacy peer: was removed; GET /pull would still accept it")
	}
	if slices.Contains(pullPeerNames(dir, ""), "c") {
		t.Fatal("GET /pull still accepts the shadowed legacy peer's key")
	}
	if !strings.Contains(out.String(), "keys/c.json") {
		t.Fatalf("output must mention the legacy peer key it deleted:\n%s", out.String())
	}
}

func TestSinkStatusesJSONIsArrayWhenEmpty(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	writeSourceFile(t, dir, "chrome:\n  db_path: /tmp/Cookies\n")
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n")

	rows, err := SinkStatuses(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[]" {
		t.Fatalf("sinks list --json = %s, want []", data)
	}
}

func TestSinksRemoveKeepsLeadingComments(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "# top of the file\n# about these sinks\nsinks:\n  - url: http://new.test:9999/sync\n    peer: new\nchrome:\n  db_path: /tmp/Cookies\n")
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n")
	savePeerKey(t, dir, "new")

	if err := runSinksRemove(&bytes.Buffer{}, dir, "new"); err != nil {
		t.Fatal(err)
	}
	body := readSourceFile(t, dir)
	for _, want := range []string{"# top of the file", "# about these sinks", "db_path: /tmp/Cookies"} {
		if !strings.Contains(body, want) {
			t.Fatalf("lost %q:\n%s", want, body)
		}
	}
	if _, err := config.LoadSource(dir); err != nil {
		t.Fatalf("source.yaml no longer loads: %v\n%s", err, body)
	}
}

func TestSinksRemoveKeepsKeyFileSharedBySanitizedName(t *testing.T) {
	// "mini box" and "mini_box" sanitize to the same keys/mini_box.json.
	dir := t.TempDir()
	writeSourceFile(t, dir, "sinks:\n  - url: http://a.test:9999/sync\n    peer: mini box\n  - url: http://b.test:9999/sync\n    peer: mini_box\n")
	savePeerKey(t, dir, "mini_box")

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "mini box"); err != nil {
		t.Fatal(err)
	}
	if !hasPeerKey(dir, "mini_box") {
		t.Fatal("deleted the key file a remaining sink still uses")
	}
	if !strings.Contains(out.String(), "can keep pulling") {
		t.Fatalf("output must say the kept key still works:\n%s", out.String())
	}
}

func TestSinksRemoveKeepsKeyWhenAnotherFileHoldsSameBytes(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, multiSinkSource)
	savePeerKey(t, dir, "mac-mini")
	k, err := keystore.Load(dir, "mac-mini")
	if err != nil {
		t.Fatal(err)
	}
	if err := keystore.Save(dir, &keystore.PeerKey{Peer: "grok-bot", Key: k.Key, PairedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "grok-bot"); err != nil {
		t.Fatal(err)
	}
	if !hasPeerKey(dir, "grok-bot") {
		t.Fatal("deleted a key file although another file holds the same key, so GET /pull would still accept it")
	}
	if strings.Contains(out.String(), "refuses that key now") || !strings.Contains(out.String(), "holds the same key") {
		t.Fatalf("output must not claim the key is refused while another file holds it:\n%s", out.String())
	}
}

func TestSinksRemoveRefusesYAMLAnchors(t *testing.T) {
	dir := t.TempDir()
	body := "sinks:\n  - &first\n    url: http://a.test:9999/sync\n    peer: a\n  - url: http://b.test:9999/sync\n    peer: b\n  - *first\n"
	writeSourceFile(t, dir, body)
	savePeerKey(t, dir, "a")
	savePeerKey(t, dir, "b")

	var out bytes.Buffer
	err := runSinksRemove(&out, dir, "a")
	if err == nil || !strings.Contains(err.Error(), "anchors") {
		t.Fatalf("want an anchors refusal, got %v", err)
	}
	if !hasPeerKey(dir, "a") || readSourceFile(t, dir) != body {
		t.Fatal("refusal must leave the key and source.yaml untouched")
	}
}

func TestSinksRemoveSharedSecretWithTeammatesSaysPullRefusesIt(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "sink:\n  url: http://legacy.test:9999/sync\nsecurity:\n  shared_secret: "+testSharedSecret+"\n")
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n")

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "http://legacy.test:9999/sync"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already refuses it while") {
		t.Fatalf("output must say GET /pull already refuses the secret while teammates are listed:\n%s", out.String())
	}
	if strings.Contains(out.String(), "still accepts it on GET /pull") {
		t.Fatalf("output claims GET /pull accepts the secret although teammates are listed:\n%s", out.String())
	}
}

func TestSinksRemoveKeptLegacyPeerKeySaysPullContinues(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "sinks:\n  - url: http://new.test:9999/sync\n    peer: new\nsink:\n  url: http://c.test:9999/sync\npeer:\n  hostname: c\n")
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n")
	writeCLIFile(t, filepath.Join(dir, "sink.yaml"), "listen:\n  addr: 127.0.0.1:9999\npeer:\n  hostname: c\n")
	savePeerKey(t, dir, "new")
	savePeerKey(t, dir, "c")

	var out bytes.Buffer
	if err := runSinksRemove(&out, dir, "new"); err != nil {
		t.Fatal(err)
	}
	if !hasPeerKey(dir, "c") {
		t.Fatal("deleted the legacy key this machine's sink.yaml uses")
	}
	if !strings.Contains(out.String(), "so c can keep pulling with that key") {
		t.Fatalf("output must warn the kept legacy key still pulls:\n%s", out.String())
	}
}
