// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/sourcelimit"
	"github.com/devicechain-io/dc-user-management/iam"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Native multi-factor authentication for PASSWORD sign-ins: the sign-in flow.
//
// The shape, in one paragraph. When a correct password belongs to an identity with an active
// factor, Login mints NOTHING: no session record, no token. It writes a short-lived challenge
// and fails with the error code MFA_CHALLENGE, carrying the challenge handle in the error's
// extensions. verifyMfa(challenge, code) then checks the code and only then starts the session,
// with the "amr" of what was presented, and mints the identity token. The second factor therefore
// gates the MINT, which matters because the admin plane never consults a session record: an
// identity token that exists is valid there until it expires.
//
// `amr` is recorded on the SESSION RECORD, by startSession, and every later token reads it from
// there (see sessionRecord.Amr).

const (
	// MfaChallengeTTL is how long a password sign-in waits for its code.
	MfaChallengeTTL = 5 * time.Minute
	// MfaChallengeAttempts is how many codes one challenge evaluates before it is void.
	MfaChallengeAttempts = 5
	// MfaRecoveryCodeCount is how many recovery codes an enrolment (or a regeneration) issues.
	MfaRecoveryCodeCount = 10
	// MfaTOTPSkew is the number of 30-second steps either side of now a code is accepted for.
	MfaTOTPSkew = 1

	// maxMfaChallengeLen and maxMfaCodeLen bound what is hashed or compared before any lookup.
	maxMfaChallengeLen = 128
	maxMfaCodeLen      = 64
	// mfaUnknownSource is the lock key of a request whose source address could not be read.
	// Every such request shares it, which fails toward locking them out together, not toward
	// each getting a fresh allowance.
	mfaUnknownSource = "unknown"
)

// The labels of the `login` audit row, which is written when a sign-in COMPLETES.
const (
	mfaLoginLabelPassword = "pwd"
	mfaLoginLabelOTP      = "pwd+otp"
	mfaLoginLabelRecovery = "pwd+recovery"
)

// The wire codes of the second-factor refusals. The set is closed (and pinned by a test): a wrong
// code, a lock and a replay are all the uniform ErrMfaInvalidCode, with no code of their own.
const (
	// CodeMfaChallenge: the password was right and a second factor is needed to finish.
	CodeMfaChallenge = "MFA_CHALLENGE"
	// CodeMfaRequired: the tenant requires a second factor and this sign-in did not use one.
	CodeMfaRequired = "MFA_REQUIRED"
	// CodeMfaEnrollmentRequired: the same, and the identity has no factor to present yet.
	CodeMfaEnrollmentRequired = "MFA_ENROLLMENT_REQUIRED"
	// CodeMfaChallengeExpired: the challenge is unknown, expired, used up or outlived by the
	// password. The person signs in again.
	CodeMfaChallengeExpired = "MFA_CHALLENGE_EXPIRED"
	// CodeMfaAlreadyEnrolled: the identity already has an active factor.
	CodeMfaAlreadyEnrolled = "MFA_ALREADY_ENROLLED"
	// CodeMfaNotEnrolled: regenerating the recovery codes or turning the factor off, for an
	// identity that has no active factor.
	CodeMfaNotEnrolled = "MFA_NOT_ENROLLED"
	// CodeMfaResetNeedsPassword is the operator reset's refusal while failures are outstanding
	// (admin plane only).
	CodeMfaResetNeedsPassword = "MFA_RESET_NEEDS_PASSWORD"
)

// MfaCodes is the data plane's closed set of second-factor error codes.
func MfaCodes() []string {
	return []string{CodeMfaChallenge, CodeMfaRequired, CodeMfaEnrollmentRequired,
		CodeMfaChallengeExpired, CodeMfaAlreadyEnrolled, CodeMfaNotEnrolled}
}

// MfaError is a second-factor refusal that carries its wire code.
type MfaError struct {
	code string
	msg  string
}

func (e *MfaError) Error() string { return e.msg }

// Code is the refusal's wire code.
func (e *MfaError) Code() string { return e.code }

