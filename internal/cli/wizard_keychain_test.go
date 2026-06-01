package cli

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestLastJSONLine_FindsLastJSONObject(t *testing.T) {
	stdout := `some log line
agentcookie: strategy partition-list:apple-tool,apple
{"name":"partition-list:apple-tool,apple","success":false}
{"winning_strategy":"trust-list:instacart-pp-cli","overall_success":true,"attempts":[]}`
	got := lastJSONLine(stdout)
	if !strings.HasPrefix(got, `{"winning_strategy"`) {
		t.Errorf("expected last JSON line, got %q", got)
	}
}

func TestLastJSONLine_NoJSON(t *testing.T) {
	stdout := "just some log\nlines here"
	if got := lastJSONLine(stdout); got != "" {
		t.Errorf("no JSON expected, got %q", got)
	}
}

func TestLastJSONLine_TrailingNewlinesIgnored(t *testing.T) {
	stdout := `{"a":1}
{"b":2}


`
	got := lastJSONLine(stdout)
	if got != `{"b":2}` {
		t.Errorf("got %q, want %q", got, `{"b":2}`)
	}
}

func TestBuildStrategies_PrimaryIsDeleteAndRecreate(t *testing.T) {
	s := buildStrategies(nil, false, false)
	if len(s) < 1 {
		t.Fatal("expected at least one default strategy")
	}
	// v0.12: primary strategy moved from -A (any app) to -T (per-binary
	// trust list anchored on the Developer-ID-signed designated
	// requirement). v0.10 -A is no longer in the strategy chain.
	if s[0].name != "delete-and-recreate-with-T" {
		t.Errorf("primary strategy: got %q, want delete-and-recreate-with-T (v0.12 ACL pin via Developer ID signed binary)", s[0].name)
	}
}

func TestBuildStrategies_PartitionListFallbackComesSecond(t *testing.T) {
	s := buildStrategies(nil, false, false)
	if len(s) < 2 {
		t.Fatal("expected partition-list fallback to exist")
	}
	if !strings.HasPrefix(s[1].name, "partition-list:") {
		t.Errorf("second strategy should be partition-list fallback, got %q", s[1].name)
	}
}

func TestBuildStrategies_ExtraBinariesAppearAfterDefaultStrategies(t *testing.T) {
	defaults := buildStrategies(nil, false, false)
	withExtras := buildStrategies([]string{"/Users/me/go/bin/instacart-pp-cli", "/Users/me/go/bin/bird"}, false, false)
	if len(withExtras) != len(defaults)+2 {
		t.Fatalf("expected %d default + 2 extra-binary strategies, got %d", len(defaults), len(withExtras))
	}
	last2 := withExtras[len(defaults):]
	if !strings.Contains(last2[0].name, "trust-list:") || !strings.Contains(last2[0].name, "instacart-pp-cli") {
		t.Errorf("first extra strategy: got %q, want trust-list:instacart-pp-cli", last2[0].name)
	}
	if !strings.Contains(last2[1].name, "trust-list:") || !strings.Contains(last2[1].name, "bird") {
		t.Errorf("second extra strategy: got %q, want trust-list:bird", last2[1].name)
	}
}

// findStrategy returns the strategy with the given name, or nil.
func findStrategy(strategies []kcStrategy, name string) *kcStrategy {
	for i := range strategies {
		if strategies[i].name == name {
			return &strategies[i]
		}
	}
	return nil
}

func TestAnyApp_StrategyLeadsChain(t *testing.T) {
	s := buildStrategies(nil, false, true)
	if len(s) < 1 {
		t.Fatal("expected at least one strategy with --any-app")
	}
	if s[0].name != "delete-and-recreate-with-A" {
		t.Errorf("with --any-app the chain must lead with delete-and-recreate-with-A, got %q", s[0].name)
	}
}

// TestAnyApp_ValuePreservation asserts that when the existing Safe Storage
// value reads as "V", the recreate argv carries -A and -w V (the exact
// value read), NOT a random one, and NOT -T.
func TestAnyApp_ValuePreservation(t *testing.T) {
	const existing = "V"

	prevExec := execSecurityFunc
	prevRead := safeStoragePasswordFunc
	defer func() {
		execSecurityFunc = prevExec
		safeStoragePasswordFunc = prevRead
	}()

	var calls [][]string
	execSecurityFunc = func(args ...string) (string, error) {
		calls = append(calls, args)
		return "", nil
	}
	safeStoragePasswordFunc = func() (string, error) {
		return existing, nil
	}

	s := findStrategy(buildStrategies(nil, false, true), "delete-and-recreate-with-A")
	if s == nil {
		t.Fatal("delete-and-recreate-with-A strategy not present with --any-app")
	}
	detail, err := s.apply()
	if err != nil {
		t.Fatalf("apply returned error: %v", err)
	}

	// A delete then an add must have happened.
	if len(calls) != 2 {
		t.Fatalf("expected exactly 2 security calls (delete + add), got %d: %v", len(calls), calls)
	}
	if calls[0][0] != "delete-generic-password" {
		t.Errorf("first call should be delete-generic-password, got %q", calls[0][0])
	}
	add := calls[1]
	if add[0] != "add-generic-password" {
		t.Errorf("second call should be add-generic-password, got %q", add[0])
	}

	joined := strings.Join(add, " ")
	// Must recreate with -A and the exact value read, not a random one.
	if !containsPair(add, "-w", existing) {
		t.Errorf("recreate must use -w %q (the value read), got argv %v", existing, add)
	}
	if !contains(add, "-A") {
		t.Errorf("recreate must carry -A (any application), got argv %v", add)
	}
	if contains(add, "-T") {
		t.Errorf("recreate must NOT carry -T (trust list) in --any-app mode, got argv %v", add)
	}
	if !strings.Contains(strings.ToLower(detail), "security warning") {
		t.Errorf("detail must carry a security warning, got %q", detail)
	}
	_ = joined
}

