package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/pkg/agentcookiesecret"
)

// withStdin feeds data to os.Stdin for the duration of the test.
func withStdin(t *testing.T, data string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(data); err != nil {
		t.Fatal(err)
	}
	w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; r.Close() })
}

func setSecret(t *testing.T, cli, key, stdin string) (string, error) {
	t.Helper()
	withStdin(t, stdin)
	out := &bytes.Buffer{}
	cmd := secretSetCmd
	cmd.SetOut(out)
	cmd.SetErr(out)
	err := runSecretSet(cmd, []string{cli, key})
	return out.String(), err
}

func getSecret(t *testing.T, cli, key string) string {
	t.Helper()
	buf := &bytes.Buffer{}
	cmd := secretGetCmd
	cmd.SetOut(buf)
	if err := runSecretGet(cmd, []string{cli, key}); err != nil {
		t.Fatalf("secret get %s %s: %v", cli, key, err)
	}
	return buf.String()
}

func busPath(home, cli string) string {
	return filepath.Join(home, ".agentcookie", "secrets", cli, "secrets.env")
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSecretSet_RefusesNewlineWithoutEchoOrWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSecrets(t, home, "c", "A=1\n")
	out, err := setSecret(t, "c", "K", "a\nEVIL=x")
	if err == nil {
		t.Fatal("a value with a newline must be refused")
	}
	if !strings.Contains(err.Error(), "c/K") || strings.Contains(err.Error()+out, "EVIL") {
		t.Fatalf("error should name c/K and not the value: %v (output %q)", err, out)
	}
	if got := readFileString(t, busPath(home, "c")); got != "A=1\n" {
		t.Fatalf("file changed: %q", got)
	}
}

func TestSecretSet_TrimsCRLF(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := setSecret(t, "c", "K", "tok\r\n"); err != nil {
		t.Fatal(err)
	}
	if got := getSecret(t, "c", "K"); got != "tok" {
		t.Fatalf("got %q, want tok", got)
	}
}

func TestSecretSet_QuotedValuesRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	values := map[string]string{"WIN": `C:\x\`, "JSONTOK": `"tok"`, "SP": "a b", "MIX": `x"y'z`}
	for k, v := range values {
		if _, err := setSecret(t, "c", k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	lib, err := agentcookiesecret.Load("c")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range values {
		if got := getSecret(t, "c", k); got != v {
			t.Errorf("secret get %s = %q, want %q", k, got, v)
		}
		if lib[k] != v {
			t.Errorf("agentcookiesecret.Load %s = %q, want %q", k, lib[k], v)
		}
	}
}

func TestSecretGet_JoinsContinuationLikeTheReader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSecrets(t, home, "c", "K=a\\\nB=2\n")
	if got := getSecret(t, "c", "K"); got != "aB=2" {
		t.Fatalf("got %q, want aB=2", got)
	}
	lib, err := agentcookiesecret.Load("c")
	if err != nil || lib["K"] != "aB=2" {
		t.Fatalf("reader got %q (%v)", lib["K"], err)
	}
}

func TestSecretSet_RefusesToRewriteFilesItCannotReproduce(t *testing.T) {
	cases := map[string]struct{ content, line string }{
		"malformed":    {"A=1\nNOT_A_PAIR\n", "line 2"},
		"old raw \\":   {"K=C:\\x\\\nL=2\n", "line 1"},
		"bad key name": {"A=1\nsk-live+X=1\n", "line 2"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			writeSecrets(t, home, "c", tc.content)
			_, err := setSecret(t, "c", "M", "v")
			if err == nil || !strings.Contains(err.Error(), tc.line) || !strings.Contains(err.Error(), "secret rm c") {
				t.Fatalf("want a refusal naming %s and secret rm, got %v", tc.line, err)
			}
			if strings.Contains(err.Error(), "sk-live") || strings.Contains(err.Error(), `C:\x`) {
				t.Fatalf("refusal echoes file text: %v", err)
			}
			if got := readFileString(t, busPath(home, "c")); got != tc.content {
				t.Fatalf("file changed: %q", got)
			}
			rm := secretRmCmd
			rm.SetOut(&bytes.Buffer{})
			if err := runSecretRm(rm, []string{"c", "A"}); err == nil {
				t.Fatal("secret rm KEY should refuse the same file")
			}
			if err := runSecretRm(rm, []string{"c"}); err != nil {
				t.Fatalf("secret rm c should still work: %v", err)
			}
		})
	}
}

func TestSecretSet_MissingFileStartsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := setSecret(t, "c", "K", "v"); err != nil {
		t.Fatal(err)
	}
	if got := getSecret(t, "c", "K"); got != "v" {
		t.Fatalf("got %q", got)
	}
}

func importFrom(t *testing.T, cli, src string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := secretImportFromCmd
	cmd.SetOut(out)
	cmd.SetErr(out)
	secretImportAs = cli
	t.Cleanup(func() { secretImportAs = "" })
	err := runSecretImportFrom(cmd, []string{src})
	return out.String(), err
}

func TestSecretImportFrom_SkipsValueTheBusCannotCarry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(home, "auth.json")
	os.WriteFile(src, []byte(`{"access_token":"AT","private_key":"-----BEGIN-----\nabc\n-----END-----\n"}`), 0o600)
	out, err := importFrom(t, "c", src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "skipped PRIVATE_KEY") || strings.Contains(out, "BEGIN") {
		t.Fatalf("note should name the skipped key without its value: %q", out)
	}
	if got := getSecret(t, "c", "OAUTH_BEARER"); got != "AT" {
		t.Fatalf("the rest should import, got %q", got)
	}
}