// Extensions gives the refusal its wire code (graphql-go copies it onto `extensions`).
func (e *MfaError) Extensions() map[string]any { return map[string]any{"code": e.code} }

var (
	// ErrMfaRequired is selectTenant's and refresh's refusal of a password sign-in that did not
	// use the second factor a tenant requires, for an identity that has one.
	ErrMfaRequired error = &MfaError{CodeMfaRequired,
		"this tenant requires a second factor; sign in again and enter your code"}
	// ErrMfaEnrollmentRequired is the same refusal for an identity with no factor yet.
	ErrMfaEnrollmentRequired error = &MfaError{CodeMfaEnrollmentRequired,
		"this tenant requires a second factor; set up two-factor authentication first, then sign in again"}
	// ErrMfaChallengeExpired is verifyMfa's refusal of a challenge it cannot use.
	ErrMfaChallengeExpired error = &MfaError{CodeMfaChallengeExpired,
		"the sign-in has expired or can no longer be completed; sign in again"}
	// ErrMfaNotEnabled refuses regenerating codes or disabling for an identity with no factor.
	ErrMfaNotEnabled error = &MfaError{CodeMfaNotEnrolled,
		"two-factor authentication is not set up for this account"}
	// ErrMfaAlreadyEnrolled refuses beginning an enrolment while a factor is active.
	ErrMfaAlreadyEnrolled error = &MfaError{CodeMfaAlreadyEnrolled,
		"two-factor authentication is already set up; turn it off first to set it up again"}

	// ErrMfaInvalidCode is the ONE answer to a code that is wrong, replayed, malformed, or
	// refused because a lock is armed. They are indistinguishable on purpose: a lock that
	// announced itself would tell a guesser which attempts were being evaluated.
	ErrMfaInvalidCode = errors.New("invalid code")
	// ErrMfaRateLimited is verifyMfa's refusal when its source sends too many attempts.
	ErrMfaRateLimited = errors.New("too many attempts; try again shortly")
	// ErrMfaUnavailable is the refusal when the second factor could not be checked because a
	// store failed. Its text carries no cause (it is returned verbatim); nothing was minted.
	ErrMfaUnavailable = errors.New("the second factor could not be checked right now; try again")
)

// errMfaRequired and errMfaEnrollmentRequired are admitPasswordSession's refusals. Each IS an
// errTenantAccessDenied, so every grant point that does not name them treats them as the denial
// it always was; SelectTenant and Refresh turn them into the typed errors above.
var (
	errMfaRequired           = fmt.Errorf("%w: the tenant requires a second factor", errTenantAccessDenied)
	errMfaEnrollmentRequired = fmt.Errorf("%w: the tenant requires a second factor and the identity has none", errTenantAccessDenied)
)

// mfaRefusalError is the typed error for a tenant-admission refusal that is about the second
// factor, or nil for any other. Named only to a member (SelectTenant and Refresh refuse a
// non-member first), so it tells nobody anything about a tenant they do not belong to.
func mfaRefusalError(err error) error {
	switch {
	case errors.Is(err, errMfaEnrollmentRequired):
		return ErrMfaEnrollmentRequired
	case errors.Is(err, errMfaRequired):
		return ErrMfaRequired
	}
	return nil
}

// MfaChallengeError is Login's answer for an identity with a second factor: the password was
// right, no token was minted, and Challenge completes the sign-in through verifyMfa.
type MfaChallengeError struct {
	// Challenge is the opaque handle verifyMfa takes. It is the only copy: the store keeps a
	// hash of it.
	Challenge string
	ExpiresAt time.Time
}

func (e *MfaChallengeError) Error() string {
	return "a second factor is required to finish signing in; send the challenge and a code to verifyMfa"
}

// Extensions carries the challenge to the client.
func (e *MfaChallengeError) Extensions() map[string]any {
	return map[string]any{
		"code":      CodeMfaChallenge,
		"challenge": e.Challenge,
		"expiresAt": e.ExpiresAt.UTC().Format(time.RFC3339),
		"methods":   []string{"TOTP", "RECOVERY_CODE"},
	}
}

