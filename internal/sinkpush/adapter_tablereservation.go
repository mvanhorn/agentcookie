package sinkpush

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// chromeEpochDeltaMicros is the microseconds between the Unix epoch
// (1970-01-01) and Chrome's WebKit epoch (1601-01-01). Same constant
// the internal/chrome package uses for its *_utc columns; duplicated
// here so this adapter's expires-conversion does not require an export
// from the chrome package just for one use site.
const chromeEpochDeltaMicros int64 = 11644473600 * 1000 * 1000

// TableReservationAdapter pushes cookies into table-reservation-goat-pp-cli's
// single-file session store. Unlike the pycookiecheat-style PP CLIs,
// this CLI's session.json contains structured cookie objects (not a
// Cookie header string) split by network (opentable_cookies vs
// tock_cookies). Schema discovered by inspecting an existing
// session.json on Matt's MBP:
//
//	{
//	  "version": 1,
//	  "updated_at": "<ISO8601>",
//	  "opentable_cookies": [ {name, value, domain, path, expires}, ... ],
//	  "tock_cookies":      [ {name, value, domain, path, expires}, ... ]
//	}
//
// The adapter requests cookies for both opentable.com and
// exploretock.com hosts, splits them by host suffix into the two
// arrays, then atomically writes session.json mode 0600.
type TableReservationAdapter struct {
	binary    string
	configDir string
}

// NewTableReservation builds the adapter pointing at the default
// install + config paths.
func NewTableReservation() *TableReservationAdapter {
	home, _ := os.UserHomeDir()
	return &TableReservationAdapter{
		binary:    filepath.Join(home, "go", "bin", "table-reservation-goat-pp-cli"),
		configDir: filepath.Join(home, ".config", "table-reservation-goat-pp-cli"),
	}
}

func (a *TableReservationAdapter) Name() string { return "table-reservation-goat-pp-cli" }

func (a *TableReservationAdapter) CLIBinary() string { return a.binary }

func (a *TableReservationAdapter) IsInstalled() bool {
	info, err := os.Stat(a.binary)
	return err == nil && !info.IsDir()
}

func (a *TableReservationAdapter) CookieHostPatterns() []string {
	// Two networks share one session file. Resy is handled out-of-band
	// (email+password exchange for a long-lived token, not a Chrome
	// cookie import), so the adapter does not push Resy cookies.
	return []string{"%opentable.com", "%exploretock.com"}
}

// sessionCookie is the JSON shape one cookie takes inside session.json.
// Field names match the schema discovered in the existing session.json.
type sessionCookie struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Domain  string `json:"domain"`
	Path    string `json:"path"`
	Expires string `json:"expires"`
}

// sessionEnvelope is the top-level structure session.json carries.
// version=1 matches the existing file Matt has on MBP; bumping it
// would be a CLI-side breaking change.
type sessionEnvelope struct {
	Version          int             `json:"version"`
	UpdatedAt        string          `json:"updated_at"`
	OpentableCookies []sessionCookie `json:"opentable_cookies"`
	TockCookies      []sessionCookie `json:"tock_cookies"`
}

// Push writes session.json with the cookies split into the two network
// buckets. Empty-value cookies are dropped per the same logic the
// other adapters use -- they carry no auth and would clobber the CLI's
// existing per-cookie state on the next read.
func (a *TableReservationAdapter) Push(cookies []chrome.Cookie) error {
	if len(cookies) == 0 {
		return nil
	}
	if err := os.MkdirAll(a.configDir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", a.configDir, err)
	}

	env := sessionEnvelope{
		Version:          1,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		OpentableCookies: []sessionCookie{},
		TockCookies:      []sessionCookie{},
	}
	for _, c := range cookies {
		if c.Value == "" {
			continue
		}
		sc := chromeToSessionCookie(c)
		// v0.12: seal the cookie value when the agentcookie master
		// key is installed. PP CLI reads the value, detects the
		// SealedPrefix, unseals via pkg/sidecar's keystore.
		sealed, err := maybeSeal(sc.Value)
		if err != nil {
			return fmt.Errorf("seal cookie value: %w", err)
		}
		sc.Value = sealed
		switch {
		case HostSuffixMatch(c.HostKey, "opentable.com"):
			env.OpentableCookies = append(env.OpentableCookies, sc)
		case HostSuffixMatch(c.HostKey, "exploretock.com"):
			env.TockCookies = append(env.TockCookies, sc)
		}
	}

	if len(env.OpentableCookies) == 0 && len(env.TockCookies) == 0 {
		// All filtered out -- nothing to write.
		return nil
	}

	body, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session.json: %w", err)
	}
	body = append(body, '\n')
	return atomicWriteFile(filepath.Join(a.configDir, "session.json"), body, 0o600)
}

// chromeToSessionCookie converts one Chrome cookie to the session.json
// per-cookie shape. Expires conversion: Chrome's *_utc fields are
// microseconds since the WebKit epoch (1601-01-01). The CLI's
// session.json carries an RFC3339Nano string.
//
// Persistent cookies have ExpiresUTC > 0; session cookies have
// ExpiresUTC = 0 (and HasExpires = 0). For consistency with the CLI's
// existing format (where every cookie carries an expires), we emit a
// far-future RFC3339 string for session cookies so the CLI parses
// them without special-casing. The exact date is far enough out that
// downstream callers cannot accidentally treat the cookie as expired.
func chromeToSessionCookie(c chrome.Cookie) sessionCookie {
	return sessionCookie{
		Name:    c.Name,
		Value:   c.Value,
		Domain:  c.HostKey,
		Path:    c.Path,
		Expires: chromeExpiresToRFC3339(c.ExpiresUTC),
	}
}

// chromeExpiresToRFC3339 converts a Chrome *_utc microsecond value to
// RFC3339Nano. Zero (session cookie) maps to a far-future date so the
// session.json schema's required-expires invariant is preserved.
func chromeExpiresToRFC3339(chromeUTC int64) string {
	if chromeUTC == 0 {
		return "2099-12-31T23:59:59Z"
	}
	unixMicros := chromeUTC - chromeEpochDeltaMicros
	return time.UnixMicro(unixMicros).UTC().Format(time.RFC3339Nano)
}
