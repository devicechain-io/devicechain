// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"fmt"
	"net/url"
	"strings"
)

// OAuth 2.1 scope vocabulary (ADR-047). Scopes are a *cap* on the authorities a
// token minted through the authorization-code flow may carry: the AS intersects
// the subject's effective authorities with the scope's allowance at issue time, so
// an OAuth session is structurally limited to its scope even for a subject holding
// AuthorityAll ("*"). The scope→authority mapping and its enforcement land with
// the token endpoint; these identifiers are the shared vocabulary the metadata
// document advertises and the authorize/token endpoints validate against.
const (
	// ScopeReadOnly names the general read-only observability surface: reads of
	// devices, events, state, commands and alarms — the same SURFACE the console's
	// viewer baseline covers.
	//
	// 🔴 "The same surface" is not "the same list", and the two must not be re-fused.
	// viewerAuthorities (user-management/identity) is a GRANT — every enabled tenant
	// member receives it whatever their roles. This is a CEILING — the most a token
	// minted through this scope may carry, intersected at issue time with what the
	// subject actually holds. They are kept equal deliberately, in two places, with a
	// test on each side; wiring one to the other is what produced the defect below.
	//
	// It deliberately does NOT cover a device's position: see ScopeLocation.
	ScopeReadOnly = "read-only"

	// ScopeLocation names one capability on its own: reading where a device — and
	// therefore, often, where a vehicle or a person — has been.
	//
	// 🔴 IT IS SEPARATE FROM ScopeReadOnly ON PURPOSE, and folding it back in is the
	// mistake this constant exists to prevent. A scope is not an internal cap; it is
	// the sentence a resource owner is shown on the consent screen and asked to agree
	// to. Position is the one read capability the platform already treats as
	// separately grantable — LocationRead is held out of the viewer baseline for
	// exactly that reason — so a consent screen that could not name it separately
	// would be unable to express the distinction the authority exists for. With one
	// combined scope there is also no way to authorize an agent to observe a fleet
	// while WITHHOLDING where it is, which is the choice a person most often wants to
	// make.
	//
	// It was briefly folded into ScopeReadOnly's allowance to fix an MCP tool that
	// could not be called by anybody. That worked, and it silently widened every
	// existing authorization: the consent screen renders the raw scope strings, so a
	// resource owner saw "read-only" before and after, and a session authorized
	// before the change would have gained position on its next automatic refresh
	// (RefreshOAuth re-caps against the CURRENT allowance) with no new consent step.
	//
	// A client that wants both asks for "read-only location". Being a ceiling, asking
	// for it grants nothing on its own: a subject no role gave location:read still
	// receives a token without it.
	ScopeLocation = "location"
)

// SupportedScopes is the set of scopes this Authorization Server will grant,
// advertised in the RFC 8414 metadata's scopes_supported.
var SupportedScopes = []string{ScopeReadOnly, ScopeLocation}

// scopeAllowances is the scope→authority ceiling table: the most a token minted
// through each scope may carry. It lives here, beside the scope vocabulary and
// IntersectAuthorities, rather than in the authorization server, so that the
// services which merely CONSUME scoped tokens can see what a scope reaches without
// importing user-management — the MCP resource server's tool catalog is checked
// against it, which is the only thing standing between a newly added read tool and
// the 100%-refusal defect described on ScopeLocation.
//
// 🔴 Every entry is a CEILING, never a grant. IntersectAuthorities emits only what
// the subject actually holds, so adding an authority here gives nobody anything —
// it decides what a role-granted authority is allowed to SURVIVE into an OAuth
// token. What it does do is widen what a consent screen's scope name silently
// covers, so an addition is a product decision, not a maintenance edit.
//
// 🔴 Nothing here may be a write authority or AuthorityAll, and the read-only
// entry must stay in step with user-management's viewer baseline. Both are pinned
// by test, on both sides of the module boundary.
var scopeAllowances = map[string][]string{
	ScopeReadOnly: {
		string(DeviceRead),
		string(EventRead),
		string(StateRead),
		string(CommandRead),
		string(AlarmRead),
	},
	ScopeLocation: {
		string(LocationRead),
	},
}

// ScopeAllowance returns the authority ceiling for a single scope, and whether the
// scope is one this AS defines. An undefined scope returns (nil, false) so every
// caller fails closed rather than minting a token for a scope with no ceiling.
// The returned slice is a copy: the table is a package global and a caller that
// unioned into it in place would rewrite the ceiling for every later grant.
func ScopeAllowance(scope string) ([]string, bool) {
	allow, ok := scopeAllowances[scope]
	if !ok {
		return nil, false
	}
	return append([]string(nil), allow...), true
}

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

// IsSupportedScope reports whether s is a single scope this AS grants.
func IsSupportedScope(s string) bool {
	for _, sup := range SupportedScopes {
		if sup == s {
			return true
		}
	}
	return false
}

// ValidateRedirectURI enforces the OAuth 2.1 redirect-URI rules for a registered
// client (ADR-047). A redirect URI must be an absolute URL with no fragment
// (RFC 6749 §3.1.2). The scheme must be https, EXCEPT that http is permitted for a
// loopback host (127.0.0.1, ::1, or localhost) — the OAuth 2.1 / RFC 8252 §7.3
// carve-out for native apps that receive the redirect on a loopback listener,
// which is exactly how the v1 MCP desktop clients authenticate. Plaintext http to
// any non-loopback host is rejected, fail-closed.
func ValidateRedirectURI(raw string) error {
	if raw == "" {
		return fmt.Errorf("redirect URI must not be empty")
	}
	if raw != strings.TrimSpace(raw) {
		return fmt.Errorf("must not have leading or trailing whitespace (got %q)", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid URL: %w", err)
	}
	if !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("must be an absolute URL with a host (got %q)", raw)
	}
	// Reject userinfo: "https://good.com@evil.com/cb" has host evil.com and would
	// exfiltrate the authorization code there — the canonical open-redirect-via-
	// userinfo bypass. Redirect URIs carry no credentials (RFC 6749 §3.1.2).
	if u.User != nil {
		return fmt.Errorf("must not contain userinfo/credentials (got %q)", raw)
	}
	// Reject any fragment, including a bare "#" (url.Parse records that as an empty
	// Fragment, so also check the raw string).
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("must not contain a fragment (got %q)", raw)
	}
	host := u.Hostname()
	isLoopback := host == "127.0.0.1" || host == "::1" || host == "localhost"
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if !isLoopback {
			return fmt.Errorf("http redirect URI is allowed only for a loopback host (got %q)", raw)
		}
		return nil
	default:
		return fmt.Errorf("redirect URI scheme must be https (or http for loopback); got %q", u.Scheme)
	}
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
