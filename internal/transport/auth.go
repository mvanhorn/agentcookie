package transport

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// HeaderTimestamp is the Unix-seconds timestamp mixed into the request MAC.
	HeaderTimestamp = "X-Agentcookie-Timestamp"
	// HeaderNonce is a unique per-request hex nonce mixed into the request MAC.
	HeaderNonce = "X-Agentcookie-Nonce"
	// HeaderMAC is hex(HMAC-SHA256(peer key, canonical request)).
	HeaderMAC = "X-Agentcookie-MAC"

	authInfo    = "agentcookie-req-v1"
	nonceBytes  = 16
	defaultSkew = 5 * time.Minute
)

// ErrAuth is returned when request HMAC verification fails.
var ErrAuth = errors.New("request authentication failed")

// SignRequest attaches HMAC request-auth headers using secret (the pairing
// peer key or legacy shared secret). The MAC covers method, path, timestamp,
// and a fresh nonce so a captured /pull cannot be replayed outside the skew
// window. Envelope crypto is unchanged: this is transport-layer request auth
// only, using the same peer key SealWithSecret already uses.
func SignRequest(req *http.Request, secret string, now time.Time) error {
	if req == nil {
		return fmt.Errorf("sign request: nil *http.Request")
	}
	if now.IsZero() {
		now = time.Now()
	}
	nonce := make([]byte, nonceBytes)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("nonce: %w", err)
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	nonceHex := hex.EncodeToString(nonce)
	mac := requestMAC(secret, req.Method, req.URL.Path, ts, nonceHex)
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderNonce, nonceHex)
	req.Header.Set(HeaderMAC, hex.EncodeToString(mac))
	return nil
}

// VerifyRequest checks HMAC request-auth headers against secret.
// maxSkew of 0 uses a 5-minute window.
func VerifyRequest(req *http.Request, secret string, now time.Time, maxSkew time.Duration) error {
	_, err := VerifyRequestAny(req, []string{secret}, now, maxSkew)
	return err
}

// VerifyRequestAny tries each secret and returns the one that matched.
// Timing-safe across the secret list: every candidate MAC is compared.
func VerifyRequestAny(req *http.Request, secrets []string, now time.Time, maxSkew time.Duration) (string, error) {
	if req == nil {
		return "", ErrAuth
	}
	if now.IsZero() {
		now = time.Now()
	}
	if maxSkew <= 0 {
		maxSkew = defaultSkew
	}
	ts := req.Header.Get(HeaderTimestamp)
	nonce := req.Header.Get(HeaderNonce)
	gotHex := req.Header.Get(HeaderMAC)
	if ts == "" || nonce == "" || gotHex == "" {
		return "", ErrAuth
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return "", ErrAuth
	}
	delta := now.Sub(time.Unix(unix, 0))
	if delta < 0 {
		delta = -delta
	}
	if delta > maxSkew {
		return "", ErrAuth
	}
	got, err := hex.DecodeString(gotHex)
	if err != nil {
		return "", ErrAuth
	}
	path := ""
	if req.URL != nil {
		path = req.URL.Path
	}
	matched := ""
	ok := 0
	for _, secret := range secrets {
		want := requestMAC(secret, req.Method, path, ts, nonce)
		if subtle.ConstantTimeCompare(got, want) == 1 {
			matched = secret
			ok++
		}
	}
	if ok == 0 {
		return "", ErrAuth
	}
	return matched, nil
}

func requestMAC(secret, method, path, timestamp, nonce string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	// Canonical form is versioned so a future header change cannot reuse
	// old MACs. Path is the URL path only (no query) so /pull?x= is equivalent
	// to /pull for auth, matching how the handler is registered.
	_, _ = fmt.Fprintf(mac, "%s\n%s\n%s\n%s\n%s", authInfo, strings.ToUpper(method), path, timestamp, nonce)
	return mac.Sum(nil)
}
