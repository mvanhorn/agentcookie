package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mvanhorn/agentcookie/internal/cli/httpserver"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/transport"
	"github.com/mvanhorn/agentcookie/internal/tsclient"
)

// normalizePullURL accepts a host, host:port, or full URL and returns a
// GET /pull URL. Bare hostnames default to port 9998 (the source pair/pull
// listener). No Muse-specific hosts are hardcoded.
func normalizePullURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("--pull-from is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse --pull-from: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("--pull-from scheme must be http or https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("--pull-from missing host")
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), defaultPullListenPort)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/pull"
	}
	return u.String(), nil
}

func runSinkPullLoop(
	ctx context.Context,
	client *http.Client,
	rt *sinkRuntime,
	transportSecret string,
	pullURL string,
	interval time.Duration,
) error {
	apply := rt.applier(ctx, transportSecret)
	return pullLoop(ctx, true, func() time.Duration { return interval }, func(ctx context.Context) error {
		_, err := pullFromSource(ctx, client, pullURL, transportSecret, rt.cfg.Pull.As, apply)
		return err
	})
}

// errPullLoopDone is returned by a poll to end pullLoop cleanly.
var errPullLoopDone = errors.New("pull loop done")

// pullLoop is the continuous pull loop: poll (first right away when
// pollFirst), then again after each next() interval, until ctx ends or a
// poll returns errPullLoopDone. Other poll errors are logged and never
// stop the loop.
func pullLoop(ctx context.Context, pollFirst bool, next func() time.Duration, poll func(context.Context) error) error {
	run := func() bool {
		err := poll(ctx)
		if errors.Is(err, errPullLoopDone) {
			return false
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie sink: pull: %v\n", err)
		}
		return true
	}
	if pollFirst && !run() {
		return nil
	}
	for {
		timer := time.NewTimer(next())
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			if !run() {
				return nil
			}
		}
	}
}

// pullOutcome says what one successful GET /pull did.
type pullOutcome int

const (
	// pullNothingNew: 204, or an envelope whose sequence was already applied.
	pullNothingNew pullOutcome = iota
	// pullApplied: a newer envelope was applied.
	pullApplied
)

// pullConnectError means the request got no HTTP response at all (laptop
// asleep, proxy down, no route). --pull-once retries these and exits 3.
type pullConnectError struct{ err error }

func (e *pullConnectError) Error() string { return e.err.Error() }
func (e *pullConnectError) Unwrap() error { return e.err }

// pullStatusError is a non-success HTTP answer from the source.
type pullStatusError struct {
	status int
	body   string
}

func (e *pullStatusError) Error() string {
	return fmt.Sprintf("source returned %d: %s", e.status, e.body)
}

// pullHTTPClient returns the client for profile, routed through proxy when
// one is set (sink.yaml pull.proxy). With no proxy it is the stock client,
// which honors HTTP_PROXY/HTTPS_PROXY/NO_PROXY.
func pullHTTPClient(profile httpserver.Profile, proxy string) (*http.Client, error) {
	c := httpserver.Client(profile)
	if proxy == "" {
		return c, nil
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("pull.proxy %q is not a proxy URL (want e.g. http://localhost:1055 or socks5://localhost:1055)", proxy)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("pull.proxy %q: scheme must be http, https, socks5 or socks5h", proxy)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("pull.proxy: unexpected transport %T", c.Transport)
	}
	tr = tr.Clone()
	tr.Proxy = http.ProxyURL(u)
	c.Transport = tr
	return c, nil
}

func pollSourceOnce(ctx context.Context, pullURL, secret string, apply func([]byte) envelopeApplyResult) error {
	_, err := pullFromSource(ctx, nil, pullURL, secret, "", apply)
	return err
}

// teammateHeader names the teammate agent sending a /pull (sink.yaml
// pull.as). It is not authenticated: the laptop uses it only to pick which
// teammate bound to the caller's verified Tailscale node lost its key.
const teammateHeader = "X-Agentcookie-Teammate"

