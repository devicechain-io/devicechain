// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rsa"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
)

// signingMethod is the one and only accepted JWT signing algorithm. Pinning it
// (rather than trusting the token header's "alg") is the defense against the
// classic algorithm-confusion attack, where an attacker re-signs a token with
// HS256 using the RSA *public* key as the HMAC secret. We reject anything that
// is not RS256 both via WithValidMethods and an explicit keyfunc type assertion.
const signingMethod = "RS256"

// Validator verifies DeviceChain tokens against the platform's RSA public keys.
// It holds no private key. It keeps a set of public keys indexed by key id (kid,
// the RFC 7638 thumbprint) so verification survives a signing-key rotation: a
// token names its signing key in the "kid" header and the validator selects the
// matching public key. When the validator was built from a remote JWKS, an
// unknown kid triggers a single throttled refetch so a rotated-in key is picked
// up without a restart. Verification is otherwise local — no per-request network
// call (ADR-008).
type Validator struct {
	parser *jwt.Parser

	mu   sync.RWMutex
	keys map[string]*rsa.PublicKey
	// refresh, when non-nil, re-fetches the key set on a cache miss (an unknown
	// kid). nil for validators built from a static key set (e.g. the issuing
	// service validating its own tokens).
	refresh     func() (map[string]*rsa.PublicKey, error)
	minInterval time.Duration
	lastRefresh time.Time
}

// newValidator builds a Validator over a key set with an optional refresher.
func newValidator(keys map[string]*rsa.PublicKey, refresh func() (map[string]*rsa.PublicKey, error), minInterval time.Duration) *Validator {
	return &Validator{
		keys:        keys,
		refresh:     refresh,
		minInterval: minInterval,
		// Require exp, reject non-RS256 algorithms, and validate expiry.
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{signingMethod}),
			jwt.WithExpirationRequired(),
		),
	}
}

// NewValidator builds a Validator from a single parsed RSA public key, indexed by
// its thumbprint. Suitable for the issuing service validating its own tokens.
func NewValidator(pub *rsa.PublicKey) *Validator {
	return newValidator(map[string]*rsa.PublicKey{Thumbprint(pub): pub}, nil, 0)
}

// NewValidatorFromKeys builds a Validator from a set of public keys already keyed
// by their thumbprint (e.g. the issuing service's active plus retained keys).
func NewValidatorFromKeys(keys map[string]*rsa.PublicKey) *Validator {
	return newValidator(keys, nil, 0)
}

// NewRefreshingValidator builds a Validator that re-fetches its key set (via
// refresh) when it encounters an unknown kid, no more than once per minInterval.
func NewRefreshingValidator(initial map[string]*rsa.PublicKey, refresh func() (map[string]*rsa.PublicKey, error), minInterval time.Duration) *Validator {
	return newValidator(initial, refresh, minInterval)
}

// NewValidatorFromPEM builds a Validator from a single PKIX PEM public key.
func NewValidatorFromPEM(pemBytes []byte) (*Validator, error) {
	pub, err := DecodePublicKeyPEM(pemBytes)
	if err != nil {
		return nil, err
	}
	return NewValidator(pub), nil
}

// SetKeys replaces the validator's key set in place. The issuing service calls
// this after a rotation to publish the new key set to a validator whose pointer
// is already held by request handlers (so the handlers see the new keys without
// being rewired).
func (v *Validator) SetKeys(keys map[string]*rsa.PublicKey) {
	v.mu.Lock()
	v.keys = keys
	v.mu.Unlock()
}

// Validate parses and verifies an access-token string. It enforces the RS256
// signature, a present and unexpired exp, and that the token is an *access*
// token (refresh tokens are never accepted on the request path). The validated
// claims are returned on success.
func (v *Validator) Validate(tokenString string) (*Claims, error) {
	return v.validateTyped(tokenString, TokenTypeAccess, true)
}

// ValidateIdentity parses and verifies an *identity*-tier token (ADR-033): the
// instance-scoped tier the admin API and the tenant-selection exchange accept. It
// asserts the identity token type but, unlike the data-plane path, does NOT
// require a tenant claim (an identity token has none).
func (v *Validator) ValidateIdentity(tokenString string) (*Claims, error) {
	return v.validateTyped(tokenString, TokenTypeIdentity, false)
}

