// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func stepUpPgSession(t *testing.T) (*mfaPgEnv, []byte, *TokenPair, *auth.Claims) {
	t.Helper()
	e := newMfaPgEnv(t)
	e.member(t, "pat@example.com", "pw-1")
	seed, codes := e.enroll(t, "pat@example.com", "pw-1")
	id, err := e.store.IdentityByEmail(context.Background(), "pat@example.com")
	require.NoError(t, err)
	factor, err := e.store.ActiveMfaFactorFor(context.Background(), id.ID)
	require.NoError(t, err)
	fixed := time.Unix(factor.LastStep*30, 0).UTC()
	e.m.now = func() time.Time { return fixed }
	// Use recovery for the initial session so the current TOTP step remains available.
	ia, err := e.m.VerifyMfa(context.Background(), e.challenge(t, "pat@example.com", "pw-1").Challenge, codes[0], "203.0.113.5")
	require.NoError(t, err)
	pair, err := e.m.SelectTenant(context.Background(), ia.IdentityToken, "acme")
	require.NoError(t, err)
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	return e, seed, pair, access
}

func TestStepUpPgWrongCodesShareSignInLockoutAndPreserveRefresh(t *testing.T) {
	e, seed, pair, access := stepUpPgSession(t)
	ctx := context.Background()
	now := e.m.clock().Add(30 * time.Second)
	e.m.now = func() time.Time { return now }
	right := credential.GenerateTOTP(seed, now)
	// Choose a code outside every accepted step, rather than assuming 000000 is wrong.
	wrong := ""
	for i := 0; i < 1000000; i++ {
		candidate := fmt.Sprintf("%06d", i)
		valid := false
		for k := -MfaTOTPSkew; k <= MfaTOTPSkew; k++ {
			if candidate == credential.GenerateTOTP(seed, now.Add(time.Duration(k)*30*time.Second)) {
				valid = true
			}
		}
		if !valid {
			wrong = candidate
			break
		}
	}
	for i := 0; i < iam.MfaIdentityLockAfter; i++ {
		_, err := e.m.StepUp(ctx, access, pair.RefreshToken, wrong, fmt.Sprintf("203.0.113.%d", i+1))
		require.ErrorIs(t, err, ErrMfaInvalidCode)
	}
	_, err := e.m.StepUp(ctx, access, pair.RefreshToken, right, "198.51.100.1")
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	require.Positive(t, e.metrics.count("totp/locked"))
	// The same factor lock applies to sign-in too.
	ch := e.challenge(t, "pat@example.com", "pw-1")
	_, err = e.m.VerifyMfa(ctx, ch.Challenge, right, "198.51.100.2")
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err)
}

func TestStepUpPgFreshTokensSameSessionRotationAuditAndReplay(t *testing.T) {
	e, seed, pair, access := stepUpPgSession(t)
	ctx := context.Background()
	refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
	require.NoError(t, err)
	// Age the session's factor and mint the actual pre-step-up access token from it.
	rec, err := e.m.loadSession(refresh.SessionID, refresh.Username)
	require.NoError(t, err)
	old := time.Now().Add(-time.Hour)
	rec.MfaAt = &old
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	_, err = e.m.refreshKV.Put(sessionKey(refresh.SessionID), b)
	require.NoError(t, err)
	pair, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err)
	access, err = e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	policy := auth.StepUp{MaxAge: 15 * time.Minute}
	now := e.m.clock().Add(30 * time.Second).Truncate(time.Second)
	e.m.now = func() time.Time { return now }
	require.False(t, auth.StepUpFresh(access, policy, now))
	// A TOTP sign-in in this step spends the same factor replay state.
	_, err = e.m.VerifyMfa(ctx, e.challenge(t, "pat@example.com", "pw-1").Challenge, credential.GenerateTOTP(seed, now), "203.0.113.5")
	require.NoError(t, err)
	_, err = e.m.StepUp(ctx, access, pair.RefreshToken, credential.GenerateTOTP(seed, now), "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	now = now.Add(30 * time.Second)
	code := credential.GenerateTOTP(seed, now)
	fresh, err := e.m.StepUp(ctx, access, pair.RefreshToken, code, "203.0.113.5")
	require.NoError(t, err)
	upgraded, err := e.validator.Validate(fresh.AccessToken)
	require.NoError(t, err)
	require.Equal(t, now.Unix(), upgraded.MfaTime.Unix())
	require.Equal(t, auth.AmrPasswordOTP(), upgraded.Amr)
	require.True(t, auth.StepUpFresh(upgraded, policy, now))
	rotated, err := e.validator.ValidateRefresh(fresh.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, refresh.SessionID, rotated.SessionID)
	require.Equal(t, refresh.SessionEpoch, rotated.SessionEpoch)
	rec, err = e.m.loadSession(rotated.SessionID, rotated.Username)
	require.NoError(t, err)
	require.Equal(t, now, *rec.MfaAt)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken)
	var count int64
	require.NoError(t, e.db.WithContext(core.WithSystemContext(ctx)).Model(&rdb.AuditEvent{}).Where("operation = ?", rdb.AuditOpStepUp).Count(&count).Error)
	require.EqualValues(t, 1, count)
	_, err = e.m.StepUp(ctx, upgraded, fresh.RefreshToken, code, "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	_, err = e.m.Refresh(ctx, fresh.RefreshToken)
	require.NoError(t, err)
}