// pullFromSource runs one GET /pull and applies what it returns. A nil
// client uses the stock pull client. An empty secret sends the request
// unsigned: the keyless /pull a teammate with no key sends so the laptop
// can start a re-pair. A non-empty as is sent in teammateHeader.
func pullFromSource(ctx context.Context, client *http.Client, pullURL, secret, as string, apply func([]byte) envelopeApplyResult) (pullOutcome, error) {
	if client == nil {
		client = httpserver.Client(httpserver.PullClient)
	}
	resolved := pullURL
	if r, err := resolveSinkURL(ctx, pullURL); err != nil {
		if errors.Is(err, tsclient.ErrAmbiguousPeer) {
			return pullNothingNew, fmt.Errorf("resolve pull URL: %w", err)
		}
	} else {
		resolved = r
	}

	reqCtx, cancel := context.WithTimeout(ctx, httpserver.Defaults(httpserver.PullClient).ClientTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, resolved, nil)
	if err != nil {
		return pullNothingNew, fmt.Errorf("new request: %w", err)
	}
	if as != "" {
		req.Header.Set(teammateHeader, as)
	}
	if secret != "" {
		if err := transport.SignRequest(req, secret, time.Now()); err != nil {
			return pullNothingNew, fmt.Errorf("sign pull request: %w", err)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return pullNothingNew, &pullConnectError{err: fmt.Errorf("GET %s: %w", resolved, err)}
	}
	defer resp.Body.Close()

	maxBody := httpserver.Defaults(httpserver.SinkSync).MaxBodyBytes
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return pullNothingNew, fmt.Errorf("read pull body: %w", err)
	}
	if int64(len(body)) > maxBody {
		return pullNothingNew, fmt.Errorf("pull body exceeds %d bytes", maxBody)
	}

	switch resp.StatusCode {
	case http.StatusNoContent:
		return pullNothingNew, nil
	case http.StatusOK:
		res := apply(body)
		if res.Status == http.StatusConflict {
			// Same sequence as last apply: skip, matching /sync replay defense.
			return pullNothingNew, nil
		}
		if res.Status != http.StatusOK {
			return pullNothingNew, fmt.Errorf("apply pulled envelope: %d: %s", res.Status, res.Reply)
		}
		return pullApplied, nil
	default:
		return pullNothingNew, &pullStatusError{status: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
}

// --pull-once exit codes. Any other failure exits 1.
const (
	pullExitUnreachable = 3 // laptop unreachable within the budget
	pullExitRepair      = 4 // no local key, or the laptop refused it (401)
)

// Pull-once and background-loop knobs. Tests shorten them.
var (
	pullOnceBudget   = 45 * time.Second
	pullOnceRetryMin = time.Second
	pullOnceRetryMax = 5 * time.Second
	// teammatePullInterval is the background loop's jittered wait.
	teammatePullInterval = func() time.Duration {
		return 5*time.Minute + time.Duration(rand.Int64N(int64(10*time.Minute)))
	}
)

func agentcookieStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".agentcookie")
}

// pullLockPath serializes every pull on this machine: --pull-once and each
// background loop iteration.
func pullLockPath() string { return filepath.Join(agentcookieStateDir(), "pull.lock") }

// pullLoopPidPath is the background loop's pidfile. The loop holds an
// exclusive flock on it for its whole life, so "a loop is running" is
// "the lock is held", which a killed loop can never leave stale.
func pullLoopPidPath() string { return filepath.Join(agentcookieStateDir(), "pull-loop.pid") }

// tryLock takes a non-blocking exclusive flock on path. ok is false when
// another process holds it. Closing the returned file releases the lock.
func tryLock(path string) (f *os.File, ok bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, true, nil
}

// pullOnceResult is the outcome of one locked pull.
type pullOnceResult struct {
	code    int // process exit code
	summary string
}

// runSinkPullOnce is `agentcookie sink --pull-once`: one pull from the
// laptop pinned in sink.yaml, under pull.lock, inside pullOnceBudget. A
// second invocation while one runs exits 0 at once. For cadence
// turn+timer it also makes sure one background pull loop is running,
// except under --dry-run.
func runSinkPullOnce(ctx context.Context, w io.Writer) error {
	cfg, err := config.LoadSink(common.ConfigDir)
	if err != nil {
		return err
	}
	if !cfg.PullMode() {
		return fmt.Errorf("sink.yaml has no pull.from; run `agentcookie join` first")
	}
	lock, ok, err := tryLock(pullLockPath())
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(w, "agentcookie pull-once: another pull is running; skipped")
		return nil
	}
	defer lock.Close()

	res := pullOnceLocked(ctx, cfg)
	// A dry run never starts the loop: the loop writes real cookies.
	if cfg.Pull.Cadence == config.CadenceTurnTimer && res.code != pullExitRepair && !sinkDryRun {
		if err := ensurePullLoop(w); err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie pull-once: background pull loop: %v\n", err)
		}
	}
	fmt.Fprintf(w, "agentcookie pull-once: %s\n", res.summary)
	if res.code != 0 {
		return &exitCodeError{code: res.code}
	}
	return nil
}

