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
