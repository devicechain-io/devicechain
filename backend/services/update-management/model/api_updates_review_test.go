// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/stretchr/testify/require"
)

// UNKNOWN blocks a new assignment, cancel cannot end it, and only an explicit, reasoned operator
// abandonment frees the component. It ends CANCELLED with the reason kept, and tells the device.
func TestUnknownBlocksANewAssignmentUntilAnOperatorAbandonsIt(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	e.availableArtifact(t, "acme", "fw-2", "2.0.0", []byte("firmware two"))
	ctx := tenantCtx("acme")
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-1", progress(v, 1, ota.StageInstalling, "boot-1"), ota.VerdictApplied)
	e.advance(31 * time.Minute)
	res, err := e.api.SweepDeadlines(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, res.Unknown)
	require.Equal(t, ota.StateUnknown, e.state(t, "acme", v))

	// It blocks a new assignment, and the ordinary cancel refuses it.
	_, err = e.api.AssignArtifact(ctx, "dev-1", "fw-2")
	requireRefusal(t, err, CodeUpdateInProgress)
	_, err = e.api.CancelUpdate(ctx, v.Assignment.Token)
	requireRefusal(t, err, CodeBeyondRecall)

	// Abandonment needs a reason, and an UNKNOWN attempt.
	_, err = e.api.AbandonUnknown(ctx, v.Assignment.Token, "   ")
	requireRefusal(t, err, CodeInvalidReason)
	other, err := e.api.AssignArtifact(ctx, "dev-2", "fw-1")
	require.NoError(t, err)
	_, err = e.api.AbandonUnknown(ctx, other.Assignment.Token, "not unknown")
	requireRefusal(t, err, CodeNotUnknown)
	_, err = e.api.AbandonUnknown(tenantCtx("globex"), v.Assignment.Token, "wrong tenant")
	requireRefusal(t, err, CodeAssignmentNotFound)
	require.Equal(t, ota.StateUnknown, e.state(t, "acme", v), "none of the refusals moved it")

	before := len(e.desired.kinds(t, "dev-1"))
	out, err := e.api.AbandonUnknown(ctx, v.Assignment.Token, "device was scrapped")
	require.NoError(t, err)
	require.Equal(t, string(ota.StateCancelled), out.Attempt.State)
	require.Equal(t, ota.AbandonCode, out.Attempt.FailureCode)
	require.Equal(t, "device was scrapped", out.Attempt.FailureDetail)
	require.True(t, out.Attempt.FailurePlatform)
	kinds := e.desired.kinds(t, "dev-1")
	require.Equal(t, ota.KindUpdateCancel, kinds[len(kinds)-1], "the device is told")
	require.Equal(t, before+1, len(kinds))

	// Terminal is terminal: it cannot be abandoned twice, and the device cannot revive it.
	_, err = e.api.AbandonUnknown(ctx, v.Assignment.Token, "again")
	requireRefusal(t, err, CodeAttemptTerminal)
	e.mustApply(t, "acme", "dev-1", running(v, 2, "boot-2"), ota.VerdictLate)

	// The component is free.
	next, err := e.api.AssignArtifact(ctx, "dev-1", "fw-2")
	require.NoError(t, err)
	require.NotEqual(t, v.Assignment.Token, next.Assignment.Token)
}

// The verify path runs before anything is authenticated and must never create a key, whatever
// tenant name a forged capability carries.
func TestVerifyingNeverCreatesAKey(t *testing.T) {
	e := newUpdateEnv(t)
	forge := func(tenant string) string {
		p := capabilityPayload{V: capabilityVersion, T: tenant, D: "dev-1", A: "asg-1", G: "sha256:x", X: "fw-1",
			E: e.clock.Add(time.Minute).Unix()}
		return encodeForTest(t, p, make([]byte, 32))
	}
	for _, tenant := range []string{"acme", "ghost", "globex"} {
		_, err := e.api.VerifyCapability(context.Background(), forge(tenant), "fw-1")
		require.ErrorIs(t, err, ErrCapabilityInvalid, tenant)
	}
	var n int64
	require.NoError(t, e.api.RDB.Database.Raw("SELECT COUNT(*) FROM secrets").Scan(&n).Error)
	require.Zero(t, n, "verifying wrote a secret")
}

// An erased tenant cannot keep verifying on a cached key, and a tenant that comes back gets a key
// its other replicas agree on within the cache lifetime.
func TestAnErasedTenantsKeyIsNotTrustedFromTheCache(t *testing.T) {
	e := newUpdateEnv(t)
	c := Capability{Tenant: "acme", DeviceToken: "dev-1", AssignmentID: "asg-1", Digest: "sha256:x", ArtifactTok: "fw-1",
		Expires: e.clock.Add(5 * time.Minute)}
	minted, err := e.api.MintCapability(context.Background(), c)
	require.NoError(t, err)
	_, err = e.api.VerifyCapability(context.Background(), minted, "fw-1")
	require.NoError(t, err, "cached and live")

	e.api.TenantDeleted = func(string) bool { return true }
	_, err = e.api.VerifyCapability(context.Background(), minted, "fw-1")
	require.ErrorIs(t, err, ErrCapabilityInvalid, "a deleted tenant has no key to trust")

	// Erased and re-created: the old row is gone, the next mint makes a new key, and a replica
	// still holding the old one stops after the cache lifetime.
	e.api.TenantDeleted = nil
	require.NoError(t, e.api.RDB.Database.Exec("DELETE FROM secrets").Error)
	e.advance(keyTTL + time.Second)
	c.Expires = e.clock.Add(5 * time.Minute)
	fresh, err := e.api.MintCapability(context.Background(), c)
	require.NoError(t, err)
	_, err = e.api.VerifyCapability(context.Background(), fresh, "fw-1")
	require.NoError(t, err)
	_, err = e.api.VerifyCapability(context.Background(), minted, "fw-1")
	require.ErrorIs(t, err, ErrCapabilityInvalid, "a capability of the erased tenant's old key no longer verifies")
}