func TestStepUpPgSessionUpdateConflictConsumesRefreshWithoutMinting(t *testing.T) {
	e, seed, pair, access := stepUpPgSession(t)
	ctx := context.Background()
	now := e.m.clock().Add(time.Minute)
	e.m.now = func() time.Time { return now }
	refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
	require.NoError(t, err)
	e.m.refreshKV = &hookKV{KeyValue: e.m.refreshKV, beforeUpdate: func(key string) {
		require.Equal(t, sessionKey(refresh.SessionID), key)
		rec, err := e.m.loadSession(refresh.SessionID, refresh.Username)
		require.NoError(t, err)
		future := now.Add(time.Minute)
		rec.MfaAt = &future
		b, err := json.Marshal(rec)
		require.NoError(t, err)
		_, err = e.m.refreshKV.Put(key, b)
		require.NoError(t, err)
	}}
	_, err = e.m.StepUp(ctx, access, pair.RefreshToken, credential.GenerateTOTP(seed, now), "203.0.113.5")
	require.ErrorIs(t, err, ErrRefreshUnavailable)
	rec, err := e.m.loadSession(refresh.SessionID, refresh.Username)
	require.NoError(t, err)
	require.Equal(t, now.Add(time.Minute), *rec.MfaAt)
	_, err = e.m.refreshKV.Get(refresh.ID)
	require.ErrorIs(t, err, nats.ErrKeyNotFound)
}

func TestStepUpPgNeverMovesFactorTimeBackwards(t *testing.T) {
	e, seed, pair, access := stepUpPgSession(t)
	ctx := context.Background()
	now := e.m.clock().Add(30 * time.Second)
	e.m.now = func() time.Time { return now }
	refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
	require.NoError(t, err)
	rec, err := e.m.loadSession(refresh.SessionID, refresh.Username)
	require.NoError(t, err)
	ahead := now.Add(10 * time.Second)
	rec.MfaAt = &ahead
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	_, err = e.m.refreshKV.Put(sessionKey(refresh.SessionID), b)
	require.NoError(t, err)
	fresh, err := e.m.StepUp(ctx, access, pair.RefreshToken, credential.GenerateTOTP(seed, now), "203.0.113.5")
	require.NoError(t, err)
	c, err := e.validator.Validate(fresh.AccessToken)
	require.NoError(t, err)
	require.Equal(t, ahead.Unix(), c.MfaTime.Unix())
	rec, err = e.m.loadSession(refresh.SessionID, refresh.Username)
	require.NoError(t, err)
	require.Equal(t, ahead, *rec.MfaAt)
}

func TestStepUpPgRefusesStaleSessionRecordEpochWithoutMintingOrConsumingRefresh(t *testing.T) {
	e, seed, pair, access := stepUpPgSession(t)
	ctx := context.Background()
	now := e.m.clock().Add(30 * time.Second)
	e.m.now = func() time.Time { return now }
	refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
	require.NoError(t, err)
	id, err := e.store.IdentityByEmail(ctx, refresh.Username)
	require.NoError(t, err)
	require.Equal(t, id.SessionEpoch, string(refresh.SessionEpoch))
	rec, err := e.m.loadSession(refresh.SessionID, refresh.Username)
	require.NoError(t, err)
	// Only the session record is stale; the identity and signed refresh still agree.
	rec.Epoch = id.SessionEpoch + "-stale"
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	_, err = e.m.refreshKV.Put(sessionKey(refresh.SessionID), b)
	require.NoError(t, err)
	before, err := e.m.refreshKV.Get(refresh.ID)
	require.NoError(t, err)
	fresh, err := e.m.StepUp(ctx, access, pair.RefreshToken, credential.GenerateTOTP(seed, now), "203.0.113.5")
	require.ErrorIs(t, err, ErrInvalidToken)
	require.Nil(t, fresh, "a stale session record must not mint tokens")
	after, err := e.m.refreshKV.Get(refresh.ID)
	require.NoError(t, err, "a stale session record must not consume the refresh token")
	require.Equal(t, before.Revision(), after.Revision())
	require.Equal(t, before.Value(), after.Value())
}

