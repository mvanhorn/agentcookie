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

	// SourcePull is the source-side GET /pull listener served during
	// `source --watch`. Request bodies are tiny (auth headers only);
	// responses can be as large as a /sync envelope.
	SourcePull

	// PullClient is the sink-side poll client that GETs /pull. Timeout
	// matches SyncClient so a large first envelope over a slow tailnet
	// (or an HTTP proxy) can complete.
	PullClient
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
			ReadTimeout:       5 * time.Minute, // Match SyncClient; first full sync (16k+ cookies) needs time over Tailscale
			WriteTimeout:      5 * time.Minute, // Response can also be slow on congested links
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
	case SourcePull:
		return Settings{
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      5 * time.Minute,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    16 * 1024,
			MaxBodyBytes:      16 * 1024,
		}
	case PullClient:
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
// PairClient, SyncClient, and PullClient are the supported client
// profiles; other inputs fall back to a 30-second timeout.
//
// Transport is always set. A bare `&http.Client{Timeout: ...}` leaves
// Transport nil, which uses http.DefaultTransport — fine until a test or
// runtime replaces DefaultTransport, and easy to misread as "no proxy".
// Client-only Tailscale shims (Muse-like sandboxes) can only reach the
// tailnet through an HTTP proxy from the environment (typically
// HTTP_PROXY on port 3130). Cloning DefaultTransport and setting
// ProxyFromEnvironment makes that path explicit. If a test has replaced
// DefaultTransport with a stub RoundTripper, that stub is used as-is.
func Client(p Profile) *http.Client {
	s := Defaults(p)
	timeout := s.ClientTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout, Transport: proxyTransport()}
}

// proxyTransport returns a RoundTripper that honors HTTP_PROXY,
// HTTPS_PROXY, and NO_PROXY. http.ProxyFromEnvironment is the stdlib
// equivalent used by DefaultTransport; setting it on a cloned transport
// keeps that behavior even if a caller later mutates DefaultTransport.Proxy.
func proxyTransport() http.RoundTripper {
	base := http.DefaultTransport
	tr, ok := base.(*http.Transport)
	if !ok {
		return base
	}
	cloned := tr.Clone()
	cloned.Proxy = http.ProxyFromEnvironment
	return cloned
}

// LimitedReader wraps r so reads beyond max bytes return
// http.MaxBytesError. Handler code calls this on r.Body before
// io.ReadAll so a hostile client cannot exhaust memory or disk.
func LimitedReader(r *http.Request, max int64) {
	r.Body = http.MaxBytesReader(nil, r.Body, max)
}
