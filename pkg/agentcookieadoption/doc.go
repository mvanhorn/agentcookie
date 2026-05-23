// Package agentcookieadoption is the author-side helper library for the
// agentcookie secrets-bus v2 adoption standard.
//
// Project authors who want to programmatically write or validate an
// agentcookie.toml manifest import this package. End-user agentcookie code
// does not import this; the agent-side discovery and parsing lives in
// internal/secretsbus.
//
// Typical use (e.g., from a printing-press generator):
//
//	m := &agentcookieadoption.Manifest{
//	    SchemaVersion: 2,
//	    Name:          "my-cli",
//	    DisplayName:   "My CLI",
//	    ProjectKind:   "cli",
//	    Secrets: agentcookieadoption.Secrets{
//	        File: &agentcookieadoption.SecretsFile{
//	            Path: "~/.config/my-cli/config.toml",
//	        },
//	    },
//	}
//	if err := agentcookieadoption.Validate(m); err != nil {
//	    return err
//	}
//	if err := agentcookieadoption.WriteTo(m, "agentcookie.toml"); err != nil {
//	    return err
//	}
//
// See docs/spec-agentcookie-secrets-bus-v2-adoption.md for the full spec.
package agentcookieadoption
