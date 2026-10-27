// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/base32"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/sourcelimit"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// The second-factor SIGN-IN FLOW, over SQLite and real JetStream buckets. What needs a real
// Postgres (the charge-first TOTP locks, the replay compare-and-set) is in
// mfa_integration_test.go; everything here that reaches a code does it with a RECOVERY code,
// which redeems in one plain statement.

// hookKV wraps a bucket so a test can run something at the exact moment between two of the
// flow's steps: the way to test a race deterministically instead of hoping for one.
type hookKV struct {
	nats.KeyValue
	beforeUpdate func(key string)
	beforeDelete func(key string)
	failCreate   error
	failGet      error
}

func (h *hookKV) Create(key string, value []byte) (uint64, error) {
	if h.failCreate != nil {
		return 0, h.failCreate
	}
	return h.KeyValue.Create(key, value)
}

func (h *hookKV) Get(key string) (nats.KeyValueEntry, error) {
	if h.failGet != nil {
		return nil, h.failGet
	}
	return h.KeyValue.Get(key)
}

func (h *hookKV) Update(key string, value []byte, last uint64) (uint64, error) {
	if h.beforeUpdate != nil {
		hook := h.beforeUpdate
		h.beforeUpdate = nil
		hook(key)
	}
	return h.KeyValue.Update(key, value, last)
}

func (h *hookKV) Delete(key string, opts ...nats.DeleteOpt) error {
	if h.beforeDelete != nil {
		hook := h.beforeDelete
		h.beforeDelete = nil
		hook(key)
	}
	return h.KeyValue.Delete(key, opts...)
}

// jetStreamBucket starts a broker and returns one bucket on it.
func jetStreamBucket(t *testing.T, name string) nats.KeyValue {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, ServerName: "mfa-test-" + name,
		JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "the test broker never became ready")
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	bucket, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: name, TTL: MfaChallengeTTL})
	require.NoError(t, err)
	return bucket
}

// recordingMfaMetrics counts what the flow reports.
type recordingMfaMetrics struct {
	mu            sync.Mutex
	verifications map[string]int
	challenges    int
	enrollments   map[string]int
	locks         int
}

func newRecordingMfaMetrics() *recordingMfaMetrics {
	return &recordingMfaMetrics{verifications: map[string]int{}, enrollments: map[string]int{}}
}

func (r *recordingMfaMetrics) Verification(method, result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verifications[method+"/"+result]++
}
func (r *recordingMfaMetrics) Challenge()    { r.mu.Lock(); r.challenges++; r.mu.Unlock() }
func (r *recordingMfaMetrics) IdentityLock() { r.mu.Lock(); r.locks++; r.mu.Unlock() }
func (r *recordingMfaMetrics) Enrollment(res string) {
	r.mu.Lock()
	r.enrollments[res]++
	r.mu.Unlock()
}
func (r *recordingMfaMetrics) count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.verifications[key]
}

type mfaEnv struct {
	*sessionEnv
	challenges *hookKV
	metrics    *recordingMfaMetrics
}

// newMfaEnv is the session environment with everything the second factor needs, built the way
// main builds it: a checker that declares the one-time kind, the secret store, the challenge
// bucket and the counters.
func newMfaEnv(t *testing.T) *mfaEnv {
	t.Helper()
	e := newSessionEnv(t)
	generous := credential.Policy{Free: 1000, Base: time.Second, Cap: time.Minute}
	checker, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity: generous, credential.KindOAuthClient: generous,
		credential.KindTotp: {Unthrottled: true},
	})
	require.NoError(t, err)
	e.m.credentials = checker
	e.m.secrets = sealedStore(t, e)
	kvb := &hookKV{KeyValue: jetStreamBucket(t, "mfa-challenges")}
	metrics := newRecordingMfaMetrics()
	e.m.SetMfaStore(kvb)
	e.m.SetMfaMetrics(metrics)
	// The per-source limiter has its own test; every other test would otherwise trip it by
	// making more than five attempts from one address.
	roomy := sourcelimit.Limits{Burst: 100000, Every: time.Microsecond, MaxKeys: 100}
	e.m.mfaLimiter = beginLimiter{perSource: sourcelimit.Must(roomy), global: sourcelimit.Must(roomy)}
	return &mfaEnv{sessionEnv: e, challenges: kvb, metrics: metrics}
}