// MfaMetrics receives the second factor's counters. The Manager works with none installed.
type MfaMetrics interface {
	// Verification counts one outcome of a code check. method is "totp" or "recovery"; result
	// is ok, invalid, replay, locked, expired, budget, rate_limited or unavailable.
	Verification(method, result string)
	// Challenge counts a password sign-in held for its second factor.
	Challenge()
	// Enrollment counts begun, confirmed, disabled or reset.
	Enrollment(result string)
	// IdentityLock counts an attempt that armed the identity-wide lock: evidence the password
	// is in use by someone failing the second factor.
	IdentityLock()
}

// SetMfaStore installs the bucket the sign-ins waiting for their second factor live in.
func (m *Manager) SetMfaStore(kv nats.KeyValue) { m.mfaKV = kv }

// SetMfaMetrics installs the counters.
func (m *Manager) SetMfaMetrics(mm MfaMetrics) { m.mfaMetrics = mm }

// SetMfaIssuer installs the source of the label an authenticator app shows for this instance.
func (m *Manager) SetMfaIssuer(issuer func(ctx context.Context) string) { m.mfaIssuer = issuer }

func (m *Manager) countMfa(method, result string) {
	if m.mfaMetrics != nil {
		m.mfaMetrics.Verification(method, result)
	}
}

func (m *Manager) countMfaEnrollment(result string) {
	if m.mfaMetrics != nil {
		m.mfaMetrics.Enrollment(result)
	}
}

// verifyMfa's own limiter: a person retries a few times, a script cannot sustain more than one
// attempt every two seconds from one address, and a replica takes at most a few dozen a second.
// It sits in front of the budget and the locks, which are what actually bound guessing.
var (
	mfaVerifyPerSourceLimits = sourcelimit.Limits{Burst: 5, Every: 2 * time.Second, MaxKeys: ssoLimiterMaxSources}
	mfaVerifyGlobalLimits    = sourcelimit.Limits{Burst: 200, Every: 100 * time.Millisecond, MaxKeys: 1}
)

func newMfaVerifyLimiter(opts ...sourcelimit.Option) beginLimiter {
	return beginLimiter{
		perSource: sourcelimit.Must(mfaVerifyPerSourceLimits, opts...),
		global:    sourcelimit.Must(mfaVerifyGlobalLimits, opts...),
	}
}

// mfaChallenge is what a challenge entry holds. It names the owner and the session epoch the
// password was verified under -- NOT a session: there is none until the code is accepted -- and
// counts the codes tried.
type mfaChallenge struct {
	Email    string    `json:"email"`
	Epoch    string    `json:"epoch"`
	Attempts int       `json:"attempts"`
	Issued   time.Time `json:"issued"`
}

// mfaChallengeKey is the entry's key: a hash of the handle, so the bucket never holds the
// secret the client presents.
func mfaChallengeKey(handle string) string {
	sum := sha256.Sum256([]byte(handle))
	return hex.EncodeToString(sum[:])
}

// beginMfaChallenge holds a password sign-in for its second factor and returns the error that
// tells the client so. It is reached only AFTER the password was verified, which is what makes
// the second factor's locks safe to charge: a code is never evaluated for a challenge that was
// not earned. With no bucket, or one that cannot be written, it FAILS CLOSED -- nothing was
// minted, and the person is told to try again.
func (m *Manager) beginMfaChallenge(ctx context.Context, id *iam.Identity) error {
	if m.mfaKV == nil {
		log.Error().Str("email", id.Email).
			Msg("An identity with a second factor signed in, but no challenge store is configured; the sign-in was refused.")
		return ErrMfaUnavailable
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return ErrMfaUnavailable
	}
	handle := base64.RawURLEncoding.EncodeToString(raw)
	now := m.clock().UTC()
	b, err := json.Marshal(mfaChallenge{Email: id.Email, Epoch: id.SessionEpoch, Issued: now})
	if err != nil {
		return ErrMfaUnavailable
	}
	if _, err := m.mfaKV.Create(mfaChallengeKey(handle), b); err != nil {
		log.Error().Err(err).Msg("Could not store a second-factor challenge; the sign-in was refused.")
		return ErrMfaUnavailable
	}
	m.recordAuthLabeled(ctx, rdb.AuditOpMfaChallenged, id.Email, "", "")
	if m.mfaMetrics != nil {
		m.mfaMetrics.Challenge()
	}
	return &MfaChallengeError{Challenge: handle, ExpiresAt: now.Add(MfaChallengeTTL)}
}

