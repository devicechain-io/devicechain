// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The second-factor store on a REAL PostgreSQL, through the real migration chain. The charge-first
// lock, the replay compare-and-set and the single-use recovery code are properties of the
// server's row locks and clock, which SQLite cannot answer.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 -run Mfa ./iam/ -v
package iam

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-user-management/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newMfaPostgres(t *testing.T) (*Store, secrets.SecretStore) {
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
	instance := fmt.Sprintf("ummfa%d", time.Now().UnixNano())
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
	store, err := secrets.NewFromConfig(ctx,
		config.SecretsConfiguration{RootKey: base64.StdEncoding.EncodeToString(raw)}, mgr.Database)
	require.NoError(t, err)
	return NewStore(mgr), store
}

// pgFactor enrols an identity with an ACTIVE factor (last accepted step 100) and returns it.
func pgFactor(t *testing.T, s *Store, store secrets.SecretStore, email string) (*Identity, *MfaFactor) {
	t.Helper()
	ctx := context.Background()
	id := &Identity{Email: email, Enabled: true}
	require.NoError(t, s.CreateIdentity(ctx, id))
	f, err := s.BeginMfaEnrollment(ctx, store, id.ID, mfaSeed)
	require.NoError(t, err)
	require.NoError(t, s.ConfirmMfaEnrollment(ctx, store, id.ID, stepVerify(100, true), []string{"c1", "c2", "c3"}, time.Now()))
	return id, f
}

// attempt is one TOTP attempt with a scripted verifier: it counts how many times the verifier
// ran (the attempt was EVALUATED) and checks it was handed the seed that was sealed.
type attempt struct {
	evaluated atomic.Int64
	step      int64
	ok        bool
}

func (a *attempt) run(t *testing.T, s *Store, store secrets.SecretStore, factorID uint, source string) (MfaAttemptOutcome, error) {
	return s.MfaVerifyTOTP(context.Background(), store, factorID, source, func(seed []byte) (int64, bool, error) {
		a.evaluated.Add(1)
		if string(seed) != string(mfaSeed) {
			return 0, false, errors.New("the verifier was not handed the sealed seed")
		}
		return a.step, a.ok, nil
	})
}

func factorRow(t *testing.T, s *Store, id uint) MfaFactor {
	t.Helper()
	var f MfaFactor
	require.NoError(t, s.sys(context.Background()).First(&f, "id = ?", id).Error)
	return f
}

// lockSeconds is how long from now the factor's identity-wide lock holds, per the DATABASE clock.
func lockSeconds(t *testing.T, s *Store, table, where string, arg any) float64 {
	t.Helper()
	var secs *float64
	require.NoError(t, s.sys(context.Background()).Raw(
		"SELECT EXTRACT(EPOCH FROM (locked_until - now())) FROM "+table+" WHERE "+where, arg).Scan(&secs).Error)
	require.NotNil(t, secs, "no lock is set")
	return *secs
}

// expireLocks moves every lock into the past, the test's way of moving the clock.
func expireLocks(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.sys(ctx).Exec(
		"UPDATE iam_mfa_factors SET locked_until = now() - interval '1 second' WHERE locked_until IS NOT NULL").Error)
	require.NoError(t, s.sys(ctx).Exec(
		"UPDATE iam_mfa_source_failures SET locked_until = now() - interval '1 second' WHERE locked_until IS NOT NULL").Error)
}