// enroll signs the member in with the password alone, enrols a TOTP factor through the public
// API (begin, then confirm with a code derived from the seed it handed back) and returns the
// seed and the ten recovery codes. The person is then an enrolled identity: their next sign-in
// is challenged.
func (e *mfaEnv) enroll(t *testing.T, email, password string) (seed []byte, recovery []string) {
	t.Helper()
	ctx := context.Background()
	ia, err := e.m.Login(ctx, email, password)
	require.NoError(t, err, "enrolment starts from a password-only sign-in")
	enr, err := e.m.BeginTotpEnrollment(ctx, ia.IdentityToken, password)
	require.NoError(t, err)
	seed, err = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enr.Secret)
	require.NoError(t, err)
	res, err := e.m.ConfirmTotpEnrollment(ctx, ia.IdentityToken, password, credential.GenerateTOTP(seed, time.Now()))
	require.NoError(t, err)
	recovery = res.RecoveryCodes
	require.Len(t, recovery, MfaRecoveryCodeCount)
	return seed, recovery
}

// challenge runs the password step for an enrolled identity and returns the challenge.
func (e *mfaEnv) challenge(t *testing.T, email, password string) *MfaChallengeError {
	t.Helper()
	ia, err := e.m.Login(context.Background(), email, password)
	require.Nil(t, ia, "an enrolled identity must get no token from its password")
	var ch *MfaChallengeError
	require.True(t, errors.As(err, &ch), "want MFA_CHALLENGE, got %v", err)
	return ch
}

func (e *mfaEnv) sessionKeys(t *testing.T) []string {
	t.Helper()
	keys, err := e.m.refreshKV.Keys()
	if errors.Is(err, nats.ErrNoKeysFound) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, k := range keys {
		if len(k) > 4 && (k[:4] == sessionKeyPrefix || k[:5] == sessionIndexPrefix) {
			out = append(out, k)
		}
	}
	return out
}

func (e *mfaEnv) claims(t *testing.T, token string) *auth.Claims {
	t.Helper()
	c, err := e.validator.ValidateIdentity(token)
	require.NoError(t, err)
	return c
}

func isCode(err error, code string) bool {
	var coded interface{ Extensions() map[string]any }
	return errors.As(err, &coded) && coded.Extensions()["code"] == code
}

// T2. The second factor gates the MINT: an enrolled identity with the right password is given a
// challenge and nothing else -- no session record, no index entry, no token -- and the error is
// the machine-readable MFA_CHALLENGE. The twin is the same sign-in for an identity with no
// factor, which gets its token at once, recorded as a password-only sign-in.
func TestAnEnrolledPasswordSignInMintsNothingUntilTheSecondFactor(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	e.createMember(t, "sam@example.com", "pw-2")
	ctx := context.Background()

	// Twin: no factor, token at once, amr is the password alone.
	ia, err := e.m.Login(ctx, "sam@example.com", "pw-2")
	require.NoError(t, err)
	require.Equal(t, []string{"pwd"}, e.claims(t, ia.IdentityToken).Amr)
	require.Len(t, e.sessionKeys(t), 2, "the control: a sign-in writes a record and an index entry")

	e.enroll(t, "pat@example.com", "pw-1")
	before := e.sessionKeys(t)
	loginsBefore := e.auditCount(t, rdb.AuditOpLogin)

	ch := e.challenge(t, "pat@example.com", "pw-1")
	require.Equal(t, before, e.sessionKeys(t), "a challenged sign-in must write no session record and no index entry")
	require.Equal(t, loginsBefore, e.auditCount(t, rdb.AuditOpLogin), "the login row is written when the sign-in COMPLETES")
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpMfaChallenged))
	require.Equal(t, 1, e.metrics.challenges)

	ext := ch.Extensions()
	require.Equal(t, CodeMfaChallenge, ext["code"])
	require.NotEmpty(t, ext["challenge"])
	require.Equal(t, []string{"TOTP", "RECOVERY_CODE"}, ext["methods"])
	require.NotEmpty(t, ext["expiresAt"])
	_, err = time.Parse(time.RFC3339, ext["expiresAt"].(string))
	require.NoError(t, err)

	// A wrong password is still the ordinary refusal, with no challenge.
	_, err = e.m.Login(ctx, "pat@example.com", "wrong")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpMfaChallenged), "a wrong password is not challenged")
}

