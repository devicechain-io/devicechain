// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// An event too large for one INSERT, against a REAL TimescaleDB.
//
// PostgreSQL's wire protocol binds at most 65535 parameters in a statement, and pgx refuses
// a larger one before sending it. A measurement row binds 11, so an event with more than
// 5,957 readings cannot be written in one statement; the event store splits it. sqlite has
// a different limit and a different driver, so none of this can be asked of it.
//
// The events here carry NO alternate id — the shape sparkplug-ingest and lwm2m-ingest
// produce — so a redelivery is not skipped by the alternate-id probe and reaches every
// split insert again, where only each statement's ON CONFLICT arbiter stops it writing
// twice.
package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// largeEvent is a resolved event from device "big" with no alternate id; fill gives it its
// payload.
func largeEvent(at time.Time, fill func(ev *dmodel.ResolvedEvent)) dmodel.ResolvedEvent {
	ev := fenceCostMeasurement("unused", at, 0)
	ev.AltId = nil
	ev.SourceDeviceToken = "big"
	fill(&ev)
	return ev
}

func measurementsOf(n int, at time.Time) func(*dmodel.ResolvedEvent) {
	return func(ev *dmodel.ResolvedEvent) {
		entries := make([]dmodel.ResolvedMeasurementEntry, n)
		for i := range entries {
			entries[i] = dmodel.ResolvedMeasurementEntry{Name: fmt.Sprintf("m%05d", i), Value: "1.5"}
		}
		ev.Payload = &dmodel.ResolvedMeasurementsPayload{Entries: []dmodel.ResolvedMeasurementsEntry{
			{OccurredTime: at, Entries: entries}}}
	}
}

// storedRows reports, under a system context so the answer is about the table and not
// about what a tenant-scoped read may see, how many of table's rows belong to the event
// ev is persisted as, how many of those carry a tenant other than tenant, and how many are
// duplicates of another by the table's own identity.
func storedRows(t *testing.T, r *pgBatchRig, tenant string, ev dmodel.ResolvedEvent, table string) (rows, foreign, dups int64) {
	t.Helper()
	pevent, err := r.ep.baseEvent(core.WithTenant(context.Background(), tenant), ev)
	require.NoError(t, err)
	identity := "payload_id"
	if table == "event_anchors" {
		identity = "(anchor_type, anchor_token)"
	}
	q := fmt.Sprintf(`SELECT count(*), count(*) FILTER (WHERE tenant_id <> ?), count(*) - count(DISTINCT %s)
		FROM "event-management".%q WHERE event_id = ?`, identity, table)
	require.NoError(t, r.mgr.DB(core.WithSystemContext(context.Background())).
		Raw(q, tenant, pevent.EventId).Row().Scan(&rows, &foreign, &dups))
	return rows, foreign, dups
}

// deliverWithTwoSmallOnes runs the large event through the batching worker between two
// ordinary messages of the same tenant, on the last delivery the stream allows, so a
// failure is reported rather than left for redelivery.
func deliverWithTwoSmallOnes(t *testing.T, r *pgBatchRig, tenant string, big dmodel.ResolvedEvent, base time.Time) {
	t.Helper()
	r.run([]messaging.Message{
		consumed(t, r.acks, 0, tenant, messaging.MaxDeliver, big),
		consumed(t, r.acks, 1, tenant, 1, batchEvent(1, true, base)),
		consumed(t, r.acks, 2, tenant, 1, batchEvent(2, true, base)),
	})
}

