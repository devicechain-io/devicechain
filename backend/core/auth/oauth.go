// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "strings"

// OAuth 2.1 scope vocabulary (ADR-047). Scopes are a *cap* on the authorities a
// token minted through the authorization-code flow may carry: the AS intersects
// the subject's effective authorities with the scope's allowance at issue time, so
// an OAuth session is structurally limited to its scope even for a subject holding
// AuthorityAll ("*"). The scope→authority mapping and its enforcement land with
// the token endpoint; these identifiers are the shared vocabulary the metadata
// document advertises and the authorize/token endpoints validate against.
const (
	// ScopeReadOnly grants the read-only observability surface — the same capability
	// set as the console's viewer baseline (device/event/state/command/alarm reads).
	// It is the only scope in the v1.0 read-only MCP cut; write scopes are a
	// post-v1.0 fast-follow gated on elevation + human-in-the-loop confirmation.
	ScopeReadOnly = "read-only"
)

// SupportedScopes is the set of scopes this Authorization Server will grant,
// advertised in the RFC 8414 metadata's scopes_supported.
var SupportedScopes = []string{ScopeReadOnly}

// ParseScope splits a space-delimited OAuth scope string into its members,
// dropping empty fields (RFC 6749 §3.3 encodes scope as space-delimited).
func ParseScope(scope string) []string {
	return strings.Fields(scope)
}

// ScopeSupported reports whether every requested scope is one this AS grants.
// An unknown scope is rejected rather than silently ignored (fail-closed).
func ScopeSupported(requested string) bool {
	supported := make(map[string]struct{}, len(SupportedScopes))
	for _, s := range SupportedScopes {
		supported[s] = struct{}{}
	}
	for _, r := range ParseScope(requested) {
		if _, ok := supported[r]; !ok {
			return false
		}
	}
	return true
}

// IntersectAuthorities caps a subject's held authorities to those an OAuth scope
// permits (ADR-047 D-SCOPE) — the load-bearing primitive the authorization-code
// flow uses so a minted token carries only what its scope allows. It returns the
// members of allowed that held actually grants, deterministically ordered by
// allowed. Crucially the super-authority "*" in held is *capped* to the allowed
// set, never expanded: a subject holding "*" granted a limited scope receives
// exactly the scope's authorities and never "*" itself — so an OAuth session
// cannot exceed its scope even for a superuser. allowed is expected to be a curated
// scope allowance (never containing "*"); any "*" in allowed is dropped so a scope
// definition can never smuggle the super-authority into a token.
func IntersectAuthorities(held, allowed []string) []string {
	if len(allowed) == 0 {
		return nil
	}
	hasAll := false
	heldSet := make(map[string]struct{}, len(held))
	for _, h := range held {
		if h == string(AuthorityAll) {
			hasAll = true
		}
		heldSet[h] = struct{}{}
	}
	out := make([]string, 0, len(allowed))
	seen := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		if a == string(AuthorityAll) {
			continue // a scope allowance must never grant the super-authority
		}
		if _, dup := seen[a]; dup {
			continue
		}
		if _, ok := heldSet[a]; ok || hasAll {
			out = append(out, a)
			seen[a] = struct{}{}
		}
	}
	return out
}