// A challenge entry holds a HASH of the handle the client holds, never the handle.
func TestTheChallengeStoreHoldsOnlyAHashOfTheHandle(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	e.enroll(t, "pat@example.com", "pw-1")
	ch := e.challenge(t, "pat@example.com", "pw-1")

	keys, err := e.challenges.Keys()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.NotEqual(t, ch.Challenge, keys[0])
	require.Equal(t, mfaChallengeKey(ch.Challenge), keys[0])
	entry, err := e.challenges.KeyValue.Get(keys[0])
	require.NoError(t, err)
	require.NotContains(t, string(entry.Value()), ch.Challenge)
}

// Recovery-code sign-in completes the challenge, once, and the session it opens records
// ["pwd","mfa"] -- which every token minted from it then carries, through select, refresh and OAuth.
func TestARecoveryCodeCompletesTheSignInOnceAndRecordsItsAmr(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	ch := e.challenge(t, "pat@example.com", "pw-1")

	ia, err := e.m.VerifyMfa(ctx, ch.Challenge, recovery[0], "203.0.113.5")
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordRecovery(), e.claims(t, ia.IdentityToken).Amr)
	require.Len(t, e.sessionKeys(t), 2+2, "the enrolling sign-in plus the completed one")
	require.Equal(t, 1, e.metrics.count("recovery/ok"))
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpMfaRecoveryCodeUsed))

	pair, err := e.m.SelectTenant(ctx, ia.IdentityToken, "acme")
	require.NoError(t, err)
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordRecovery(), access.Amr)
	require.True(t, access.Mfa())
	refreshed, err := e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err)
	access, err = e.validator.Validate(refreshed.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordRecovery(), access.Amr, "a refresh keeps the amr of the sign-in it descends from")

	// One sign-in per challenge: the same challenge is gone.
	_, err = e.m.VerifyMfa(ctx, ch.Challenge, recovery[1], "203.0.113.5")
	require.True(t, isCode(err, CodeMfaChallengeExpired), "got %v", err)

	// A used recovery code is spent, on a fresh challenge.
	ch2 := e.challenge(t, "pat@example.com", "pw-1")
	_, err = e.m.VerifyMfa(ctx, ch2.Challenge, recovery[0], "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	_, err = e.m.VerifyMfa(ctx, ch2.Challenge, recovery[1], "203.0.113.5")
	require.NoError(t, err, "the control: an unused code signs in")
}

// T3 (epoch half). A password reset between login and verify voids the challenge.
func TestAPasswordResetBetweenLoginAndVerifyVoidsTheChallenge(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	ch := e.challenge(t, "pat@example.com", "pw-1")

	_, err := e.admin.SetPassword(ctx, "pat@example.com", "pw-new")
	require.NoError(t, err)

	_, err = e.m.VerifyMfa(ctx, ch.Challenge, recovery[0], "203.0.113.5")
	require.True(t, isCode(err, CodeMfaChallengeExpired), "got %v", err)
	require.Equal(t, 1, e.metrics.count("recovery/expired"))
	// The password reset did not spend the code: the twin signs in with it.
	ch2 := e.challenge(t, "pat@example.com", "pw-new")
	_, err = e.m.VerifyMfa(ctx, ch2.Challenge, recovery[0], "203.0.113.5")
	require.NoError(t, err)
}

