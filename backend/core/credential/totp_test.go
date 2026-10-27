// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// rfcSeed is the RFC 6238 Appendix B SHA-1 shared secret, ASCII "12345678901234567890".
var rfcSeed = []byte("12345678901234567890")

// totpChecker is a Checker declaring KindTotp, the way an owning service builds one.
func totpChecker(t *testing.T) (*credential.Checker, *prometheus.CounterVec) {
	t.Helper()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "checks_total"}, []string{"kind", "outcome"})
	c, err := credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindTotp: {Unthrottled: true}},
		credential.WithCounter(counter))
	require.NoError(t, err)
	return c, counter
}

// verify runs VerifyTOTP the only way it can be run: inside a checked attempt.
func verify(t *testing.T, c *credential.Checker, seed []byte, code string, now time.Time, skew int) (step int64, ok bool) {
	t.Helper()
	err := c.CheckOneTime(context.Background(), credential.KindTotp, func(_ context.Context, ot credential.OneTime) error {
		step, ok = ot.VerifyTOTP(seed, code, now, skew)
		if !ok {
			return credential.ErrMismatch
		}
		return nil
	})
	if ok {
		require.NoError(t, err)
	} else {
		require.ErrorIs(t, err, credential.ErrMismatch)
	}
	return step, ok
}

// T1. RFC 6238 Appendix B, the SHA-1 rows. The RFC lists 8-digit codes; ours are 6, the
// low six digits of the same truncated integer, so each vector's last six digits are the
// expected code. The twin flips one seed bit and must mismatch.
func TestTOTPMatchesRFC6238AppendixBSHA1(t *testing.T) {
	vectors := []struct {
		unix int64
		code string // the RFC's 8-digit TOTP, last six digits
	}{
		{59, "287082"},          // 94287082
		{1111111109, "081804"},  // 07081804
		{1111111111, "050471"},  // 14050471
		{1234567890, "005924"},  // 89005924
		{2000000000, "279037"},  // 69279037
		{20000000000, "353130"}, // 65353130
	}
	c, _ := totpChecker(t)
	for _, v := range vectors {
		at := time.Unix(v.unix, 0)
		assert.Equal(t, v.code, credential.GenerateTOTP(rfcSeed, at), "T=%d", v.unix)
		step, ok := verify(t, c, rfcSeed, v.code, at, 1)
		require.True(t, ok, "T=%d", v.unix)
		assert.Equal(t, v.unix/30, step, "T=%d", v.unix)

		flipped := append([]byte(nil), rfcSeed...)
		flipped[0] ^= 0x01
		_, ok = verify(t, c, flipped, v.code, at, 1)
		assert.False(t, ok, "T=%d accepted under a seed with one flipped bit", v.unix)
	}
}

// T5. The window is exactly ±1 step: a code from step −2 or +2 is refused, −1, 0 and +1
// are accepted and report the step they matched (the replay compare-and-set needs it).
func TestTOTPSkewBoundaries(t *testing.T) {
	c, _ := totpChecker(t)
	now := time.Unix(1111111109, 0)
	cur := now.Unix() / 30
	for delta := int64(-3); delta <= 3; delta++ {
		code := credential.GenerateTOTP(rfcSeed, time.Unix((cur+delta)*30, 0))
		step, ok := verify(t, c, rfcSeed, code, now, credential.TOTPSkew)
		inside := delta >= -1 && delta <= 1
		require.Equal(t, inside, ok, "delta %d", delta)
		if inside {
			assert.Equal(t, cur+delta, step, "delta %d", delta)
		}
	}
	// Skew 0 narrows to the current step only.
	prev := credential.GenerateTOTP(rfcSeed, time.Unix((cur-1)*30, 0))
	_, ok := verify(t, c, rfcSeed, prev, now, 0)
	assert.False(t, ok)
}

// A code is exactly six ASCII digits; anything else matches nothing, however close.
func TestTOTPRefusesMalformedCodes(t *testing.T) {
	c, _ := totpChecker(t)
	now := time.Unix(59, 0)
	good := credential.GenerateTOTP(rfcSeed, now)
	_, ok := verify(t, c, rfcSeed, good, now, 1)
	require.True(t, ok, "control")
	for _, bad := range []string{"", good[:5], good + "0", " " + good, good + " ", "12345a"} {
		_, ok := verify(t, c, rfcSeed, bad, now, 1)
		assert.False(t, ok, "%q", bad)
	}
	_, ok = verify(t, c, nil, good, now, 1)
	assert.False(t, ok, "an empty seed verifies nothing")
}

