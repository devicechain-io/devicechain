// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 specifies HMAC-SHA1 as the interoperable default every authenticator app implements
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// This file is the second-factor primitive: an RFC 6238 time-based one-time password
// and the single-use recovery codes that stand in for it.
//
// 🔴 IT IS A PRIMITIVE, NOT A POLICY. Everything that decides whether an attempt may be
// EVALUATED at all — the per-identity and per-source locks, the attempt count, the
// replay compare-and-set on the last accepted step — lives in the owning service's
// database, where it can be a single charge-first statement. What lives here is the
// part that must be the same everywhere: how a code is derived, that it is compared in
// constant time, and that the compare is reachable only inside a request-budgeted
// CheckOneTime.

// TOTP parameters. They are fixed, not configurable: the otpauth:// URI carries them to
// the authenticator app, and several apps silently ignore anything but these.
const (
	// TOTPDigits is the length of a code.
	TOTPDigits = 6
	// TOTPPeriod is the length of one time step.
	TOTPPeriod = 30 * time.Second
	// TOTPSeedBytes is the length of a seed: 160 bits, the RFC 4226 recommendation for
	// HMAC-SHA1.
	TOTPSeedBytes = 20
	// TOTPSkew is the number of steps either side of the current one a code is accepted
	// from. The docs require NTP on the nodes.
	TOTPSkew = 1

	// RecoveryCodeBytes is the entropy of a recovery code: 80 bits.
	RecoveryCodeBytes = 10
	// RecoveryCodesPerSet is how many codes an enrolment hands out.
	RecoveryCodesPerSet = 10

	// recoveryDigestDomain separates a recovery-code digest from every other SHA-256 in
	// the system, and versions the construction.
	recoveryDigestDomain = "dc-mfa-recovery/v1"
	// recoveryCodeChars is the length of a code with its separators stripped: 80 bits in
	// base32 is exactly 16 characters.
	recoveryCodeChars = 16
)

// ErrOneTimeKind is returned by Check for a kind that is verified through CheckOneTime,
// and by CheckOneTime for a kind that is not.
var ErrOneTimeKind = errors.New("credential: the kind is checked through CheckOneTime, not Check")

// OneTime is the capability CheckOneTime hands its attempt function: the one-time-secret
// compares. It can be obtained only there, and it is valid only for the duration of that
// call: CheckOneTime disarms the shared state it points at when attempt returns, so a copy
// kept past the call (stored, sent on a channel, captured by a goroutine) compares nothing.
// A code can therefore be compared only inside a check that spent the request's
// credential-check budget, once per budget unit.
type OneTime struct{ s *oneTimeState }

// oneTimeState is the per-call arming flag every copy of one OneTime shares.
type oneTimeState struct{ armed atomic.Bool }

func (ot OneTime) live() bool { return ot.s != nil && ot.s.armed.Load() }

// The zero OneTime, which an outside package can construct, and any copy kept after its
// CheckOneTime call returned, compare nothing: every method answers "no match".

// CheckOneTime is Check for a one-time secret whose attempt state is not the Checker's.
//
// A TOTP code is guessable (10⁶ values, three accepted at once), so its throttle cannot
// be the Checker's soft per-principal backoff, which a password success clears and which
// fails open when its bucket is full. The owning service charges durable locks in its
// database BEFORE evaluating, and runs the compare inside attempt through the OneTime it
// is handed. What CheckOneTime adds is what every check gets: the request's budget is
// spent first, before anything is read, and the outcome is counted.
//
// attempt returns nil for a good code and ErrMismatch for a wrong, replayed or locked one
// (the caller answers all three identically). Any other error is a failure to decide.
func (c *Checker) CheckOneTime(ctx context.Context, kind Kind, attempt func(ctx context.Context, ot OneTime) error) error {
	if !spend(ctx) {
		err := &RequestBudgetError{}
		c.observe(kind, err, false)
		return err
	}
	return c.runOneTime(ctx, kind, attempt)
}

// runOneTime arms a OneTime for the duration of attempt. The caller has already spent the
// request's budget.
func (c *Checker) runOneTime(ctx context.Context, kind Kind, attempt func(ctx context.Context, ot OneTime) error) error {
	if _, ok := c.policies[kind]; !ok {
		return fmt.Errorf("%w: %q", ErrUndeclaredKind, kind)
	}
	if !comparators[kind].oneTime {
		return fmt.Errorf("%w: %q", ErrOneTimeKind, kind)
	}
	st := &oneTimeState{}
	st.armed.Store(true)
	defer st.armed.Store(false)
	err := attempt(ctx, OneTime{s: st})
	c.observe(kind, err, false)
	return err
}

