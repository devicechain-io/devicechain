// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// calculatedEvents against a REAL TimescaleDB: stored inputs are decimal(20,8), and whether a
// formula over them agrees with the value computed at resolve is a property of that column and
// of the driver's float conversion, neither of which sqlite has. Run it as the other files of
// this package say (DC_IT_PGPORT=... go test -tags integration ./model/... -run IntegrationCalculated).
package model

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/calcfield"
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
		ev := Event{EventId: id[:], DeviceToken: "meter-1", EventType: esmodel.Measurement,
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