// Every candidate step is compared, in constant time, whatever the outcome: a match on
// the first, a miss, a wrong-length code. An early exit would let the time taken say how
// close a guess was.
func TestTOTPComparesEveryCandidateInConstantTime(t *testing.T) {
	c, _ := totpChecker(t)
	var compares int
	restore := credential.RecordConstantTimeCompareLengths(func(a, b int) { compares++ })
	defer restore()
	now := time.Unix(1111111109, 0)
	cur := now.Unix() / 30
	for name, code := range map[string]string{
		"match at the earliest step": credential.GenerateTOTP(rfcSeed, time.Unix((cur-1)*30, 0)),
		"match at the current step":  credential.GenerateTOTP(rfcSeed, now),
		"no match":                   "000000",
		"wrong length":               "12",
	} {
		compares = 0
		verify(t, c, rfcSeed, code, now, 1)
		assert.Equal(t, 3, compares, name)
	}
}

// The zero OneTime, the only one an outside package can build, compares nothing: the
// compare is reachable only inside CheckOneTime.
func TestOneTimeIsUsableOnlyInsideCheckOneTime(t *testing.T) {
	var ot credential.OneTime
	now := time.Unix(59, 0)
	_, ok := ot.VerifyTOTP(rfcSeed, credential.GenerateTOTP(rfcSeed, now), now, 1)
	assert.False(t, ok)
	_, ok = ot.MatchRecoveryDigest("a", []string{"a"})
	assert.False(t, ok)
}

// CheckOneTime spends the request's budget first, counts the outcome, and refuses the
// kinds that are not its own — and Check refuses KindTotp, so the one-time compare has
// exactly one door.
func TestCheckOneTimeBudgetKindsAndOutcomes(t *testing.T) {
	c, counter := totpChecker(t)
	ran := 0
	attempt := func(err error) func(context.Context, credential.OneTime) error {
		return func(context.Context, credential.OneTime) error { ran++; return err }
	}

	ctx := credential.WithRequestBudget(context.Background(), 2)
	require.NoError(t, c.CheckOneTime(ctx, credential.KindTotp, attempt(nil)))
	require.ErrorIs(t, c.CheckOneTime(ctx, credential.KindTotp, attempt(credential.ErrMismatch)), credential.ErrMismatch)
	err := c.CheckOneTime(ctx, credential.KindTotp, attempt(nil))
	require.ErrorIs(t, err, credential.ErrRequestBudgetExhausted)
	var be *credential.RequestBudgetError
	require.True(t, errors.As(err, &be))
	assert.Equal(t, 2, ran, "an attempt past the budget never runs")
	assert.Equal(t, 1.0, testutil.ToFloat64(counter.WithLabelValues("totp", credential.OutcomeSuccess)))
	assert.Equal(t, 1.0, testutil.ToFloat64(counter.WithLabelValues("totp", credential.OutcomeMismatch)))
	assert.Equal(t, 1.0, testutil.ToFloat64(counter.WithLabelValues("totp", credential.OutcomeRequestBudget)))

	// A failure to decide is not a mismatch.
	boom := errors.New("db down")
	require.ErrorIs(t, c.CheckOneTime(context.Background(), credential.KindTotp, attempt(boom)), boom)
	assert.Equal(t, 1.0, testutil.ToFloat64(counter.WithLabelValues("totp", credential.OutcomeError)))

	// Check is not the door for this kind.
	require.ErrorIs(t, c.Check(context.Background(), credential.Principal{Kind: credential.KindTotp, ID: "a@example.com"},
		"123456", func(context.Context) (string, error) { return "x", nil }), credential.ErrOneTimeKind)
	// And CheckOneTime is not the door for a password kind.
	pw, err := credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindIdentity: testPolicy})
	require.NoError(t, err)
	ran = 0
	require.ErrorIs(t, pw.CheckOneTime(context.Background(), credential.KindIdentity, attempt(nil)), credential.ErrOneTimeKind)
	// An undeclared kind is refused too.
	require.ErrorIs(t, c.CheckOneTime(context.Background(), credential.KindIdentity, attempt(nil)), credential.ErrUndeclaredKind)
	assert.Zero(t, ran)
}

// KindTotp keeps no Checker state, so a throttled policy for it is refused rather than
// silently ignored.
func TestKindTotpMustBeUnthrottled(t *testing.T) {
	_, err := credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindTotp: testPolicy})
	require.ErrorIs(t, err, credential.ErrMustBeUnthrottled)
}

