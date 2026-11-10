// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
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

// WithSessionID stamps the "sid" claim on a token that does not carry one by construction (an
// OAuth ACCESS token). The value must come from the server-side session record the grant
// descends from, never from a request. An empty value writes no claim: a missing sid means
// "unknown" and is never read as a match (SameSession).
func WithSessionID(sid SessionID) MintOption {
	return func(s *tokenSpec) { s.sessionID = sid }
}

// WithMfaTime stamps the "mfa_time" claim: when the sign-in last verified a second factor. Like
// WithAmr, the value must come from the session RECORD, never from a request or a presented token,
// so a refresh carries the time of the verification it descends from and never advances it. A
// zero time writes no claim. The mint fails if the token's amr does not prove a second factor.
func WithMfaTime(at time.Time) MintOption {
	return func(s *tokenSpec) {
		if at.IsZero() {
			s.mfaTime = nil
			return
		}
		t := at.UTC()
		s.mfaTime = &t
	}
}

// StepUp is what a sensitive mutation demands of the caller's second factor.
type StepUp struct {
	// MaxAge is how recently the factor must have been verified.
	MaxAge time.Duration
	// AcceptRecoveryCodes admits a sign-in completed with a recovery code (amr ["pwd","mfa"]).
	// Off, only a TOTP verification (amr containing "otp") counts.
	AcceptRecoveryCodes bool
}

// stepUpClockSkew tolerates a mfa_time slightly ahead of this replica's clock.
const stepUpClockSkew = 30 * time.Second

// StepUpFresh reports whether claims prove a second factor verified no longer than su.MaxAge
// before now.
//
// 🔴 FRESHNESS IS mfa_time AND NOTHING ELSE. A token's iat is when it was MINTED, and a refresh
// mints a new one every few minutes without anyone presenting a factor, so reading iat would let a
// sign-in from last week pass a fifteen-minute step-up. A token with no mfa_time (minted before the
// claim existed, or from a sign-in without a second factor) is not fresh.
//
// It also requires an ACCESS token: an identity, refresh or service token never authorizes a
// tenant mutation, and the amr rule already keeps a machine credential from carrying a factor.
func StepUpFresh(c *Claims, su StepUp, now time.Time) bool {
	if c == nil || c.TokenType != TokenTypeAccess {
		return false
	}
	// A step-up is a PERSON proving presence at their own session. An agent-origin token (an
	// OAuth client's, a service account's) never counts, whatever it carries.
	if IsAgentOrigin(c) {
		return false
	}
	var at *time.Time
	if c.MfaTime != nil {
		at = &c.MfaTime.Time
	}
	return StepUpFreshAt(c.Amr, at, su, now)
}

// StepUpFreshAt checks a recorded second factor under the current policy and clock.
func StepUpFreshAt(amr []string, mfaTime *time.Time, su StepUp, now time.Time) bool {
	if mfaTime == nil || su.MaxAge <= 0 {
		return false
	}
	switch {
	case slices.Contains(amr, AmrOTP):
	case su.AcceptRecoveryCodes && slices.Contains(amr, AmrMFA):
	default:
		return false
	}
	at := *mfaTime
	if at.After(now.Add(stepUpClockSkew)) {
		return false
	}
	return now.Sub(at) <= su.MaxAge
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
	// mfa_time is a statement about the second factor, so it may ride only beside an amr that
	// proves one: alone, or beside a password-only amr, it is a forgery.
	if c.MfaTime != nil && !slices.Contains(c.Amr, AmrMFA) {
		return fmt.Errorf("%w: mfa_time without a second-factor amr", ErrMalformedAmr)
	}
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
