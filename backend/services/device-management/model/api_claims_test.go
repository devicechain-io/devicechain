// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ClaimStatus.Valid accepts the known states and rejects others (ADR-012).
func TestClaimStatusValid(t *testing.T) {
	for _, valid := range []ClaimStatus{
		ClaimStatusOpen,
		ClaimStatusClaimed,
		ClaimStatusCanceled,
	} {
		if !valid.Valid() {
			t.Errorf("known status %q rejected", valid)
		}
	}
	for _, invalid := range []ClaimStatus{"", "BOGUS", "open"} {
		if invalid.Valid() {
			t.Errorf("unknown status %q accepted", invalid)
		}
	}
}

// claim builds a device claim with the given status/secret/expiry for exercising
// evaluateDeviceClaim in isolation.
func claim(status ClaimStatus, secret string, expires *time.Time) *DeviceClaim {
	c := &DeviceClaim{
		Status:      string(status),
		ClaimSecret: secret,
	}
	if expires != nil {
		c.ExpiresAt = sql.NullTime{Time: *expires, Valid: true}
	}
	return c
}

// An open, unexpired claim whose secret matches can be redeemed.
func TestEvaluateDeviceClaim_OK(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	err := evaluateDeviceClaim(claim(ClaimStatusOpen, "s3cret", nil), "s3cret", now)
	assert.NoError(t, err)
}

// A non-open claim (already redeemed or canceled) cannot be redeemed.
func TestEvaluateDeviceClaim_NotOpen(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	for _, status := range []ClaimStatus{ClaimStatusClaimed, ClaimStatusCanceled} {
		err := evaluateDeviceClaim(claim(status, "s3cret", nil), "s3cret", now)
		assert.ErrorIs(t, err, ErrClaimNotOpen, "status %q should not be redeemable", status)
	}
}

// A wrong secret is rejected.
func TestEvaluateDeviceClaim_SecretMismatch(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	err := evaluateDeviceClaim(claim(ClaimStatusOpen, "s3cret", nil), "wrong", now)
	assert.ErrorIs(t, err, ErrClaimSecretMismatch)
}

// Expiry is enforced; a claim expiring exactly at now is already expired.
func TestEvaluateDeviceClaim_Expired(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	err := evaluateDeviceClaim(claim(ClaimStatusOpen, "s3cret", &past), "s3cret", now)
	assert.ErrorIs(t, err, ErrClaimExpired)
}

func TestEvaluateDeviceClaim_ExpiresAtExactlyNow(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	err := evaluateDeviceClaim(claim(ClaimStatusOpen, "s3cret", &now), "s3cret", now)
	assert.ErrorIs(t, err, ErrClaimExpired)
}

func TestEvaluateDeviceClaim_NotYetExpired(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	err := evaluateDeviceClaim(claim(ClaimStatusOpen, "s3cret", &future), "s3cret", now)
	assert.NoError(t, err)
}

// The status gate takes precedence over an otherwise-bad secret: a redeemed claim
// reports not-open rather than secret-mismatch.
func TestEvaluateDeviceClaim_NotOpenBeatsSecret(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	err := evaluateDeviceClaim(claim(ClaimStatusClaimed, "s3cret", nil), "wrong", now)
	assert.ErrorIs(t, err, ErrClaimNotOpen)
}

// An empty stored secret never matches, even when an empty secret is presented —
// a constant-time "" == "" would otherwise pass (review #5 defense in depth).
func TestEvaluateDeviceClaim_EmptyStoredSecret(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	err := evaluateDeviceClaim(claim(ClaimStatusOpen, "", nil), "", now)
	assert.ErrorIs(t, err, ErrClaimSecretMismatch)
}