// mfaSourceKey is the per-source lock key of a request: an IPv4 address as itself, an IPv6
// address as its /64. An address that cannot be read keys a bucket every such request shares.
func mfaSourceKey(source string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(source))
	if err != nil {
		return mfaUnknownSource
	}
	key, err := iam.MfaSourceKey(addr)
	if err != nil {
		return mfaUnknownSource
	}
	return key
}

// compactCode removes the spaces an authenticator app puts in the middle of a code ("123 456").
func compactCode(code string) string {
	return strings.ReplaceAll(strings.TrimSpace(code), " ", "")
}

// codeMethod names how a presented code will be checked, for metrics and for choosing the path.
func codeMethod(code string) string {
	if credential.IsTOTPCodeShape(code) {
		return "totp"
	}
	if _, ok := credential.NormalizeRecoveryCode(code); ok {
		return "recovery"
	}
	return "totp"
}

// checkSecondFactor is THE evaluation of a presented second-factor code against an identity's
// active factor: a TOTP code through the charge-first locks, a recovery code through the
// single-use redemption. It runs only inside a credential.OneTime check (ot), which spent the
// request's budget. It reports the result label ("ok", "invalid", "replay", "locked") or an
// error when the check could not be made at all; a refusal, whatever its label, is the one
// ErrMfaInvalidCode to the caller.
func (m *Manager) checkSecondFactor(ctx context.Context, ot credential.OneTime, id *iam.Identity,
	factor *iam.MfaFactor, code, source string) (method, result string, err error) {
	method = codeMethod(code)
	if method == "totp" && credential.IsTOTPCodeShape(code) {
		res, err := m.iam.MfaVerifyTOTPAttempt(ctx, m.secrets, factor.ID, mfaSourceKey(source),
			func(seed []byte) (int64, bool, error) {
				step, ok := ot.VerifyTOTP(seed, code, m.clock(), MfaTOTPSkew)
				return step, ok, nil
			})
		if err != nil {
			log.Error().Err(err).Msg("A second-factor attempt could not be checked.")
			return method, "unavailable", err
		}
		result = res.Outcome.String()
		if res.IdentityLockArmed {
			if m.mfaMetrics != nil {
				m.mfaMetrics.IdentityLock()
			}
			m.recordAuthLabeled(ctx, rdb.AuditOpMfaLocked, id.Email, "", "")
		}
		// A refused attempt is journalled only when it was EVALUATED; one refused by a lock is
		// counted and nothing more, so a sender holding the password cannot drive writes to the
		// audit log.
		if result == "invalid" || result == "replay" {
			m.recordAuthLabeled(ctx, rdb.AuditOpMfaFailed, id.Email, "", "")
		}
		return method, result, nil
	}
	if method == "recovery" {
		norm, _ := credential.NormalizeRecoveryCode(code)
		digest, derr := credential.RecoveryCodeDigest(identityKey(id), norm)
		if derr != nil {
			return method, "invalid", nil
		}
		won, err := m.iam.RedeemMfaRecoveryCode(ctx, id.ID, digest, m.clock())
		if err != nil {
			log.Error().Err(err).Msg("A recovery code could not be checked.")
			return method, "unavailable", err
		}
		if !won {
			m.recordAuthLabeled(ctx, rdb.AuditOpMfaFailed, id.Email, "", "")
			return method, "invalid", nil
		}
		return method, "ok", nil
	}
	// Neither shape: nothing to evaluate, and nothing was charged.
	return method, "invalid", nil
}