// CheckPasswordAndOneTime is ONE re-authentication that presents two secrets: a password,
// checked exactly as Check checks it, and then a one-time code, checked as CheckOneTime
// checks it. It is for the operations that must see both -- confirming an enrolment,
// replacing the recovery codes, switching the factor off -- and it spends ONE unit of the
// request's budget for the pair.
//
// 🔴 WHY ONE UNIT, AND WHY THIS IS NOT A WIDENING. A budget exists to bound the BCRYPT
// compares a request can buy (see budget.go), and this pair makes one: the one-time code
// is an HMAC behind charge-first durable locks, not a hashing cost. Spending two units
// instead would make the pair impossible under the default budget of one, and a budget can
// be tightened but never widened, so there is no way to ask for two. Aliasing the
// mutation N times in one document still buys exactly one pair: the second and later
// copies are refused before anything is read.
//
// The one-time attempt runs ONLY when the password matched, so a wrong password evaluates
// no code and charges no lock. Any password failure (a mismatch, a throttle, an
// unavailable store) is returned as Check returns it.
func (c *Checker) CheckPasswordAndOneTime(ctx context.Context, p Principal, password string,
	lookup func(context.Context) (string, error), kind Kind,
	attempt func(ctx context.Context, ot OneTime) error) error {
	if !spend(ctx) {
		err := &RequestBudgetError{}
		c.observe(p.Kind, err, false)
		return err
	}
	failedOpen, err := c.check(ctx, p, password, lookup)
	c.observe(p.Kind, err, failedOpen)
	if err != nil {
		return err
	}
	return c.runOneTime(ctx, kind, attempt)
}

// VerifyTOTP checks code against the seed at time now, accepting the current step and
// skew steps either side of it. It returns the step the code matched, which the caller
// must record in a compare-and-set (last_step < step) so the same code cannot be used
// twice.
//
// All 2*skew+1 candidates are computed and compared in constant time whatever the
// outcome, so the time taken does not say which step matched or how close a wrong code
// was. A code that is not exactly TOTPDigits ASCII digits matches nothing.
func (ot OneTime) VerifyTOTP(seed []byte, code string, now time.Time, skew int) (step int64, ok bool) {
	if !ot.live() || len(seed) == 0 {
		return 0, false
	}
	if skew < 0 {
		skew = 0
	}
	current := totpStep(now)
	matched, found := 0, 0
	for s := current - int64(skew); s <= current+int64(skew); s++ {
		if s < 0 {
			continue
		}
		want := totpCode(seed, s)
		m := constantTimeCompare([]byte(want), []byte(code))
		// Ascending, so the latest matching step wins: a replay record that advanced as
		// far as possible is the safer one to keep. Selected, not branched on.
		matched = subtle.ConstantTimeSelect(m, int(s), matched)
		found = subtle.ConstantTimeSelect(m, 1, found)
	}
	return int64(matched), found == 1
}

// MatchRecoveryDigest reports which of stored equals presented, comparing every entry in
// constant time and not stopping at the first match. Digests are what RecoveryCodeDigest
// returns. The service that stores them looks a presented code up by an equality
// predicate on the digest (the codes carry 80 bits, so the digest needs no salt); this
// is the check for a caller that has loaded the identity's digests instead.
func (ot OneTime) MatchRecoveryDigest(presented string, stored []string) (index int, ok bool) {
	if !ot.live() {
		return -1, false
	}
	index = -1
	for i, d := range stored {
		if constantTimeCompare([]byte(d), []byte(presented)) == 1 {
			index = i
		}
	}
	return index, index >= 0
}

// totpStep is the RFC 6238 time-step counter T = floor(unix / period).
// It floors, so a time before the epoch lands in the step below zero rather than the one
// above it (Go's integer division truncates toward zero).
func totpStep(t time.Time) int64 {
	u, p := t.Unix(), int64(TOTPPeriod/time.Second)
	q := u / p
	if u%p < 0 {
		q--
	}
	return q
}

// totpCode is the RFC 4226 HOTP value for the counter, truncated to TOTPDigits.
func totpCode(seed []byte, counter int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(sha1.New, seed)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < TOTPDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", TOTPDigits, bin%mod)
}

// GenerateTOTP is the code an authenticator would show for the seed at time now. It is
// for tests and for tools that stand in for an authenticator; verification is OneTime.
func GenerateTOTP(seed []byte, now time.Time) string { return totpCode(seed, totpStep(now)) }

// TOTPStepAt is the time step containing t, the unit VerifyTOTP returns.
func TOTPStepAt(t time.Time) int64 { return totpStep(t) }

// NewTOTPSeed returns a fresh random seed.
func NewTOTPSeed() ([]byte, error) {
	seed := make([]byte, TOTPSeedBytes)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("credential: generate a TOTP seed: %w", err)
	}
	return seed, nil
}

