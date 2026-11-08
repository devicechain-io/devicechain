// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/devicechain-io/dc-microservice/streams"
)

// TokenAudienceMode is how a token-audience rule treats a request that breaks it.
type TokenAudienceMode string

const (
	// TokenAudienceObserve counts and logs a would-be refusal and lets the request
	// through. It changes no served behaviour.
	TokenAudienceObserve TokenAudienceMode = "observe"
	// TokenAudienceEnforce refuses the request.
	TokenAudienceEnforce TokenAudienceMode = "enforce"

	// TokenAudienceReleaseDefault is what an absent or empty `enforcement` means. It is
	// the ONE place the release default lives: observe in v1.2, enforce from v1.3. An
	// operator who needs the v1.2 behaviour back sets `enforcement: observe` explicitly.
	//
	// The flip is safe across a rolling upgrade because a token with NO audience is accepted
	// by every plane in both modes (see auth.PlanePolicy): every token the platform mints for
	// itself (sign-in sessions, service tokens) carries none, and an OAuth token carries one
	// only when its client asked for a resource. What enforce refuses is a token bound to a
	// resource the plane does not serve, the same set observe counted as would_reject.
	TokenAudienceReleaseDefault = TokenAudienceEnforce
)

// MaxTokenAudienceResources is the most entries either resource list may hold.
const MaxTokenAudienceResources = 8

// MaxTokenAudienceResourceLen is the longest resource identifier the authorization server will
// accept in a request (user-management's ValidateResource refuses anything longer as
// invalid_target). A configured identifier above it could never match a request, so the
// configuration refuses it at startup instead of enforcing a list that can never be satisfied.
const MaxTokenAudienceResourceLen = 2048

// TokenAudienceConfiguration is the instance's token-audience policy: which `aud` values an
// API plane accepts and which `resource` values the authorization server may issue for.
//
// It is FIXED configuration (the mounted instance document), not tenant data: every input
// is instance topology (the ingress origin, whether mcp is deployed, mcp's resource
// identifier). The authorization server and every plane read this one document, so the list
// the AS issues against and the set a plane accepts cannot drift apart.
type TokenAudienceConfiguration struct {
	// Enforcement is "observe", "enforce" or "" (the release default, enforce; see
	// TokenAudienceReleaseDefault). It governs the authorization-server resource check and
	// the plane audience check together.
	Enforcement string
	// AreaBaseUrl is <origin>/api, the base each area's resource identifier is built from:
	// <AreaBaseUrl>/<area>. Empty means no area audience is issuable or matched.
	AreaBaseUrl string
	// McpResources are the MCP resource identifiers tokens may be bound to: mcp's resolved
	// resourceUrl. Empty when mcp is not deployed.
	McpResources []string
	// ExternalResources are resource servers OUTSIDE the platform that validate DeviceChain
	// tokens through /oauth/jwks. The authorization server may issue for them; no platform
	// plane accepts them.
	ExternalResources []string
	// UnscopedClientCredentials is "observe" or "" (observe through v1.x). "enforce" is
	// REFUSED by Validate until the release that refuses scope-less requests. It
	// governs the client_credentials rule that a resource must be paired with a scope.
	UnscopedClientCredentials string
}

// AreaAudiences are the areas whose resource identifier is an AREA audience: every platform
// area except mcp. mcp's identifier is an MCP resource, not an area (and the chart derives it
// as <origin>/api/mcp, which would otherwise collide with AreaResource(base, "mcp")). This is
// the one definition of the set: validation, plane classification and the AS allowlist all
// read it.
func AreaAudiences() []string {
	out := make([]string, 0, len(streams.PlatformAreas))
	for _, a := range streams.PlatformAreas {
		if a != "mcp" {
			out = append(out, a)
		}
	}
	return out
}

// IsAreaAudience reports whether area is one of AreaAudiences.
func IsAreaAudience(area string) bool {
	return area != "mcp" && streams.IsPlatformArea(area)
}

// AreaResource is the resource identifier of area under base: base + "/" + area. It is ""
// when base or area is empty, so an unconfigured base can never produce a matchable value.
func AreaResource(base, area string) string {
	if base == "" || area == "" {
		return ""
	}
	return base + "/" + area
}

// EffectiveEnforcement resolves Enforcement: empty is the release default, otherwise the
// configured value. Validate has refused anything else.
func (c TokenAudienceConfiguration) EffectiveEnforcement() TokenAudienceMode {
	if c.Enforcement == "" {
		return TokenAudienceReleaseDefault
	}
	return TokenAudienceMode(c.Enforcement)
}

// EffectiveUnscopedClientCredentials resolves UnscopedClientCredentials. Unlike
// Enforcement, an empty value is observe through v1.x whatever the release default of the
// resource rules is.
func (c TokenAudienceConfiguration) EffectiveUnscopedClientCredentials() TokenAudienceMode {
	if c.UnscopedClientCredentials == "" {
		return TokenAudienceObserve
	}
	return TokenAudienceMode(c.UnscopedClientCredentials)
}