// pullOnceLocked pulls once. The caller holds pull.lock.
func pullOnceLocked(ctx context.Context, cfg *config.SinkConfig) pullOnceResult {
	pullURL, err := normalizePullURL(cfg.Pull.From)
	if err != nil {
		return pullOnceResult{code: 1, summary: err.Error()}
	}
	client, err := pullHTTPClient(httpserver.PullClient, cfg.Pull.Proxy)
	if err != nil {
		return pullOnceResult{code: 1, summary: err.Error()}
	}
	budgetCtx, cancel := context.WithTimeout(ctx, pullOnceBudget)
	defer cancel()

	pk, keyErr := keystore.Load(common.ConfigDir, cfg.Peer.Hostname)
	if keyErr != nil || pk == nil || len(pk.Key) == 0 {
		// One keyless GET /pull: from this machine's approved node it tells
		// the laptop the key is lost, and the laptop starts a re-pair.
		_, err := pullFromSource(budgetCtx, client, pullURL, "", cfg.Pull.As, func([]byte) envelopeApplyResult {
			return envelopeApplyResult{Status: http.StatusUnauthorized, Reply: "no local key"}
		})
		summary := "no local key for " + cfg.Peer.Hostname + "; asked the laptop to re-pair (run the join command from its next notice)"
		if _, unreachable := errors.AsType[*pullConnectError](err); unreachable {
			summary = "no local key for " + cfg.Peer.Hostname + ", and the laptop is unreachable; re-pair needed"
		}
		return pullOnceResult{code: pullExitRepair, summary: summary}
	}

	rt, err := newSinkRuntime(cfg)
	if err != nil {
		return pullOnceResult{code: 1, summary: err.Error()}
	}
	// Keep the status counters across invocations.
	if home, err := os.UserHomeDir(); err == nil {
		if prev, _ := state.LoadSink(state.SinkPath(home)); prev != nil {
			rt.st = prev
		}
	}
	// The apply step (Chrome connection, cookie writes) shares the budget,
	// so a stalled Chrome cannot hold the agent's turn past it.
	apply := pullApplier(rt, budgetCtx, string(pk.Key))

	delay := pullOnceRetryMin
	for {
		outcome, err := pullFromSource(budgetCtx, client, pullURL, string(pk.Key), cfg.Pull.As, apply)
		if err == nil {
			if outcome == pullApplied {
				return pullOnceResult{summary: "applied fresh cookies from " + cfg.Pull.From}
			}
			return pullOnceResult{summary: "nothing new from " + cfg.Pull.From}
		}
		if se, ok := errors.AsType[*pullStatusError](err); ok {
			if se.status == http.StatusUnauthorized {
				return pullOnceResult{code: pullExitRepair, summary: "the laptop refused this machine's key (401); re-pair needed"}
			}
			return pullOnceResult{code: 1, summary: err.Error()}
		}
		if _, ok := errors.AsType[*pullConnectError](err); !ok {
			return pullOnceResult{code: 1, summary: err.Error()}
		}
		deadline, _ := budgetCtx.Deadline()
		if budgetCtx.Err() != nil || time.Until(deadline) <= delay {
			return pullOnceResult{code: pullExitUnreachable, summary: fmt.Sprintf("laptop %s unreachable within %s: %v", cfg.Pull.From, pullOnceBudget, err)}
		}
		timer := time.NewTimer(delay)
		select {
		case <-budgetCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
		delay = min(delay*2, pullOnceRetryMax)
	}
}

// pullApplier builds the envelope apply step for one pull-once. Tests
// replace it.
var pullApplier = func(rt *sinkRuntime, ctx context.Context, secret string) func([]byte) envelopeApplyResult {
	return rt.applier(ctx, secret)
}

// pullLoopCommand builds the detached background loop process. Tests
// replace it.
var pullLoopCommand = func() (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "--config-dir", common.ConfigDir, "sink", "--pull-loop")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, nil
}

// ensurePullLoop starts the background pull loop unless one already holds
// the pidfile lock. The caller holds pull.lock, so two starters never race;
// the loop itself also exits at once if it cannot take the pidfile lock.
func ensurePullLoop(w io.Writer) error {
	f, ok, err := tryLock(pullLoopPidPath())
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	f.Close()

	cmd, err := pullLoopCommand()
	if err != nil {
		return err
	}
	logPath := filepath.Join(agentcookieStateDir(), "pull-loop.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, logf, logf
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	fmt.Fprintf(w, "agentcookie pull-once: started background pull loop (pid %d, log %s)\n", cmd.Process.Pid, logPath)
	return cmd.Process.Release()
}

// runTeammatePullLoop is the detached loop (`sink --pull-loop`). It holds
// the pidfile lock for its life, and runs the continuous pull loop with a
// 5 to 15 minute jittered interval. Each iteration takes pull.lock and
// skips when a --pull-once holds it. It exits once sink.yaml no longer
// asks for cadence turn+timer.
func runTeammatePullLoop(ctx context.Context) error {
	f, ok, err := tryLock(pullLoopPidPath())
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "agentcookie sink --pull-loop: a loop is already running; exiting")
		return nil
	}
	defer f.Close()
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	fmt.Fprintf(os.Stderr, "agentcookie sink --pull-loop: started (pid %d)\n", os.Getpid())

	return pullLoop(ctx, false, teammatePullInterval, func(ctx context.Context) error {
		cfg, err := config.LoadSink(common.ConfigDir)
		if err != nil {
			return err
		}
		if !cfg.PullMode() || cfg.Pull.Cadence != config.CadenceTurnTimer {
			fmt.Fprintln(os.Stderr, "agentcookie sink --pull-loop: sink.yaml no longer asks for cadence turn+timer; exiting")
			return errPullLoopDone
		}
		lock, ok, err := tryLock(pullLockPath())
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		defer lock.Close()
		res := pullOnceLocked(ctx, cfg)
		fmt.Fprintf(os.Stderr, "agentcookie sink --pull-loop: %s\n", res.summary)
		return nil
	})
}
