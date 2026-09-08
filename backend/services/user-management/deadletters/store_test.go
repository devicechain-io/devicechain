// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletters

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&DeadLetter{}))
	return &Store{rdbm: &rdb.RdbManager{Database: db}}
}

func envelope(kind deadletter.Kind, ref string, at time.Time) deadletter.Envelope {
	return deadletter.Envelope{
		Kind: kind, Reason: deadletter.ReasonExhausted, Source: "event-processing",
		Summary: "it did not happen", Detail: "connection refused", Attempts: 5,
		Subject: "inst.acme.derived-events", Sequence: 42, Correlation: "corr-1",
		Reference: ref, OccurredAt: at, Payload: []byte(`{}`),
	}
}

// 🔴 IDEMPOTENT BY THE STREAM'S OWN SEQUENCE. A consumer that stored a row and then failed
// to ack re-delivers, and without this the redelivery cap would decide how many copies of
// one failure an operator sees.
func TestARedeliveredLetterIsStoredOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := envelope(deadletter.KindDetectionAction, "rule-1", time.Now().UTC())

	require.NoError(t, s.Record(ctx, "acme", 7, appendAt(7), e))
	require.NoError(t, s.Record(ctx, "acme", 7, appendAt(7), e))

	page, err := s.List(ctx, SearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	assert.Len(t, page.Results, 1, "a redelivery stored a second copy of one failure")

	// The counterweight: a DIFFERENT sequence is a different failure and must be stored.
	require.NoError(t, s.Record(ctx, "acme", 8, appendAt(8), e))
	page, err = s.List(ctx, SearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	assert.Len(t, page.Results, 2, "two distinct failures collapsed into one")
}

// Every filter narrows, and the bystanders are what prove each one is doing something
// rather than the list being short for another reason.
func TestEveryFilterNarrows(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	require.NoError(t, s.Record(ctx, "acme", 1, appendAt(1), envelope(deadletter.KindDetectionAction, "r1", base)))
	require.NoError(t, s.Record(ctx, "globex", 2, appendAt(2), envelope(deadletter.KindNotification, "a1", base)))
	third := envelope(deadletter.KindNotification, "a2", base.Add(48*time.Hour))
	third.Source = "notification-management"
	require.NoError(t, s.Record(ctx, "acme", 3, appendAt(3), third))

	all := rdb.Pagination{PageNumber: 1, PageSize: 10}
	for name, tc := range map[string]struct {
		criteria SearchCriteria
		want     int
	}{
		"tenant":     {SearchCriteria{Pagination: all, Tenant: "acme"}, 2},
		"kind":       {SearchCriteria{Pagination: all, Kind: string(deadletter.KindNotification)}, 2},
		"source":     {SearchCriteria{Pagination: all, Source: "notification-management"}, 1},
		"since":      {SearchCriteria{Pagination: all, Since: ptr(base.Add(time.Hour))}, 1},
		"until":      {SearchCriteria{Pagination: all, Until: ptr(base.Add(time.Hour))}, 2},
		"unfiltered": {SearchCriteria{Pagination: all}, 3},
	} {
		t.Run(name, func(t *testing.T) {
			page, err := s.List(ctx, tc.criteria)
			require.NoError(t, err)
			assert.Len(t, page.Results, tc.want)
		})
	}
}

// 🔴 NEWEST FIRST, AND TOTAL. occurred_time alone is not unique — a burst of failures
// shares an instant — so a tie-break is what keeps rows from moving between pages.
func TestTheOrderIsNewestFirstAndStableAcrossATie(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for i := uint64(1); i <= 5; i++ {
		require.NoError(t, s.Record(ctx, "acme", i, appendAt(int(i)), envelope(deadletter.KindNotification, "a", at)))
	}
	newer := envelope(deadletter.KindNotification, "newest", at.Add(time.Hour))
	require.NoError(t, s.Record(ctx, "acme", 99, appendAt(99), newer))

	first, err := s.List(ctx, SearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 3}})
	require.NoError(t, err)
	require.Len(t, first.Results, 3)
	assert.Equal(t, "newest", first.Results[0].Reference, "the newest record is not first")

	second, err := s.List(ctx, SearchCriteria{Pagination: rdb.Pagination{PageNumber: 2, PageSize: 3}})
	require.NoError(t, err)
	require.Len(t, second.Results, 3)

	// No row appears on both pages, which is what an unstable order would produce.
	seen := map[uint]bool{}
	for _, r := range append(append([]DeadLetter{}, first.Results...), second.Results...) {
		assert.Falsef(t, seen[r.ID], "row %d appeared on both pages", r.ID)
		seen[r.ID] = true
	}
	assert.Len(t, seen, 6)
}

// The store is bounded by age, and only by age — a sweep that took everything, or nothing,
// would satisfy a test that only counted what was left.
func TestPruneRemovesOnlyWhatIsPastTheWindow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, s.Record(ctx, "acme", 1, appendAt(1), envelope(deadletter.KindNotification, "old", now.Add(-40*24*time.Hour))))
	require.NoError(t, s.Record(ctx, "acme", 2, appendAt(2), envelope(deadletter.KindNotification, "new", now.Add(-time.Hour))))

	n, err := s.Prune(ctx, now.Add(-30*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	page, err := s.List(ctx, SearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	assert.Equal(t, "new", page.Results[0].Reference, "the sweep took the wrong row")
}

// The boundary the retention window turns on. A row EXACTLY at the cutoff is inside the
// window — `<` not `<=` — and a mutation flipping that survived until this existed.
func TestPruneKeepsARowExactlyAtTheCutoff(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cut := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	require.NoError(t, s.Record(ctx, "acme", 1, appendAt(1),
		envelope(deadletter.KindNotification, "at-the-cutoff", cut)))
	require.NoError(t, s.Record(ctx, "acme", 2, appendAt(2),
		envelope(deadletter.KindNotification, "one-nanosecond-older", cut.Add(-time.Nanosecond))))

	n, err := s.Prune(ctx, cut)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "the sweep took the row sitting exactly on its cutoff")

	page, err := s.List(ctx, SearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	assert.Equal(t, "at-the-cutoff", page.Results[0].Reference)
}

func TestByIdReturnsNilRatherThanAnErrorWhenThereIsNone(t *testing.T) {
	s := testStore(t)
	found, err := s.ByID(context.Background(), 404)
	require.NoError(t, err, "a missing record must not read as a failure")
	assert.Nil(t, found)
}

// 🔑 THE TABLE IS INSTANCE-WIDE BY CONSTRUCTION. It is written by a consumer draining every
// tenant's letters off one stream and read by an operator asking across tenants — so it
// must NOT carry the tenant-scoped embed, whose callbacks would fail closed on a context
// that has no tenant and can never have one here.
func TestTheStoreWorksWithNoTenantInContext(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.Record(ctx, "acme", 1, appendAt(1), envelope(deadletter.KindNotification, "a", time.Now().UTC())))
	page, err := s.List(ctx, SearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	assert.Len(t, page.Results, 1)
	assert.False(t, core.IsSystemContext(ctx), "the caller passed no system marker; the store adds its own")
}

func ptr(t time.Time) *time.Time { return &t }

// appendAt gives each stream sequence a distinct broker write time, as a real stream does.
// The pair is the dedup key: the same sequence with a DIFFERENT append time is a different
// message, which is what a rebuilt broker produces.
func appendAt(seq int) time.Time {
	return time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC).Add(time.Duration(seq) * time.Second)
}
