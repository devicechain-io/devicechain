// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The second-factor sign-in on a REAL PostgreSQL, through the real migration chain, driving the
// whole flow (login, verifyMfa, the tenant policy) rather than the store alone. The charge-first
// lock, the replay compare-and-set and the TOTP window are properties of the database's row locks
// and clock, which SQLite cannot answer.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 -run MfaPg ./identity/ -v
package identity

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/sourcelimit"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/schema"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type mfaPgEnv struct {
	m          *Manager
	store      *iam.Store
	db         *gorm.DB
	challenges *hookKV
	metrics    *recordingMfaMetrics
	validator  *auth.Validator
}

func newMfaPgEnv(t *testing.T) *mfaPgEnv {
	t.Helper()
	port := 5432
	if raw := os.Getenv("DC_IT_PGPORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = p
	}
	host := "localhost"
	if h := os.Getenv("DC_IT_PGHOST"); h != "" {
		host = h
	}
	ctx := context.Background()
	instance := fmt.Sprintf("idmfa%d", time.Now().UnixNano())
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "user-management"},
		Migrations:   schema.Migrations,
		InstanceConfig: config.DatastoreConfiguration{Type: "postgres", Configuration: map[string]interface{}{
			"hostname": host, "port": port, "username": "postgres", "password": "postgres", "sslMode": "disable",
		}},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	store, err := secrets.NewFromConfig(ctx, config.SecretsConfiguration{RootKey: base64.StdEncoding.EncodeToString(raw)}, mgr.Database)
	require.NoError(t, err)

	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	generous := credential.Policy{Free: 100000, Base: time.Second, Cap: time.Minute}
	checker, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity: generous, credential.KindOAuthClient: generous, credential.KindTotp: {Unthrottled: true},
	})
	require.NoError(t, err)
	kvb := &hookKV{KeyValue: jetStreamBucket(t, "mfa-challenges")}
	metrics := newRecordingMfaMetrics()
	roomy := sourcelimit.Limits{Burst: 1000000, Every: time.Microsecond, MaxKeys: 1000}
	m := &Manager{
		iam: iam.NewStore(mgr), db: mgr, secrets: store, credentials: checker,
		issuer:    auth.NewIssuer(key, "https://as.example.com", time.Minute, time.Hour),
		validator: auth.NewValidator(&key.PublicKey),
		refreshKV: jetStreamBucket(t, "refresh-tokens"), accessTTL: time.Minute,
	}
	m.initLimiters()
	m.mfaLimiter = beginLimiter{perSource: sourcelimit.Must(roomy), global: sourcelimit.Must(roomy)}
	m.SetMfaStore(kvb)
	m.SetMfaMetrics(metrics)
	e := &mfaPgEnv{m: m, store: m.iam, db: mgr.Database, challenges: kvb, metrics: metrics, validator: m.validator}

	tier, err := e.store.TenantTierByToken(ctx, "silver") // seeded by the migrations
	require.NoError(t, err)
	require.NoError(t, e.store.CreateTenant(ctx, &iam.Tenant{Token: "acme", Enabled: true, TierID: tier.ID, PurgeState: iam.PurgeActive}))
	return e
}

func (e *mfaPgEnv) member(t *testing.T, email, password string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, e.store.CreateIdentity(context.Background(), &iam.Identity{
		Email: email, Enabled: true, PasswordHash: string(hash),
		Memberships: []iam.Membership{{TenantId: "acme", Enabled: true}},
	}))
}

// enroll enrols a TOTP factor through the public API and returns its seed and recovery codes. The
// proving code's step is then the factor's last accepted one.
func (e *mfaPgEnv) enroll(t *testing.T, email, password string) ([]byte, []string) {
	t.Helper()
	ctx := context.Background()
	ia, err := e.m.Login(ctx, email, password)
	require.NoError(t, err)
	enr, err := e.m.BeginTotpEnrollment(ctx, ia.IdentityToken, password)
	require.NoError(t, err)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enr.Secret)
	require.NoError(t, err)
	recovery, err := e.m.ConfirmTotpEnrollment(ctx, ia.IdentityToken, password, credential.GenerateTOTP(seed, time.Now()))
	require.NoError(t, err)
	return seed, recovery
}

