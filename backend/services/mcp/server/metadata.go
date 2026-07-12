// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"net/http"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
)

// ProtectedResourceMetadataPath is the RFC 9728 well-known location a client
// probes (also named in the 401 WWW-Authenticate challenge) to discover which
// Authorization Server issues tokens for this resource.
const ProtectedResourceMetadataPath = "/.well-known/oauth-protected-resource"

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
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	})
}
