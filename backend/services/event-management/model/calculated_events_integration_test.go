// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// calculatedEvents against a REAL TimescaleDB: stored inputs are decimal(20,8), and whether a
// formula over them agrees with the value computed at resolve is a property of that column and
// of the driver's float conversion, neither of which sqlite has. Run it as the other files of
// this package say (DC_IT_PGPORT=... go test -tags integration ./model/... -run IntegrationCalculated).
package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolveTimeValue is what the resolver computes for one reading: the formula over the
// device's own strings, through the same calcfield code (Sample.Set parses each raw string,
// Formula.Eval evaluates it).
func resolveTimeValue(t *testing.T, f *calcfield.Formula, env *calcfield.Env, reading map[string]string) calcfield.Result {
	t.Helper()
	s := env.Sample()
	defer s.Release()
	for k, v := range reading {
		_, _ = s.Set(k, v)
	}
	return f.Eval(s)
}

// TestIntegrationCalculatedEventsAgreesWithResolveOnStorableInputs is the determinism contract
// of the history preview, and the statement of where it ends.
//
//   - A reading whose inputs have at most eight decimal places (and fit the column) is stored
//     exactly, so the preview over the stored row equals the value computed from the device's
//     string at resolve — the same formula, the same parse, the same formatting.
//   - A reading with more decimal places is stored rounded, and the preview is the formula over
//     the ROUNDED inputs. That difference is expected and is why the console labels the panel
//     as previewed from stored 8-decimal inputs; the case below pins it rather than hiding it.
func TestIntegrationCalculatedEventsAgreesWithResolveOnStorableInputs(t *testing.T) {
	api := newPostgresApi(t, "dccalcevit")
	ctx := acme()
	req := powerRequest()
	formula, env, _, err := compileDraftFormula(req)
	require.NoError(t, err)

	storable := []map[string]string{
		{"voltage": "230.5", "current": "12.25"},
		{"voltage": "229.99999999", "current": "0.00000001"},
		{"voltage": "-0.5", "current": "1000"},
		{"voltage": "999999999999", "current": "0.001"},
		{"voltage": "12345.6789", "current": "3.14159265"},
		{"voltage": "0.1", "current": "0.2"},
		{"voltage": "1e3", "current": "7"},
		{"voltage": "231"}, // a missing input skips the same way in both
	}
	beyond := []struct {
		reading    map[string]string
		storedCurr string // the current as stored, rounded to eight places
	}{
		{map[string]string{"voltage": "230", "current": "0.000000015"}, "0.00000002"},
		{map[string]string{"voltage": "230", "current": "0.123456789"}, "0.12345679"},
	}

	at := calcAt
	write := func(i int, reading map[string]string) {
		id := sha256.Sum256([]byte(fmt.Sprint("calc-it-", i)))
		ev := Event{EventId: id[:], DeviceToken: "meter-1", EventType: eventtype.Measurement,
			OccurredTime: at.Add(time.Duration(i) * time.Second), ProcessedTime: at, Source: "mqtt"}
		var reqs []*MeasurementEventCreateRequest
		for k, raw := range reading {
			v, err := strconv.ParseFloat(raw, 64) // as the persistence worker parses it
			require.NoError(t, err)
			reqs = append(reqs, &MeasurementEventCreateRequest{Event: ev, EntryOccurredTime: ev.OccurredTime,
				Name: k, Value: &v})
		}
		_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), reqs)
		require.NoError(t, err)
	}
	for i, r := range storable {
		write(i, r)
	}
	for i, b := range beyond {
		write(len(storable)+i, b.reading)
	}

	res, err := api.CalculatedEvents(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Samples, len(storable)+len(beyond))

	for i, reading := range storable {
		want := resolveTimeValue(t, formula, env, reading)
		got := res.Samples[i]
		if want.OK() {
			if assert.NotNil(t, got.Value, "sample %d %v skipped as %v", i, reading, got.SkippedReason) {
				assert.Equal(t, want.Value, *got.Value, "sample %d %v: the preview differs from resolve", i, reading)
			}
		} else if assert.NotNil(t, got.SkippedReason, "sample %d %v", i, reading) {
			assert.Equal(t, string(want.Skip), *got.SkippedReason, "sample %d %v", i, reading)
		}
	}

	for j, b := range beyond {
		got := res.Samples[len(storable)+j]
		atResolve := resolveTimeValue(t, formula, env, b.reading)
		fromStored := resolveTimeValue(t, formula, env, map[string]string{"voltage": b.reading["voltage"], "current": b.storedCurr})
		require.True(t, atResolve.OK() && fromStored.OK())
		require.NotNil(t, got.Value)
		assert.Equal(t, fromStored.Value, *got.Value, "the preview must be the formula over the stored (rounded) inputs")
		assert.NotEqual(t, atResolve.Value, *got.Value,
			"%v: expected the documented difference between the stored-input preview and the resolve-time value", b.reading)
	}
}

