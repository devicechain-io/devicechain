// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/purge"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newPurgeTestService is newTestService plus the membership tables, because
// DeleteTenant counts memberships before it will cut anything.
// The purge windows every admin test builds its Service with. They are the SHIPPED defaults
// (config's defaultTenantPurgeSettleSeconds and defaultTenantPurgeTokenHoldSeconds) rather
// than round test numbers, so a progress assertion here is about a Service the binary would
// actually build. A test picking its own would still pass while the real windows made the
// same deletion report something else entirely.
const (
	testSettle    = 300 * time.Second
	testTokenHold = 12 * time.Hour
)

func newPurgeTestService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(
		&iam.TenantTier{}, &iam.Tenant{}, &iam.Role{}, &iam.Identity{}, &iam.Membership{},
		&iam.TenantPurge{}, &iam.TenantPurgeStore{}))
	s := NewService(iam.NewStore(&rdb.RdbManager{Database: db}), testSettle, testTokenHold)
	seedTiers(t, s)
	return s
}

func createTenant(t *testing.T, s *Service, token string) {
	t.Helper()
	_, err := s.CreateTenant(context.Background(), TenantInput{Token: token, TierToken: iam.TierSilverToken})
	require.NoError(t, err)
}

// Deleting a tenant KEEPS THE ROW. That is the whole of ADR-077 slice 1 in one
// assertion, and the reason is not tidiness: the tenant token is the isolation key
// every other functional area writes into its rows (rdb.TenantScoped.TenantId), and
// the old hard delete freed that token for reuse — so the next tenant created at it
// inherited the deleted one's devices, dashboards, telemetry and secrets.
func TestDeleteTenantKeepsTheRowAndStampsAnEpoch(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")

	removed, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)
	require.True(t, removed, "the delete door was walked through")

	after, err := s.iam.TenantByToken(ctx, "acme")
	require.NoError(t, err, "the row must survive — it IS the token reservation")
	require.Equal(t, iam.PurgePurging, after.PurgeState)
	require.False(t, after.Enabled, "a purging tenant must not be enabled")
	require.NotNil(t, after.PurgeEpoch, "the epoch dates the cut; fences and the deletion record key on it")
}

// Idempotent in both directions, because a teardown that failed partway must be
// retryable: a missing tenant and an already-purging one both report "nothing changed"
// rather than erroring.
func TestDeleteTenantIsIdempotent(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")

	first, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)
	require.True(t, first)

	second, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err, "a second delete must not error")
	require.False(t, second, "nothing changed the second time")

	missing, err := s.DeleteTenant(ctx, "never-existed")
	require.NoError(t, err)
	require.False(t, missing)
}

// The reservation itself: once a token has been through the delete door, nothing may
// create a tenant at it again. This is the assertion that closes the disclosure path —
// with no successor tenant, there is nothing for the residue to be handed to.
func TestCreateTenantRefusesAReservedToken(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")
	_, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)

	_, err = s.CreateTenant(ctx, TenantInput{Token: "acme", TierToken: iam.TierSilverToken})
	require.ErrorIs(t, err, ErrTenantTokenReserved)

	// The negative control: an unrelated token is unaffected, so the refusal is about
	// this token's history and not a service that has stopped creating tenants.
	_, err = s.CreateTenant(ctx, TenantInput{Token: "acme-2", TierToken: iam.TierSilverToken})
	require.NoError(t, err)
}

// 🔴 The refusal must not read like an "already exists".
//
// dcctl's sim flow wraps createTenant in tolerateExists (backend/cli/sim/admin.go),
// which swallows any error whose text contains "already exists", "duplicate" or
// "unique" so that re-running `sim create` is idempotent. A reservation refusal
// phrased with any of those words would be silently treated as success by the caller
// most likely to hit it — dcctl would report ✅, having minted an identity and a
// membership against a tenant nobody can enter, and the sim would then fail at login
// with nothing pointing back here. (Not a disclosure: resolveTenantGrant refuses a
// deleted tenant, so the layer below still holds. A silent, misattributed break.)
//
// The coupling is real and cross-module, so it is asserted on both sides; the dcctl
// half lives in backend/cli/sim/admin_test.go.
func TestTenantTokenReservedIsNotMistakenForAnAlreadyExistsError(t *testing.T) {
	msg := strings.ToLower(ErrTenantTokenReserved.Error())
	for _, tolerated := range []string{"already exists", "duplicate", "unique"} {
		require.NotContainsf(t, msg, tolerated,
			"the reservation refusal contains %q, which dcctl's tolerateExists swallows as success", tolerated)
	}
	// And it has to actually say something, or the check above passes over an empty
	// string and proves nothing.
	require.Contains(t, msg, "reserved")
}

