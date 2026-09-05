// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "testing"

// The construction rule, including the example the RFCs themselves give.
//
// 🔴 THE RFC'S OWN EXAMPLE IS THE FIRST CASE ON PURPOSE. Every other case here was
// written by the same person who wrote the function, so they can all agree with each
// other and still be wrong together. RFC 9728 §3.1 states that a request for the
// metadata of https://resource.example.com/resource1 is `GET
// /.well-known/oauth-protected-resource/resource1`; that one is evidence from outside
// this repository.
func TestWellKnownPath(t *testing.T) {
	for _, tc := range []struct{ name, suffix, identifier, want string }{
		{
			"RFC 9728 §3.1's own example",
			ProtectedResourceMetadataSuffix, "https://resource.example.com/resource1",
			"/.well-known/oauth-protected-resource/resource1",
		},
		{
			"the base case: no path, nothing to insert around",
			ProtectedResourceMetadataSuffix, "https://resource.example.com",
			"/.well-known/oauth-protected-resource",
		},
		{
			"a terminating slash is removed before insertion",
			ProtectedResourceMetadataSuffix, "https://resource.example.com/",
			"/.well-known/oauth-protected-resource",
		},
		{
			"a multi-segment path is inserted whole",
			ProtectedResourceMetadataSuffix, "https://iot.example.com/api/mcp",
			"/.well-known/oauth-protected-resource/api/mcp",
		},
		{
			"a terminating slash on a multi-segment path, likewise",
			ProtectedResourceMetadataSuffix, "https://iot.example.com/api/mcp/",
			"/.well-known/oauth-protected-resource/api/mcp",
		},
		{
			"a port belongs to the origin and never reaches the path",
			ProtectedResourceMetadataSuffix, "http://localhost:8080/api/mcp",
			"/.well-known/oauth-protected-resource/api/mcp",
		},
		{
			"RFC 8414 uses the same construction with its own suffix",
			AuthorizationServerMetadataSuffix, "https://iot.example.com/api/user-management",
			"/.well-known/oauth-authorization-server/api/user-management",
		},
		{
			"an unparseable identifier falls back to the bare suffix",
			ProtectedResourceMetadataSuffix, "://not a url",
			"/.well-known/oauth-protected-resource",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := WellKnownPath(tc.suffix, tc.identifier); got != tc.want {
				t.Errorf("WellKnownPath(%q, %q) = %q, want %q",
					tc.suffix, tc.identifier, got, tc.want)
			}
		})
	}
}

// The two suffixes must stay distinct, and neither may be a prefix of the other.
//
// 🔴 THIS IS A ROUTING CONSTRAINT, NOT A TYPO CHECK. The ingress routes each suffix
// as a PREFIX to a different service — protected-resource metadata to the MCP server,
// authorization-server metadata to the AS — so if one were a prefix of the other, one
// of the two would be shadowed and a client would be handed the wrong document (or
// the right document from a service that does not own the identifier).
func TestTheWellKnownSuffixesDoNotShadowEachOther(t *testing.T) {
	a, b := ProtectedResourceMetadataSuffix, AuthorizationServerMetadataSuffix
	if a == b {
		t.Fatalf("the two suffixes are the same string (%q)", a)
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b) > len(a) && b[:len(a)] == a {
		t.Errorf("%q is a prefix of %q, so routing one as a prefix swallows the other", a, b)
	}
}
