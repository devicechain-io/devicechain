// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatTimePreservesSubSecond pins the read surface at the resolution the system
// actually records. Every timestamp crosses the pipeline at RFC3339Nano and lands in a
// microsecond timestamptz column, so formatting the READ at whole seconds published a
// coarser clock than the one stored: two readings 200ms apart came back identical, and
// a value read and written back moved.
//
// The two halves matter together. Dropping sub-second precision is the defect; changing
// what a whole-second time looks like on the wire would be a gratuitous break of every
// client, and RFC3339Nano's suppression of trailing zeros is what makes that safe.
func TestFormatTimePreservesSubSecond(t *testing.T) {
	sub := time.Date(2026, 7, 31, 10, 15, 30, 123456000, time.UTC)
	require.NotNil(t, FormatTime(sub))
	assert.Equal(t, "2026-07-31T10:15:30.123456Z", *FormatTime(sub),
		"sub-second precision must survive the read surface")

	whole := time.Date(2026, 7, 31, 10, 15, 30, 0, time.UTC)
	require.NotNil(t, FormatTime(whole))
	assert.Equal(t, "2026-07-31T10:15:30Z", *FormatTime(whole),
		"a whole-second time must be byte-identical to the old RFC3339 output")
	assert.Equal(t, whole.Format(time.RFC3339), *FormatTime(whole))
}

// TestFormatTimeSeparatesTimesWithinOneSecond is the negative control for the above: it
// asserts the OLD formatting actively collided, so the fix is measured against a failure
// that was really there rather than against a hypothetical one. Two distinct instants
// 250ms apart formatted to one identical string under RFC3339 — which is what let a stale
// optimistic-concurrency precondition pass, and what made two events indistinguishable
// through the API.
func TestFormatTimeSeparatesTimesWithinOneSecond(t *testing.T) {
	a := time.Date(2026, 7, 31, 10, 15, 30, 250000000, time.UTC)
	b := time.Date(2026, 7, 31, 10, 15, 30, 500000000, time.UTC)

	require.Equal(t, a.Format(time.RFC3339), b.Format(time.RFC3339),
		"negative control: these two instants MUST be indistinguishable under the old layout, "+
			"otherwise this test proves nothing about the bug it guards")

	assert.NotEqual(t, *FormatTime(a), *FormatTime(b),
		"two instants inside one second must be distinguishable on the read surface")
}

// TestFormatTimeZeroIsNull keeps the existing null contract intact — a zero time is absent,
// not the epoch. Callers rely on this to render an optional timestamp as GraphQL null.
func TestFormatTimeZeroIsNull(t *testing.T) {
	assert.Nil(t, FormatTime(time.Time{}))
}