// An ACTIVE tenant at the same token still collides the old way, and that is
// deliberate: callers rely on tolerating a duplicate-key error to make create
// idempotent, so the reservation must not swallow the ordinary re-create path.
func TestCreateTenantStillCollidesOnAnActiveToken(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")

	_, err := s.CreateTenant(ctx, TenantInput{Token: "acme", TierToken: iam.TierSilverToken})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTenantTokenReserved,
		"an active duplicate is an already-exists, not a reservation")
}

// A deleted tenant takes no new members. Without this, DeleteTenant's zero-membership
// precondition holds only at the instant of the cut: an operator could attach a user
// afterwards, get a success toast, and hand them a tenant that answers every login
// attempt with "invalid credentials".
func TestAddMembershipRefusesADeletedTenant(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")
	_, err := s.CreateIdentity(ctx, CreateIdentityInput{Email: "someone@example.com", Password: "hunter2hunter2", Enabled: true})
	require.NoError(t, err)
	_, err = s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)

	_, err = s.AddMembership(ctx, "someone@example.com", "acme", nil)
	require.ErrorIs(t, err, ErrTenantDeleted)
}

// A deleted tenant must not pin its tier forever. The row keeps its NOT NULL tier_id
// (it survives to reserve the token), so counting tombstones would refuse every future
// tier deletion with "move them to another tier first" — naming tenants an operator
// can neither see nor move.
func TestDeletedTenantsDoNotBlockTierDeletion(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()
	createTenant(t, s, "acme")
	_, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)

	removed, err := s.DeleteTenantTier(ctx, iam.TierSilverToken)
	require.NoError(t, err, "a tier whose only tenant was deleted must be removable")
	require.True(t, removed)

	// The negative control: a LIVE tenant still pins its tier, so the exclusion above
	// narrowed the count rather than disabling the guard.
	createTenant2 := func(token, tier string) {
		_, err := s.CreateTenant(ctx, TenantInput{Token: token, TierToken: tier})
		require.NoError(t, err)
	}
	createTenant2("live-one", iam.TierGoldToken)
	_, err = s.DeleteTenantTier(ctx, iam.TierGoldToken)
	require.ErrorIs(t, err, ErrTierInUse)
}

// completePurgeOf drives the REAL completion path for a token: it reads the tenant row the
// delete stamped, and completes the purge through the same call the coordinator makes — which
// removes the row and releases the token.
//
// Reconstructing completion by stamping CompletedAt directly would skip the epoch check that
// is the entire subject of the test below, and the test would then be asserting against a
// state the platform can never actually reach.
func completePurgeOf(t *testing.T, s *Service, token string, rows int64) *iam.TenantPurge {
	t.Helper()
	ctx := context.Background()
	tenant, err := s.iam.TenantByToken(ctx, token)
	require.NoError(t, err)
	require.NotNil(t, tenant.PurgeEpoch, "the delete must have stamped an epoch")
	rec, err := s.iam.EnsurePurgeRecord(ctx, token, *tenant.PurgeEpoch)
	require.NoError(t, err)
	require.NoError(t, s.iam.CompleteTenantPurge(ctx, tenant, rec, rows, time.Now().UTC()))
	return rec
}

