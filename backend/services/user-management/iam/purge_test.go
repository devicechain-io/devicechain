// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// newPurgeTestStore is newTestStore plus the deletion-record tables.
func newPurgeTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&Tenant{}, &TenantPurge{}, &TenantPurgeStore{}))
	return NewStore(&rdb.RdbManager{Database: db})
}

// beginPurge creates a tenant and cuts it, returning the tenant and its epoch.
func beginPurge(t *testing.T, s *Store, token string) (*Tenant, time.Time) {
	t.Helper()
	ctx := context.Background()
	tenant := &Tenant{Token: token}
	require.NoError(t, s.CreateTenant(ctx, tenant))
	epoch := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.BeginTenantPurge(ctx, tenant, epoch))
	reloaded, err := s.TenantByToken(ctx, token)
	require.NoError(t, err)
	return reloaded, epoch
}

// TestCompleteTenantPurgeReleasesTheTokenForReuse is the whole point of ADR-077 slice 2
// stated as a test: after a purge completes, the token is available again.
//
// It asserts the OUTCOME rather than the mechanism, and the distinction matters here
// more than usual. Tenant embeds gorm.Model, so an ordinary Delete is a SOFT delete: it
// sets deleted_at, reports one row affected, and leaves the row — and the unique index on
// token counts soft-deleted rows, so the token stays reserved forever while every line of
// code reports it released. A test that asserted "CompleteTenantPurge returned nil" or
// even "TenantByToken now returns not-found" passes with that bug present. Creating a new
// tenant at the same token is the assertion the bug cannot survive.
func TestCompleteTenantPurgeReleasesTheTokenForReuse(t *testing.T) {
	s := newPurgeTestStore(t)
	ctx := context.Background()

	tenant, epoch := beginPurge(t, s, "acme")
	rec, err := s.EnsurePurgeRecord(ctx, "acme", epoch)
	require.NoError(t, err)

	require.NoError(t, s.CompleteTenantPurge(ctx, tenant, rec, 42, time.Now().UTC()))

	// The row is physically gone, not merely filtered out of ordinary reads.
	var remaining int64
	require.NoError(t, s.sys(ctx).Unscoped().Model(&Tenant{}).Where("token = ?", "acme").Count(&remaining).Error)
	require.Zero(t, remaining, "the tenant row must be hard-deleted; a soft delete keeps the unique index occupied")

	// And the token really is free — the successor this ADR exists to make safe.
	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "acme"}))

	// The record survives its subject and says the erasure finished.
	var stored TenantPurge
	require.NoError(t, s.sys(ctx).Where("id = ?", rec.ID).First(&stored).Error)
	require.True(t, stored.Complete())
	require.EqualValues(t, 42, stored.Rows)
}

// TestCompleteTenantPurgeRefusesATenantThatIsNotPurging pins the predicate inside the
// DELETE. The coordinator establishes the lifecycle before it sweeps, but this method is
// the one that removes a row, and a guard that lives only in the caller is a guard one
// refactor away from being gone.
func TestCompleteTenantPurgeRefusesATenantThatIsNotPurging(t *testing.T) {
	s := newPurgeTestStore(t)
	ctx := context.Background()

	live := &Tenant{Token: "still-in-business"}
	require.NoError(t, s.CreateTenant(ctx, live))

	// A record that names the live tenant — the transposition this guards against.
	rec, err := s.EnsurePurgeRecord(ctx, live.Token, time.Now().UTC())
	require.NoError(t, err)

	err = s.CompleteTenantPurge(ctx, live, rec, 0, time.Now().UTC())
	require.Error(t, err)
	require.Contains(t, err.Error(), "affected 0 rows")

	// The refusal is worth nothing unless the tenant survived it.
	got, err := s.TenantByToken(ctx, live.Token)
	require.NoError(t, err)
	require.Equal(t, PurgeActive, got.PurgeState)
}

// TestCompleteTenantPurgeRefusesAMismatchedRecord catches the other transposition: the
// right tenant loaded with somebody else's record, which would release one token while
// recording the erasure of another.
func TestCompleteTenantPurgeRefusesAMismatchedRecord(t *testing.T) {
	s := newPurgeTestStore(t)
	ctx := context.Background()

	victim, epoch := beginPurge(t, s, "victim")
	other, err := s.EnsurePurgeRecord(ctx, "somebody-else", epoch)
	require.NoError(t, err)

	err = s.CompleteTenantPurge(ctx, victim, other, 0, time.Now().UTC())
	require.Error(t, err)
	require.Contains(t, err.Error(), "somebody-else")

	_, err = s.TenantByToken(ctx, "victim")
	require.NoError(t, err, "a mismatched record must not remove the tenant")
}

