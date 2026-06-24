// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rsa"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// signingMethod is the one and only accepted JWT signing algorithm. Pinning it
// (rather than trusting the token header's "alg") is the defense against the
// classic algorithm-confusion attack, where an attacker re-signs a token with
// HS256 using the RSA *public* key as the HMAC secret. We reject anything that
// is not RS256 both via WithValidMethods and an explicit keyfunc type assertion.
const signingMethod = "RS256"

// Validator verifies DeviceChain access tokens against the platform's RSA
// public key. It holds no private key and performs no network calls per
// request — verification is local (ADR-008).
type Validator struct {
	publicKey *rsa.PublicKey
	parser    *jwt.Parser
}

// NewValidator builds a Validator from a parsed RSA public key.
func NewValidator(pub *rsa.PublicKey) *Validator {
	return &Validator{
		publicKey: pub,
		// Require exp, reject non-RS256 algorithms, and validate expiry.
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{signingMethod}),
			jwt.WithExpirationRequired(),
		),
	}
}

// NewValidatorFromPEM builds a Validator from a PKIX PEM public key.
func NewValidatorFromPEM(pemBytes []byte) (*Validator, error) {
	pub, err := DecodePublicKeyPEM(pemBytes)
	if err != nil {
		return nil, err
	}
	return NewValidator(pub), nil
}

// Validate parses and verifies an access-token string. It enforces the RS256
// signature, a present and unexpired exp, and that the token is an *access*
// token (refresh tokens are never accepted on the request path). The validated
// claims are returned on success.
func (v *Validator) Validate(tokenString string) (*Claims, error) {
	return v.validateTyped(tokenString, TokenTypeAccess)
}

// ValidateRefresh parses and verifies a *refresh* token. Used by the issuing
// service's /refresh path to confirm a refresh token's signature before
// consulting its server-side store. Refresh tokens are never accepted on the
// API request path (that is Validate's job).
func (v *Validator) ValidateRefresh(tokenString string) (*Claims, error) {
	return v.validateTyped(tokenString, TokenTypeRefresh)
}

// validateTyped verifies the signature/expiry and asserts the token type and a
// non-empty tenant claim.
func (v *Validator) validateTyped(tokenString, expectedType string) (*Claims, error) {
	claims := &Claims{}
	_, err := v.parser.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		// Belt-and-suspenders alg pin: WithValidMethods already rejects
		// non-RS256, but assert the concrete RSA method here too so the public
		// key is never handed to an HMAC verifier.
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method %q", t.Header["alg"])
		}
		return v.publicKey, nil
	})
	if err != nil {
		return nil, err
	}
	if claims.TokenType != expectedType {
		return nil, fmt.Errorf("auth: token type %q does not match expected %q", claims.TokenType, expectedType)
	}
	if claims.Tenant == "" {
		return nil, fmt.Errorf("auth: token has no tenant claim")
	}
	return claims, nil
}