// identityKey is the string an identity's recovery-code digests are bound to.
func identityKey(id *iam.Identity) string { return strconv.FormatUint(uint64(id.ID), 10) }

// mfaVerified is what a verification attempt leaves behind for VerifyMfa to act on.
type mfaVerified struct {
	fail   error
	id     *iam.Identity
	method string
}

// VerifyMfa completes a password sign-in that Login held for its second factor. challenge is the
// handle Login's MFA_CHALLENGE error carried, code a TOTP code or a recovery code. source is the
// caller's address, for the limiter and the per-source lock.
//
// The order is the contract. The source limiter first, then the request's credential budget (so
// fifty aliased copies in one document evaluate one), and only inside it: the challenge lookup,
// the owner's epoch (a password reset or a disable since Login voids every outstanding challenge),
// the challenge's attempt count (charged BEFORE the code is judged, by a revision-checked write so
// concurrent attempts cannot both spend the same count), the code itself, and finally a
// revision-checked delete of the entry, so exactly one sign-in comes out of one challenge. The
// session is started, and the token minted, only after all of that.
func (m *Manager) VerifyMfa(ctx context.Context, challenge, code, source string) (*IdentityAuth, error) {
	if m.credentials == nil {
		return nil, errNoCredentialChecker
	}
	code = compactCode(code)
	method := codeMethod(code)
	if !m.mfaLimiter.allow(source) {
		m.countMfa(method, "rate_limited")
		return nil, ErrMfaRateLimited
	}
	if m.mfaKV == nil {
		m.countMfa(method, "unavailable")
		return nil, ErrMfaUnavailable
	}
	if len(challenge) == 0 || len(challenge) > maxMfaChallengeLen {
		m.countMfa(method, "expired")
		return nil, ErrMfaChallengeExpired
	}
	if len(code) == 0 || len(code) > maxMfaCodeLen {
		m.countMfa(method, "invalid")
		return nil, ErrMfaInvalidCode
	}

	var out mfaVerified
	err := m.credentials.CheckOneTime(ctx, credential.KindTotp, func(ctx context.Context, ot credential.OneTime) error {
		out = m.verifyChallenge(ctx, ot, challenge, code, source)
		switch {
		case out.fail == nil:
			return nil
		case errors.Is(out.fail, ErrMfaUnavailable):
			return fmt.Errorf("%w: the challenge store or the database failed", credential.ErrUnavailable)
		}
		return credential.ErrMismatch
	})
	switch {
	case err == nil:
	case errors.Is(err, credential.ErrRequestBudgetExhausted):
		m.countMfa(method, "budget")
		return nil, err
	case out.fail != nil:
		return nil, out.fail
	default:
		return nil, err
	}

	amr := auth.AmrPasswordOTP()
	label := mfaLoginLabelOTP
	if out.method == "recovery" {
		amr = auth.AmrPasswordRecovery()
		label = mfaLoginLabelRecovery
	}
	// The rows are written only once the sign-in has happened: a mint that failed is not a login.
	ia, err := m.completeSignIn(ctx, out.id, amr)
	if err != nil {
		return nil, err
	}
	if out.method == "recovery" {
		m.recordAuthLabeled(ctx, rdb.AuditOpMfaRecoveryCodeUsed, out.id.Email, "", "")
	}
	m.recordAuthLabeled(ctx, rdb.AuditOpLogin, out.id.Email, "", label)
	return ia, nil
}

