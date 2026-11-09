// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/rdb"

	"github.com/devicechain-io/dc-outbound-connectors/schema"
)

// The store over a real (SQLite) database whose table is created by the MIGRATION, not by
// AutoMigrate of the live model: the migration's own snapshot must be a table the live model works on.
func newDBPending(t *testing.T) *DBPending {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, schema.NewLpwanPendingAcksSchema().Migrate(db))
	return NewDBPending(&rdb.RdbManager{Database: db})
}

func TestTheStoreRoundTripsAndIsolatesTenants(t *testing.T) {
	s := newDBPending(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	p := Pending{Tenant: "acme", Integration: "cs1", DeviceToken: "dev-1", DevEUI: "0102030405060708",
		CommandToken: "cmd-1", DispatchNonce: "n1", QueueItemID: itemID, DeadlineAt: now.Add(time.Hour)}
	require.NoError(t, s.Add(ctx, p))

	got, err := s.Lookup(ctx, "acme", itemID)
	require.NoError(t, err)
	require.Equal(t, "n1", got.DispatchNonce)
	require.Equal(t, "cmd-1", got.CommandToken)
	require.Nil(t, got.TransmittedAt)

	_, err = s.Lookup(ctx, "other", itemID)
	require.ErrorIs(t, err, ErrNoPending, "the same queue item id in another tenant is not this downlink")
	_, err = s.Lookup(ctx, "acme", "6e0a6c1e-6a1c-4f38-9d2b-0c7a2b7d9f22")
	require.ErrorIs(t, err, ErrNoPending)

	// (tenant, queue item id) is unique.
	require.Error(t, s.Add(ctx, p))
	// ...but another tenant may hold the same id.
	p2 := p
	p2.Tenant = "other"
	require.NoError(t, s.Add(ctx, p2))

	first := now.Add(time.Minute)
	require.NoError(t, s.MarkTransmitted(ctx, "acme", got.ID, first))
	require.NoError(t, s.MarkTransmitted(ctx, "acme", got.ID, first.Add(time.Hour)))
	got, err = s.Lookup(ctx, "acme", itemID)
	require.NoError(t, err)
	require.NotNil(t, got.TransmittedAt)
	require.True(t, got.TransmittedAt.Equal(first), "the first transmission report wins")

	// A delete needs the lease: without it, or in the wrong tenant, nothing is removed.
	tok, won, err := s.Claim(ctx, "acme", got.ID, now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.Delete(ctx, "other", got.ID, tok))
	require.NoError(t, s.Delete(ctx, "acme", got.ID, tok.Add(time.Second)))
	_, err = s.Lookup(ctx, "acme", itemID)
	require.NoError(t, err)
	require.NoError(t, s.Delete(ctx, "acme", got.ID, tok))
	_, err = s.Lookup(ctx, "acme", itemID)
	require.ErrorIs(t, err, ErrNoPending)
	require.NoError(t, s.Delete(ctx, "acme", got.ID, tok), "deleting what is gone is not an error")
}

func TestExpiredIsCrossTenantAndBoundedAndOrdered(t *testing.T) {
	s := newDBPending(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	add := func(tenant, item string, deadline time.Time) {
		require.NoError(t, s.Add(ctx, Pending{Tenant: tenant, Integration: "cs1", DeviceToken: "d", DevEUI: "0102030405060708",
			CommandToken: "c-" + item, DispatchNonce: "n", QueueItemID: item, DeadlineAt: deadline}))
	}
	add("acme", "i-late", now.Add(time.Hour))
	add("acme", "i-2", now.Add(-time.Minute))
	add("other", "i-1", now.Add(-time.Hour))
	add("other", "i-3", now)

	due, err := s.Expired(ctx, now, 10)
	require.NoError(t, err)
	var items []string
	for _, d := range due {
		items = append(items, d.QueueItemID)
	}
	require.Equal(t, []string{"i-1", "i-2", "i-3"}, items, "overdue across tenants, oldest first, the future one excluded")

	due, err = s.Expired(ctx, now, 2)
	require.NoError(t, err)
	require.Len(t, due, 2)
}

func TestClaimIsAtomicAndExpiresAndHidesTheRecordFromTheSweep(t *testing.T) {
	s := newDBPending(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	require.NoError(t, s.Add(ctx, Pending{Tenant: "acme", Integration: "cs1", DeviceToken: "d", DevEUI: "0102030405060708",
		CommandToken: "c", DispatchNonce: "n", QueueItemID: itemID, DeadlineAt: now.Add(-time.Minute)}))
	p, err := s.Lookup(ctx, "acme", itemID)
	require.NoError(t, err)

	tok1, won, err := s.Claim(ctx, "acme", p.ID, now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, won, err = s.Claim(ctx, "acme", p.ID, now.Add(30*time.Second), time.Minute)
	require.NoError(t, err)
	require.False(t, won, "a second settler cannot take a held record")
	due, err := s.Expired(ctx, now.Add(30*time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, due, "a held record is not offered to the sweep")

	_, won, err = s.Claim(ctx, "other", p.ID, now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.False(t, won, "another tenant cannot claim it")
	tok2, won, err := s.Claim(ctx, "acme", p.ID, now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.True(t, won, "an expired lease can be taken again")

	// The first settler, overtaken after its lease expired, deletes nothing; the holder does.
	require.NoError(t, s.Delete(ctx, "acme", p.ID, tok1))
	_, err = s.Lookup(ctx, "acme", itemID)
	require.NoError(t, err, "a settler that no longer holds the lease must not delete the record")
	require.NoError(t, s.Delete(ctx, "acme", p.ID, tok2))
	_, err = s.Lookup(ctx, "acme", itemID)
	require.ErrorIs(t, err, ErrNoPending)
}
