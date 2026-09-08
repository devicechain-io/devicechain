// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package observe

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 THE CASE WITH NO SIGNAL AT ALL WAS THE COMMON ONE. A Notify carrying only boolean
// readings takes the `len(samples) == 0` early return: the payload is well-formed, the
// decode returns no error, the ingester is never called, and before this nothing was
// incremented anywhere on the way. So "this device reports only booleans", "this device's
// firmware sends garbage" and "this device stopped sending" produced byte-identical
// metrics — three different operator actions behind one observation.
//
// The decode's own accounting is tested in decode/senml_skips_test.go; what THIS pins is
// that the manager actually reports it, and reports it BEFORE the early return.
func TestSkippedRecordsAreReportedByTheNotifyPath(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		nonNumeric float64
		nonFinite  float64
		unnamed    float64
		ingested   int
	}{
		{
			name:       "a boolean-only notify still says something happened",
			body:       `[{"bn":"/3311/0/","n":"5850","vb":true},{"n":"5750","vs":"kitchen"}]`,
			nonNumeric: 2,
		},
		{
			name:      "a value that resolved non-finite",
			body:      `[{"bn":"/3303/0/","bv":1e308,"n":"5700","v":1e308,"bt":1700000500}]`,
			nonFinite: 1,
		},
		{
			name:    "an empty resolved name",
			body:    `[{"bn":"","n":"","v":1,"bt":1700000500}]`,
			unnamed: 1,
		},
		{
			name: "a mixed notify reports its skips AND ingests the good samples",
			body: `[{"bn":"/3303/0/","n":"5700","v":21.5,"bt":1700000500},` +
				`{"n":"5850","vb":true},{"bn":"","n":"","v":3}]`,
			nonNumeric: 1,
			unnamed:    1,
			ingested:   1,
		},
		{
			// The counterweight: a clean Notify must count nothing, or these counters are
			// noise an operator learns to scroll past.
			name:     "a clean notify counts no skips",
			body:     `[{"bn":"/3303/0/","n":"5700","v":21.5,"bt":1700000500}]`,
			ingested: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ing, metrics := newHarness(t)
			c := newFakeConn(1)
			require.True(t, m.Establish("id-1", 1, c, testTarget, []string{"/3303/0"}))

			c.deliver("/3303/0", senmlNotify(tc.body))

			assert.Equal(t, tc.ingested, ing.callCount(), "ingest calls")
			assert.Equal(t, tc.nonNumeric, testutil.ToFloat64(metrics.RecordsNonNumeric), "non-numeric records")
			assert.Equal(t, tc.nonFinite, testutil.ToFloat64(metrics.RecordsNonFinite), "non-finite records")
			assert.Equal(t, tc.unnamed, testutil.ToFloat64(metrics.RecordsUnnamed), "unnamed records")
		})
	}
}

// TestASilentDeviceAndASkippedOneAreDistinguishable is the observation the counters exist
// to split, asserted at the layer an operator actually reads. Both Notifies ingest nothing;
// only the counters differ.
func TestASilentDeviceAndASkippedOneAreDistinguishable(t *testing.T) {
	empty, _, emptyMetrics := newHarness(t)
	ec := newFakeConn(1)
	require.True(t, empty.Establish("id-1", 1, ec, testTarget, []string{"/3303/0"}))
	ec.deliver("/3303/0", senmlNotify(`[]`))

	booleans, _, boolMetrics := newHarness(t)
	bc := newFakeConn(1)
	require.True(t, booleans.Establish("id-1", 1, bc, testTarget, []string{"/3303/0"}))
	bc.deliver("/3303/0", senmlNotify(`[{"bn":"/3311/0/","n":"5850","vb":true}]`))

	assert.Equal(t, float64(0), testutil.ToFloat64(emptyMetrics.RecordsNonNumeric),
		"a device that sent an empty pack skipped nothing")
	assert.Equal(t, float64(1), testutil.ToFloat64(boolMetrics.RecordsNonNumeric),
		"a device that sent only booleans skipped a record — this is the difference that did not exist")
}