// verifyChallenge is VerifyMfa's body, run inside a budgeted one-time check. It returns the
// identity and method on success, or the error to answer with.
func (m *Manager) verifyChallenge(ctx context.Context, ot credential.OneTime, challenge, code, source string) mfaVerified {
	method := codeMethod(code)
	fail := func(result string, err error) mfaVerified {
		m.countMfa(method, result)
		return mfaVerified{fail: err}
	}
	key := mfaChallengeKey(challenge)
	entry, err := m.mfaKV.Get(key)
	if err != nil {
		if errors.Is(err, nats.ErrKeyNotFound) {
			return fail("expired", ErrMfaChallengeExpired)
		}
		log.Error().Err(err).Msg("Could not read a second-factor challenge.")
		return fail("unavailable", ErrMfaUnavailable)
	}
	var ch mfaChallenge
	if json.Unmarshal(entry.Value(), &ch) != nil {
		m.dropChallenge(key)
		return fail("expired", ErrMfaChallengeExpired)
	}
	// 🔴 THE LIFETIME IS READ FROM THE ENTRY, not left to the bucket: every attempt rewrites the
	// entry, and a rewrite restarts the bucket's own expiry, so five attempts would stretch five
	// minutes to twenty-five. The issue time never moves.
	if m.clock().Sub(ch.Issued) > MfaChallengeTTL {
		m.dropChallenge(key)
		return fail("expired", ErrMfaChallengeExpired)
	}
	id, err := m.iam.IdentityByEmail(ctx, ch.Email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			m.dropChallenge(key)
			return fail("expired", ErrMfaChallengeExpired)
		}
		log.Error().Err(err).Msg("Could not load the identity of a second-factor challenge.")
		return fail("unavailable", ErrMfaUnavailable)
	}
	// A password reset, a disable or a delete since Login changed the epoch: the password this
	// challenge was earned with is no longer the identity's, so the challenge dies with it.
	if !id.Enabled || id.SessionEpoch == "" || id.SessionEpoch != ch.Epoch {
		m.dropChallenge(key)
		return fail("expired", ErrMfaChallengeExpired)
	}
	factor, err := m.iam.ActiveMfaFactorFor(ctx, id.ID)
	if err != nil {
		if errors.Is(err, iam.ErrMfaFactorNotFound) {
			m.dropChallenge(key)
			return fail("expired", ErrMfaChallengeExpired)
		}
		log.Error().Err(err).Msg("Could not load the factor of a second-factor challenge.")
		return fail("unavailable", ErrMfaUnavailable)
	}
	// The attempt is charged to the challenge before the code is judged. The write is revision
	// checked, so two concurrent attempts cannot both take the same count; the loser is told
	// the code is invalid and nothing of its was evaluated.
	if ch.Attempts >= MfaChallengeAttempts {
		m.dropChallenge(key)
		return fail("expired", ErrMfaChallengeExpired)
	}
	ch.Attempts++
	b, _ := json.Marshal(ch)
	rev, err := m.mfaKV.Update(key, b, entry.Revision())
	if err != nil {
		if errors.Is(err, nats.ErrKeyRevisionMismatch) {
			return fail("invalid", ErrMfaInvalidCode)
		}
		log.Error().Err(err).Msg("Could not record a second-factor attempt.")
		return fail("unavailable", ErrMfaUnavailable)
	}

	_, result, err := m.checkSecondFactor(ctx, ot, id, factor, code, source)
	if err != nil {
		return fail("unavailable", ErrMfaUnavailable)
	}
	if result != "ok" {
		m.countMfa(method, result)
		return mfaVerified{fail: ErrMfaInvalidCode}
	}
	// Exactly one sign-in per challenge: the entry is deleted at the revision this attempt wrote.
	// Another attempt that got in after it moved the revision, and this one then signs nobody in.
	if err := m.mfaKV.Delete(key, nats.LastRevision(rev)); err != nil {
		if errors.Is(err, nats.ErrKeyRevisionMismatch) || errors.Is(err, nats.ErrKeyNotFound) {
			return fail("expired", ErrMfaChallengeExpired)
		}
		log.Error().Err(err).Msg("Could not consume a second-factor challenge.")
		return fail("unavailable", ErrMfaUnavailable)
	}
	m.countMfa(method, "ok")
	return mfaVerified{id: id, method: method}
}

// dropChallenge removes a challenge that can no longer be used. Best effort: it expires with
// the bucket either way.
func (m *Manager) dropChallenge(key string) {
	if err := m.mfaKV.Delete(key); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
		log.Warn().Err(err).Msg("Could not remove a second-factor challenge; it expires with the bucket.")
	}
}