// 🔴 THE (token, epoch) TRAP. This is the one test in the visibility work that is a SECURITY
// assertion rather than a UI one.
//
// A token is RELEASED on completion, so one token carries several deletion records over an
// instance's life and only the pair (token, epoch) names a deletion. A lookup that resolved a
// token to "the latest record at that token" would attribute a predecessor's erasure evidence
// to whatever tenant holds that name now — the same cross-tenant confusion ADR-077 exists to
// close, re-entering through the very surface built to audit it.
//
// It drives the whole real sequence — create, delete, complete, RE-create at the reclaimed
// token, delete again — because the reuse is the premise. A test that seeded two records by
// hand would be asserting about a situation it had asserted into existence.
func TestADeletionLookupByTokenNeverReturnsAPredecessorsRecord(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()

	// The predecessor: created, deleted, and its purge completed — token back in circulation.
	createTenant(t, s, "acme")
	_, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)
	predecessor := completePurgeOf(t, s, "acme", 4242)
	require.NoError(t, s.iam.RecordPurgeStore(ctx, &iam.TenantPurgeStore{
		TenantPurgeID: predecessor.ID, Store: "rdb", Complete: true,
		Deferred: "PREDECESSOR DATA — must never surface under the successor",
	}))

	// The successor takes the reclaimed token and is itself deleted.
	createTenant(t, s, "acme")
	_, err = s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)
	successor, err := s.iam.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	require.False(t, predecessor.Epoch.Equal(*successor.PurgeEpoch),
		"the two deletions must have different epochs, or this test proves nothing")
	// The coordinator's first pass is what creates the record; DeleteTenant only stamps the
	// row. See TestATenantDeletedButNotYetSweptHasNoRecordYet.
	_, err = s.iam.EnsurePurgeRecord(ctx, "acme", *successor.PurgeEpoch)
	require.NoError(t, err)

	// Asking by token alone must find the IN-FLIGHT one, which is unambiguous.
	rec, lines, err := s.TenantDeletion(ctx, "acme", nil)
	require.NoError(t, err)
	assert.True(t, rec.Epoch.Equal(*successor.PurgeEpoch),
		"a token-only lookup must resolve to the deletion in flight, never to a completed one")
	assert.EqualValues(t, 0, rec.Rows, "the predecessor's row count must not surface here")
	for _, l := range lines {
		assert.NotContains(t, l.Deferred, "PREDECESSOR DATA",
			"the predecessor's ledger must not be served under the successor's deletion")
	}

	// And the predecessor is still reachable — by its epoch, which is what names it.
	old, oldLines, err := s.TenantDeletion(ctx, "acme", &predecessor.Epoch)
	require.NoError(t, err)
	assert.EqualValues(t, 4242, old.Rows, "the predecessor's own record must remain readable")
	require.Len(t, oldLines, 1)
	assert.Contains(t, oldLines[0].Deferred, "PREDECESSOR DATA")
}

// TestDeletionsListIsInstanceWideAndNewestFirst pins the ordering the history page reads in.
//
// It is the OPPOSITE of the coordinator's work-queue ordering (oldest cut first, so a backlog
// drains in the order deletions were asked for). Both are deliberate; a shared "list purges"
// helper serving both would have to get one of them wrong.
func TestDeletionsListIsInstanceWideAndNewestFirst(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	for i, token := range []string{"oldest", "middle", "newest"} {
		_, err := s.iam.EnsurePurgeRecord(ctx, token, base.Add(time.Duration(i)*time.Hour))
		require.NoError(t, err)
	}

	recs, err := s.TenantDeletions(ctx, nil, 0, 0)
	require.NoError(t, err)
	require.Len(t, recs, 3, "the list is instance-wide: every tenant's records, not one tenant's")
	assert.Equal(t, []string{"newest", "middle", "oldest"},
		[]string{recs[0].Token, recs[1].Token, recs[2].Token},
		"a human asking 'what happened recently' reads newest first")
}

// TestDeletionsCanBeFilteredByCompletion covers the filter the history page's two tabs use.
// The both-directions assertion is the point: a filter that ignored its argument would satisfy
// either half alone.
func TestDeletionsCanBeFilteredByCompletion(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()

	createTenant(t, s, "finished")
	_, err := s.DeleteTenant(ctx, "finished")
	require.NoError(t, err)
	completePurgeOf(t, s, "finished", 7)

	createTenant(t, s, "in-flight")
	_, err = s.DeleteTenant(ctx, "in-flight")
	require.NoError(t, err)
	_, err = s.iam.EnsurePurgeRecord(ctx, "in-flight", *mustTenant(t, s, "in-flight").PurgeEpoch)
	require.NoError(t, err)

	yes, no := true, false
	done, err := s.TenantDeletions(ctx, &yes, 0, 0)
	require.NoError(t, err)
	require.Len(t, done, 1)
	assert.Equal(t, "finished", done[0].Token)

	open, err := s.TenantDeletions(ctx, &no, 0, 0)
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, "in-flight", open[0].Token)
}

func mustTenant(t *testing.T, s *Service, token string) *iam.Tenant {
	t.Helper()
	tenant, err := s.iam.TenantByToken(context.Background(), token)
	require.NoError(t, err)
	return tenant
}

// TestACompletedDeletionWaitsOnNothing pins the early return in TenantDeletionProgress.
//
// A finished deletion's lines are all clean and both its windows are long elapsed, so today
// the arithmetic would reach the same answer without the check — by coincidence. If either
// window grew, a completed record would start reporting a countdown, which is a plain lie
// about a deletion that is already over. The epoch here is NOW, so the token hold is very
// much outstanding by the arithmetic alone.
func TestACompletedDeletionWaitsOnNothing(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()

	createTenant(t, s, "acme")
	_, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)
	rec := completePurgeOf(t, s, "acme", 5)

	done, lines, err := s.TenantDeletion(ctx, "acme", &rec.Epoch)
	require.NoError(t, err)
	p := s.TenantDeletionProgress(done, lines)

	assert.Equal(t, purge.WaitNone, p.Awaiting,
		"a completed deletion is over; it must not report a countdown")
	assert.Nil(t, p.ElapsesAt)
}