// TestPurgeRecordsAreKeyedByTokenAndEpoch is the reason the epoch is load-bearing rather
// than decorative. Once the token is released it can be purged again, and a record keyed
// by token alone would let the second purge overwrite the first — erasing the evidence
// that the first tenant was erased.
func TestPurgeRecordsAreKeyedByTokenAndEpoch(t *testing.T) {
	s := newPurgeTestStore(t)
	ctx := context.Background()

	first := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	second := time.Now().UTC().Truncate(time.Second)

	a, err := s.EnsurePurgeRecord(ctx, "acme", first)
	require.NoError(t, err)
	b, err := s.EnsurePurgeRecord(ctx, "acme", second)
	require.NoError(t, err)
	require.NotEqual(t, a.ID, b.ID, "two purges of the same token must be two records")

	var n int64
	require.NoError(t, s.sys(ctx).Model(&TenantPurge{}).Where("token = ?", "acme").Count(&n).Error)
	require.EqualValues(t, 2, n)
}

// TestEnsurePurgeRecordIsIdempotent covers the ordinary case: every pass calls it, and it
// must converge on one record rather than conflict or duplicate.
func TestEnsurePurgeRecordIsIdempotent(t *testing.T) {
	s := newPurgeTestStore(t)
	ctx := context.Background()
	epoch := time.Now().UTC().Truncate(time.Second)

	first, err := s.EnsurePurgeRecord(ctx, "acme", epoch)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		again, err := s.EnsurePurgeRecord(ctx, "acme", epoch)
		require.NoError(t, err)
		require.Equal(t, first.ID, again.ID)
	}
}

// TestEnsurePurgeRecordRefusesAnEmptyToken guards the record's identity: a row with no
// token names nothing, and would collide with every other tokenless purge at the same
// epoch.
func TestEnsurePurgeRecordRefusesAnEmptyToken(t *testing.T) {
	s := newPurgeTestStore(t)
	_, err := s.EnsurePurgeRecord(context.Background(), "", time.Now().UTC())
	require.Error(t, err)
}

// TestRecordPurgeStoreReplacesTheLine pins the upsert. Passes repeat, and a second row
// for the same store would make "is every store complete?" ambiguous: a stale incomplete
// line beside a fresh complete one has no defined answer, and the natural read (any row
// complete) would release the token on the strength of an outdated observation.
func TestRecordPurgeStoreReplacesTheLine(t *testing.T) {
	s := newPurgeTestStore(t)
	ctx := context.Background()
	rec, err := s.EnsurePurgeRecord(ctx, "acme", time.Now().UTC().Truncate(time.Second))
	require.NoError(t, err)

	clean := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.RecordPurgeStore(ctx, &TenantPurgeStore{
		TenantPurgeID: rec.ID, Store: "rdb", Rows: 7, Deferred: "something remains",
	}))
	require.NoError(t, s.RecordPurgeStore(ctx, &TenantPurgeStore{
		TenantPurgeID: rec.ID, Store: "rdb", Rows: 0, Complete: true, CleanSince: &clean,
	}))

	lines, err := s.PurgeStores(ctx, rec.ID)
	require.NoError(t, err)
	require.Len(t, lines, 1, "a repeated pass must replace the store's line, not append one")
	require.True(t, lines[0].Complete)
	require.Empty(t, lines[0].Deferred, "the replacement must clear a deferral that no longer applies")
	require.NotNil(t, lines[0].CleanSince)
}

// TestRecordPurgeStoreRefusesAnUnattachedLine keeps a ledger line from existing without
// the record it explains — a line at purge 0 belongs to nothing and would be invisible to
// every completeness check while looking like data.
func TestRecordPurgeStoreRefusesAnUnattachedLine(t *testing.T) {
	s := newPurgeTestStore(t)
	ctx := context.Background()
	require.Error(t, s.RecordPurgeStore(ctx, &TenantPurgeStore{Store: "rdb"}))
	rec, err := s.EnsurePurgeRecord(ctx, "acme", time.Now().UTC())
	require.NoError(t, err)
	require.Error(t, s.RecordPurgeStore(ctx, &TenantPurgeStore{TenantPurgeID: rec.ID}))
}

// TestTenantsPurgingIsTheWorkList covers the coordinator's queue: only purging tenants,
// oldest cut first.
func TestTenantsPurgingIsTheWorkList(t *testing.T) {
	s := newPurgeTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "live"}))
	older := &Tenant{Token: "older"}
	require.NoError(t, s.CreateTenant(ctx, older))
	newer := &Tenant{Token: "newer"}
	require.NoError(t, s.CreateTenant(ctx, newer))

	base := time.Now().UTC().Truncate(time.Second)
	// Cut the NEWER one first, so insertion order and epoch order disagree — otherwise
	// the id tiebreak alone would satisfy the assertion and the ordering would be untested.
	require.NoError(t, s.BeginTenantPurge(ctx, newer, base))
	require.NoError(t, s.BeginTenantPurge(ctx, older, base.Add(-time.Hour)))

	purging, err := s.TenantsPurging(ctx)
	require.NoError(t, err)
	require.Len(t, purging, 2, "a live tenant must never appear in the purge work list")
	require.Equal(t, "older", purging[0].Token, "the queue drains in the order deletions were asked for")
	require.Equal(t, "newer", purging[1].Token)
}