// The otpauth URI carries the documented parameters, with the issuer and account
// escaped so a hostile display name cannot add or smuggle a parameter.
func TestOTPAuthURI(t *testing.T) {
	seed := rfcSeed
	got, err := credential.OTPAuthURI("Acme Fleet", "a@example.com", seed)
	require.NoError(t, err)
	assert.Equal(t, "otpauth://totp/Acme%20Fleet:a%40example.com?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"+
		"&issuer=Acme%20Fleet&algorithm=SHA1&digits=6&period=30", got)

	hostile, err := credential.OTPAuthURI("Evil&secret=AAAA:#?/ x", "u+1@example.com", seed)
	require.NoError(t, err)
	u, err := url.Parse(hostile)
	require.NoError(t, err)
	assert.Equal(t, "otpauth", u.Scheme)
	assert.Equal(t, "totp", u.Host)
	q := u.Query()
	assert.Equal(t, credential.EncodeTOTPSeed(seed), q.Get("secret"), "an issuer cannot replace the seed")
	assert.Len(t, q["secret"], 1)
	assert.Equal(t, "Evil&secret=AAAA:#?/ x", q.Get("issuer"))
	// The label's only literal colon is the separator.
	label := strings.TrimPrefix(u.EscapedPath(), "/")
	assert.Equal(t, 1, strings.Count(label, ":"), label)
	assert.Contains(t, label, "u%2B1%40example.com")

	for _, bad := range [][2]string{{"", "a@example.com"}, {"Acme", ""}, {"  ", "a@example.com"}} {
		_, err := credential.OTPAuthURI(bad[0], bad[1], seed)
		assert.Error(t, err, "%q", bad)
	}
	_, err = credential.OTPAuthURI("Acme", "a@example.com", nil)
	assert.Error(t, err)
}

func TestNewTOTPSeedIsTwentyRandomBytes(t *testing.T) {
	a, err := credential.NewTOTPSeed()
	require.NoError(t, err)
	b, err := credential.NewTOTPSeed()
	require.NoError(t, err)
	assert.Len(t, a, 20)
	assert.NotEqual(t, a, b)
	assert.Len(t, credential.EncodeTOTPSeed(a), 32, "20 bytes is 32 unpadded base32 characters")
}

// T8 (primitive half). A recovery code is 80 bits in a fixed, typeable shape, and a set
// has no repeats.
func TestRecoveryCodeFormatAndEntropy(t *testing.T) {
	codes, err := credential.NewRecoveryCodes(credential.RecoveryCodesPerSet)
	require.NoError(t, err)
	require.Len(t, codes, 10)
	seen := map[string]bool{}
	for _, code := range codes {
		require.Regexp(t, `^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`, code)
		norm, ok := credential.NormalizeRecoveryCode(code)
		require.True(t, ok)
		require.Len(t, norm, 16, "16 base32 characters is exactly 80 bits")
		require.False(t, seen[code], "repeat")
		seen[code] = true
	}
	// Over a larger sample every character position draws from the whole alphabet, which
	// a generator that fixed or truncated bits would not.
	sample, err := credential.NewRecoveryCodes(400)
	require.NoError(t, err)
	var distinct [16]map[byte]bool
	for i := range distinct {
		distinct[i] = map[byte]bool{}
	}
	for _, code := range sample {
		norm, _ := credential.NormalizeRecoveryCode(code)
		for i := 0; i < 16; i++ {
			distinct[i][norm[i]] = true
		}
	}
	for i, d := range distinct {
		assert.Greater(t, len(d), 20, "position %d draws from %d symbols of 32", i, len(d))
	}
}

func TestNormalizeRecoveryCode(t *testing.T) {
	for _, in := range []string{"abcd-efgh-jkmn-pqrs", "ABCDEFGHJKMNPQRS", " abcd efgh jkmn pqrs ", "ABCD-EFGH-JKMN-PQRS"} {
		got, ok := credential.NormalizeRecoveryCode(in)
		require.True(t, ok, in)
		assert.Equal(t, "ABCDEFGHJKMNPQRS", got)
	}
	// Crockford's look-alikes fold: O is 0, and I and L are 1.
	for in, want := range map[string]string{"0O0O-1I1L-0000-1111": "0000111100001111", "olil-0o0o-abcd-efgh": "0111000 0ABCDEFGH"} {
		got, ok := credential.NormalizeRecoveryCode(in)
		require.True(t, ok, in)
		assert.Equal(t, strings.ReplaceAll(want, " ", ""), got, in)
	}
	// U is not in the alphabet and is not folded into anything.
	for _, in := range []string{"", "123456", "ABCD-EFGH-JKMN-PQR", "ABCD-EFGH-JKMN-PQRST", "ABCD-EFGH-JKMN-PQRU", "ABCD-EFGH-JKMN-PQR!"} {
		_, ok := credential.NormalizeRecoveryCode(in)
		assert.False(t, ok, in)
	}
	assert.True(t, credential.IsTOTPCodeShape("012345"))
	for _, in := range []string{"", "12345", "1234567", "12345a", " 12345"} {
		assert.False(t, credential.IsTOTPCodeShape(in), in)
	}
}