func TestSecretImportFrom_EnvInvalidKeyStoredByLineNumber(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(home, "old.env")
	os.WriteFile(src, []byte("A=1\n# comment\nsk-live-abc==\nLONG=part1\\\npart2\n"), 0o600)
	out, err := importFrom(t, "c", src)
	if err != nil {
		t.Fatal(err)
	}
	file := readFileString(t, busPath(home, "c"))
	list := &bytes.Buffer{}
	lc := secretListCmd
	lc.SetOut(list)
	if err := runSecretList(lc, nil); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"sk-live-abc", "sk_live_abc"} {
		if strings.Contains(out+file+list.String(), leak) {
			t.Fatalf("%q leaked: note %q, file %q, list %q", leak, out, file, list.String())
		}
	}
	if got := getSecret(t, "c", "_unknown_line3"); got != "=" {
		t.Fatalf("_unknown_line3 = %q, want =", got)
	}
	if !strings.Contains(out, "line 3") {
		t.Fatalf("note should name line 3: %q", out)
	}
	if got := getSecret(t, "c", "LONG"); got != "part1part2" {
		t.Fatalf("continuation not joined: %q", got)
	}
}

func TestSecretEnv_QuotesForShellAndReaders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := setSecret(t, "c", "Q", `"tok"`); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "pwned")
	if _, err := setSecret(t, "c", "CMD", "$(touch "+marker+")"); err != nil {
		t.Fatal(err)
	}
	out := envLines(t, "c")
	if !strings.Contains(out, `Q='"tok"'`+"\n") {
		t.Fatalf("want Q='\"tok\"', got %q", out)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	got, err := exec.Command(sh, "-c", `eval "$1"; printf '%s|%s' "$CMD" "$Q"`, "sh", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := "$(touch " + marker + `)|"tok"`; string(got) != want {
		t.Fatalf("eval read %q, want %q", got, want)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("eval of secret env output ran a command")
	}
}

func TestSecretEnv_LeavesOutValueNoQuotingServesAndFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSecrets(t, home, "c", "OK=1\nBAD=\"it's $HOME\"\n")
	buf, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := secretEnvCmd
	cmd.SetOut(buf)
	cmd.SetErr(errBuf)
	t.Cleanup(func() { cmd.SetErr(nil) })
	err := runSecretEnv(cmd, []string{"c"})
	if err == nil || !strings.Contains(err.Error(), "BAD") {
		t.Fatalf("leaving a key out must fail and name it, got %v", err)
	}
	if strings.Contains(err.Error()+errBuf.String(), "HOME") {
		t.Fatalf("value leaked: %v / %q", err, errBuf.String())
	}
	if buf.String() != "OK=1\n" || !strings.Contains(errBuf.String(), "BAD") {
		t.Fatalf("stdout %q, stderr %q", buf.String(), errBuf.String())
	}
}

func TestSecretEnv_NoAliasesPrintsValuesVerbatim(t *testing.T) {
	// The released Python reader parses this output and keeps each value
	// as printed, so it must not gain shell quoting.
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSecrets(t, home, "c", "Q='\"tok\"'\nSP='a b'\nBAD=\"it's $HOME\"\n")
	secretEnvNoAliases = true
	t.Cleanup(func() { secretEnvNoAliases = false })
	want := "BAD=it's $HOME\nQ=\"tok\"\nSP=a b\n"
	if got := envLines(t, "c"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSecretAlias_RefusesUnreproducibleAliasesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSecrets(t, home, "c", "K=v\n")
	path := aliasesPath("c")
	const content = "A=B\nNOT_A_PAIR\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := secretAliasCmd
	alias.SetOut(&bytes.Buffer{})
	err := runSecretAlias(alias, []string{"c", "X", "K"})
	if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "delete that file") {
		t.Fatalf("want a refusal naming line 2 and the alias file, got %v", err)
	}
	if strings.Contains(err.Error(), "secret rm") {
		t.Fatalf("recovery for aliases.env must not suggest removing every secret: %v", err)
	}
	if got := readFileString(t, path); got != content {
		t.Fatalf("aliases.env changed: %q", got)
	}
}

func TestNewCLINameRefusesDoubleHyphen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := setSecret(t, "a--b", "K", "v"); err == nil || !strings.Contains(err.Error(), "single hyphens") {
		t.Fatalf("secret set a--b: want the naming rule, got %v", err)
	}
	src := filepath.Join(home, "x.env")
	os.WriteFile(src, []byte("A=1\n"), 0o600)
	if _, err := importFrom(t, "a--b", src); err == nil {
		t.Fatal("import-from --as a--b should be refused")
	}
	alias := secretAliasCmd
	alias.SetOut(&bytes.Buffer{})
	if err := runSecretAlias(alias, []string{"a--b", "X", "Y"}); err == nil {
		t.Fatal("secret alias a--b should be refused")
	}

	// An existing a--b entry still reads, lists and removes.
	writeSecrets(t, home, "a--b", "K=v\n")
	if got := getSecret(t, "a--b", "K"); got != "v" {
		t.Fatalf("secret get a--b = %q", got)
	}
	if lib, err := agentcookiesecret.Load("a--b"); err != nil || lib["K"] != "v" {
		t.Fatalf("agentcookiesecret.Load(a--b) = %q, %v", lib["K"], err)
	}
	list := &bytes.Buffer{}
	lc := secretListCmd
	lc.SetOut(list)
	if err := runSecretList(lc, nil); err != nil || !strings.Contains(list.String(), "a--b") {
		t.Fatalf("secret list: %q, %v", list.String(), err)
	}
	rm := secretRmCmd
	rm.SetOut(&bytes.Buffer{})
	if err := runSecretRm(rm, []string{"a--b"}); err != nil {
		t.Fatalf("secret rm a--b: %v", err)
	}
}
