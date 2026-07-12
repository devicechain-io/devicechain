// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"encoding/json"
	"net/http"

	"github.com/devicechain-io/dc-microservice/auth"
)

// OAuth 2.1 Authorization-Server endpoint paths, relative to the issuer origin
// (ADR-047). These are the paths this service registers on its mux; externally
// they sit under the issuer URL (the cluster ingress strips the functional-area
// prefix, so <issuer>/oauth/authorize reaches the service as /oauth/authorize).
const (
	// MetadataPath is the RFC 8414 Authorization-Server Metadata document. It is
	// served at the issuer-suffixed location (<issuer>/.well-known/...), the form
	// OAuth/MCP clients probe first.
	MetadataPath  = "/.well-known/oauth-authorization-server"
	AuthorizePath = "/oauth/authorize"
	TokenPath     = "/oauth/token"
	// jwksPath is the existing JWK Set endpoint (ADR-008), advertised as jwks_uri.
	jwksPath = "/auth/jwks"
)

// AuthorizationServerMetadata is the subset of RFC 8414 Authorization-Server
// Metadata this AS publishes (ADR-047). It advertises the authorization-code +
// PKCE flow over the existing JWT mint; fields are omitted rather than sent empty
// so the document reflects exactly what is supported.
type AuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	JwksURI                           string   `json:"jwks_uri"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	ResponseModesSupported            []string `json:"response_modes_supported,omitempty"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
}

// BuildAuthorizationServerMetadata assembles the metadata document for a given
// issuer URL. issuer must be an absolute origin with no trailing slash (enforced
// by config validation) so endpoint URLs concatenate cleanly and the "issuer"
// field compares byte-for-byte against the value a client derived from discovery.
// The advertised surface is deliberately narrow: authorization-code + refresh
// grants only, PKCE S256 mandatory, public clients (no client secret), and the
// scopes this AS actually grants.
func BuildAuthorizationServerMetadata(issuer string) AuthorizationServerMetadata {
	return AuthorizationServerMetadata{
		Issuer:                            issuer,
		AuthorizationEndpoint:             issuer + AuthorizePath,
		TokenEndpoint:                     issuer + TokenPath,
		JwksURI:                           issuer + jwksPath,
		ScopesSupported:                   auth.SupportedScopes,
		ResponseTypesSupported:            []string{"code"},
		ResponseModesSupported:            []string{"query"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
		CodeChallengeMethodsSupported:     []string{"S256"},
	}
}

// AuthorizationServerMetadataHandler serves the RFC 8414 metadata document. It is
// registered only when an issuer URL is configured (OAuth enabled), so issuer is
// always a validated absolute origin here. The document is public (unauthenticated
// discovery) and cacheable.
func AuthorizationServerMetadataHandler(issuer string) http.Handler {
	body, _ := json.Marshal(BuildAuthorizationServerMetadata(issuer))
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