// TestATenantDeletedButNotYetSweptHasNoRecordYet pins a state the console WILL hit and that
// nothing else in this file covers.
//
// Deleting a tenant stamps the row; it does not create the deletion record. The record is
// written by the coordinator's first pass, up to a minute later at the default interval — so
// between the operator clicking delete and that pass, the tenant is `purging` and there is no
// record to read. A surface that treated the absent record as an error would show a failure
// for the first minute of every single deletion, which is exactly when the operator is
// watching.
func TestATenantDeletedButNotYetSweptHasNoRecordYet(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()

	createTenant(t, s, "acme")
	_, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)

	_, _, err = s.TenantDeletion(ctx, "acme", nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound,
		"the store reports absence as ErrRecordNotFound; the resolver maps that to a null field "+
			"rather than a GraphQL error, so the console can say 'starting' instead of 'failed'")

	tenant, err := s.iam.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	assert.True(t, tenant.PurgeState.Deleted(),
		"the control: the tenant really IS deleted, so this is a missing RECORD and not a "+
			"missing deletion")
}

// 🔴 TestADeletionLookupNeverSurfacesACompletedRecordUnderALiveToken is the OTHER half of the
// (token, epoch) trap, and the half a mutation proved the first test does not cover.
//
// The first test has a completed predecessor AND an in-flight successor, so "the record in
// flight" and "the newest record" are the same row and a lookup that took the newest would
// pass it. The dangerous case is the one where they DIFFER: a token whose only deletion is
// finished, now held by a LIVE tenant.
//
// A console opening that live tenant asks tenantDeletion(token:) with no epoch. Resolved as
// "the newest record at this token", it would hand back the predecessor's deletion — showing
// a healthy tenant as deleted, with someone else's erasure evidence attached. Resolved as
// "the deletion in flight", there is none, and the honest answer is nothing.
func TestADeletionLookupNeverSurfacesACompletedRecordUnderALiveToken(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()

	createTenant(t, s, "acme")
	_, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)
	predecessor := completePurgeOf(t, s, "acme", 4242)

	// The token is back in circulation and a NEW, perfectly healthy tenant takes it.
	createTenant(t, s, "acme")
	live, err := s.iam.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	require.False(t, live.PurgeState.Deleted(), "the successor must be alive, or this proves nothing")

	_, _, err = s.TenantDeletion(ctx, "acme", nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound,
		"a live tenant has no deletion in flight; returning the predecessor's completed record "+
			"would attribute a prior tenant's erasure to this one")

	// The predecessor's record is not lost — it is addressed by its epoch, which is what
	// names it. This is the control: the refusal above must be about IDENTITY, not about the
	// record having become unreachable.
	old, _, err := s.TenantDeletion(ctx, "acme", &predecessor.Epoch)
	require.NoError(t, err)
	assert.EqualValues(t, 4242, old.Rows)
}

// TestTheEpochRoundTripsThroughTheApi pins the property that makes the epoch usable as half of
// an identifier: what the API publishes must be what the API accepts.
//
// The epoch is time.Now().UTC() at the cut — not truncated anywhere — and it is looked up with
// an EXACT match. Publishing it at second precision would publish an identifier that does not
// identify anything, and the first casualty would be the deletion-history page's own detail
// link. The test formats through the resolver's formatter and feeds the result straight back.
func TestTheEpochRoundTripsThroughTheApi(t *testing.T) {
	s := newPurgeTestService(t)
	ctx := context.Background()

	createTenant(t, s, "acme")
	_, err := s.DeleteTenant(ctx, "acme")
	require.NoError(t, err)
	tenant, err := s.iam.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	_, err = s.iam.EnsurePurgeRecord(ctx, "acme", *tenant.PurgeEpoch)
	require.NoError(t, err)
	require.NotZero(t, tenant.PurgeEpoch.Nanosecond(),
		"the epoch must carry sub-second precision, or this test cannot detect losing it")

	published := tenant.PurgeEpoch.UTC().Format(time.RFC3339Nano)
	parsed, err := time.Parse(time.RFC3339Nano, published)
	require.NoError(t, err)

	rec, _, err := s.TenantDeletion(ctx, "acme", &parsed)
	require.NoError(t, err,
		"the epoch this API publishes must find the record it names when handed back")
	assert.True(t, rec.Epoch.Equal(*tenant.PurgeEpoch))
}
