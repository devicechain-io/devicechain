// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newFailedEventsTestApi is the index on SQLite, with the tenant scope and the erasure fence
// registered in the order production registers them. The unique index restates the
// migration's (production gets it from the Timescale chain).
func newFailedEventsTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTenantFence(db))
	require.NoError(t, db.AutoMigrate(&FailedEventRecord{}, &rdb.PurgedTenant{}))
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_failed_event_records_seq `+
		`ON failed_event_records (tenant_id, record_seq, occurred_time)`).Error)
	return NewApi(&rdb.RdbManager{Database: db})
}

func fe(tenantCtx context.Context, api *Api, seq int64, at time.Time, reason int32, device string) *FailedEventRecord {
	rec := &FailedEventRecord{OccurredTime: at, RecordSeq: seq, SourceArea: "device-management",
		Reason: reason, DeviceToken: device, Error: "boom"}
	if _, err := api.InsertFailedEventRecord(tenantCtx, rec); err != nil {
		panic(err)
	}
	return rec
}

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// A redelivery of an indexed record inserts nothing.
func TestInsertIsIdempotentOnTheRecordsIdentity(t *testing.T) {
	api := newFailedEventsTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	rec := &FailedEventRecord{OccurredTime: t0, RecordSeq: 7, SourceArea: "device-management", Reason: 3}
	ok, err := api.InsertFailedEventRecord(ctx, rec)
	require.NoError(t, err)
	assert.True(t, ok)
	again := &FailedEventRecord{OccurredTime: t0, RecordSeq: 7, SourceArea: "device-management", Reason: 3}
	ok, err = api.InsertFailedEventRecord(ctx, again)
	require.NoError(t, err)
	assert.False(t, ok, "the same stream position and broker time is the same record")
	res, err := api.FailedEvents(ctx, FailedEventSearchCriteria{})
	require.NoError(t, err)
	assert.Len(t, res.Results, 1)
}

// A tenant sees its own rows only. The negative control is the second tenant: with the scope
// removed it would see acme's.
func TestFailedEventsAreTenantScoped(t *testing.T) {
	api := newFailedEventsTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "other")
	fe(acme, api, 1, t0, 3, "ghost")
	fe(other, api, 2, t0, 3, "ghost")

	res, err := api.FailedEvents(acme, FailedEventSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	assert.Equal(t, "acme", res.Results[0].TenantId)

	sum, err := api.FailedEventSummary(other, t0.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, sum, 1)
	assert.EqualValues(t, 1, sum[0].Records, "the summary counts the caller's tenant only")

	// A query with no tenant in context fails closed rather than reading every tenant.
	_, err = api.FailedEvents(context.Background(), FailedEventSearchCriteria{})
	assert.Error(t, err)
}

// A deleted tenant's rows are refused by the fence at insert, so a late record cannot put one
// back after the purge erased them.
func TestAFencedTenantCannotBeIndexed(t *testing.T) {
	api := newFailedEventsTestApi(t)
	sys := core.WithSystemContext(context.Background())
	require.NoError(t, api.RDB.DB(sys).Create(&rdb.PurgedTenant{Token: "gone", Epoch: t0, PlantedAt: t0}).Error)

	ctx := core.WithTenant(context.Background(), "gone")
	_, err := api.InsertFailedEventRecord(ctx, &FailedEventRecord{OccurredTime: t0, RecordSeq: 1, SourceArea: "x", Reason: 3})
	require.ErrorIs(t, err, rdb.ErrTenantPurged)

	// The negative control: a tenant with no fence is written.
	live := core.WithTenant(context.Background(), "live")
	ok, err := api.InsertFailedEventRecord(live, &FailedEventRecord{OccurredTime: t0, RecordSeq: 1, SourceArea: "x", Reason: 3})
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestFilters(t *testing.T) {
	api := newFailedEventsTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	fe(ctx, api, 1, t0, int32(resolvedevent.FailureReason_DeviceNotFound), "a")
	fe(ctx, api, 2, t0.Add(time.Minute), int32(resolvedevent.FailureReason_Unauthenticated), "b")
	fe(ctx, api, 3, t0.Add(2*time.Minute), 99, "c") // a reason a newer writer added
	fe(ctx, api, 4, t0.Add(3*time.Minute), int32(resolvedevent.FailureReason_Unknown), "d")

	reason := int32(resolvedevent.FailureReason_Unauthenticated)
	res, err := api.FailedEvents(ctx, FailedEventSearchCriteria{Reason: &reason})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	assert.Equal(t, "b", res.Results[0].DeviceToken)

	dev := "a"
	res, err = api.FailedEvents(ctx, FailedEventSearchCriteria{DeviceToken: &dev})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	// UNKNOWN selects the number this build does not know AND "no reason", and nothing known.
	res, err = api.FailedEvents(ctx, FailedEventSearchCriteria{UnknownReasons: true, KnownReasons: KnownReasonCodes()})
	require.NoError(t, err)
	var seqs []int64
	for _, r := range res.Results {
		seqs = append(seqs, r.RecordSeq)
	}
	assert.ElementsMatch(t, []int64{3, 4}, seqs)

	// Newest first, and the time bounds are inclusive.
	since, until := t0.Add(time.Minute), t0.Add(2*time.Minute)
	res, err = api.FailedEvents(ctx, FailedEventSearchCriteria{Since: &since, Until: &until})
	require.NoError(t, err)
	require.Len(t, res.Results, 2)
	assert.EqualValues(t, 3, res.Results[0].RecordSeq)
	assert.EqualValues(t, 2, res.Results[1].RecordSeq)
}

func TestSummaryCountsSuppressedAndRanksDevices(t *testing.T) {
	api := newFailedEventsTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	nf := int32(resolvedevent.FailureReason_DeviceNotFound)
	a := fe(ctx, api, 1, t0, nf, "a")
	fe(ctx, api, 2, t0, nf, "b")
	fe(ctx, api, 3, t0, nf, OverflowDeviceToken)
	require.NoError(t, api.BumpFailedEventSuppressed(ctx, FailedEventRef{RecordSeq: a.RecordSeq, OccurredTime: a.OccurredTime}, 41))

	sum, err := api.FailedEventSummary(ctx, t0.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, sum, 1)
	assert.EqualValues(t, 3+41, sum[0].Records)
	assert.EqualValues(t, 41, sum[0].Suppressed)
	require.Len(t, sum[0].Devices, 2, "the overflow row names no device")
	assert.Equal(t, "a", sum[0].Devices[0].DeviceToken)
	assert.EqualValues(t, 42, sum[0].Devices[0].Records)

	n, err := api.DeviceFailedEventCount(ctx, "a", t0.Add(-time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 42, n)
	n, err = api.DeviceFailedEventCount(ctx, "a", t0.Add(time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 0, n, "nothing since a later instant")
}

func TestSanitizeFailedEventText(t *testing.T) {
	assert.Equal(t, "ab", SanitizeFailedEventText("a\x00b", 10), "a NUL would be refused by the column")
	assert.True(t, utf8.ValidString(SanitizeFailedEventText("a\xffb", 10)))
	cut := SanitizeFailedEventText(strings.Repeat("é", 400), MaxFailedEventError)
	assert.LessOrEqual(t, len(cut), MaxFailedEventError)
	assert.True(t, utf8.ValidString(cut), "cut on a rune boundary")
	assert.Equal(t, "short", SanitizeFailedEventText("short", 10))
}

// A sender cannot pose as the overflow row, and an over-long claim is bounded.
func TestClaimedDeviceToken(t *testing.T) {
	assert.NotEqual(t, OverflowDeviceToken, ClaimedDeviceToken(OverflowDeviceToken))
	assert.LessOrEqual(t, len(ClaimedDeviceToken(strings.Repeat("d", 1000))), 128)
	assert.Equal(t, "dev-1", ClaimedDeviceToken("dev-1"))
}

// An unknown reason number is named UNKNOWN-ward: ReasonName reports it as unknown, never as
// a neighbour, and the vocabulary comes from the protobuf enum itself.
func TestReasonNameFollowsTheProtobufVocabulary(t *testing.T) {
	name, ok := ReasonName(int32(resolvedevent.FailureReason_NotGatewayChild))
	assert.True(t, ok)
	assert.Equal(t, "not_gateway_child", name)
	_, ok = ReasonName(9999)
	assert.False(t, ok)
	for code := range resolvedevent.FailureReason_name {
		_, ok := ReasonName(code)
		assert.True(t, ok, "every vocabulary member has a name")
	}
	assert.NotContains(t, KnownReasonCodes(), int32(0), "Unknown is not a known reason")
}
