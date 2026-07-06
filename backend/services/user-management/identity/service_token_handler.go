// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"

	"github.com/devicechain-io/dc-microservice/auth"
)

// maxMintBodyBytes caps the mint request body read.
const maxMintBodyBytes = 1 << 16

// ServiceTokenHandler builds the HTTP handler for the service-token mint endpoint
// (ADR-044 amendment) — the trust root of the synchronous cross-service call
// primitive. secret returns the configured shared service secret (empty disables
// minting, fail-closed); issue signs a service token for the requested subject +
// authorities. It is a standalone function (not an inline closure over main's
// globals) so every branch is unit-testable.
func ServiceTokenHandler(secret func() string, issue func(subject string, authorities []string) (auth.IssuedToken, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		configured := secret()
		if configured == "" {
			http.Error(w, "service-token minting is not configured", http.StatusServiceUnavailable)
			return
		}
		// Compare fixed-width SHA-256 digests under a constant-time compare so the
		// check leaks neither the secret's content nor its length.
		presented := r.Header.Get(auth.ServiceSecretHeader)
		want := sha256.Sum256([]byte(configured))
		got := sha256.Sum256([]byte(presented))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			http.Error(w, "invalid service secret", http.StatusUnauthorized)
			return
		}
		var req auth.ServiceTokenRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, maxMintBodyBytes)).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Subject == "" || len(req.Authorities) == 0 {
			http.Error(w, "subject and authorities are required", http.StatusBadRequest)
			return
		}
		// Reject unknown authorities up front so a typo (e.g. "device:raed") fails
		// fast here rather than opaquely at the callee's gate. AuthorityAll ("*") is
		// a valid request — the shared-secret holder is trusted (the acknowledged
		// blast radius), so this is a shape check, not an authorization ceiling.
		for _, a := range req.Authorities {
			if !auth.ValidAuthority(a) {
				http.Error(w, "unknown authority: "+a, http.StatusBadRequest)
				return
			}
		}
		tok, err := issue(req.Subject, req.Authorities)
		if err != nil {
			http.Error(w, "failed to mint service token", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: tok.Token, ExpiresAt: tok.ExpiresAt.Unix()})
	}
}
