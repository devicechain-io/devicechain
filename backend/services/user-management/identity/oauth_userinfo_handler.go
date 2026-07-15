// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
)

// bearerPrefix is the RFC 6750 Authorization scheme the userinfo endpoint accepts.
const bearerPrefix = "Bearer "

// userinfoResponse is the OIDC-style claims document the userinfo endpoint returns
// (ADR-047 SSO). It is derived entirely from the presented access token — no DB
// read — so it reflects exactly what the token asserts about its own bearer.
//
// Sub/Email/PreferredUsername are the subject identity a login client (Grafana)
// maps to a user; Sudo is the operator/superuser-tier signal it maps with
// role_attribute_path to gate cross-tenant metrics access to operators only (metrics
// are instance-level + cross-tenant — never tenant users). Tenant is the tenant the
// OAuth grant was pinned to (carried for audit; a login client ignores it).
type userinfoResponse struct {
	Sub               string `json:"sub"`
	Email             string `json:"email,omitempty"`
	PreferredUsername string `json:"preferred_username,omitempty"`
	Sudo              bool   `json:"sudo"`
	Tenant            string `json:"tenant,omitempty"`
}

// UserinfoHandler builds the OIDC-style userinfo endpoint (ADR-047 SSO / OIDC Core
// §5.3). It authenticates the request with the access token presented as a Bearer
// credential and returns the token's own identity claims — the endpoint a login
// client that treats the access token as opaque (Grafana generic_oauth's api_url)
// calls to learn who the user is and whether they are an operator. The validator is
// injected (like the token-endpoint grants) so the HTTP shaping is unit-testable
// without a live key set; it must verify the signature + access-token type + tenant
// (auth.Validator.Validate). Supports GET and POST (OIDC Core §5.3.1); the token is
// read from the Authorization header for both.
func UserinfoHandler(validate func(tokenString string) (*auth.Claims, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token, ok := bearerToken(r)
		if !ok {
			// No credential presented at all → a bare Bearer challenge (RFC 6750 §3).
			w.Header().Set("WWW-Authenticate", `Bearer`)
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		claims, err := validate(token)
		if err != nil {
			// An invalid/expired token → invalid_token challenge (RFC 6750 §3.1). Never
			// echo the parse error (no oracle about why the token failed).
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		// sub is the stable subject (the registered "sub"/Subject, which the issuer
		// sets to the identity's email); fall back to the email claim defensively.
		sub := claims.Subject
		if sub == "" {
			sub = claims.Email
		}
		writeJSON(w, userinfoResponse{
			Sub:               sub,
			Email:             claims.Email,
			PreferredUsername: claims.Username,
			Sudo:              claims.ActingAsSuperuser,
			Tenant:            claims.Tenant,
		})
	}
}

// bearerToken extracts the access token from the Authorization header, returning
// ("", false) when there is no non-empty Bearer credential.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if len(header) <= len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(bearerPrefix):])
	return token, token != ""
}

// writeJSON renders v as a no-store JSON body (userinfo is per-user + token-bound,
// so it must not be cached).
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
