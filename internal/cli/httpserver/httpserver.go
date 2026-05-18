// Package httpserver consolidates the timeout and body-size policy that
// agentcookie's HTTP listeners and clients all share. v0.11 left every
// http.Server and http.Client at standard-library defaults (no timeouts,
// no body cap), which is the kind of mistake that turns a reachable
// listener into a memory and disk exhaustion surface.
//
// One helper, one place to tune: server side via Configure, client side
// via Client, body-size cap per profile via MaxBodyBytes.
package httpserver

import (
	"net/http"
	"time"
)

// Profile names the route this configuration is for. Each profile carries
// timeouts, header limit, and a body-size cap chosen for that route's
// expected payload shape.
type Profile int

const (
	// SinkSync is the /sync endpoint on the sink. Envelopes include
	// the cookie payload plus optional LevelDB tarballs for Chrome's
	// LocalStorage and IndexedDB, so the body cap is generous (256 MB
	// default; configurable in sink.yaml).
	SinkSync Profile = iota

	// Pair is the source-side /pair endpoint. Pairing envelopes are
	// tiny (X25519 pubkey + hostname). Body cap is 16 KB; anything
	// larger is a misuse.
	Pair

	// PairClient is the sink-side pairing client that POSTs to the
	// source's Pair listener. Bound by Timeout rather than per-phase
	// timeouts.
	PairClient

	// SyncClient is the source-side sync client that POSTs to the
	// sink's /sync endpoint. Larger Timeout to allow the bigger
	// envelopes.
	SyncClient
)

// Settings carries the resolved values for a Profile. Exported so callers
// (e.g. sink.yaml override path) can read and mutate the body cap.
type Settings struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
	ClientTimeout     time.Duration
}

// Defaults returns the baseline Settings for a profile.
func Defaults(p Profile) Settings {
	switch p {
	case SinkSync:
		return Settings{
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    16 * 1024,
			MaxBodyBytes:      256 * 1024 * 1024,
		}
	case Pair:
		return Settings{
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    16 * 1024,
			MaxBodyBytes:      16 * 1024,
		}
	case PairClient:
		return Settings{
			ClientTimeout: 30 * time.Second,
		}
	case SyncClient:
		return Settings{
			ClientTimeout: 5 * time.Minute,
		}
	}
	return Settings{}
}

// Configure applies a profile's server-side settings to srv. Returns srv
// for chaining convenience. Pass an explicit Settings via ConfigureWith
// when sink.yaml has overridden the body cap.
func Configure(srv *http.Server, p Profile) *http.Server {
	return ConfigureWith(srv, Defaults(p))
}

// ConfigureWith applies arbitrary Settings to srv. The MaxBodyBytes
// field is not applied to the server itself; handlers wrap r.Body with
// LimitedReader(s, MaxBodyBytes) at read time.
func ConfigureWith(srv *http.Server, s Settings) *http.Server {
	srv.ReadHeaderTimeout = s.ReadHeaderTimeout
	srv.ReadTimeout = s.ReadTimeout
	srv.WriteTimeout = s.WriteTimeout
	srv.IdleTimeout = s.IdleTimeout
	srv.MaxHeaderBytes = s.MaxHeaderBytes
	return srv
}

// Client returns an http.Client configured for the given profile.
// PairClient and SyncClient are the supported profiles; other inputs
// fall back to a 30-second timeout.
func Client(p Profile) *http.Client {
	s := Defaults(p)
	timeout := s.ClientTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

// LimitedReader wraps r so reads beyond max bytes return
// http.MaxBytesError. Handler code calls this on r.Body before
// io.ReadAll so a hostile client cannot exhaust memory or disk.
func LimitedReader(r *http.Request, max int64) {
	r.Body = http.MaxBytesReader(nil, r.Body, max)
}