func (e *mfaPgEnv) challenge(t *testing.T, email, password string) *MfaChallengeError {
	t.Helper()
	_, err := e.m.Login(context.Background(), email, password)
	var ch *MfaChallengeError
	require.ErrorAs(t, err, &ch)
	return ch
}

// resetSteps forgets the factor's last accepted step, the test's way of probing one window at a
// time: a success moves it, and a probe at an earlier step would then be a replay.
func (e *mfaPgEnv) resetSteps(t *testing.T) {
	t.Helper()
	require.NoError(t, e.db.Exec("UPDATE iam_mfa_factors SET last_step = 0, failed_count = 0, locked_until = NULL").Error)
	require.NoError(t, e.db.Exec("DELETE FROM iam_mfa_source_failures").Error)
}

func (e *mfaPgEnv) sessions(t *testing.T) int {
	t.Helper()
	keys, _ := e.m.refreshKV.Keys()
	n := 0
	for _, k := range keys {
		if len(k) > 4 && k[:4] == sessionKeyPrefix {
			n++
		}
	}
	return n
}

func at(offsetSteps int) time.Time {
	return time.Now().Add(time.Duration(offsetSteps) * 30 * time.Second)
}

// A TOTP sign-in completes with ["pwd","otp","mfa"] on the session, and the tokens minted from it.
func TestMfaPgATotpCodeCompletesTheSignInWithTheOtpAmr(t *testing.T) {
	e := newMfaPgEnv(t)
	ctx := context.Background()
	e.member(t, "pat@example.com", "pw-1")
	seed, _ := e.enroll(t, "pat@example.com", "pw-1")
	ch := e.challenge(t, "pat@example.com", "pw-1")

	// The proving code's step is spent; the next step's code is in the window.
	ia, err := e.m.VerifyMfa(ctx, ch.Challenge, credential.GenerateTOTP(seed, at(1)), "203.0.113.5")
	require.NoError(t, err)
	claims, err := e.validator.ValidateIdentity(ia.IdentityToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordOTP(), claims.Amr)
	pair, err := e.m.SelectTenant(ctx, ia.IdentityToken, "acme")
	require.NoError(t, err)
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordOTP(), access.Amr)

	// The tenant requires a factor; this session has one, and a password-only one does not.
	tn, err := e.store.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	require.NoError(t, e.store.SetTenantMfaRequired(ctx, tn, true))
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err, "a pwd+otp session is admitted to a tenant that requires a factor")
}

// T5. The window is the current step and one either side. Two steps out is refused; the twin is
// the boundary that is accepted. A step that was just used is refused as a replay.
func TestMfaPgTheTotpWindowIsPlusOrMinusOneStep(t *testing.T) {
	e := newMfaPgEnv(t)
	ctx := context.Background()
	e.member(t, "pat@example.com", "pw-1")
	seed, _ := e.enroll(t, "pat@example.com", "pw-1")

	for _, tc := range []struct {
		offset int
		ok     bool
	}{{-2, false}, {2, false}, {-1, true}, {0, true}, {1, true}} {
		e.resetSteps(t)
		ch := e.challenge(t, "pat@example.com", "pw-1")
		_, err := e.m.VerifyMfa(ctx, ch.Challenge, credential.GenerateTOTP(seed, at(tc.offset)), "203.0.113.5")
		if tc.ok {
			require.NoError(t, err, "offset %d", tc.offset)
		} else {
			require.ErrorIs(t, err, ErrMfaInvalidCode, "offset %d", tc.offset)
		}
	}
}

