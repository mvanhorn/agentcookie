package transport

import (
	"net/http"
	"testing"
	"time"
)

func TestSignVerifyRequestRoundTrip(t *testing.T) {
	secret := "peer-key-not-real"
	req, err := http.NewRequest(http.MethodGet, "http://mac.tailnet:9998/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := SignRequest(req, secret, now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := VerifyRequest(req, secret, now, 0); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyRequestRejectsBadMAC(t *testing.T) {
	secret := "peer-key-not-real"
	req, err := http.NewRequest(http.MethodGet, "http://mac.tailnet:9998/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := SignRequest(req, secret, now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	req.Header.Set(HeaderMAC, "00"+req.Header.Get(HeaderMAC)[2:])
	if err := VerifyRequest(req, secret, now, 0); err == nil {
		t.Fatal("expected error for bad HMAC, got nil")
	}
}

func TestVerifyRequestRejectsWrongSecret(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://mac.tailnet:9998/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := SignRequest(req, "correct-secret", now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := VerifyRequest(req, "wrong-secret", now, 0); err == nil {
		t.Fatal("expected error for wrong secret, got nil")
	}
}

func TestVerifyRequestRejectsStaleTimestamp(t *testing.T) {
	secret := "peer-key-not-real"
	req, err := http.NewRequest(http.MethodGet, "http://mac.tailnet:9998/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	signedAt := time.Unix(1_700_000_000, 0)
	if err := SignRequest(req, secret, signedAt); err != nil {
		t.Fatalf("sign: %v", err)
	}
	later := signedAt.Add(10 * time.Minute)
	if err := VerifyRequest(req, secret, later, 5*time.Minute); err == nil {
		t.Fatal("expected error for stale timestamp, got nil")
	}
}

func TestVerifyRequestRejectsMissingHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://mac.tailnet:9998/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRequest(req, "secret", time.Now(), 0); err == nil {
		t.Fatal("expected error for missing auth headers, got nil")
	}
}

func TestVerifyRequestAnyPicksMatchingSecret(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://mac.tailnet:9998/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := SignRequest(req, "beta-key", now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, err := VerifyRequestAny(req, []string{"alpha-key", "beta-key", "gamma-key"}, now, 0)
	if err != nil {
		t.Fatalf("verify any: %v", err)
	}
	if got != "beta-key" {
		t.Errorf("matched %q, want beta-key", got)
	}
}

func TestVerifyRequestAnyRejectsWhenNoneMatch(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://mac.tailnet:9998/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := SignRequest(req, "real-key", now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := VerifyRequestAny(req, []string{"alpha-key", "beta-key"}, now, 0); err == nil {
		t.Fatal("expected error when no secret matches")
	}
}

func TestVerifyRequestBindsMethodAndPath(t *testing.T) {
	secret := "peer-key-not-real"
	req, err := http.NewRequest(http.MethodGet, "http://mac.tailnet:9998/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := SignRequest(req, secret, now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	replay, err := http.NewRequest(http.MethodPost, "http://mac.tailnet:9998/pair", nil)
	if err != nil {
		t.Fatal(err)
	}
	replay.Header.Set(HeaderTimestamp, req.Header.Get(HeaderTimestamp))
	replay.Header.Set(HeaderNonce, req.Header.Get(HeaderNonce))
	replay.Header.Set(HeaderMAC, req.Header.Get(HeaderMAC))
	if err := VerifyRequest(replay, secret, now, 0); err == nil {
		t.Fatal("MAC for GET /pull must not verify on POST /pair")
	}
}
