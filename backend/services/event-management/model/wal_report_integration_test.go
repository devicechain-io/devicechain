// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// BenchmarkWalPerEventReport is a MEASUREMENT, not an assertion: it writes DC_WAL_EVENTS measurement
// events (default 100000) the way the persistence worker's batches do (DC_WAL_BATCH rows per
// statement, 23 as in the r26 profile; ascending instants at one reading per device in
// turn) and reports the WAL the event store wrote per event, from pg_current_wal_lsn
// deltas. It is a Benchmark so the integration job lists it rather than running it; run it
// once, by hand:
//
//	DC_WAL_PLAIN=0|1 DC_WAL_TIME_INDEX=desc|asc|brin|none DC_IT_PGPORT=... \
//	  go test -tags integration -run '^$' -bench BenchmarkWalPerEventReport -benchtime 1x -v ./model/
//
// DC_WAL_PLAIN=1 writes plain INSERTs (rdb.WithPlainInsert), 0 keeps ON CONFLICT DO NOTHING.
// DC_WAL_TIME_INDEX swaps measurement_events' cross-tenant time index first (the chain builds
// a btree DESC), which is how the index alternatives were costed. A CHECKPOINT precedes the run so a full-page image is paid the way
// it is after any checkpoint, and the continuous aggregate is refreshed over the written
// range at the end so its WAL is counted.
func BenchmarkWalPerEventReport(b *testing.B) {
	t := testing.TB(b)
	events := atoiEnv(t, "DC_WAL_EVENTS", 100000)
	batch := atoiEnv(t, "DC_WAL_BATCH", 23)
	devices := atoiEnv(t, "DC_WAL_DEVICES", 6000)
	plain := os.Getenv("DC_WAL_PLAIN") == "1"
	instance := fmt.Sprintf("emwal%d", time.Now().UnixNano()%1000000000)
	mgr := newPostgresManager(t, instance)
	api := NewApi(mgr)

	tenant := "sim-r26-4mqtt"
	ctx := core.WithTenant(context.Background(), tenant)
	if plain {
		ctx = rdb.WithPlainInsert(ctx)
	}
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	// DC_WAL_TIME_INDEX swaps measurement_events' cross-tenant time index before the run:
	// desc, asc, brin or none. Unset keeps whatever the chain built.
	if v := os.Getenv("DC_WAL_TIME_INDEX"); v != "" {
		require.NoError(t, sys.Exec(`DROP INDEX IF EXISTS "event-management".measurement_events_occurred_time_idx`).Error)
		require.NoError(t, sys.Exec(`DROP INDEX IF EXISTS "event-management".measurement_events_occurred_time_brin`).Error)
		create := map[string]string{
			"asc":  `CREATE INDEX measurement_events_occurred_time_idx ON "event-management".measurement_events (occurred_time)`,
			"brin": `CREATE INDEX measurement_events_occurred_time_brin ON "event-management".measurement_events USING brin (occurred_time) WITH (pages_per_range = 32, autosummarize = on)`,
			"desc": `CREATE INDEX measurement_events_occurred_time_idx ON "event-management".measurement_events (occurred_time DESC)`,
			"none": `SELECT 1`,
		}[v]
		require.NotEmpty(t, create, "DC_WAL_TIME_INDEX: desc|asc|brin|none")
		require.NoError(t, sys.Exec(create).Error)
	}
	require.NoError(t, sys.Exec("CHECKPOINT").Error)
	lsn := func() string {
		var s string
		require.NoError(t, sys.Raw("SELECT pg_current_wal_lsn()::text").Scan(&s).Error)
		return s
	}
	diff := func(a, b string) float64 {
		var d float64
		require.NoError(t, sys.Raw("SELECT pg_wal_lsn_diff(?::pg_lsn, ?::pg_lsn)", b, a).Scan(&d).Error)
		return d
	}

	unit, dt := "kph", "float"
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	first := lsn()
	began := time.Now()
	for done := 0; done < events; done += batch {
		var rows EventRows
		for i := done; i < done+batch && i < events; i++ {
			at := start.Add(time.Duration(i) * 160 * time.Microsecond)
			parent := Event{
				DeviceToken:   fmt.Sprintf("devicepulse-%05d", i%devices),
				EventType:     eventtype.Measurement,
				OccurredTime:  at,
				Source:        "mqtt",
				ProcessedTime: at,
			}
			payload := &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
				OccurredTime: at,
				Entries: []resolvedevent.ResolvedMeasurementEntry{
					{Name: "speed_kph", Value: strconv.Itoa(i % 130), Unit: &unit, DataType: &dt},
				},
			}}}
			id, err := DeriveEventIdForPayload(tenant, &parent, payload)
			require.NoError(t, err)
			parent.EventId = id
			req, err := MeasurementRequestFor(parent, at, MeasurementReading(payload.Entries[0].Entries[0]))
			require.NoError(t, err)
			parents, ms, err := BuildMeasurementRows([]*MeasurementEventCreateRequest{req})
			require.NoError(t, err)
			rows.Parents = append(rows.Parents, parents...)
			rows.Measurements = append(rows.Measurements, ms...)
		}
		require.NoError(t, mgr.DB(ctx).Transaction(func(tx *gorm.DB) error {
			return api.CreateEventRows(ctx, tx, &rows)
		}))
	}
	inserted := lsn()
	elapsed := time.Since(began)
	require.NoError(t, sys.Exec(fmt.Sprintf(
		`CALL refresh_continuous_aggregate('event-management.measurement_rollups', '%s', '%s')`,
		start.Add(-time.Hour).Format(time.RFC3339), start.Add(time.Hour*3).Format(time.RFC3339))).Error)
	refreshed := lsn()

	t.Logf("WAL_REPORT index=%s plain=%v events=%d batch=%d: inserts %.1f B/event, +cagg refresh %.1f B/event "+
		"(total %.1f MB, %.0f events/s)",
		os.Getenv("DC_WAL_TIME_INDEX"), plain, events, batch, diff(first, inserted)/float64(events),
		diff(first, refreshed)/float64(events), diff(first, refreshed)/1e6, float64(events)/elapsed.Seconds())
}

func atoiEnv(t testing.TB, key string, fallback int) int {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	require.NoError(t, err, key)
	return n
}
