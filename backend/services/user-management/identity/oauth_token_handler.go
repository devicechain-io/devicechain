// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// maxTokenBodyBytes caps the token-request body (a small set of form fields).
const maxTokenBodyBytes = 1 << 16

// tokenResponse is the RFC 6749 §5.1 successful token-endpoint body.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// tokenErrorResponse is the RFC 6749 §5.2 error body.
type tokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// clientCredentials is the client identity + secret a token request presents.
type clientCredentials struct {
	clientID  string
	secret    string
	presented bool // a client_secret was supplied (by either method)
}

// extractClientCredentials reads client authentication from a token request per RFC
// 6749 §2.3.1: HTTP Basic (client_secret_basic) or the client_id/client_secret form
// fields (client_secret_post). Using BOTH methods at once, or a Basic client_id that
// disagrees with a body client_id, is invalid_request. client_id may also arrive
// with no secret (a public client) — allowed here and resolved by AuthenticateClient.
func extractClientCredentials(r *http.Request) (clientCredentials, *oauthError) {
	formID := r.PostForm.Get("client_id")
	formSecret := r.PostForm.Get("client_secret")
	basicID, basicSecret, hasBasic := r.BasicAuth()

	if hasBasic && formSecret != "" {
		return clientCredentials{}, errInvalidRequest("use only one client authentication method")
	}
	if hasBasic {
		if formID != "" && formID != basicID {
			return clientCredentials{}, errInvalidRequest("client_id mismatch between Authorization header and request body")
		}
		return clientCredentials{clientID: basicID, secret: basicSecret, presented: true}, nil
	}
	return clientCredentials{clientID: formID, secret: formSecret, presented: formSecret != ""}, nil
}

// TokenHandler builds the OAuth 2.1 token endpoint (ADR-047 / RFC 6749 §3.2). It is
// a POST form endpoint supporting the authorization_code (+ PKCE) and refresh_token
// grants. Before the grant it authenticates the client (authenticateClient): a
// confidential client must present a valid secret (client_secret_basic/post), a
// public client proves possession by PKCE / the refresh token alone. The client-auth
// and grant implementations are injected (like ServiceTokenHandler) so the HTTP
// shaping is unit-testable without a live store; each returns an *oauthError to
// select the RFC 6749 §5.2 error code + status, or a generic error → server_error.
func TokenHandler(
	authenticateClient func(ctx context.Context, clientID, secret string, presented bool) error,
	redeemCode func(ctx context.Context, code, clientID, redirectURI, codeVerifier string) (*OAuthTokens, error),
	refreshGrant func(ctx context.Context, refreshToken, scope, clientID string) (*OAuthTokens, error),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeTokenError(w, errInvalidRequest("the token endpoint requires POST"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxTokenBodyBytes)
		if err := r.ParseForm(); err != nil {
			writeTokenError(w, errInvalidRequest("could not parse request body"))
			return
		}

		// Client authentication runs for BOTH grants, before the grant logic. A
		// confidential client that fails it never reaches the grant.
		creds, cerr := extractClientCredentials(r)
		if cerr != nil {
			writeTokenError(w, cerr)
			return
		}
		if err := authenticateClient(r.Context(), creds.clientID, creds.secret, creds.presented); err != nil {
			writeGrantError(w, err)
			return
		}

		var tokens *OAuthTokens
		var err error
		switch grant := r.PostForm.Get("grant_type"); grant {
		case "authorization_code":
			code := r.PostForm.Get("code")
			verifier := r.PostForm.Get("code_verifier")
			redirectURI := r.PostForm.Get("redirect_uri")
			// client_id comes from the authenticated credentials (Basic header or the
			// client_id form field), not re-read from the body, so a Basic-authenticated
			// confidential client need not also repeat client_id in the form.
			if code == "" || verifier == "" || creds.clientID == "" || redirectURI == "" {
				writeTokenError(w, errInvalidRequest("code, code_verifier, client_id, and redirect_uri are required"))
				return
			}
			tokens, err = redeemCode(r.Context(), code, creds.clientID, redirectURI, verifier)
		case "refresh_token":
			refreshToken := r.PostForm.Get("refresh_token")
			if refreshToken == "" {
				writeTokenError(w, errInvalidRequest("refresh_token is required"))
				return
			}
			tokens, err = refreshGrant(r.Context(), refreshToken, r.PostForm.Get("scope"), creds.clientID)
		case "":
			writeTokenError(w, errInvalidRequest("grant_type is required"))
			return
		default:
			writeTokenError(w, &oauthError{"unsupported_grant_type", "unsupported grant_type " + grant, http.StatusBadRequest})
			return
		}

		if err != nil {
			writeGrantError(w, err)
			return
		}
		if tokens == nil {
			writeTokenError(w, errServer("internal error"))
			return
		}
		writeTokenSuccess(w, tokens)
	}
}

// writeGrantError renders an error from client-auth or a grant: a typed *oauthError
// selects the RFC 6749 §5.2 code + status; anything else is an opaque server_error.
func writeGrantError(w http.ResponseWriter, err error) {
	var oerr *oauthError
	if errors.As(err, &oerr) {
		writeTokenError(w, oerr)
		return
	}
	writeTokenError(w, errServer("internal error"))
}

// writeTokenSuccess renders the §5.1 body. Token responses MUST NOT be cached
// (RFC 6749 §5.1), so no-store is set on both success and error.
func writeTokenSuccess(w http.ResponseWriter, t *OAuthTokens) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(tokenResponse{
		AccessToken:  t.AccessToken,
		TokenType:    "Bearer",
		ExpiresIn:    t.ExpiresIn,
		RefreshToken: t.RefreshToken,
		Scope:        t.Scope,
	})
}

// writeTokenError renders the §5.2 error body with the mapped status. Like the
// success response it is marked no-store (§5.1). An invalid_client (401) carries a
// WWW-Authenticate: Basic challenge, as RFC 6749 §5.2 requires when a client that
// tried to authenticate is rejected.
func writeTokenError(w http.ResponseWriter, e *oauthError) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if e.Code == "invalid_client" {
		w.Header().Set("WWW-Authenticate", `Basic realm="oauth", charset="UTF-8"`)
	}
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(tokenErrorResponse{Error: e.Code, ErrorDescription: e.Desc})
}