// A measurement event with 6,000 readings is stored whole, in the batch's one transaction,
// and a redelivery of it adds nothing.
func TestALargeMeasurementEventIsStoredWhole(t *testing.T) {
	r := newPgBatchRig(t, "emlargetest", 8)
	tenant := fmt.Sprintf("lg%d", time.Now().UnixNano())
	base := time.Now().UTC().Truncate(time.Hour)
	const readings = 6000
	require.Greater(t, readings*11, rdb.MaxBindParameters, "precondition: one statement cannot carry them")
	big := largeEvent(base, measurementsOf(readings, base))

	for delivery := 1; delivery <= 2; delivery++ {
		deliverWithTwoSmallOnes(t, r, tenant, big, base)

		rows, foreign, dups := storedRows(t, r, tenant, big, "measurement_events")
		assert.EqualValuesf(t, readings, rows, "measurement rows after delivery %d", delivery)
		assert.Zerof(t, foreign, "rows stamped with another tenant after delivery %d", delivery)
		assert.Zerof(t, dups, "duplicate rows after delivery %d", delivery)
		assert.EqualValuesf(t, 3, r.count(t, tenant, ""), "events after delivery %d", delivery)
		assert.Emptyf(t, r.failures, "failures after delivery %d", delivery)
		assert.Lenf(t, r.acks.snapshot(), 3*delivery, "acks after delivery %d", delivery)
		// One transaction per batch: the split insert nests inside it rather than costing
		// the message a transaction, or a set-aside, of its own.
		assert.EqualValuesf(t, delivery, r.api.txs.Load(), "transactions after delivery %d", delivery)
	}

	// The negative control: the same rows in ONE statement are refused, by the driver and
	// not the server, and the worker gives up on that refusal at once rather than retrying
	// a statement that is the same on every delivery. This is what makes 6,000 a real
	// test of the split and not a number that would have fitted anyway.
	t.Run("a single statement is refused", func(t *testing.T) {
		ctx := core.WithTenant(context.Background(), tenant+"x")
		at := base
		rows := make([]*model.MeasurementEvent, readings)
		for i := range rows {
			rows[i] = &model.MeasurementEvent{EventId: []byte("single"), PayloadId: []byte(fmt.Sprintf("p%05d", i)),
				DeviceToken: "big", EventType: esmodel.Measurement, OccurredTime: at, Name: "m"}
		}
		err := r.mgr.DB(ctx).Transaction(func(tx *gorm.DB) error {
			return tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "payload_id"}, {Name: "occurred_time"}},
				DoNothing: true,
			}).Create(&rows).Error
		})
		require.Error(t, err)
		var pgerr *pgconn.PgError
		assert.False(t, errors.As(err, &pgerr), "the refusal came from the server: %v", err)
		assert.ErrorIs(t, classifyPersistFailure(err), ErrDeterministic,
			"a statement refused for its size must not be retried")
	})
}