// T6: THE CHARGE-FIRST LOCK UNDER A CONCURRENT BURST. 200 wrong codes at once, from 40 sources
// (5 each, so no source lock is what stops them), against a fresh factor: exactly 20 are
// EVALUATED -- the identity-wide threshold -- and the other 180 are refused unevaluated.
//
// A lock that read the count and then wrote it would let every attempt in the burst see "free"
// and evaluate all 200. The assertion is on the verifier's call count, not on the outcome
// labels, because "was it evaluated" is the property that matters: an evaluated guess is a
// chance at a 3-in-a-million hit.
func TestMfaTheChargeFirstLockEvaluatesExactlyTheThresholdUnderABurst(t *testing.T) {
	s, store := newMfaPostgres(t)
	_, f := pgFactor(t, s, store, "burst@example.com")

	const sources, perSource = 40, 5
	a := &attempt{step: 999, ok: false}
	var invalid, locked, other atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for src := 0; src < sources; src++ {
		for i := 0; i < perSource; i++ {
			wg.Add(1)
			go func(src int) {
				defer wg.Done()
				<-start
				out, err := a.run(t, s, store, f.ID, fmt.Sprintf("198.51.100.%d", src))
				switch {
				case err != nil:
					other.Add(1)
				case out == MfaAttemptInvalid:
					invalid.Add(1)
				case out == MfaAttemptLocked:
					locked.Add(1)
				default:
					other.Add(1)
				}
			}(src)
		}
	}
	close(start)
	wg.Wait()

	require.Zero(t, other.Load(), "no attempt may error or succeed")
	// The literals are the policy (20 failures identity-wide), written out on purpose: an
	// assertion built from the constant would pass for any value the constant is changed to.
	require.EqualValues(t, 20, a.evaluated.Load(),
		"the burst must evaluate exactly the identity-wide threshold")
	require.EqualValues(t, 20, invalid.Load())
	require.EqualValues(t, 180, locked.Load())
	row := factorRow(t, s, f.ID)
	require.Equal(t, 20, row.FailedCount, "refused attempts are not charged")
	require.NotNil(t, row.LockedUntil)
	secs := lockSeconds(t, s, "iam_mfa_factors", "id = ?", f.ID)
	require.InDelta(t, MfaLockBase.Seconds(), secs, 5, "the first identity lock is the base duration")
}

// The per-source half: 5 failures from source A lock A while B is still evaluated; and a source
// is never what locks the person out.
func TestMfaASourceLocksItselfBeforeItCanLockThePersonOut(t *testing.T) {
	s, store := newMfaPostgres(t)
	_, f := pgFactor(t, s, store, "src@example.com")
	a := &attempt{step: 999, ok: false}

	for i := 0; i < 5; i++ {
		out, err := a.run(t, s, store, f.ID, "203.0.113.1")
		require.NoError(t, err)
		require.Equal(t, MfaAttemptInvalid, out)
	}
	out, err := a.run(t, s, store, f.ID, "203.0.113.1")
	require.NoError(t, err)
	require.Equal(t, MfaAttemptLocked, out, "the sixth attempt from one source is refused")
	require.EqualValues(t, 5, a.evaluated.Load(), "and it was not evaluated")

	// B is untouched, and the identity is not locked (5 < 20).
	out, err = a.run(t, s, store, f.ID, "203.0.113.2")
	require.NoError(t, err)
	require.Equal(t, MfaAttemptInvalid, out, "another source must still be evaluated")
	require.EqualValues(t, 6, a.evaluated.Load())
	require.Nil(t, factorRow(t, s, f.ID).LockedUntil, "five failures from one source must not lock the identity")
}

// A locked factor refuses even the CORRECT code, without evaluating it and without changing the
// factor's accepted step -- and the same code works, resetting the counts, once the lock expires.
func TestMfaALockedFactorRefusesTheCorrectCodeAndRecoversAfterExpiry(t *testing.T) {
	s, store := newMfaPostgres(t)
	_, f := pgFactor(t, s, store, "locked@example.com")
	wrong := &attempt{step: 999, ok: false}
	for src := 0; src < 4; src++ {
		for i := 0; i < MfaSourceLockAfter; i++ {
			_, err := wrong.run(t, s, store, f.ID, fmt.Sprintf("192.0.2.%d", src))
			require.NoError(t, err)
		}
	}
	require.EqualValues(t, 20, wrong.evaluated.Load())

	right := &attempt{step: 101, ok: true}
	out, err := right.run(t, s, store, f.ID, "192.0.2.200")
	require.NoError(t, err)
	require.Equal(t, MfaAttemptLocked, out, "the correct code must be refused while the factor is locked")
	require.Zero(t, right.evaluated.Load(), "a locked attempt must not be evaluated")
	require.EqualValues(t, 100, factorRow(t, s, f.ID).LastStep, "a refused code must not advance the accepted step")

	// The twin: after the lock expires the same code succeeds and resets the counts.
	expireLocks(t, s)
	out, err = right.run(t, s, store, f.ID, "192.0.2.200")
	require.NoError(t, err)
	require.Equal(t, MfaAttemptSuccess, out)
	row := factorRow(t, s, f.ID)
	require.Zero(t, row.FailedCount, "a success resets the failure count")
	require.Nil(t, row.LockedUntil, "and clears the lock the charge had re-armed")
	require.EqualValues(t, 101, row.LastStep)
	require.NotNil(t, row.LastUsedAt)
	var n int64
	require.NoError(t, s.sys(context.Background()).Model(&MfaSourceFailure{}).
		Where("factor_id = ? AND source_key = ?", f.ID, "192.0.2.200").Count(&n).Error)
	require.Zero(t, n, "the winning source's failure row is removed")
}