// T6 (challenge half). A challenge evaluates five codes. The sixth is MFA_CHALLENGE_EXPIRED even
// when it is right, and a right code inside the five signs in.
func TestAChallengeEvaluatesFiveCodesAndNoMore(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	wrong := "ABCD-EFGH-JKMN-PQRS"

	ch := e.challenge(t, "pat@example.com", "pw-1")
	for i := 0; i < MfaChallengeAttempts; i++ {
		_, err := e.m.VerifyMfa(ctx, ch.Challenge, wrong, "203.0.113.9")
		require.ErrorIs(t, err, ErrMfaInvalidCode, "attempt %d", i+1)
	}
	_, err := e.m.VerifyMfa(ctx, ch.Challenge, recovery[0], "203.0.113.9")
	require.True(t, isCode(err, CodeMfaChallengeExpired), "the sixth attempt must find the challenge void, got %v", err)

	// Twin: four wrong codes and then the right one signs in.
	ch2 := e.challenge(t, "pat@example.com", "pw-1")
	for i := 0; i < MfaChallengeAttempts-1; i++ {
		_, err := e.m.VerifyMfa(ctx, ch2.Challenge, wrong, "203.0.113.9")
		require.ErrorIs(t, err, ErrMfaInvalidCode)
	}
	_, err = e.m.VerifyMfa(ctx, ch2.Challenge, recovery[0], "203.0.113.9")
	require.NoError(t, err, "the fifth attempt is still evaluated")
}

// T3 (race half), the attempt count. Another attempt on the same challenge lands between this one's
// read and its write: the revision-checked write refuses the loser BEFORE it evaluates anything,
// so one challenge cannot be spent twice by racing.
func TestAConcurrentAttemptCannotTakeTheSameChallengeCount(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	ch := e.challenge(t, "pat@example.com", "pw-1")
	sessionsBefore := len(e.sessionKeys(t))

	var inner *IdentityAuth
	var innerErr error
	e.challenges.beforeUpdate = func(string) {
		inner, innerErr = e.m.VerifyMfa(ctx, ch.Challenge, recovery[1], "203.0.113.5")
	}
	outer, err := e.m.VerifyMfa(ctx, ch.Challenge, recovery[0], "203.0.113.5")

	require.NoError(t, innerErr)
	require.NotNil(t, inner)
	require.Nil(t, outer, "the attempt that read first lost the write and signed nobody in")
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	require.Len(t, e.sessionKeys(t), sessionsBefore+2, "exactly one sign-in came out of one challenge")
	// The loser's code was never evaluated: it still works.
	ch2 := e.challenge(t, "pat@example.com", "pw-1")
	_, err = e.m.VerifyMfa(ctx, ch2.Challenge, recovery[0], "203.0.113.5")
	require.NoError(t, err)
}

// T3 (race half), the single use. A second attempt completes between this one's code being
// accepted and its challenge being consumed: the delete is revision-checked, so it signs nobody in
// and exactly one session results.
func TestTheChallengeIsConsumedAtTheRevisionThisAttemptWrote(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	ch := e.challenge(t, "pat@example.com", "pw-1")
	sessionsBefore := len(e.sessionKeys(t))

	var inner *IdentityAuth
	var innerErr error
	e.challenges.beforeDelete = func(string) {
		inner, innerErr = e.m.VerifyMfa(ctx, ch.Challenge, recovery[1], "203.0.113.5")
	}
	outer, err := e.m.VerifyMfa(ctx, ch.Challenge, recovery[0], "203.0.113.5")

	require.NoError(t, innerErr)
	require.NotNil(t, inner, "the attempt that finished inside signs in")
	require.Nil(t, outer)
	require.True(t, isCode(err, CodeMfaChallengeExpired), "got %v", err)
	require.Len(t, e.sessionKeys(t), sessionsBefore+2, "exactly one sign-in came out of one challenge")
}

