// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package decode

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 THE FAILURE THESE PIN IS AN ABSENCE, AND ABSENCES DO NOT ALARM. Every skip path here
// returned an empty sample slice and NO error, and incremented nothing on the way. So
// "this device reports only booleans" (fine), "this device's firmware sends garbage"
// (broken) and "this device stopped sending" (broken differently) produced byte-identical
// metrics. The counts are the whole fix; the drops themselves were always right.

// TestEverySkipPathIsCounted walks each reason separately, because a single total would
// have let any one of them be wired to the wrong counter and still add up.
func TestEverySkipPathIsCounted(t *testing.T) {
	for _, tc := range []struct {
		name        string
		payload     string
		wantSamples int
		want        Skips
	}{
		{
			// The benign one, and the reason an empty result is not an error: an IPSO
			// object reporting a boolean is a device working correctly.
			name:        "boolean, string and opaque readings",
			payload:     `[{"bn":"/3311/0/","n":"5850","vb":true},{"n":"5750","vs":"kitchen"},{"n":"5751","vd":"AAEC"}]`,
			wantSamples: 0,
			want:        Skips{NonNumeric: 3},
		},
		{
			// RFC 8428 §4.1: a record carrying only a sum is not a value reading.
			name:        "a sum-only record",
			payload:     `[{"bn":"/3303/0/","n":"5700","s":42}]`,
			wantSamples: 0,
			want:        Skips{NonNumeric: 1},
		},
		{
			// v+bv overflows to +Inf, which the resolver's ParseFloat ACCEPTS — so this
			// is the skip that would otherwise put a non-finite value in the store.
			name:        "a value that resolves non-finite",
			payload:     `[{"bn":"/3303/0/","bv":1e308,"n":"5700","v":1e308}]`,
			wantSamples: 0,
			want:        Skips{NonFinite: 1},
		},
		{
			// bn+n normalises to "" or "/": a sample with no resource path has no series.
			name:        "an empty resolved name",
			payload:     `[{"bn":"","n":"","v":1},{"bn":"/","n":"","v":2}]`,
			wantSamples: 0,
			want:        Skips{Unnamed: 2},
		},
		{
			// The realistic pack: some readings, some not, one fault. Each lands on its
			// own counter and the good ones still come through.
			name: "a mixed pack",
			// The unnamed record clears the base name and comes LAST, because bn is
			// STICKY (RFC 8428 §4.1): an empty n under a live bn resolves to the base
			// name, which is a perfectly good series and not a skip at all.
			payload: `[{"bn":"/3303/0/","n":"5700","v":21.5},{"n":"5850","vb":true},` +
				`{"n":"5601","v":20.1},{"bn":"","n":"","v":3}]`,
			wantSamples: 2,
			want:        Skips{NonNumeric: 1, Unnamed: 1},
		},
		{
			// The counterweight: a clean pack must count nothing at all, or every one of
			// these counters is noise an operator learns to ignore.
			name:        "a clean pack skips nothing",
			payload:     `[{"bn":"/3303/0/","n":"5700","v":21.5},{"n":"5601","v":20.1}]`,
			wantSamples: 2,
			want:        Skips{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples, skips, err := Samples(message.AppSenmlJSON, []byte(tc.payload), fixedNow)
			require.NoError(t, err, "every case here is a well-formed pack; skipping is not failing")
			assert.Len(t, samples, tc.wantSamples)
			assert.Equal(t, tc.want, skips)
		})
	}
}

// TestAnEmptyPackIsDistinguishableFromASkippedOne is the observation the counters exist to
// split. Both decode to zero samples and no error; only the skip counts tell them apart.
func TestAnEmptyPackIsDistinguishableFromASkippedOne(t *testing.T) {
	empty, emptySkips, err := Samples(message.AppSenmlJSON, []byte(`[]`), fixedNow)
	require.NoError(t, err)
	require.Empty(t, empty)

	booleans, boolSkips, err := Samples(message.AppSenmlJSON,
		[]byte(`[{"bn":"/3311/0/","n":"5850","vb":true}]`), fixedNow)
	require.NoError(t, err)
	require.Empty(t, booleans)

	assert.Equal(t, 0, emptySkips.Total(), "a device that sent nothing skipped nothing")
	assert.Equal(t, 1, boolSkips.Total(), "a device that sent only booleans skipped a record")
	assert.NotEqual(t, emptySkips, boolSkips,
		"these two are the cases that read identically before the counts existed")
}