// The escalation and the cap: the identity lock is 1 minute at the threshold and doubles per
// failure after it, never past 15 minutes; the source lock is the same at its own threshold. And
// after 1,000 failures the arithmetic has not overflowed (an unbounded power() raises "interval
// out of range" long before that, turning a locked factor into a failing request).
func TestMfaLocksEscalateAndCapAtFifteenMinutesWithoutOverflow(t *testing.T) {
	s, store := newMfaPostgres(t)
	_, f := pgFactor(t, s, store, "cap@example.com")
	a := &attempt{step: 999, ok: false}

	for n := 1; n <= 1000; n++ {
		out, err := a.run(t, s, store, f.ID, "192.0.2.9")
		require.NoErrorf(t, err, "attempt %d", n)
		require.Equalf(t, MfaAttemptInvalid, out, "attempt %d must be evaluated once the locks are expired", n)

		row := factorRow(t, s, f.ID)
		require.Equal(t, n, row.FailedCount)
		if n >= MfaIdentityLockAfter {
			want := MfaLockBase.Seconds() * float64(uint(1)<<min(n-MfaIdentityLockAfter, 20))
			want = min(want, MfaLockCap.Seconds())
			got := lockSeconds(t, s, "iam_mfa_factors", "id = ?", f.ID)
			require.InDeltaf(t, want, got, 5, "identity lock after %d failures", n)
			require.LessOrEqual(t, got, MfaLockCap.Seconds()+1, "the lock must never pass the cap")
		} else {
			require.Nilf(t, row.LockedUntil, "no lock before the threshold (attempt %d)", n)
		}
		if n >= MfaSourceLockAfter {
			want := min(MfaLockBase.Seconds()*float64(uint(1)<<min(n-MfaSourceLockAfter, 20)), MfaLockCap.Seconds())
			got := lockSeconds(t, s, "iam_mfa_source_failures", "factor_id = "+strconv.Itoa(int(f.ID))+" AND source_key = ?", "192.0.2.9")
			require.InDeltaf(t, want, got, 5, "source lock after %d failures", n)
		}
		expireLocks(t, s)
	}
	// Four doublings reach 16 minutes: the cap holds, and is reached exactly.
	require.InDelta(t, MfaLockCap.Seconds(), min(MfaLockBase.Seconds()*16, MfaLockCap.Seconds()), 0.5)
}

// T4: replay. The same step twice -> the second is refused; an older step is refused; the next
// step succeeds.
func TestMfaAStepIsAcceptedOnceAndNeverAgainOrBefore(t *testing.T) {
	s, store := newMfaPostgres(t)
	_, f := pgFactor(t, s, store, "replay@example.com")

	step := func(n int64) *attempt { return &attempt{step: n, ok: true} }
	out, err := step(105).run(t, s, store, f.ID, "192.0.2.1")
	require.NoError(t, err)
	require.Equal(t, MfaAttemptSuccess, out)

	out, err = step(105).run(t, s, store, f.ID, "192.0.2.1")
	require.NoError(t, err)
	require.Equal(t, MfaAttemptReplay, out, "the same step twice")
	out, err = step(104).run(t, s, store, f.ID, "192.0.2.1")
	require.NoError(t, err)
	require.Equal(t, MfaAttemptReplay, out, "an older step after a newer one")
	require.EqualValues(t, 105, factorRow(t, s, f.ID).LastStep)

	// A replay is charged like any failure; the next accepted step resets it (the twin).
	require.Equal(t, 2, factorRow(t, s, f.ID).FailedCount)
	out, err = step(106).run(t, s, store, f.ID, "192.0.2.1")
	require.NoError(t, err)
	require.Equal(t, MfaAttemptSuccess, out, "the next step succeeds")
	row := factorRow(t, s, f.ID)
	require.EqualValues(t, 106, row.LastStep)
	require.Zero(t, row.FailedCount)
}

