package transport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// HeaderResponseMAC authenticates capability/status response bytes against
// the fresh request nonce. Request authentication alone does not authenticate
// a high-water mark used to reseed lost source state.
const HeaderResponseMAC = "X-Agentcookie-Response-MAC"

func ResponseMAC(secret, nonce string, body []byte) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte("agentcookie-response-v1\n" + nonce + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
func VerifyResponseMAC(secret, nonce string, body []byte, mac string) bool {
	got, err := hex.DecodeString(mac)
	if err != nil {
		return false
	}
	want, _ := hex.DecodeString(ResponseMAC(secret, nonce, body))
	return hmac.Equal(got, want)
}