// TestIntegrationCalculatedEventsSevenDayWindowAtScale: on a real hypertable holding a busy
// device, a sparse one and a tenant's worth of other devices, a seven-day window answers or
// refuses within a bounded time — the span cap is what bounds the database's work (the
// planner may sort every row of the device in range before the first one streams), and this
// measures it rather than asserting it in prose. DC_IT_CALC_NOISE_DEVICES scales the other
// devices (each 30 days at 10 s with two metrics, about 518k rows); 20 gives an 11M-row
// table.
func TestIntegrationCalculatedEventsSevenDayWindowAtScale(t *testing.T) {
	api := newPostgresApi(t, "dccalcevscale")
	ctx := acme()
	noise := 1
	if v := os.Getenv("DC_IT_CALC_NOISE_DEVICES"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err)
		noise = n
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	sys := api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))
	seed := func(device string, every time.Duration, samples int) {
		require.NoError(t, sys.Exec(`INSERT INTO "event-management".measurement_events
			(tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value)
			SELECT 'acme', decode(md5(?::text || g), 'hex'), decode(md5(?::text || g || n), 'hex'), ?, ?,
				?::timestamptz + g * (? * interval '1 second'), n, (g % 1000) / 10.0
			FROM generate_series(0, ? - 1) g, unnest(ARRAY['voltage', 'current']) n`,
			device, device, device, eventtype.Measurement, start, every.Seconds(), samples).Error)
	}
	began := time.Now()
	month := 30 * 24 * time.Hour
	seed("busy", 10*time.Second, int(month/(10*time.Second)))
	seed("sparse", time.Minute, int(month/time.Minute))
	for i := 0; i < noise; i++ {
		seed(fmt.Sprintf("other-%d", i), 10*time.Second, int(month/(10*time.Second)))
	}
	require.NoError(t, sys.Exec(`ANALYZE "event-management".measurement_events`).Error)
	var rows int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".measurement_events`).Scan(&rows).Error)
	t.Logf("seeded %d rows in %s", rows, time.Since(began).Round(time.Millisecond))

	const budget = 30 * time.Second
	window := func(device string) (*CalculatedEventsResult, time.Duration, error) {
		req := powerRequest()
		req.DeviceToken = device
		req.From = start.Add(20 * 24 * time.Hour)
		req.To = req.From.Add(MaxCalculatedSpan)
		t0 := time.Now()
		res, err := api.CalculatedEvents(ctx, req)
		return res, time.Since(t0), err
	}

	// The busy device holds about 60k samples in seven days: refused, not truncated.
	_, took, err := window("busy")
	var le *limit.Error
	require.True(t, errors.As(err, &le), "want LIMIT_EXCEEDED, got %v", err)
	assert.Equal(t, "samples", le.What)
	assert.Less(t, took, budget, "refusing the busy device's seven days took %s", took)
	t.Logf("busy: refused in %s", took)

	// The sparse device's seven days are about 10k samples: answered whole.
	res, took, err := window("sparse")
	require.NoError(t, err)
	assert.Equal(t, int(MaxCalculatedSpan/time.Minute)+1, len(res.Samples))
	assert.Less(t, took, budget, "the sparse device's seven days took %s", took)
	t.Logf("sparse: %d samples in %s", len(res.Samples), took)
}