// The other tables a large event writes, each with its own arbiter, and so each with its
// own redelivery.
func TestALargeEventOfEachOtherKindIsStoredWhole(t *testing.T) {
	coord := func(v float64) *string { s := fmt.Sprintf("%.4f", v); return &s }
	for _, tc := range []struct {
		name  string
		table string
		// rows is how many the event writes to table, and width the parameters one binds.
		rows, width int
		fill        func(at time.Time) func(*dmodel.ResolvedEvent)
	}{
		{name: "locations", table: "location_events", rows: 6000, width: 12,
			fill: func(at time.Time) func(*dmodel.ResolvedEvent) {
				return func(ev *dmodel.ResolvedEvent) {
					ev.EventType = esmodel.Location
					entries := make([]dmodel.ResolvedLocationEntry, 6000)
					for i := range entries {
						entries[i] = dmodel.ResolvedLocationEntry{Latitude: coord(float64(i%80) + 0.25),
							Longitude: coord(float64(i%170) + 0.5), OccurredTime: at.Add(time.Duration(i) * time.Millisecond)}
					}
					ev.Payload = &dmodel.ResolvedLocationsPayload{Entries: entries}
				}
			}},
		{name: "alerts", table: "alert_events", rows: 7000, width: 10,
			fill: func(at time.Time) func(*dmodel.ResolvedEvent) {
				return func(ev *dmodel.ResolvedEvent) {
					ev.EventType = esmodel.Alert
					entries := make([]dmodel.ResolvedAlertEntry, 7000)
					for i := range entries {
						entries[i] = dmodel.ResolvedAlertEntry{Type: fmt.Sprintf("a%05d", i), Level: 1,
							Message: "m", Source: "s", OccurredTime: at}
					}
					ev.Payload = &dmodel.ResolvedAlertsPayload{Entries: entries}
				}
			}},
		// Anchors are written by persistEventAnchors, after the payload: a three-reading
		// event anchored to 10,000 targets.
		{name: "anchors", table: "event_anchors", rows: 10000, width: 7,
			fill: func(at time.Time) func(*dmodel.ResolvedEvent) {
				return func(ev *dmodel.ResolvedEvent) {
					measurementsOf(3, at)(ev)
					for i := 0; i < 10000; i++ {
						ev.Anchors = append(ev.Anchors, dmodel.ResolvedAnchor{AnchorType: "area",
							AnchorToken: fmt.Sprintf("a%05d", i)})
					}
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Greater(t, tc.rows*tc.width, rdb.MaxBindParameters, "precondition: one statement cannot carry them")
			r := newPgBatchRig(t, "emlargetest", 8)
			tenant := fmt.Sprintf("lk%d", time.Now().UnixNano())
			base := time.Now().UTC().Truncate(time.Hour)
			big := largeEvent(base, tc.fill(base))
			for delivery := 1; delivery <= 2; delivery++ {
				deliverWithTwoSmallOnes(t, r, tenant, big, base)
				rows, foreign, dups := storedRows(t, r, tenant, big, tc.table)
				assert.EqualValuesf(t, tc.rows, rows, "%s rows after delivery %d", tc.table, delivery)
				assert.Zerof(t, foreign, "rows stamped with another tenant after delivery %d", delivery)
				assert.Zerof(t, dups, "duplicate rows after delivery %d", delivery)
				assert.EqualValuesf(t, 3, r.count(t, tenant, ""), "events after delivery %d", delivery)
				assert.Emptyf(t, r.failures, "failures after delivery %d", delivery)
				assert.Lenf(t, r.acks.snapshot(), 3*delivery, "acks after delivery %d", delivery)
			}
		})
	}
}

// The split is at the size derived from the row, and no smaller: 6,000 readings take
// exactly two INSERTs into measurement_events, and three readings take the one they
// always did.
func TestAnEventTooLargeForOneStatementUsesTwo(t *testing.T) {
	r := newPgBatchRig(t, "emlargetest", 8)
	counter := rdbtest.NewStatementCounter(`measurement_events"`)
	counter.Record(true)
	r.mgr.Database = r.mgr.Database.Session(&gorm.Session{Logger: counter})
	tenant := fmt.Sprintf("lc%d", time.Now().UnixNano())
	base := time.Now().UTC().Truncate(time.Hour)

	inserts := func() int {
		n := 0
		for _, sql := range counter.MarkedSQL() {
			if strings.HasPrefix(strings.TrimSpace(sql), "INSERT INTO") {
				n++
			}
		}
		return n
	}
	for _, tc := range []struct {
		readings, want int
	}{
		// The positive control: the marker matches the rendered SQL at all, and the
		// common path is one statement, as it was before any of this.
		{3, 1},
		{6000, 2},
	} {
		counter.Reset()
		ev := largeEvent(base.Add(time.Duration(tc.readings)*time.Second), measurementsOf(tc.readings, base))
		r.run([]messaging.Message{consumed(t, r.acks, tc.readings, tenant, 1, ev)})
		assert.Equalf(t, tc.want, inserts(), "INSERTs into measurement_events for %d readings", tc.readings)
		rows, _, _ := storedRows(t, r, tenant, ev, "measurement_events")
		assert.EqualValuesf(t, tc.readings, rows, "rows stored for %d readings", tc.readings)
	}
	assert.Empty(t, r.failures)
}

// A statement refused part-way through a split insert takes the whole message with it: the
// rows of the statements before it are rolled back to the savepoint the split ran under,
// and the message's parent row with the batch, so nothing of it is stored. Its batch-mates
// are, and the value the server refused is reported as invalid on the first delivery.
func TestALargeEventRefusedInItsLastStatementIsStoredNowhere(t *testing.T) {
	r := newPgBatchRig(t, "emlargetest", 8)
	tenant := fmt.Sprintf("lr%d", time.Now().UnixNano())
	base := time.Now().UTC().Truncate(time.Hour)
	big := largeEvent(base, measurementsOf(6000, base))
	entries := big.Payload.(*dmodel.ResolvedMeasurementsPayload).Entries[0].Entries
	// numeric(20,8) holds 12 integer digits; this parses, and overflows at the INSERT — in
	// the second statement, since the first carries fewer than 6,000 rows.
	entries[len(entries)-1].Value = "1e13"

	r.run([]messaging.Message{
		consumed(t, r.acks, 0, tenant, 1, big),
		consumed(t, r.acks, 1, tenant, 1, batchEvent(1, true, base)),
		consumed(t, r.acks, 2, tenant, 1, batchEvent(2, true, base)),
	})

	rows, _, _ := storedRows(t, r, tenant, big, "measurement_events")
	assert.Zero(t, rows, "measurement rows of the refused message were committed")
	assert.Zero(t, r.count(t, tenant, "big"), "the refused message's parent event was committed")
	assert.EqualValues(t, 2, r.count(t, tenant, ""), "its batch-mates must be stored")
	if assert.Len(t, r.failures, 1) {
		assert.Equal(t, "big", r.failures[0].device)
		assert.Equal(t, uint(dmproto.FailureReason_Invalid), r.failures[0].reason)
	}
	assert.Len(t, r.acks.snapshot(), 3)
}

// The erasure fence refuses a purged tenant's split insert when its FIRST statement is
// already one of the split ones, and the refusal rolls back to the split's savepoint, not
// the transaction: the transaction goes on working. Lifting the fence is the negative
// control, and stores every row.
//
// What this does not show is a fresh fence read per statement: the fence's answer is
// remembered for the transaction, so later statements are admitted on it. The per-statement
// property that matters — the tenant stamped on every row of every statement — is asserted
// by the tests above, which check every stored row's tenant.
func TestCreateChunkedRefusesAPurgedTenant(t *testing.T) {
	for _, fenced := range []bool{true, false} {
		t.Run(fmt.Sprintf("fenced=%v", fenced), func(t *testing.T) {
			r := newPgBatchRig(t, "emlargetest", 8)
			gone := fmt.Sprintf("lf%d", time.Now().UnixNano())
			plantOn(t, r.mgr, gone)
			if !fenced {
				liftOn(t, r.mgr, gone)
			}
			ctx := core.WithTenant(context.Background(), gone)
			at := time.Now().UTC().Truncate(time.Hour)
			rows := make([]*model.MeasurementEvent, 6000)
			for i := range rows {
				rows[i] = &model.MeasurementEvent{EventId: []byte("fenced"), PayloadId: []byte(fmt.Sprintf("p%05d", i)),
					DeviceToken: "big", EventType: esmodel.Measurement, OccurredTime: at, Name: "m"}
			}
			arbiter := clause.OnConflict{
				Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "payload_id"}, {Name: "occurred_time"}},
				DoNothing: true,
			}
			per, err := rdb.RowsPerInsert(r.mgr.DB(ctx), &rows)
			require.NoError(t, err)
			require.Greater(t, len(rows), per, "precondition: the insert must split")

			require.NoError(t, r.mgr.DB(ctx).Transaction(func(tx *gorm.DB) error {
				err := rdb.CreateChunked(tx.Clauses(arbiter), &rows).Error
				if !fenced {
					return err
				}
				require.ErrorIs(t, err, rdb.ErrTenantPurged)
				var one int
				require.NoError(t, tx.Raw("SELECT 1").Scan(&one).Error,
					"the refusal aborted the transaction instead of rolling back to the savepoint")
				assert.Equal(t, 1, one)
				return nil
			}))

			want := int64(0)
			if !fenced {
				want = int64(len(rows))
			}
			var n int64
			require.NoError(t, r.mgr.DB(core.WithSystemContext(context.Background())).
				Model(&model.MeasurementEvent{}).Where("tenant_id = ?", gone).Count(&n).Error)
			assert.Equal(t, want, n)
		})
	}
}