// T4 through the flow. A code that signed in cannot sign in again, and neither can an older one.
func TestMfaPgAUsedCodeAndAnOlderCodeAreRefused(t *testing.T) {
	e := newMfaPgEnv(t)
	ctx := context.Background()
	e.member(t, "pat@example.com", "pw-1")
	seed, _ := e.enroll(t, "pat@example.com", "pw-1")
	e.resetSteps(t)

	newer := credential.GenerateTOTP(seed, at(1))
	older := credential.GenerateTOTP(seed, at(-1))
	_, err := e.m.VerifyMfa(ctx, e.challenge(t, "pat@example.com", "pw-1").Challenge, newer, "203.0.113.5")
	require.NoError(t, err)
	_, err = e.m.VerifyMfa(ctx, e.challenge(t, "pat@example.com", "pw-1").Challenge, newer, "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaInvalidCode, "a code that signed in once signs in once")
	_, err = e.m.VerifyMfa(ctx, e.challenge(t, "pat@example.com", "pw-1").Challenge, older, "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaInvalidCode, "a code for an earlier step than the last accepted is refused")
	require.Equal(t, 2, e.metrics.count("totp/replay"))
}

// T3 (concurrency). Two verifications on ONE challenge with valid codes for different steps: exactly
// one session comes out.
func TestMfaPgOneChallengeSignsInOnceUnderConcurrency(t *testing.T) {
	e := newMfaPgEnv(t)
	ctx := context.Background()
	e.member(t, "pat@example.com", "pw-1")
	seed, _ := e.enroll(t, "pat@example.com", "pw-1")
	e.resetSteps(t)

	for round := 0; round < 5; round++ {
		e.resetSteps(t)
		ch := e.challenge(t, "pat@example.com", "pw-1")
		before := e.sessions(t)
		var wins atomic.Int32
		var wg sync.WaitGroup
		for _, step := range []int{0, 1} {
			wg.Add(1)
			go func(step int) {
				defer wg.Done()
				if _, err := e.m.VerifyMfa(ctx, ch.Challenge, credential.GenerateTOTP(seed, at(step)), "203.0.113.5"); err == nil {
					wins.Add(1)
				}
			}(step)
		}
		wg.Wait()
		require.EqualValues(t, 1, wins.Load(), "round %d: one challenge signed in %d times", round, wins.Load())
		require.Equal(t, before+1, e.sessions(t), "round %d", round)
	}
}

// T6. The charge-first locks, through the whole flow: a burst of 200 wrong codes across 40
// challenges from 40 sources evaluates exactly 20 (the identity-wide threshold); the rest are
// refused UNEVALUATED. While locked the correct code is answered byte-for-byte like a wrong one,
// a recovery code still works, and after the lock expires the correct code signs in and resets
// the counts.
func TestMfaPgTheBurstIsBoundedByTheIdentityLockAndIsIndistinguishable(t *testing.T) {
	e := newMfaPgEnv(t)
	ctx := context.Background()
	e.member(t, "pat@example.com", "pw-1")
	seed, recovery := e.enroll(t, "pat@example.com", "pw-1")
	e.resetSteps(t)
	wrongCode := credential.GenerateTOTP(seed, at(10)) // far outside the window: never right

	const challenges, perChallenge = 40, 5
	chs := make([]*MfaChallengeError, challenges)
	for i := range chs {
		chs[i] = e.challenge(t, "pat@example.com", "pw-1")
	}
	var wg sync.WaitGroup
	var refused atomic.Int32
	// Challenges run concurrently; the attempts on ONE challenge run in turn, because concurrent
	// attempts on a single challenge are serialized by its revision-checked count (TestMfaPg
	// OneChallengeSignsInOnceUnderConcurrency) and the loser is never evaluated.
	for i, ch := range chs {
		wg.Add(1)
		go func(i int, ch *MfaChallengeError) {
			defer wg.Done()
			for j := 0; j < perChallenge; j++ {
				_, err := e.m.VerifyMfa(ctx, ch.Challenge, wrongCode, fmt.Sprintf("198.51.100.%d", i+1))
				if err == ErrMfaInvalidCode {
					refused.Add(1)
				}
			}
		}(i, ch)
	}
	wg.Wait()
	require.EqualValues(t, challenges*perChallenge, refused.Load(), "every attempt got the one answer")
	require.Equal(t, 20, e.metrics.count("totp/invalid"), "exactly the identity threshold was evaluated")
	require.Equal(t, challenges*perChallenge-20, e.metrics.count("totp/locked"))
	require.Equal(t, 1, e.metrics.locks, "the lock was armed once")
	var failed, locked int64
	sys := e.db.WithContext(core.WithSystemContext(ctx))
	require.NoError(t, sys.Model(&rdb.AuditEvent{}).Where("operation = ?", rdb.AuditOpMfaFailed).Count(&failed).Error)
	require.NoError(t, sys.Model(&rdb.AuditEvent{}).Where("operation = ?", rdb.AuditOpMfaLocked).Count(&locked).Error)
	require.EqualValues(t, 20, failed, "only evaluated attempts are journalled, never the ones a lock refused")
	require.EqualValues(t, 1, locked)

	// Locked: the CORRECT code is refused exactly as a wrong one, and nothing says why.
	ch := e.challenge(t, "pat@example.com", "pw-1")
	_, wrongErr := e.m.VerifyMfa(ctx, ch.Challenge, wrongCode, "203.0.113.200")
	ch = e.challenge(t, "pat@example.com", "pw-1")
	_, correctErr := e.m.VerifyMfa(ctx, ch.Challenge, credential.GenerateTOTP(seed, at(1)), "203.0.113.200")
	require.Equal(t, wrongErr, correctErr)
	require.Equal(t, wrongErr.Error(), correctErr.Error())

	// A recovery code still works: it is exempt from the TOTP locks.
	ch = e.challenge(t, "pat@example.com", "pw-1")
	ia, err := e.m.VerifyMfa(ctx, ch.Challenge, recovery[0], "203.0.113.200")
	require.NoError(t, err, "a recovery code must work while the authenticator is locked")
	claims, err := e.validator.ValidateIdentity(ia.IdentityToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordRecovery(), claims.Amr)

	// The lock expires: the correct code signs in and the counts reset.
	require.NoError(t, e.db.Exec("UPDATE iam_mfa_factors SET locked_until = now() - interval '1 minute'").Error)
	require.NoError(t, e.db.Exec("UPDATE iam_mfa_source_failures SET locked_until = now() - interval '1 minute'").Error)
	ch = e.challenge(t, "pat@example.com", "pw-1")
	_, err = e.m.VerifyMfa(ctx, ch.Challenge, credential.GenerateTOTP(seed, at(1)), "203.0.113.201")
	require.NoError(t, err, "after the lock the correct code signs in")
	var f iam.MfaFactor
	require.NoError(t, e.db.First(&f).Error)
	require.Zero(t, f.FailedCount)
	require.Nil(t, f.LockedUntil)
}

// Disabling and regenerating with a TOTP code (not a recovery code) go through the same locks.
func TestMfaPgRegenerateAndDisableAcceptAnAuthenticatorCode(t *testing.T) {
	e := newMfaPgEnv(t)
	ctx := context.Background()
	e.member(t, "pat@example.com", "pw-1")
	seed, _ := e.enroll(t, "pat@example.com", "pw-1")
	e.resetSteps(t)
	ia, err := e.m.Login(ctx, "pat@example.com", "pw-1")
	if err == nil {
		t.Fatalf("an enrolled sign-in must be challenged, got %v", ia)
	}
	tok := func() string {
		ch := e.challenge(t, "pat@example.com", "pw-1")
		out, err := e.m.VerifyMfa(ctx, ch.Challenge, credential.GenerateTOTP(seed, at(1)), "203.0.113.5")
		require.NoError(t, err)
		return out.IdentityToken
	}()

	wrong := credential.GenerateTOTP(seed, at(10))
	_, err = e.m.RegenerateRecoveryCodes(ctx, tok, "pw-1", wrong, "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	_, err = e.m.RegenerateRecoveryCodes(ctx, tok, "wrong", credential.GenerateTOTP(seed, at(1)), "203.0.113.5")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	e.resetSteps(t) // the sign-in above spent this step
	fresh, err := e.m.RegenerateRecoveryCodes(ctx, tok, "pw-1", credential.GenerateTOTP(seed, at(1)), "203.0.113.5")
	require.NoError(t, err)
	require.Len(t, fresh, 10)

	e.resetSteps(t)
	require.NoError(t, e.m.DisableTotp(ctx, tok, "pw-1", credential.GenerateTOTP(seed, at(1)), "203.0.113.5"))
	var left int64
	require.NoError(t, e.db.Model(&iam.MfaFactor{}).Count(&left).Error)
	require.Zero(t, left, "disabling with an authenticator code removed the factor")
}
