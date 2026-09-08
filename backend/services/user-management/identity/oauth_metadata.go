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
	// MetadataPath is the RFC 8414 Authorization-Server Metadata document, at the
	// location for an issuer that carries NO path — and the suffix MetadataPathFor
	// builds the path-carrying location from.
	MetadataPath  = auth.AuthorizationServerMetadataSuffix
	AuthorizePath = "/oauth/authorize"
	TokenPath     = "/oauth/token"
	// UserinfoPath is the OIDC-style userinfo endpoint (advertised as
	// userinfo_endpoint). A confidential login client (Grafana, ADR-047 SSO) that
	// treats the access token as opaque calls it with the token as a Bearer
	// credential to read the subject's identity + the operator-tier `sudo` claim.
	UserinfoPath = "/oauth/userinfo"
	// OAuthJwksPath is the JWK Set endpoint advertised to *external* OAuth clients
	// as jwks_uri. It deliberately sits under /oauth/ rather than reusing the
	// internal /auth/jwks (ADR-008): the cluster ingress 404s all external
	// /api/<area>/auth/* requests (so the service-token mint is not a public
	// oracle), which would also blackhole /auth/jwks. Serving an identical key set
	// here keeps external token validators working without punching a hole in that
	// edge rule. In-cluster peers keep fetching /auth/jwks directly, unaffected.
	OAuthJwksPath = "/oauth/jwks"
)

// AuthorizationServerMetadata is the subset of RFC 8414 Authorization-Server
// Metadata this AS publishes (ADR-047). It advertises the authorization-code +
// PKCE flow over the existing JWT mint; fields are omitted rather than sent empty
// so the document reflects exactly what is supported.
type AuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	UserinfoEndpoint                  string   `json:"userinfo_endpoint,omitempty"`
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
// grants only, PKCE S256 mandatory, and the scopes this AS actually grants. Client
// authentication is "none" (public PKCE clients, e.g. MCP) OR client_secret_basic/
// _post (confidential clients, e.g. a server-side app like Grafana).
func BuildAuthorizationServerMetadata(issuer string) AuthorizationServerMetadata {
	return AuthorizationServerMetadata{
		Issuer:                issuer,
		AuthorizationEndpoint: issuer + AuthorizePath,
		TokenEndpoint:         issuer + TokenPath,
		UserinfoEndpoint:      issuer + UserinfoPath,
		JwksURI:               issuer + OAuthJwksPath,
		// Copy the exported scope slice so the served document can't be skewed by a
		// later mutation of the package-global.
		ScopesSupported:                   append([]string(nil), auth.SupportedScopes...),
		ResponseTypesSupported:            []string{"code"},
		ResponseModesSupported:            []string{"query"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		TokenEndpointAuthMethodsSupported: []string{"none", "client_secret_basic", "client_secret_post"},
		CodeChallengeMethodsSupported:     []string{"S256"},
	}
}

// MetadataPathFor is the local path RFC 8414 §3.1 requires the metadata document to
// be served at for a given issuer: the suffix INSERTED between the host and the
// issuer's path.
//
// 🔴 THIS IS THE LOCATION A CLIENT ACTUALLY ASKS FOR, and for a path-carrying issuer
// it is NOT MetadataPath. This instance's issuer is https://<host>/api/user-management
// whenever the AS is on, so a client's first request is
// /.well-known/oauth-authorization-server/api/user-management — and the MCP reference
// client aborts discovery on the first error rather than trying its later fallbacks,
// so this location failing is the whole flow failing, not a degraded path. It shares
// coreauth.WellKnownPath with the MCP resource server's equivalent because the two
// RFCs specify word-for-word the same construction.
func MetadataPathFor(issuer string) string {
	return auth.WellKnownPath(MetadataPath, issuer)
}

// RegisterMetadataHandlers mounts the RFC 8414 document at every location a client
// may ask for it: the path-inserted location for this issuer, and the bare suffix.
//
// 🔴 IT IS A FUNCTION SO THAT A TEST CAN DRIVE THE ACTUAL MUX. This used to be two
// lines in main.go, and main.go is reached by no test in this repository — which is
// precisely how the service came to serve its most important discovery document at
// one path while clients asked for another, with every package here green. The
// registration is the thing that was wrong, so the registration is the thing under
// test.
//
// The bare suffix is kept for a path-less issuer (where it IS the location) and for a
// client that appends rather than inserts; the conditional avoids registering one
// pattern twice, which ServeMux panics on.
func RegisterMetadataHandlers(mux *http.ServeMux, issuer string) {
	h := AuthorizationServerMetadataHandler(issuer)
	mux.Handle(MetadataPath, h)
	if p := MetadataPathFor(issuer); p != MetadataPath {
		mux.Handle(p, h)
	}
}

// AuthorizationServerMetadataHandler serves the RFC 8414 metadata document. It is
// registered only when an issuer URL is configured (OAuth enabled), so issuer is
// always a validated absolute origin here. The document is public (unauthenticated
// discovery) and cacheable.
func AuthorizationServerMetadataHandler(issuer string) http.Handler {
	body, err := json.Marshal(BuildAuthorizationServerMetadata(issuer))
	if err != nil {
		// The struct has no marshaler-bearing fields, so this is unreachable today;
		// panic rather than silently serve an empty 200 if a future field breaks it.
		panic("identity: marshaling authorization-server metadata: " + err.Error())
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
