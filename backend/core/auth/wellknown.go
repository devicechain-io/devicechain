// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"net/url"
	"strings"
)

// The two OAuth metadata well-known suffixes this instance serves.
//
// They are SUFFIXES, not paths: for an identifier that carries a path the document
// does not live at the suffix, it lives at the suffix with the identifier's path
// appended after it. WellKnownPath is the only thing that builds that.
const (
	// ProtectedResourceMetadataSuffix is RFC 9728's registered well-known suffix —
	// the MCP server's protected-resource metadata.
	ProtectedResourceMetadataSuffix = "/.well-known/oauth-protected-resource"
	// AuthorizationServerMetadataSuffix is RFC 8414's — this instance's OAuth 2.1
	// Authorization Server metadata.
	AuthorizationServerMetadataSuffix = "/.well-known/oauth-authorization-server"
)

// WellKnownPath is the local path a metadata document must be served at for a given
// identifier: the well-known suffix INSERTED between the host and the identifier's
// path.
//
// 🔴 INSERTED. NOT APPENDED, AND NOT SUBSTITUTED. RFC 8414 §3.1 and RFC 9728 §3.1
// use word-for-word the same construction, and for an identifier carrying a path
// there are three plausible-looking results of which only one is what a client
// builds:
//
//	identifier https://host/api/mcp, suffix /.well-known/oauth-protected-resource
//	  inserted   /.well-known/oauth-protected-resource/api/mcp   ← the specified one
//	  appended   /api/mcp/.well-known/oauth-protected-resource   ← used in the wild, specified nowhere
//	  origin     /.well-known/oauth-protected-resource           ← a DIFFERENT identifier's document
//
// Both RFCs also say any terminating slash after the host is removed before the
// insertion, so ".../api/mcp" and ".../api/mcp/" resolve to one location rather than
// to two differing by a dangling slash.
//
// 🔑 IT LIVES IN core BECAUSE THE RULE WAS ABOUT TO BE WRITTEN THREE TIMES — once
// for the MCP resource server, once for the Authorization Server, and once more in
// the Helm chart to route them. Three implementations of one construction agree only
// by coincidence, and the coincidence is invisible: each is exercised against its own
// expectations. Two of the three are now this function; the chart no longer
// implements it at all, because it routes the constant SUFFIX as a prefix and leaves
// deciding which exact path is correct to the service that owns the identifier.
//
// An unparseable identifier yields the bare suffix. That is the safe direction: it is
// a real location (the base case for a path-less identifier) rather than a
// concatenation of garbage, and every caller here validates its identifier at config
// load, so this is a fallback no configured service reaches.
func WellKnownPath(suffix, identifier string) string {
	u, err := url.Parse(identifier)
	if err != nil {
		return suffix
	}
	return suffix + strings.TrimSuffix(u.EscapedPath(), "/")
}