// Domain separation: the digest binds the code to its identity, so a code held by A
// does not match B's digest; typing it differently (case, dashes) changes nothing; and a
// digest is hex SHA-256 with no plaintext in it.
func TestRecoveryCodeDigestIsBoundToItsIdentity(t *testing.T) {
	const code = "ABCD-EFGH-JKMN-PQRS"
	a, err := credential.RecoveryCodeDigest("identity-a", code)
	require.NoError(t, err)
	b, err := credential.RecoveryCodeDigest("identity-b", code)
	require.NoError(t, err)
	require.NotEqual(t, a, b, "the same code digests differently for two identities")
	require.Regexp(t, `^[0-9a-f]{64}$`, a)
	assert.NotContains(t, a, "abcd")

	again, err := credential.RecoveryCodeDigest("identity-a", "abcd efgh jkmn pqrs")
	require.NoError(t, err)
	assert.Equal(t, a, again, "how the code was typed is not part of it")

	other, err := credential.RecoveryCodeDigest("identity-a", "ABCD-EFGH-JKMN-PQRT")
	require.NoError(t, err)
	assert.NotEqual(t, a, other)

	// Known answer: SHA-256("dc-mfa-recovery/v1" || "identity-a" || "ABCDEFGHJKMNPQRS").
	assert.Equal(t, "4e7d26095040215ea60afbcc8134490ac4b4b376c29a5658a9ff90c75c3890b9", a)
	// A person typing a look-alike redeems the same code.
	assert.Equal(t, a, mustDigest(t, "identity-a", "abcd-efgh-jkmn-pqrs"))
	fold1, err := credential.RecoveryCodeDigest("identity-a", "O1L3-4567-89AB-CDEF")
	require.NoError(t, err)
	assert.Equal(t, mustDigest(t, "identity-a", "0113-4567-89AB-CDEF"), fold1)

	_, err = credential.RecoveryCodeDigest("", code)
	assert.Error(t, err)
	_, err = credential.RecoveryCodeDigest("identity-a", "123456")
	assert.Error(t, err)

	// The lookup helper matches the right identity's digest only, scanning all entries.
	var compares int
	restore := credential.RecordConstantTimeCompareLengths(func(int, int) { compares++ })
	defer restore()
	c, _ := totpChecker(t)
	err = c.CheckOneTime(context.Background(), credential.KindTotp, func(_ context.Context, ot credential.OneTime) error {
		i, ok := ot.MatchRecoveryDigest(a, []string{other, a, b})
		require.True(t, ok)
		assert.Equal(t, 1, i)
		assert.Equal(t, 3, compares, "every stored digest is compared, not just up to the match")
		_, ok = ot.MatchRecoveryDigest(b, []string{other, a})
		assert.False(t, ok, "identity B's digest is not in A's set")
		return nil
	})
	require.NoError(t, err)
}

func mustDigest(t *testing.T, id, code string) string {
	t.Helper()
	d, err := credential.RecoveryCodeDigest(id, code)
	require.NoError(t, err)
	return d
}

// 🔴 The OneTime capability does not outlive its CheckOneTime call. A copy kept past the
// call compares nothing, so an attempt function cannot stash it and keep verifying codes
// without spending the budget. Twin: the same copy matches inside the call.
func TestOneTimeDoesNotEscapeItsCheck(t *testing.T) {
	c, _ := totpChecker(t)
	now := time.Unix(59, 0)
	code := credential.GenerateTOTP(rfcSeed, now)
	digest := mustDigest(t, "identity-a", "ABCD-EFGH-JKMN-PQRS")

	var kept credential.OneTime
	err := c.CheckOneTime(context.Background(), credential.KindTotp, func(_ context.Context, ot credential.OneTime) error {
		kept = ot
		_, ok := kept.VerifyTOTP(rfcSeed, code, now, 1)
		assert.True(t, ok, "twin: valid inside the call")
		_, ok = kept.MatchRecoveryDigest(digest, []string{digest})
		assert.True(t, ok, "twin: valid inside the call")
		return nil
	})
	require.NoError(t, err)

	_, ok := kept.VerifyTOTP(rfcSeed, code, now, 1)
	assert.False(t, ok, "a kept OneTime must not verify after its check returned")
	_, ok = kept.MatchRecoveryDigest(digest, []string{digest})
	assert.False(t, ok)

	// Also disarmed when the attempt returns an error.
	err = c.CheckOneTime(context.Background(), credential.KindTotp, func(_ context.Context, ot credential.OneTime) error {
		kept = ot
		return credential.ErrMismatch
	})
	require.ErrorIs(t, err, credential.ErrMismatch)
	_, ok = kept.VerifyTOTP(rfcSeed, code, now, 1)
	assert.False(t, ok)
}