// Validate fails closed on anything that would make the policy mean something other than
// what was written.
func (c TokenAudienceConfiguration) Validate() error {
	for _, f := range []struct{ key, val string }{
		{"enforcement", c.Enforcement},
		{"unscopedClientCredentials", c.UnscopedClientCredentials},
	} {
		switch f.val {
		case "", string(TokenAudienceObserve), string(TokenAudienceEnforce):
		default:
			return fmt.Errorf("infrastructure.tokenAudience.%s is %q; it must be %q or %q (or left unset)",
				f.key, f.val, TokenAudienceObserve, TokenAudienceEnforce)
		}
	}
	if c.UnscopedClientCredentials == string(TokenAudienceEnforce) {
		// Accepting a value that does nothing would let an operator believe the rule is on.
		return fmt.Errorf("infrastructure.tokenAudience.unscopedClientCredentials cannot be %q in this release: "+
			"a client_credentials request that names a resource without a scope is counted and logged, not refused; "+
			"refusal arrives in the next major release. Leave the key unset or set it to %q",
			TokenAudienceEnforce, TokenAudienceObserve)
	}
	if c.AreaBaseUrl != "" {
		if err := ValidateResourceURL("infrastructure.tokenAudience.areaBaseUrl", c.AreaBaseUrl, true); err != nil {
			return err
		}
	}

	areas := map[string]struct{}{}
	for _, a := range AreaAudiences() {
		if r := AreaResource(c.AreaBaseUrl, a); r != "" {
			if len(r) > MaxTokenAudienceResourceLen {
				return fmt.Errorf("infrastructure.tokenAudience.areaBaseUrl: the %s area's resource identifier is %d bytes; at most %d are allowed",
					a, len(r), MaxTokenAudienceResourceLen)
			}
			areas[r] = struct{}{}
		}
	}
	seen := map[string]string{}
	lists := []struct {
		key      string
		entries  []string
		external bool
	}{
		{"mcpResources", c.McpResources, false},
		{"externalResources", c.ExternalResources, true},
	}
	for _, l := range lists {
		if len(l.entries) > MaxTokenAudienceResources {
			return fmt.Errorf("infrastructure.tokenAudience.%s has %d entries; at most %d are allowed",
				l.key, len(l.entries), MaxTokenAudienceResources)
		}
		for i, e := range l.entries {
			field := fmt.Sprintf("infrastructure.tokenAudience.%s[%d]", l.key, i)
			if err := ValidateResourceURL(field, e, true); err != nil {
				return err
			}
			if len(e) > MaxTokenAudienceResourceLen {
				return fmt.Errorf("%s: %d bytes; at most %d are allowed (a longer identifier is refused as invalid_target on every request)",
					field, len(e), MaxTokenAudienceResourceLen)
			}
			if prev, dup := seen[e]; dup {
				return fmt.Errorf("%s: %q is already listed in %s; a resource may appear once", field, e, prev)
			}
			seen[e] = l.key
			if _, isArea := areas[e]; isArea {
				return fmt.Errorf("%s: %q is a platform area's resource identifier and cannot be listed as %s",
					field, e, l.key)
			}
			if l.external && c.AreaBaseUrl != "" && (e == c.AreaBaseUrl || strings.HasPrefix(e, c.AreaBaseUrl+"/")) {
				return fmt.Errorf("%s: %q sits under areaBaseUrl %q, so it would re-label a platform area as external",
					field, e, c.AreaBaseUrl)
			}
		}
	}
	return nil
}

// ValidateResourceURL is the one rule for a resource or issuer identifier: an absolute URL
// with a host, no query, fragment or userinfo, no trailing slash, and https (http only for a
// loopback host). Identifiers are compared byte-for-byte as the token audience and as the
// `resource` of the metadata document, so each must have exactly one spelling.
//
// It lives here, not in core/auth, because core/auth imports core/config; a validator in auth
// called from config would be an import cycle.
//
// lowercase additionally requires a lowercase scheme and host. The tokenAudience fields set
// it; mcp's existing resourceUrl does not, because refusing in a minor a value that starts
// today would be a compat break.
func ValidateResourceURL(field, raw string, lowercase bool) error {
	if raw == "" {
		return fmt.Errorf("%s is required", field)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: not a valid URL: %w", field, err)
	}
	if !u.IsAbs() || u.Hostname() == "" {
		return fmt.Errorf("%s: must be an absolute URL with a host (got %q)", field, raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.ContainsAny(raw, "?#") {
		return fmt.Errorf("%s: must have no query, fragment, or userinfo (got %q)", field, raw)
	}
	if strings.HasSuffix(u.Path, "/") {
		return fmt.Errorf("%s: must not end with a trailing slash (got %q); the identifier is compared "+
			"byte-for-byte, so it must have exactly one spelling", field, raw)
	}
	host := u.Hostname()
	isLoopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback) {
		return fmt.Errorf("%s: must use https (http allowed only for a loopback host; got %q)", field, raw)
	}
	if lowercase {
		// The scheme and authority are everything up to the first "/" after "://". The
		// path keeps its case: it is case-sensitive.
		head := raw
		if i := strings.Index(raw, "://"); i >= 0 {
			head = raw[:i+3]
			rest := raw[i+3:]
			if j := strings.Index(rest, "/"); j >= 0 {
				head += rest[:j]
			} else {
				head += rest
			}
		}
		if head != strings.ToLower(head) {
			return fmt.Errorf("%s: scheme and host must be lowercase (got %q)", field, raw)
		}
	}
	return nil
}
