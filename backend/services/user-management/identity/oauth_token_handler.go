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

// TokenHandler builds the OAuth 2.1 token endpoint (ADR-047 / RFC 6749 §3.2). It
// is a POST form endpoint supporting the authorization_code (+ PKCE) and
// refresh_token grants for public clients (no client authentication — possession is
// proven by PKCE for the code grant and by the refresh token itself). The two grant
// implementations are injected (like ServiceTokenHandler) so the HTTP shaping is
// unit-testable without a live store; each returns an *oauthError to select the RFC
// 6749 §5.2 error code + status, or a generic error rendered as server_error.
func TokenHandler(
	redeemCode func(ctx context.Context, code, clientID, redirectURI, codeVerifier string) (*OAuthTokens, error),
	refreshGrant func(ctx context.Context, refreshToken, scope string) (*OAuthTokens, error),
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

		var tokens *OAuthTokens
		var err error
		switch grant := r.PostForm.Get("grant_type"); grant {
		case "authorization_code":
			code := r.PostForm.Get("code")
			verifier := r.PostForm.Get("code_verifier")
			clientID := r.PostForm.Get("client_id")
			redirectURI := r.PostForm.Get("redirect_uri")
			if code == "" || verifier == "" || clientID == "" || redirectURI == "" {
				writeTokenError(w, errInvalidRequest("code, code_verifier, client_id, and redirect_uri are required"))
				return
			}
			tokens, err = redeemCode(r.Context(), code, clientID, redirectURI, verifier)
		case "refresh_token":
			refreshToken := r.PostForm.Get("refresh_token")
			if refreshToken == "" {
				writeTokenError(w, errInvalidRequest("refresh_token is required"))
				return
			}
			tokens, err = refreshGrant(r.Context(), refreshToken, r.PostForm.Get("scope"))
		case "":
			writeTokenError(w, errInvalidRequest("grant_type is required"))
			return
		default:
			writeTokenError(w, &oauthError{"unsupported_grant_type", "unsupported grant_type " + grant, http.StatusBadRequest})
			return
		}

		if err != nil {
			var oerr *oauthError
			if errors.As(err, &oerr) {
				writeTokenError(w, oerr)
				return
			}
			writeTokenError(w, errServer("internal error"))
			return
		}
		if tokens == nil {
			writeTokenError(w, errServer("internal error"))
			return
		}
		writeTokenSuccess(w, tokens)
	}
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
// success response it is marked no-store (§5.1).
func writeTokenError(w http.ResponseWriter, e *oauthError) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(tokenErrorResponse{Error: e.Code, ErrorDescription: e.Desc})
}