// 20 concurrent verifies of ONE valid code, from 20 sources: exactly one wins.
func TestMfaConcurrentVerifiesOfOneCodeHaveExactlyOneWinner(t *testing.T) {
	s, store := newMfaPostgres(t)
	_, f := pgFactor(t, s, store, "race@example.com")

	var wins, replays, other atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := &attempt{step: 101, ok: true}
			<-start
			out, err := a.run(t, s, store, f.ID, fmt.Sprintf("192.0.2.%d", i))
			switch {
			case err != nil:
				other.Add(1)
			case out == MfaAttemptSuccess:
				wins.Add(1)
			case out == MfaAttemptReplay:
				replays.Add(1)
			default:
				other.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	require.Zero(t, other.Load())
	require.EqualValues(t, 1, wins.Load(), "one code, one session")
	require.EqualValues(t, 19, replays.Load())
}

// A verifier error (or a missing seed) rolls the whole attempt back: nothing is charged and
// nothing advances, and a factor that is not active is a loud error, not a quiet "locked".
func TestMfaAVerifierErrorKeepsItsChargeAndAMissingFactorIsLoud(t *testing.T) {
	s, store := newMfaPostgres(t)
	ctx := context.Background()
	_, f := pgFactor(t, s, store, "err@example.com")

	boom := errors.New("verifier failed")
	_, err := s.MfaVerifyTOTP(ctx, store, f.ID, "192.0.2.1", func([]byte) (int64, bool, error) { return 0, false, boom })
	require.ErrorIs(t, err, boom)
	require.Equal(t, 1, factorRow(t, s, f.ID).FailedCount,
		"a verifier that errors after comparing must not be a free guess: the charge stands")

	_, err = s.MfaVerifyTOTP(ctx, store, 999999, "192.0.2.1", func([]byte) (int64, bool, error) { return 0, false, nil })
	require.ErrorIs(t, err, ErrMfaFactorNotFound)

	pendingID := &Identity{Email: "pending@example.com", Enabled: true}
	require.NoError(t, s.CreateIdentity(ctx, pendingID))
	pending, err := s.BeginMfaEnrollment(ctx, store, pendingID.ID, mfaSeed)
	require.NoError(t, err)
	_, err = s.MfaVerifyTOTP(ctx, store, pending.ID, "192.0.2.1", func([]byte) (int64, bool, error) { return 1, true, nil })
	require.ErrorIs(t, err, ErrMfaFactorNotFound, "a pending factor must not verify a sign-in")

	require.NoError(t, store.Delete(ctx, MfaSeedRef(f.ID)))
	_, err = s.MfaVerifyTOTP(ctx, store, f.ID, "192.0.2.1", func([]byte) (int64, bool, error) { return 1, true, nil })
	require.ErrorIs(t, err, secrets.ErrSecretNotFound, "a factor whose seed is gone must fail closed")
}

// T8: a recovery code works once even under a race, and is NOT subject to the TOTP lock.
func TestMfaARecoveryCodeIsSingleUseUnderConcurrency(t *testing.T) {
	s, store := newMfaPostgres(t)
	id, _ := pgFactor(t, s, store, "rc@example.com")

	var wins atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := s.RedeemMfaRecoveryCode(context.Background(), id.ID, "c1", time.Now())
			if err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.EqualValues(t, 1, wins.Load(), "50 concurrent redemptions of one code: exactly one winner")
	left, err := s.MfaRecoveryCodesRemaining(context.Background(), id.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, left)
}

func TestMfaRecoveryCodesAreExemptFromTheTotpLock(t *testing.T) {
	s, store := newMfaPostgres(t)
	ctx := context.Background()
	id, f := pgFactor(t, s, store, "exempt@example.com")
	wrong := &attempt{step: 999, ok: false}
	for src := 0; src < 4; src++ {
		for i := 0; i < MfaSourceLockAfter; i++ {
			_, err := wrong.run(t, s, store, f.ID, fmt.Sprintf("192.0.2.%d", src))
			require.NoError(t, err)
		}
	}
	out, err := (&attempt{step: 101, ok: true}).run(t, s, store, f.ID, "192.0.2.77")
	require.NoError(t, err)
	require.Equal(t, MfaAttemptLocked, out, "precondition: the TOTP path is locked")

	ok, err := s.RedeemMfaRecoveryCode(ctx, id.ID, "c2", time.Now())
	require.NoError(t, err)
	require.True(t, ok, "a recovery code must still work while the identity is locked")
}

// Two enrolments at once for one identity serialize on the identity row: both succeed in turn,
// exactly one pending factor and exactly one sealed seed remain.
func TestMfaConcurrentEnrollmentsLeaveOnePendingFactorAndOneSeed(t *testing.T) {
	s, store := newMfaPostgres(t)
	ctx := context.Background()
	id := &Identity{Email: "enrol@example.com", Enabled: true}
	require.NoError(t, s.CreateIdentity(ctx, id))

	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.BeginMfaEnrollment(ctx, store, id.ID, mfaSeed)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoErrorf(t, err, "enrolment %d", i)
	}
	var n int64
	require.NoError(t, s.sys(ctx).Model(&MfaFactor{}).Where("identity_id = ?", id.ID).Count(&n).Error)
	require.EqualValues(t, 1, n)
	var seeds int64
	require.NoError(t, s.sys(ctx).Raw("SELECT COUNT(*) FROM secrets WHERE name LIKE 'mfa/totp/%'").Scan(&seeds).Error)
	require.EqualValues(t, 1, seeds, "a replaced pending factor's seed must not be orphaned")
}

