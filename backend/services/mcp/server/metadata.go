// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"net/http"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
)

// ProtectedResourceMetadataPath is the RFC 9728 well-known location for a resource
// identifier that carries NO path — and the suffix every other location is built
// from. Aliased from core so this package names it once and the routing, the chart
// and the Authorization Server's equivalent all draw on one definition.
const ProtectedResourceMetadataPath = coreauth.ProtectedResourceMetadataSuffix

// ProtectedResourceMetadataPathFor is the local path RFC 9728 §3.1 requires the
// document to be served at for a given resource identifier: the suffix INSERTED
// between the host and the identifier's path, never appended after it and never
// substituted for it. coreauth.WellKnownPath is the one implementation of that
// construction in this repository — see its comment for why it is not three.
//
// The origin-only form (the path discarded) is what this server used to advertise,
// and it is not merely unreachable through the ingress: for an instance that ever
// hosts a second resource on the same origin it names the wrong document. The path
// is what distinguishes them.
func ProtectedResourceMetadataPathFor(resourceID string) string {
	return coreauth.WellKnownPath(ProtectedResourceMetadataPath, resourceID)
}

// protectedResourceMetadata is the RFC 9728 document (ADR-047): it names this
// resource's identifier, the Authorization Server(s) that issue tokens for it, and
// the scopes/bearer methods it accepts. A client fetches it after a 401 to learn
// where to run the authorization-code flow and which `resource` to bind the token
// to (RFC 8707).
type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

// ProtectedResourceMetadataHandler serves the RFC 9728 document. resourceID is
// this server's identifier (the token audience); issuer is the AS. The document is
// public (unauthenticated discovery) and cacheable.
func ProtectedResourceMetadataHandler(resourceID, issuer string) http.Handler {
	body, err := json.Marshal(protectedResourceMetadata{
		Resource:               resourceID,
		AuthorizationServers:   []string{issuer},
		ScopesSupported:        append([]string(nil), coreauth.SupportedScopes...),
		BearerMethodsSupported: []string{"header"},
	})
	if err != nil {
		panic("mcp: marshaling protected-resource metadata: " + err.Error())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Browser-based MCP clients fetch this cross-origin, so it must be
		// CORS-open (it is public discovery data — no credentials, no secrets).
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	})
}