// recoveryEncoding is Crockford's base32 alphabet: no I, L, O or U, so a code read off a
// printout has no look-alike pair. NormalizeRecoveryCode applies Crockford's decode mapping
// (O to 0, I and L to 1) to what a person types.
var recoveryEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// seedEncoding is the unpadded base32 every authenticator app accepts.
var seedEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// EncodeTOTPSeed is the seed as shown for manual entry and carried in the otpauth URI.
func EncodeTOTPSeed(seed []byte) string { return seedEncoding.EncodeToString(seed) }

// OTPAuthURI builds the otpauth://totp URI an authenticator app enrols from:
//
//	otpauth://totp/<Issuer>:<account>?secret=…&issuer=<Issuer>&algorithm=SHA1&digits=6&period=30
//
// issuer is the instance's display name and account the identity's email. Both are
// escaped, the colon that separates them in the label is the only one left literal, and
// an empty issuer or account is refused rather than producing a label an app misreads.
func OTPAuthURI(issuer, account string, seed []byte) (string, error) {
	issuer, account = strings.TrimSpace(issuer), strings.TrimSpace(account)
	if issuer == "" || account == "" {
		return "", errors.New("credential: an otpauth URI needs an issuer and an account")
	}
	if len(seed) == 0 {
		return "", errors.New("credential: an otpauth URI needs a seed")
	}
	label := uriEscape(issuer) + ":" + uriEscape(account)
	q := url.Values{}
	q.Set("secret", EncodeTOTPSeed(seed))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(TOTPDigits))
	q.Set("period", fmt.Sprint(int(TOTPPeriod/time.Second)))
	// url.Values.Encode sorts keys and escapes spaces as "+", which several apps read
	// literally in an issuer; build the query in the documented order with %20.
	var b strings.Builder
	for _, k := range []string{"secret", "issuer", "algorithm", "digits", "period"} {
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k + "=" + uriEscape(q.Get(k)))
	}
	return "otpauth://totp/" + label + "?" + b.String(), nil
}

// uriEscape percent-encodes everything but RFC 3986 unreserved characters, spaces as %20.
func uriEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// NewRecoveryCodes returns n fresh recovery codes, each 80 random bits written as four
// groups of four Crockford base32 characters (ABCD-EFGH-JKMN-PQRS). The set is shown to the user
// once; only RecoveryCodeDigest of each is stored.
func NewRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		raw := make([]byte, RecoveryCodeBytes)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("credential: generate a recovery code: %w", err)
		}
		c := recoveryEncoding.EncodeToString(raw) // 10 bytes -> exactly 16 characters
		codes = append(codes, c[0:4]+"-"+c[4:8]+"-"+c[8:12]+"-"+c[12:16])
	}
	return codes, nil
}

// NormalizeRecoveryCode reduces what a person typed to the canonical form that is
// digested: separators and spaces removed, upper-cased, and Crockford's look-alikes folded
// (O to 0, I and L to 1). The fold applies the same way when a code is generated and when
// one is presented, because the digest is always of the normalized form. ok is false for anything that
// cannot be a recovery code (wrong length or alphabet), so a TOTP code, which is six
// digits, is told apart from a recovery code by shape before either is evaluated.
func NormalizeRecoveryCode(input string) (code string, ok bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(input) {
		switch {
		case r == '-' || r == ' ':
			continue
		case r == 'O':
			b.WriteByte('0')
		case r == 'I' || r == 'L':
			b.WriteByte('1')
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z' && r != 'U':
			b.WriteRune(r)
		default:
			return "", false
		}
	}
	if b.Len() != recoveryCodeChars {
		return "", false
	}
	return b.String(), true
}

// IsTOTPCodeShape reports whether input is exactly TOTPDigits ASCII digits.
func IsTOTPCodeShape(input string) bool {
	if len(input) != TOTPDigits {
		return false
	}
	for i := 0; i < len(input); i++ {
		if input[i] < '0' || input[i] > '9' {
			return false
		}
	}
	return true
}

// RecoveryCodeDigest is hex(SHA-256("dc-mfa-recovery/v1" ‖ identityID ‖ code)) of the
// NORMALIZED code. The code carries 80 bits, so the digest needs no per-row salt and no
// key, which is what lets the store find it with an equality lookup. identityID binds the
// digest to its owner, so the same code text held by two identities digests differently
// and one identity's code can never redeem another's. The code is fixed-width once
// normalized, so the concatenation is unambiguous.
func RecoveryCodeDigest(identityID, code string) (string, error) {
	norm, ok := NormalizeRecoveryCode(code)
	if !ok {
		return "", errors.New("credential: not a recovery code")
	}
	if identityID == "" {
		return "", errors.New("credential: a recovery code digest needs an identity id")
	}
	h := sha256.New()
	h.Write([]byte(recoveryDigestDomain))
	h.Write([]byte(identityID))
	h.Write([]byte(norm))
	return hex.EncodeToString(h.Sum(nil)), nil
}