// The step is floored, so a time before the epoch is in the step below zero.
func TestTOTPStepAtFloorsBeforeTheEpoch(t *testing.T) {
	assert.Equal(t, int64(-1), credential.TOTPStepAt(time.Unix(-1, 0)))
	assert.Equal(t, int64(-1), credential.TOTPStepAt(time.Unix(-30, 0)))
	assert.Equal(t, int64(-2), credential.TOTPStepAt(time.Unix(-31, 0)))
	assert.Equal(t, int64(0), credential.TOTPStepAt(time.Unix(0, 0)))
	assert.Equal(t, int64(0), credential.TOTPStepAt(time.Unix(29, 0)))
	assert.Equal(t, int64(1), credential.TOTPStepAt(time.Unix(30, 0)))
}

// pairChecker declares a password kind and the one-time kind, the way user-management does,
// with a stored bcrypt hash for "secret" behind principal a@example.com.
func pairChecker(t *testing.T) (*credential.Checker, func(context.Context) (string, error)) {
	t.Helper()
	c, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity: testPolicy,
		credential.KindTotp:     {Unthrottled: true},
	})
	require.NoError(t, err)
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	require.NoError(t, err)
	return c, func(context.Context) (string, error) { return string(hash), nil }
}

// CheckPasswordAndOneTime is ONE re-authentication: it spends one unit of the request's
// budget for the pair, so it works under the default budget of one, and an aliased second
// copy in the same request is refused before anything is read.
// The code is evaluated only after the password matched.
func TestCheckPasswordAndOneTimeSpendsOneUnitForThePair(t *testing.T) {
	c, lookup := pairChecker(t)
	p := credential.Principal{Kind: credential.KindIdentity, ID: "a@example.com"}
	ran := 0
	attempt := func(context.Context, credential.OneTime) error { ran++; return nil }

	ctx := credential.WithRequestBudget(context.Background(), 1)
	require.NoError(t, c.CheckPasswordAndOneTime(ctx, p, "secret", lookup, credential.KindTotp, attempt))
	assert.Equal(t, 1, ran)
	// The second copy in the same request: refused, nothing evaluated.
	err := c.CheckPasswordAndOneTime(ctx, p, "secret", lookup, credential.KindTotp, attempt)
	require.ErrorIs(t, err, credential.ErrRequestBudgetExhausted)
	assert.Equal(t, 1, ran, "a copy past the budget evaluates no code")
	// And the pair shares the budget with a plain check.
	require.ErrorIs(t, c.CheckOneTime(ctx, credential.KindTotp, attempt), credential.ErrRequestBudgetExhausted)
}

func TestCheckPasswordAndOneTimeEvaluatesNoCodeForAWrongPassword(t *testing.T) {
	c, lookup := pairChecker(t)
	p := credential.Principal{Kind: credential.KindIdentity, ID: "a@example.com"}
	ran := 0
	attempt := func(context.Context, credential.OneTime) error { ran++; return nil }

	err := c.CheckPasswordAndOneTime(context.Background(), p, "wrong", lookup, credential.KindTotp, attempt)
	require.ErrorIs(t, err, credential.ErrMismatch)
	assert.Zero(t, ran, "a wrong password must not reach the code")

	// The twin: the right password reaches it, and the code's own refusal is returned.
	err = c.CheckPasswordAndOneTime(context.Background(), p, "secret", lookup, credential.KindTotp,
		func(context.Context, credential.OneTime) error { return credential.ErrMismatch })
	require.ErrorIs(t, err, credential.ErrMismatch)

	// The pair is not a door to a non-one-time kind.
	err = c.CheckPasswordAndOneTime(context.Background(), p, "secret", lookup, credential.KindIdentity, attempt)
	require.ErrorIs(t, err, credential.ErrOneTimeKind)
}