// TestAnyApp_RefuseToDeleteWhenUnreadable asserts that when the existing
// value cannot be read, NO delete is attempted and a clear error is
// returned. Deleting then recreating with a different value would
// permanently destroy all existing Chrome cookies.
func TestAnyApp_RefuseToDeleteWhenUnreadable(t *testing.T) {
	prevExec := execSecurityFunc
	prevRead := safeStoragePasswordFunc
	defer func() {
		execSecurityFunc = prevExec
		safeStoragePasswordFunc = prevRead
	}()

	var calls [][]string
	execSecurityFunc = func(args ...string) (string, error) {
		calls = append(calls, args)
		return "", nil
	}
	safeStoragePasswordFunc = func() (string, error) {
		return "", fmt.Errorf("not trusted in ACL")
	}

	s := findStrategy(buildStrategies(nil, false, true), "delete-and-recreate-with-A")
	if s == nil {
		t.Fatal("delete-and-recreate-with-A strategy not present with --any-app")
	}
	_, err := s.apply()
	if err == nil {
		t.Fatal("expected an error when the existing value is unreadable")
	}
	for _, c := range calls {
		if c[0] == "delete-generic-password" {
			t.Fatalf("delete-generic-password MUST NOT be called when the value is unreadable; calls=%v", calls)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("no security calls should happen on a read failure, got %v", calls)
	}
	if !strings.Contains(err.Error(), "runbook-v0.10-keychain-access.md") {
		t.Errorf("error must point the operator at the one-time GUI grant runbook, got %q", err.Error())
	}
}

// TestAnyApp_DefaultUnchanged asserts that without --any-app the chain is
// the existing -T shape: no delete-and-recreate-with-A strategy, and the
// primary recreate argv carries -T, not -A.
func TestAnyApp_DefaultUnchanged(t *testing.T) {
	s := buildStrategies(nil, false, false)
	if findStrategy(s, "delete-and-recreate-with-A") != nil {
		t.Error("without --any-app, delete-and-recreate-with-A must not be present")
	}
	if s[0].name != "delete-and-recreate-with-T" {
		t.Fatalf("default primary should be delete-and-recreate-with-T, got %q", s[0].name)
	}

	prevExec := execSecurityFunc
	defer func() { execSecurityFunc = prevExec }()
	var calls [][]string
	execSecurityFunc = func(args ...string) (string, error) {
		calls = append(calls, args)
		return "", nil
	}
	if _, err := s[0].apply(); err != nil {
		t.Fatalf("default strategy apply error: %v", err)
	}
	var add []string
	for _, c := range calls {
		if c[0] == "add-generic-password" {
			add = c
		}
	}
	if add == nil {
		t.Fatal("expected an add-generic-password call in default strategy")
	}
	if !contains(add, "-T") {
		t.Errorf("default recreate must carry -T, got %v", add)
	}
	if contains(add, "-A") {
		t.Errorf("default recreate must NOT carry -A, got %v", add)
	}
}

func contains(args []string, want string) bool {
	return slices.Contains(args, want)
}

func containsPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

func TestRandomKeychainPassword_NonEmptyAndUnique(t *testing.T) {
	a := randomKeychainPassword()
	b := randomKeychainPassword()
	if a == "" {
		t.Error("password is empty")
	}
	if a == b {
		t.Errorf("two consecutive calls returned same value: %q", a)
	}
	if len(a) < 16 {
		t.Errorf("password unexpectedly short (%d chars): %q", len(a), a)
	}
}

// --- U2: inline one-password partition strategy ---

func TestKeychainStrategyMode_Routing(t *testing.T) {
	if got := keychainStrategyMode(false, false); got != keychainModeInline {
		t.Errorf("default should route inline, got %q", got)
	}
	if got := keychainStrategyMode(true, false); got != keychainModeRecreate {
		t.Errorf("--any-app should route recreate, got %q", got)
	}
	if got := keychainStrategyMode(false, true); got != keychainModeRecreate {
		t.Errorf("--recreate should route recreate, got %q", got)
	}
}

func TestRunInlinePartitionAccess_SetsTeamPartitionNoDelete(t *testing.T) {
	origTeam, origSet, origVerify, origPw, origSec := resolveTeamFunc, setPartitionWithPwFunc, verifyPartitionReadFunc, acquireLoginPasswordFunc, execSecurityFunc
	defer func() {
		resolveTeamFunc, setPartitionWithPwFunc, verifyPartitionReadFunc, acquireLoginPasswordFunc, execSecurityFunc = origTeam, origSet, origVerify, origPw, origSec
	}()

	resolveTeamFunc = func(string) (string, error) { return "NM8VT393AR", nil }
	acquireLoginPasswordFunc = func() (string, error) { return "pw", nil }
	verifyPartitionReadFunc = func() error { return nil }

	var gotPartitions, gotPw string
	setPartitionWithPwFunc = func(partitions, pw string) error {
		gotPartitions, gotPw = partitions, pw
		return nil
	}
	// Guard: the inline path must NEVER shell out a delete-generic-password.
	var sawDelete bool
	execSecurityFunc = func(args ...string) (string, error) {
		if len(args) > 0 && strings.Contains(args[0], "delete") {
			sawDelete = true
		}
		return "", nil
	}

	if err := runInlinePartitionAccess(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPartitions != "apple-tool:,apple:,teamid:NM8VT393AR" {
		t.Errorf("partitions = %q, want team partition", gotPartitions)
	}
	if gotPw != "pw" {
		t.Errorf("password not passed through, got %q", gotPw)
	}
	if sawDelete {
		t.Error("inline path must not delete the Safe Storage item")
	}
}

func TestRunInlinePartitionAccess_EmptyTeamFallsBackToDefaultPartition(t *testing.T) {
	origTeam, origSet, origVerify, origPw := resolveTeamFunc, setPartitionWithPwFunc, verifyPartitionReadFunc, acquireLoginPasswordFunc
	defer func() {
		resolveTeamFunc, setPartitionWithPwFunc, verifyPartitionReadFunc, acquireLoginPasswordFunc = origTeam, origSet, origVerify, origPw
	}()
	resolveTeamFunc = func(string) (string, error) { return "", nil } // ad-hoc binary
	acquireLoginPasswordFunc = func() (string, error) { return "pw", nil }
	verifyPartitionReadFunc = func() error { return nil }
	var gotPartitions string
	setPartitionWithPwFunc = func(partitions, _ string) error { gotPartitions = partitions; return nil }

	if err := runInlinePartitionAccess(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(gotPartitions, "teamid:") {
		t.Errorf("empty team must not add teamid:, got %q", gotPartitions)
	}
	if gotPartitions != "apple-tool:,apple:" {
		t.Errorf("partitions = %q, want default", gotPartitions)
	}
}

func TestRunInlinePartitionAccess_NoPasswordReturnsActionableError(t *testing.T) {
	origTeam, origPw := resolveTeamFunc, acquireLoginPasswordFunc
	defer func() { resolveTeamFunc, acquireLoginPasswordFunc = origTeam, origPw }()
	resolveTeamFunc = func(string) (string, error) { return "NM8VT393AR", nil }
	acquireLoginPasswordFunc = func() (string, error) { return "", errNoInteractivePassword }

	err := runInlinePartitionAccess()
	if err == nil {
		t.Fatal("expected an error when no password is available")
	}
	if !strings.Contains(err.Error(), loginPasswordEnv) {
		t.Errorf("error should name the env override, got %v", err)
	}
}

func TestRunInlinePartitionAccess_VerifyFailureIsNonFatal(t *testing.T) {
	origTeam, origSet, origVerify, origPw := resolveTeamFunc, setPartitionWithPwFunc, verifyPartitionReadFunc, acquireLoginPasswordFunc
	defer func() {
		resolveTeamFunc, setPartitionWithPwFunc, verifyPartitionReadFunc, acquireLoginPasswordFunc = origTeam, origSet, origVerify, origPw
	}()
	resolveTeamFunc = func(string) (string, error) { return "NM8VT393AR", nil }
	acquireLoginPasswordFunc = func() (string, error) { return "pw", nil }
	setPartitionWithPwFunc = func(string, string) error { return nil }
	// Verification read fails (e.g. SSH keychain re-locked) -- must NOT fail the step.
	verifyPartitionReadFunc = func() error { return fmt.Errorf("interaction not allowed") }

	if err := runInlinePartitionAccess(); err != nil {
		t.Errorf("verify failure must be non-fatal once the partition set succeeded, got %v", err)
	}
}

func TestAcquireLoginPassword_EnvOverride(t *testing.T) {
	t.Setenv(loginPasswordEnv, "from-env")
	got, err := acquireLoginPassword()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from-env" {
		t.Errorf("got %q, want from-env", got)
	}
}

func TestAcquireLoginPassword_NonInteractiveNoEnv(t *testing.T) {
	t.Setenv(loginPasswordEnv, "")
	orig := stdinIsTerminal
	defer func() { stdinIsTerminal = orig }()
	stdinIsTerminal = func() bool { return false }
	if _, err := acquireLoginPassword(); err == nil {
		t.Error("expected errNoInteractivePassword when no TTY and no env")
	}
}