// Force the ordinary refresh to win after TOTP verification, at the claim itself.
func TestStepUpPgRefreshWinsClaimWithoutElevatingSession(t *testing.T) {
	e, seed, pair, access := stepUpPgSession(t)
	e.metrics = newRecordingMfaMetrics()
	e.m.SetMfaMetrics(e.metrics)
	ctx := context.Background()
	now := e.m.clock().Add(time.Minute)
	e.m.now = func() time.Time { return now }
	refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
	require.NoError(t, err)
	before, err := e.m.loadSession(refresh.SessionID, refresh.Username)
	require.NoError(t, err)
	var competing *TokenPair
	e.m.refreshKV = &hookKV{KeyValue: e.m.refreshKV, beforeDelete: func(key string) {
		require.Equal(t, refresh.ID, key)
		require.Equal(t, 1, e.metrics.count("totp/ok"), "factor verified before the race")
		competing, err = e.m.Refresh(ctx, pair.RefreshToken)
		require.NoError(t, err)
	}}
	fresh, err := e.m.StepUp(ctx, access, pair.RefreshToken, credential.GenerateTOTP(seed, now), "203.0.113.5")
	require.ErrorIs(t, err, ErrInvalidToken)
	require.Nil(t, fresh)
	after, err := e.m.loadSession(refresh.SessionID, refresh.Username)
	require.NoError(t, err)
	require.Equal(t, before, after)
	claims, err := e.validator.Validate(competing.AccessToken)
	require.NoError(t, err)
	require.Equal(t, before.MfaAt.Unix(), claims.MfaTime.Unix())
	require.Equal(t, before.Amr, claims.Amr)
}

func TestStepUpPgAbsoluteSessionLifetime(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint("expired=", expired), func(t *testing.T) {
			e, seed, pair, access := stepUpPgSession(t)
			e.metrics = newRecordingMfaMetrics()
			e.m.SetMfaMetrics(e.metrics)
			ctx := context.Background()
			e.m.refreshTTL = time.Hour
			refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
			require.NoError(t, err)
			rec, err := e.m.loadSession(refresh.SessionID, refresh.Username)
			require.NoError(t, err)
			now := e.m.clock().Add(time.Minute)
			e.m.now = func() time.Time { return now }
			rec.Anchor = now.Add(-e.m.refreshTTL).Add(time.Nanosecond)
			if expired {
				rec.Anchor = rec.Anchor.Add(-2 * time.Nanosecond)
			}
			data, err := json.Marshal(rec)
			require.NoError(t, err)
			_, err = e.m.refreshKV.Put(sessionKey(refresh.SessionID), data)
			require.NoError(t, err)
			session, err := e.m.refreshKV.Get(sessionKey(refresh.SessionID))
			require.NoError(t, err)
			token, err := e.m.refreshKV.Get(refresh.ID)
			require.NoError(t, err)
			fresh, err := e.m.StepUp(ctx, access, pair.RefreshToken, credential.GenerateTOTP(seed, now), "203.0.113.5")
			if !expired {
				require.NoError(t, err)
				require.NotNil(t, fresh)
				return
			}
			require.ErrorIs(t, err, ErrInvalidToken)
			require.Nil(t, fresh)
			require.Empty(t, e.metrics.verifications, "no code was evaluated")
			after, err := e.m.refreshKV.Get(sessionKey(refresh.SessionID))
			require.NoError(t, err)
			require.Equal(t, session.Revision(), after.Revision())
			require.Equal(t, session.Value(), after.Value())
			after, err = e.m.refreshKV.Get(refresh.ID)
			require.NoError(t, err)
			require.Equal(t, token.Revision(), after.Revision())
			require.Equal(t, token.Value(), after.Value())
		})
	}
}
