package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// loginPasswordEnv lets fully non-interactive installs (CI, headless
// re-runs with no PTY) supply the macOS login keychain password without a
// prompt. When unset, the onboarding path prompts once on the terminal.
const loginPasswordEnv = "AGENTCOOKIE_LOGIN_PASSWORD"

// errNoInteractivePassword is returned when neither the env override nor an
// interactive terminal is available, so callers can downgrade non-fatally.
var errNoInteractivePassword = errors.New(
	"no macOS login password available: set " + loginPasswordEnv +
		" or run this step from an interactive terminal (e.g. an ssh session with a TTY)")

// acquireLoginPasswordFunc is the seam the keychain onboarding path calls to
// obtain the operator's macOS login keychain password. Tests stub it. The
// production implementation prefers the AGENTCOOKIE_LOGIN_PASSWORD env
// override, then a one-time no-echo terminal prompt.
//
// SECURITY: the returned password is passed straight to `security -k` and is
// never logged or persisted by agentcookie.
var acquireLoginPasswordFunc = acquireLoginPassword

func acquireLoginPassword() (string, error) {
	if v := os.Getenv(loginPasswordEnv); v != "" {
		return v, nil
	}
	return promptLoginPasswordNoEcho()
}

// stdinIsTerminal reports whether stdin is an interactive terminal, without
// pulling in golang.org/x/term. A PTY-allocated ssh session is a character
// device; a piped or headless stdin is not.
var stdinIsTerminal = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func promptLoginPasswordNoEcho() (string, error) {
	if !stdinIsTerminal() {
		return "", errNoInteractivePassword
	}
	fmt.Fprint(os.Stderr, "macOS login password (grants Chrome Safe Storage access; entered once, never stored): ")
	setTerminalEcho(false)
	defer func() {
		setTerminalEcho(true)
		fmt.Fprintln(os.Stderr)
	}()
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read login password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// setTerminalEcho toggles terminal echo via stty so the password is not
// shown as it is typed. Best-effort: a stty failure leaves echo as-is rather
// than aborting the install.
func setTerminalEcho(on bool) {
	arg := "-echo"
	if on {
		arg = "echo"
	}
	c := exec.Command("/bin/stty", arg)
	c.Stdin = os.Stdin
	_ = c.Run()
}
