// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"net/http"
	"strings"
)

// BearerToken reads an RFC 6750 credential from the request headers.
func BearerToken(h http.Header) (string, bool) { return BearerFromValue(h.Get("Authorization")) }

// BearerFromValue accepts a case-insensitive scheme and trims the token.
func BearerFromValue(v string) (string, bool) {
	token, _, _ := bearerFromValue(v)
	return token, token != ""
}

var errEmptyCredential = errors.New("auth: whitespace-only bearer credential")

func bearerFromValue(v string) (string, bool, error) {
	const prefix = "Bearer "
	if len(v) < len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return "", false, nil
	}
	raw := v[len(prefix):]
	token := strings.TrimSpace(raw)
	if raw != "" && token == "" {
		return "", v[:len(prefix)] == prefix, errEmptyCredential
	}
	return token, true, nil
}

// TokenVerifier verifies the signature, expiry and tier before returning claims.
type TokenVerifier func(token string, header func(string) string) (*Claims, string, error)

var ErrValidatorUnavailable = errors.New("auth: validator not available")

// AccessVerifier resolves the current validator for each authentication attempt.
func AccessVerifier(src func() *Validator) TokenVerifier {
	return func(token string, _ func(string) string) (*Claims, string, error) {
		if src == nil {
			return nil, "", ErrValidatorUnavailable
		}
		v := src()
		if v == nil {
			return nil, "", ErrValidatorUnavailable
		}
		c, err := v.Validate(token)
		if err != nil {
			return nil, "", err
		}
		return c, c.Tenant, nil
	}
}

type BearerRefusal int

const (
	BearerMissing BearerRefusal = iota + 1
	BearerUnavailable
	BearerInvalid
)

// BearerError keeps transport shaping at the caller. Invalid signatures and refused
// audiences deliberately share one reason.
type BearerError struct {
	Reason          BearerRefusal
	cause           error
	canonicalScheme bool
}

func (e *BearerError) Error() string { return "auth: bearer authentication refused" }
func (e *BearerError) Unwrap() error { return e.cause }

// EmptyCredential distinguishes a whitespace-only credential from an absent one.
// Both are BearerMissing; transports use this only to preserve existing refusals.
func (e *BearerError) EmptyCredential() bool { return errors.Is(e.cause, errEmptyCredential) }

// CanonicalScheme reports whether an empty credential used the exact "Bearer"
// spelling. Branding historically rejected only that spelling as an invalid token.
func (e *BearerError) CanonicalScheme() bool { return e.canonicalScheme }

type BearerAuth struct {
	Plane  Plane
	Verify TokenVerifier
	Policy func() *PlanePolicy
}
type Authenticated struct {
	Claims        *Claims
	Tenant, Token string
}

// Authenticate never writes a response.
func (a BearerAuth) Authenticate(r *http.Request) (Authenticated, error) {
	return a.AuthenticateValue(r.Header.Get("Authorization"), r.Header.Get)
}

// AuthenticateValue parses a bearer value, retaining empty-credential metadata
// for transports whose existing refusal differs from an absent credential.
func (a BearerAuth) AuthenticateValue(value string, header func(string) string) (Authenticated, error) {
	token, canonical, cause := bearerFromValue(value)
	if cause != nil {
		return Authenticated{}, &BearerError{Reason: BearerMissing, cause: cause, canonicalScheme: canonical}
	}
	return a.AuthenticateToken(token, header)
}

// AuthenticateToken always verifies before admitting; unverified tokens cannot
// affect the audience recorder. A nil policy is the closed policy.
func (a BearerAuth) AuthenticateToken(token string, header func(string) string) (Authenticated, error) {
	raw := token
	token = strings.TrimSpace(token)
	if token == "" {
		var cause error
		if raw != "" {
			cause = errEmptyCredential
		}
		return Authenticated{}, &BearerError{Reason: BearerMissing, cause: cause}
	}
	if a.Verify == nil {
		return Authenticated{}, &BearerError{Reason: BearerUnavailable, cause: ErrValidatorUnavailable}
	}
	if header == nil {
		header = func(string) string { return "" }
	}
	claims, tenant, err := a.Verify(token, header)
	if err != nil {
		reason := BearerInvalid
		if errors.Is(err, ErrValidatorUnavailable) {
			reason = BearerUnavailable
		}
		return Authenticated{}, &BearerError{Reason: reason, cause: err}
	}
	var p *PlanePolicy
	if a.Policy != nil {
		p = a.Policy()
	}
	if err := p.Admit(claims, a.Plane); err != nil {
		return Authenticated{}, &BearerError{Reason: BearerInvalid, cause: err}
	}
	return Authenticated{Claims: claims, Tenant: tenant, Token: token}, nil
}