// The same-transaction guarantees on the real database.
func TestMfaTheSeedRollsBackWithItsRowOnPostgres(t *testing.T) {
	s, store := newMfaPostgres(t)
	ctx := context.Background()
	id := &Identity{Email: "rb@example.com", Enabled: true}
	require.NoError(t, s.CreateIdentity(ctx, id))
	errRollback := errors.New("roll back")
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := beginMfaEnrollmentIn(ctx, tx, store, id.ID, mfaSeed)
		require.NoError(t, err)
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	var seeds, rows int64
	require.NoError(t, s.sys(ctx).Raw("SELECT COUNT(*) FROM secrets WHERE name LIKE 'mfa/totp/%'").Scan(&seeds).Error)
	require.NoError(t, s.sys(ctx).Model(&MfaFactor{}).Count(&rows).Error)
	require.Zero(t, seeds, "a seed sealed in a rolled-back transaction must not survive it")
	require.Zero(t, rows)
}

// Purge on Postgres: deleting the identity cascades the per-source rows and recovery codes, and
// the seed (which no foreign key reaches) goes in the same transaction.
func TestMfaDeletingAnIdentityLeavesNothingOnPostgres(t *testing.T) {
	s, store := newMfaPostgres(t)
	ctx := context.Background()
	id, f := pgFactor(t, s, store, "purge@example.com")
	_, err := (&attempt{step: 999, ok: false}).run(t, s, store, f.ID, "192.0.2.1")
	require.NoError(t, err)
	loaded, err := s.IdentityByID(ctx, id.ID)
	require.NoError(t, err)

	require.NoError(t, s.DeleteIdentity(ctx, store, loaded))
	for table, q := range map[string]string{
		"factors":  "SELECT COUNT(*) FROM iam_mfa_factors",
		"sources":  "SELECT COUNT(*) FROM iam_mfa_source_failures",
		"codes":    "SELECT COUNT(*) FROM iam_mfa_recovery_codes",
		"secrets":  "SELECT COUNT(*) FROM secrets WHERE name LIKE 'mfa/totp/%'",
		"identity": "SELECT COUNT(*) FROM iam_identities WHERE id = " + strconv.Itoa(int(id.ID)),
	} {
		var n int64
		require.NoError(t, s.sys(ctx).Raw(q).Scan(&n).Error)
		require.Zerof(t, n, "%s must be empty after the identity is deleted", table)
	}
}