// ValidateRefresh parses and verifies a *refresh* token. Used by the issuing
// service's /refresh path to confirm a refresh token's signature before
// consulting its server-side store. Refresh tokens are never accepted on the
// API request path (that is Validate's job).
func (v *Validator) ValidateRefresh(tokenString string) (*Claims, error) {
	return v.validateTyped(tokenString, TokenTypeRefresh, true)
}

// ValidateService parses and verifies a *service*-tier token (ADR-044
// amendment): the instance-scoped machine credential a service presents on a
// synchronous cross-service call. Like an identity token it carries no tenant
// (the caller passes the tenant explicitly), so requireTenant is false; the
// callee gates the call on the token's authorities.
func (v *Validator) ValidateService(tokenString string) (*Claims, error) {
	return v.validateTyped(tokenString, TokenTypeService, false)
}

// Parse verifies a token's signature and expiry (pinned RS256, present exp) and
// returns its claims WITHOUT asserting a token type or a tenant. The data-plane
// handler uses it to admit either an access token (tenant from the signed claim)
// or a service token (tenant from an explicit header) through one verified path;
// the type-asserting Validate* helpers are the right choice everywhere a single
// tier is expected.
func (v *Validator) Parse(tokenString string) (*Claims, error) {
	claims := &Claims{}
	if _, err := v.parser.ParseWithClaims(tokenString, claims, v.keyfunc); err != nil {
		return nil, err
	}
	return claims, nil
}

// validateTyped verifies the signature/expiry and asserts the token type. When
// requireTenant is set it also asserts a non-empty tenant claim (the data-plane
// invariant); the instance-scoped identity/service tiers pass requireTenant=false.
func (v *Validator) validateTyped(tokenString, expectedType string, requireTenant bool) (*Claims, error) {
	claims, err := v.Parse(tokenString)
	if err != nil {
		return nil, err
	}
	if claims.TokenType != expectedType {
		return nil, fmt.Errorf("auth: token type %q does not match expected %q", claims.TokenType, expectedType)
	}
	if requireTenant && claims.Tenant == "" {
		return nil, fmt.Errorf("auth: token has no tenant claim")
	}
	return claims, nil
}

// keyfunc selects the verification key for a token by its kid header, pinning the
// RS256 method, and refetches the key set once (throttled) on an unknown kid.
func (v *Validator) keyfunc(t *jwt.Token) (interface{}, error) {
	// Belt-and-suspenders alg pin: WithValidMethods already rejects non-RS256,
	// but assert the concrete RSA method here too so a public key is never handed
	// to an HMAC verifier.
	if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
		return nil, fmt.Errorf("auth: unexpected signing method %q", t.Header["alg"])
	}
	kid, _ := t.Header["kid"].(string)
	if key := v.lookup(kid); key != nil {
		return key, nil
	}
	if key := v.tryRefresh(kid); key != nil {
		return key, nil
	}
	return nil, fmt.Errorf("auth: no verification key for kid %q", kid)
}

// lookup returns the public key for a kid, or nil. A token with no kid is only
// honored when exactly one key is known (defensive: tokens this codebase mints
// always carry a kid).
func (v *Validator) lookup(kid string) *rsa.PublicKey {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if kid != "" {
		return v.keys[kid]
	}
	if len(v.keys) == 1 {
		for _, k := range v.keys {
			return k
		}
	}
	return nil
}

// tryRefresh refetches the key set at most once per minInterval and returns the
// key for kid if the refresh produced it. The throttle is updated before the
// fetch so a burst of tokens bearing unknown kids cannot stampede the endpoint.
func (v *Validator) tryRefresh(kid string) *rsa.PublicKey {
	// Fast path under the read lock: a flood of tokens bearing bogus kids stays
	// off the writer, so forged tokens cannot serialize all validation on it.
	v.mu.RLock()
	throttled := v.refresh == nil || time.Since(v.lastRefresh) < v.minInterval
	v.mu.RUnlock()
	if throttled {
		return nil
	}

	v.mu.Lock()
	// Re-check under the write lock — another goroutine may have refreshed since.
	if v.refresh == nil || time.Since(v.lastRefresh) < v.minInterval {
		v.mu.Unlock()
		return nil
	}
	v.lastRefresh = time.Now()
	refresh := v.refresh
	v.mu.Unlock()

	keys, err := refresh()
	if err != nil || len(keys) == 0 {
		log.Warn().Err(err).Msg("JWKS refresh on unknown kid failed; keeping existing keys.")
		return nil
	}
	v.mu.Lock()
	v.keys = keys
	v.mu.Unlock()
	return v.lookup(kid)
}
