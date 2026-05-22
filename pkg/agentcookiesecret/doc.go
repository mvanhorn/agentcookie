// Package agentcookiesecret is the canonical Go reader for the
// agentcookie secrets bus.
//
// A CLI that wants to pick up secrets from agentcookie does this at startup:
//
//	env, err := agentcookiesecret.Load("my-pp-cli")
//	if err != nil {
//	    // fall back to your existing config-file loading
//	}
//	token := env["MY_OAUTH_BEARER"]
//
// The library resolves values from the v1 secrets-bus format described in
// docs/spec-agentcookie-secrets-bus-v1.md. The resolution priority chain is:
//
//  1. ~/.agentcookie/secrets/<cli>/secrets.env.sealed
//     (when the agentcookie master key is available)
//  2. ~/.agentcookie/secrets/<cli>/secrets.env
//  3. Any caller-registered fallback file (via LoadWithFallback)
//  4. Process environment variables
//
// Bus values take precedence over caller fallback and process env. This is
// deliberate: if a key exists in the bus, the bus owns its value, and an
// older env-var leak does not silently override.
//
// The library never writes. The bus is source-of-truth; the CLI is read-only
// against it. Rotation, format changes, and sealing transitions are
// agentcookie's responsibility.
package agentcookiesecret