// One source must never be able to lock the PERSON out. Twenty-five concurrent attempts from ONE
// address: exactly 5 are evaluated (the source threshold), and the 20 refused ones must not be
// charged to the identity -- otherwise 5 + 15 more would arm the identity-wide lock from a single
// IP. A second address is then still evaluated, and the factor is not locked.
func TestMfaOneSourceCannotLockThePersonOut(t *testing.T) {
	s, store := newMfaPostgres(t)
	_, f := pgFactor(t, s, store, "onesrc@example.com")
	a := &attempt{step: 999, ok: false}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := a.run(t, s, store, f.ID, "203.0.113.50")
			require.NoError(t, err)
		}()
	}
	close(start)
	wg.Wait()
	require.EqualValues(t, 5, a.evaluated.Load(), "one source is evaluated 5 times and no more")
	row := factorRow(t, s, f.ID)
	require.Equal(t, 5, row.FailedCount, "refused attempts of a locked source must not be charged to the identity")
	require.Nil(t, row.LockedUntil, "one address must not arm the identity-wide lock")

	out, err := a.run(t, s, store, f.ID, "203.0.113.51")
	require.NoError(t, err)
	require.Equal(t, MfaAttemptInvalid, out, "another source is still evaluated")
	right := &attempt{step: 101, ok: true}
	out, err = right.run(t, s, store, f.ID, "203.0.113.51")
	require.NoError(t, err)
	require.Equal(t, MfaAttemptSuccess, out, "and the person can still sign in")
}

// Removal and attempts take the factor, its source rows and its secret in the SAME order, so
// racing them neither deadlocks nor errors with anything but "no such factor".
func TestMfaARemovalRacingAttemptsNeverDeadlocks(t *testing.T) {
	s, store := newMfaPostgres(t)
	ctx := context.Background()
	for round := 0; round < 25; round++ {
		id, f := pgFactor(t, s, store, fmt.Sprintf("race%d@example.com", round))
		// Source rows exist before the race, so the removal and the attempts contend on them.
		for i := 0; i < 6; i++ {
			_, err := (&attempt{step: 999, ok: false}).run(t, s, store, f.ID, fmt.Sprintf("192.0.2.%d", i))
			require.NoError(t, err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		start := make(chan struct{})
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				a := &attempt{step: 999, ok: false}
				_, err := a.run(t, s, store, f.ID, fmt.Sprintf("192.0.2.%d", i))
				if err != nil && !errors.Is(err, ErrMfaFactorNotFound) {
					errs <- err
				}
			}(i)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.RemoveMfa(ctx, store, id.ID); err != nil {
				errs <- err
			}
		}()
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoErrorf(t, err, "round %d", round)
		}
		var seeds int64
		require.NoError(t, s.sys(ctx).Raw("SELECT COUNT(*) FROM secrets WHERE name = ?", f.SecretName).Scan(&seeds).Error)
		require.Zero(t, seeds, "round %d: the seed must be gone", round)
	}
}

// A removal that has deleted the factor but not yet committed must not be followed by a
// regeneration that inserts codes for it. The regeneration locks the active factor row, so it
// waits for the removal and then finds no factor.
func TestMfaRegeneratingCodesCannotOutliveARacingRemoval(t *testing.T) {
	s, store := newMfaPostgres(t)
	ctx := context.Background()
	id, _ := pgFactor(t, s, store, "regen@example.com")

	removed := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.sys(ctx).Transaction(func(tx *gorm.DB) error {
			if _, err := removeMfaIn(ctx, tx, store, id.ID); err != nil {
				return err
			}
			close(removed)
			<-release
			return nil
		})
	}()
	<-removed

	regen := make(chan error, 1)
	go func() { regen <- s.ReplaceMfaRecoveryCodes(ctx, id.ID, []string{"x1", "x2"}) }()
	time.Sleep(700 * time.Millisecond) // long enough for an unlocked regeneration to have finished
	close(release)
	require.NoError(t, <-done)
	require.ErrorIs(t, <-regen, ErrMfaFactorNotFound, "the regeneration must see the removal")

	var codes int64
	require.NoError(t, s.sys(ctx).Model(&MfaRecoveryCode{}).Where("identity_id = ?", id.ID).Count(&codes).Error)
	require.Zero(t, codes, "recovery codes must not outlive their factor")
}