// T7. With the challenge store missing or failing, an enrolled sign-in fails CLOSED: a retryable
// error, no token, no session. The twin (an identity with no factor) does not need the store.
func TestAnEnrolledSignInFailsClosedWithoutItsChallengeStore(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	e.createMember(t, "sam@example.com", "pw-2")
	e.enroll(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	sessions := len(e.sessionKeys(t))

	for name, arrange := range map[string]func(){
		"no bucket": func() { e.m.SetMfaStore(nil) },
		"failing bucket": func() {
			e.m.SetMfaStore(&hookKV{KeyValue: e.challenges.KeyValue, failCreate: errors.New("bucket full")})
		},
	} {
		arrange()
		ia, err := e.m.Login(ctx, "pat@example.com", "pw-1")
		require.ErrorIs(t, err, ErrMfaUnavailable, name)
		require.Nil(t, ia, name)
		require.Len(t, e.sessionKeys(t), sessions, "%s: nothing was minted", name)
	}
	// verifyMfa fails closed too.
	e.m.SetMfaStore(nil)
	_, err := e.m.VerifyMfa(ctx, "anything", "123456", "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaUnavailable)

	ia, err := e.m.Login(ctx, "sam@example.com", "pw-2")
	require.NoError(t, err, "the control: an identity with no factor never needs the store")
	require.NotNil(t, ia)
}

// The request budget: one verification per request. A second verifyMfa in the same request is
// refused before it reads the challenge, so aliased copies cannot spend a challenge's attempts.
func TestAliasedVerificationsInOneRequestEvaluateOne(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	ch := e.challenge(t, "pat@example.com", "pw-1")

	ctx := credential.WithRequestBudget(context.Background(), 1)
	for i := 0; i < 50; i++ {
		_, err := e.m.VerifyMfa(ctx, ch.Challenge, "ABCD-EFGH-JKMN-PQRS", "203.0.113.5")
		if i == 0 {
			require.ErrorIs(t, err, ErrMfaInvalidCode)
			continue
		}
		require.ErrorIs(t, err, credential.ErrRequestBudgetExhausted, "copy %d", i)
	}
	require.Equal(t, 49, e.metrics.count("recovery/budget"))
	// Only one attempt was charged to the challenge: four more are still available.
	for i := 0; i < MfaChallengeAttempts-1; i++ {
		_, err := e.m.VerifyMfa(context.Background(), ch.Challenge, "ABCD-EFGH-JKMN-PQRS", "203.0.113.5")
		require.ErrorIs(t, err, ErrMfaInvalidCode)
	}
	_, err := e.m.VerifyMfa(context.Background(), ch.Challenge, recovery[0], "203.0.113.5")
	require.True(t, isCode(err, CodeMfaChallengeExpired))
}

// verifyMfa is rationed per source address, ahead of everything else. The twin is another source.
func TestVerifyMfaIsRationedPerSource(t *testing.T) {
	e := newMfaEnv(t)
	e.m.mfaLimiter = newMfaVerifyLimiter()
	ctx := context.Background()
	var limited int
	for i := 0; i < 20; i++ {
		_, err := e.m.VerifyMfa(ctx, "no-such-challenge", "123456", "198.51.100.7")
		if errors.Is(err, ErrMfaRateLimited) {
			limited++
		} else {
			require.True(t, isCode(err, CodeMfaChallengeExpired), "got %v", err)
		}
	}
	require.Positive(t, limited, "a script from one address must be refused")
	require.Equal(t, limited, e.metrics.count("totp/rate_limited"))
	_, err := e.m.VerifyMfa(ctx, "no-such-challenge", "123456", "198.51.100.8")
	require.True(t, isCode(err, CodeMfaChallengeExpired), "the control: another source is not limited, got %v", err)
}

// A challenge for an identity whose factor was removed since is void, not an invitation to sign in
// with the password alone.
func TestAChallengeOutlivesNeitherItsFactorNorItsIdentity(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	ch := e.challenge(t, "pat@example.com", "pw-1")

	id, err := e.store.IdentityByEmail(ctx, "pat@example.com")
	require.NoError(t, err)
	_, err = e.store.RemoveMfa(ctx, e.m.secrets, id.ID)
	require.NoError(t, err)

	_, err = e.m.VerifyMfa(ctx, ch.Challenge, recovery[0], "203.0.113.5")
	require.True(t, isCode(err, CodeMfaChallengeExpired), "got %v", err)
}

// The set of data-plane codes is closed. A test pins it so a new code is a decision.
func TestTheMfaErrorCodeSetIsClosed(t *testing.T) {
	require.Equal(t, []string{"MFA_CHALLENGE", "MFA_REQUIRED", "MFA_ENROLLMENT_REQUIRED",
		"MFA_CHALLENGE_EXPIRED", "MFA_ALREADY_ENROLLED", "MFA_NOT_ENROLLED"}, MfaCodes())
	for _, err := range []error{ErrMfaRequired, ErrMfaEnrollmentRequired, ErrMfaChallengeExpired, ErrMfaAlreadyEnrolled, ErrMfaNotEnabled} {
		var coded *MfaError
		require.True(t, errors.As(err, &coded))
		require.Contains(t, MfaCodes(), coded.Code())
	}
	// A wrong code, a lock and a replay carry no code of their own.
	var coded interface{ Extensions() map[string]any }
	require.False(t, errors.As(ErrMfaInvalidCode, &coded))
}

// The checker main builds from CredentialPolicies must answer for the one-time kind: without its
// policy every verifyMfa would fail with ErrUndeclaredKind at the first enrolled sign-in rather
// than at startup. The kind is unthrottled because its attempt state is the database's.
func TestCredentialPoliciesDeclareTheOneTimeKind(t *testing.T) {
	require.Equal(t, credential.Policy{Unthrottled: true}, CredentialPolicies[credential.KindTotp])
	saKey, err := credential.DeriveServiceAccountSecretKey(make([]byte, 32))
	require.NoError(t, err)
	c, err := credential.NewChecker(credentialtest.NewStore(), CredentialPolicies, credential.WithServiceAccountSecretKey(saKey))
	require.NoError(t, err)
	err = c.CheckOneTime(context.Background(), credential.KindTotp, func(context.Context, credential.OneTime) error { return nil })
	require.NoError(t, err)
}

// The challenge's five minutes are counted from when it was ISSUED. An attempt rewrites the entry,
// which restarts the bucket's own expiry, so without reading the issue time a challenge would live
// five minutes past its last attempt. The twin: with no attempts it expires on the same clock.
func TestAnAttemptDoesNotExtendTheChallenge(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	now := time.Now()
	e.m.now = func() time.Time { return now }

	ch := e.challenge(t, "pat@example.com", "pw-1")
	idle := e.challenge(t, "pat@example.com", "pw-1")
	now = now.Add(4 * time.Minute)
	_, err := e.m.VerifyMfa(ctx, ch.Challenge, "ABCD-EFGH-JKMN-PQRS", "203.0.113.5") // rewrites the entry
	require.ErrorIs(t, err, ErrMfaInvalidCode)

	now = now.Add(2 * time.Minute) // six minutes after issue, two after the last attempt
	_, err = e.m.VerifyMfa(ctx, ch.Challenge, recovery[0], "203.0.113.5")
	require.True(t, isCode(err, CodeMfaChallengeExpired), "an attempt extended the challenge: %v", err)
	_, err = e.m.VerifyMfa(ctx, idle.Challenge, recovery[0], "203.0.113.5")
	require.True(t, isCode(err, CodeMfaChallengeExpired), "the control: an untouched challenge expires too: %v", err)

	// And inside the window the same code signs in.
	fresh := e.challenge(t, "pat@example.com", "pw-1")
	now = now.Add(4 * time.Minute)
	_, err = e.m.VerifyMfa(ctx, fresh.Challenge, recovery[0], "203.0.113.5")
	require.NoError(t, err)
}