// A tenant with no key costs one secret-store read, not one per request.
func TestAMissingKeyIsRememberedBriefly(t *testing.T) {
	e := newUpdateEnv(t)
	counting := &countingResolves{SecretStore: e.api.Secrets}
	e.api.Secrets = counting
	p := capabilityPayload{V: capabilityVersion, T: "acme", D: "d", A: "a", G: "g", X: "fw-1", E: e.clock.Add(time.Minute).Unix()}
	forged := encodeForTest(t, p, make([]byte, 32))
	for range 5 {
		_, err := e.api.VerifyCapability(context.Background(), forged, "fw-1")
		require.ErrorIs(t, err, ErrCapabilityInvalid)
	}
	require.Equal(t, 1, counting.n, "five forged capabilities for a tenant with no key made one read")

	// The memory is short: after it, the store is asked again (the tenant may have minted since).
	e.advance(missTTL + time.Second)
	_, _ = e.api.VerifyCapability(context.Background(), forged, "fw-1")
	require.Equal(t, 2, counting.n)

	// And a mint clears it at once, on this replica.
	c := Capability{Tenant: "acme", DeviceToken: "dev-1", AssignmentID: "asg-1", Digest: "sha256:x", ArtifactTok: "fw-1",
		Expires: e.clock.Add(time.Minute)}
	minted, err := e.api.MintCapability(context.Background(), c)
	require.NoError(t, err)
	_, err = e.api.VerifyCapability(context.Background(), minted, "fw-1")
	require.NoError(t, err)
}

// The deleted-tenant gate on the download path: a capability that verified is still refused once
// its tenant has been through the delete door.
func TestADeletedTenantCannotResolveADownload(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	g, err := e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
	require.NoError(t, err)
	_ = g
	c := &Capability{Tenant: "acme", DeviceToken: "dev-1", AssignmentID: v.Assignment.Token,
		Digest: v.Attempt.TargetDigest, ArtifactTok: "fw-1", Expires: e.clock.Add(time.Minute)}
	_, err = e.api.ResolveDownload(ctx, c)
	require.NoError(t, err)

	e.api.TenantDeleted = func(tenant string) bool { return tenant == "acme" }
	_, err = e.api.ResolveDownload(ctx, c)
	require.ErrorIs(t, err, ErrDownloadStale)
}

// Two writers on one attempt: a write made from a stale read changes nothing.
func TestAStaleWriteLosesTheCompareAndSet(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)

	stale, err := e.api.AssignmentByToken(ctx, v.Assignment.Token) // version 0
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-1", progress(v, 1, ota.StageReceived, "b"), ota.VerdictApplied) // version 1

	cancelled, err := ota.Cancel(stale.Attempt.ToOTA(stale.Assignment.Token), e.clock)
	require.NoError(t, err)
	won, err := e.api.saveAttempt(ctx, stale.Attempt, cancelled)
	require.NoError(t, err)
	require.False(t, won, "a write from a stale read won")
	require.Equal(t, ota.StateInitiated, e.state(t, "acme", v), "the loser wrote nothing")
}

// A device report landing between the sweep reading an attempt and writing it wins; the sweep
// notices it lost, re-reads, finds the attempt no longer overdue, and does nothing: no state
// change and no cancel sent to the device.
func TestAReportRacingASweepTickWinsAndTheSweepDoesNothing(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-1", progress(v, 1, ota.StageDownloading, "b"), ota.VerdictApplied)
	e.advance(11 * time.Minute) // past the download deadline

	fired := false
	e.api.beforeSave = func(UpdateAttempt) {
		if fired {
			return
		}
		fired = true
		// The device reports progress at this instant, from another replica.
		got, err := e.api.ApplyReport(ctx, "dev-1", progress(v, 2, ota.StageDownloaded, "b"))
		require.NoError(t, err)
		require.Equal(t, ota.VerdictApplied, got)
	}
	sent := len(e.desired.kinds(t, "dev-1"))
	res, err := e.api.SweepDeadlines(context.Background(), 10)
	require.NoError(t, err)
	require.True(t, fired, "the race was not staged")
	require.Zero(t, res.TimedOut, "the sweep claimed a timeout it lost")
	require.Equal(t, ota.StateDownloaded, e.state(t, "acme", v), "the report won")
	require.Equal(t, sent, len(e.desired.kinds(t, "dev-1")), "the losing sweep sent the device a cancel")
}

type countingResolves struct {
	secrets.SecretStore
	n int
}

func (c *countingResolves) Resolve(ctx context.Context, ref secrets.SecretRef) ([]byte, error) {
	c.n++
	return c.SecretStore.Resolve(ctx, ref)
}

func (c *countingResolves) CreateIfAbsent(ctx context.Context, ref secrets.SecretRef, v []byte) (bool, error) {
	return secrets.CreateIfAbsent(ctx, c.SecretStore, ref, v)
}

// encodeForTest signs a payload with the given key, the way MintCapability does with the tenant key.
func encodeForTest(t *testing.T, p capabilityPayload, key []byte) string {
	t.Helper()
	body, err := json.Marshal(p)
	require.NoError(t, err)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(body) + "." + enc.EncodeToString(sign(key, p))
}
