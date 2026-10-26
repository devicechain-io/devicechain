// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The authentication-method references a DeviceChain token can carry in its "amr"
// claim (RFC 8176 values).
const (
	// AmrPassword: the sign-in presented a password.
	AmrPassword = "pwd"
	// AmrOTP: the sign-in presented a TOTP code.
	AmrOTP = "otp"
	// AmrMFA: the sign-in passed a second factor. Policy ("this tenant requires MFA")
	// checks for THIS value and nothing else, so a recovery-code sign-in, which proves a
	// second factor but not an OTP, satisfies it.
	AmrMFA = "mfa"
)

// AmrPasswordOnly is the claim of a password sign-in with no second factor.
func AmrPasswordOnly() []string { return []string{AmrPassword} }

// AmrPasswordOTP is the claim of a password sign-in completed with a TOTP code.
func AmrPasswordOTP() []string { return []string{AmrPassword, AmrOTP, AmrMFA} }

// AmrPasswordRecovery is the claim of a password sign-in completed with a recovery
// code: a second factor, but not an OTP.
func AmrPasswordRecovery() []string { return []string{AmrPassword, AmrMFA} }

// ErrMalformedAmr is returned for an "amr" claim that is not one of the three shapes this
// system writes, or that rides a token that may not carry one.
var ErrMalformedAmr = errors.New("malformed amr claim")

// MintOption adjusts a token mint. The variadic form keeps every existing call of the
// issue methods valid: a mint that is given no option writes no "amr", exactly as before.
type MintOption func(*tokenSpec)

// WithAmr stamps the "amr" claim on a token minted from a sign-in session. The value must
// come from the session RECORD the caller loaded, never from a request or a presented
// token: a refresh must carry the amr of the sign-in it descends from, not one it
// asserts. An invalid value makes the mint fail (ErrMalformedAmr) rather than write a
// claim the validator would then refuse.
func WithAmr(amr []string) MintOption {
	return func(s *tokenSpec) { s.amr = slices.Clone(amr); s.amrSet = true }
}

// Mfa reports whether the token's sign-in passed a second factor.
func (c *Claims) Mfa() bool {
	return c != nil && slices.Contains(c.Amr, AmrMFA)
}

// validAmrShape reports whether amr is exactly one of the three lists this system writes.
func validAmrShape(amr []string) bool {
	for _, ok := range [][]string{AmrPasswordOnly(), AmrPasswordOTP(), AmrPasswordRecovery()} {
		if slices.Equal(amr, ok) {
			return true
		}
	}
	return false
}

// validateAmrClaim is the parse-time rule for the "amr" claim, applied to every token by
// Validator.Parse beside validateScopeClaim.
//
// 🔴 A MACHINE CREDENTIAL NEVER CARRIES amr. A service token and a service account's
// token are not a person who signed in, and a second-factor claim on one would be a
// forgery that policy ("MFA required" checks for amr=mfa) could mistake for proof. So the
// claim is refused on those, and on the refresh-less tiers that never get one, rather
// than ignored. The only tiers that may carry it are the three a sign-in session mints:
// access, refresh and identity. A present-but-empty or unrecognised list is refused too,
// never read as "no second factor".
func validateAmrClaim(c *Claims) error {
	if c.Amr == nil {
		return nil
	}
	switch c.TokenType {
	case TokenTypeAccess, TokenTypeRefresh, TokenTypeIdentity:
	default:
		return fmt.Errorf("%w: a %q token may not carry amr", ErrMalformedAmr, c.TokenType)
	}
	if strings.HasPrefix(c.Username, ServiceAccountSubjectPrefix) || strings.HasPrefix(c.Subject, ServiceAccountSubjectPrefix) {
		return fmt.Errorf("%w: a service account token may not carry amr", ErrMalformedAmr)
	}
	if !validAmrShape(c.Amr) {
		return fmt.Errorf("%w: %v is not a value this system issues", ErrMalformedAmr, c.Amr)
	}
	return nil
}