// And a code that outlived its factor by any other route is not a way in: redemption requires an
// ACTIVE factor.
func TestMfaARecoveryCodeWithoutAnActiveFactorIsRefused(t *testing.T) {
	s, store := newMfaPostgres(t)
	ctx := context.Background()
	id, f := pgFactor(t, s, store, "orphan@example.com")
	ok, err := s.RedeemMfaRecoveryCode(ctx, id.ID, "c1", time.Now())
	require.NoError(t, err)
	require.True(t, ok, "the twin: with an active factor the code works")

	require.NoError(t, s.sys(ctx).Exec("DELETE FROM iam_mfa_factors WHERE id = ?", f.ID).Error)
	ok, err = s.RedeemMfaRecoveryCode(ctx, id.ID, "c2", time.Now())
	require.NoError(t, err)
	require.False(t, ok, "a recovery code with no active factor must not redeem")
}

// An identity delete racing an enrolment: the delete locks the identity row first (as the
// enrolment does), so it cannot look for factors, miss the one being sealed, and leave its seed
// behind.
func TestMfaDeletingAnIdentityWaitsForARacingEnrollmentAndRemovesItsSeed(t *testing.T) {
	s, store := newMfaPostgres(t)
	ctx := context.Background()
	id := &Identity{Email: "delrace@example.com", Enabled: true}
	require.NoError(t, s.CreateIdentity(ctx, id))
	loaded, err := s.IdentityByID(ctx, id.ID)
	require.NoError(t, err)

	begun := make(chan struct{})
	release := make(chan struct{})
	enrol := make(chan error, 1)
	go func() {
		enrol <- s.sys(ctx).Transaction(func(tx *gorm.DB) error {
			if _, err := beginMfaEnrollmentIn(ctx, tx, store, id.ID, mfaSeed); err != nil {
				return err
			}
			close(begun)
			<-release
			return nil
		})
	}()
	<-begun
	del := make(chan error, 1)
	go func() { del <- s.DeleteIdentity(ctx, store, loaded) }()
	time.Sleep(700 * time.Millisecond)
	close(release)
	require.NoError(t, <-enrol)
	require.NoError(t, <-del)

	var seeds, ids int64
	require.NoError(t, s.sys(ctx).Raw("SELECT COUNT(*) FROM secrets WHERE name LIKE 'mfa/totp/%'").Scan(&seeds).Error)
	require.NoError(t, s.sys(ctx).Model(&Identity{}).Where("id = ?", id.ID).Count(&ids).Error)
	require.Zero(t, ids)
	require.Zero(t, seeds, "the seed sealed by the racing enrolment must be removed with the identity")
}

// Failed confirmations race for the pending enrolment's budget: exactly 4 are charged as wrong
// codes, the 5th voids it, and the rest find nothing pending.
func TestMfaConcurrentWrongConfirmationsSpendTheBudgetOnceAndVoidIt(t *testing.T) {
	s, store := newMfaPostgres(t)
	ctx := context.Background()
	id := &Identity{Email: "confirm@example.com", Enabled: true}
	require.NoError(t, s.CreateIdentity(ctx, id))
	f, err := s.BeginMfaEnrollment(ctx, store, id.ID, mfaSeed)
	require.NoError(t, err)

	var invalid, void, none, other atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := s.ConfirmMfaEnrollment(ctx, store, id.ID, stepVerify(0, false), []string{"d"}, time.Now())
			switch {
			case errors.Is(err, ErrMfaCodeInvalid):
				invalid.Add(1)
			case errors.Is(err, ErrMfaEnrollmentVoid):
				void.Add(1)
			case errors.Is(err, ErrMfaEnrollmentNotPending):
				none.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Zero(t, other.Load())
	require.EqualValues(t, 4, invalid.Load(), "four wrong codes are tolerated")
	require.EqualValues(t, 1, void.Load(), "the fifth voids the enrolment")
	require.EqualValues(t, 7, none.Load(), "the rest find nothing pending")
	var seeds int64
	require.NoError(t, s.sys(ctx).Raw("SELECT COUNT(*) FROM secrets WHERE name = ?", f.SecretName).Scan(&seeds).Error)
	require.Zero(t, seeds, "a void enrolment's seed is removed")
}
