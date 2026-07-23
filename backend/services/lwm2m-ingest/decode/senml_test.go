// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package decode

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedNow is a deterministic receipt clock: its UnixMilli is 1_700_000_500_000. Any sample
// that falls back to receipt time must carry exactly this, so a broken absolute-time path is
// visible as an absolute-literal mismatch (the L1a lesson: relative assertions hid a severed
// clock).
func fixedNow() time.Time { return time.Unix(1_700_000_500, 0).UTC() }

const fixedNowMs = int64(1_700_000_500_000)

// TestDecodeAbsoluteTimeAndBaseName is the load-bearing golden pin: a mainstream LwM2M pack
// carries an absolute bt and per-record t deltas, and the resolved time must be (bt+t)*1000.
// The assertions are absolute literals so severing the bt+t addition (which would stamp the
// receipt clock) reddens the test.
func TestDecodeAbsoluteTimeAndBaseName(t *testing.T) {
	// bt = 1_700_000_000 (absolute seconds); two resources, the second 1.5s later.
	payload := []byte(`[
      {"bn":"/3303/0/","bt":1700000000,"n":"5700","v":21.5,"t":0},
      {"n":"5601","v":20.1,"t":1.5}
    ]`)

	samples, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	require.Len(t, samples, 2)

	assert.Equal(t, adapter.Sample{Name: "/3303/0/5700", Value: 21.5, Time: 1_700_000_000_000}, samples[0])
	assert.Equal(t, adapter.Sample{Name: "/3303/0/5601", Value: 20.1, Time: 1_700_000_001_500}, samples[1])
	// Neither fell back to the receipt clock — the whole point of the absolute path.
	assert.NotEqual(t, fixedNowMs, samples[0].Time)
}

// TestBaseValueAddsToEveryRecord pins RFC 8428 bv: the resolved value is v+bv. Dropping bv
// reddens this.
func TestBaseValueAddsToEveryRecord(t *testing.T) {
	payload := []byte(`[
      {"bn":"/3303/0/","bt":1700000000,"bv":100,"n":"5700","v":1},
      {"n":"5601","v":2}
    ]`)
	samples, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Equal(t, 101.0, samples[0].Value)
	assert.Equal(t, 102.0, samples[1].Value)
}

// TestBaseFieldsAreStickyFromAnyRecord proves base fields apply from the record that sets them
// forward — not read only from record 0. Record 1 inherits record 0's bn+bt; record 2 changes
// bn mid-array but still inherits bt.
func TestBaseFieldsAreStickyFromAnyRecord(t *testing.T) {
	payload := []byte(`[
      {"bn":"/3303/0/","bt":1700000000,"n":"5700","v":1,"t":0},
      {"n":"5601","v":2,"t":0},
      {"bn":"/3304/0/","n":"5700","v":3,"t":0}
    ]`)
	samples, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	require.Len(t, samples, 3)
	assert.Equal(t, "/3303/0/5700", samples[0].Name)
	assert.Equal(t, "/3303/0/5601", samples[1].Name)
	assert.Equal(t, "/3304/0/5700", samples[2].Name) // new bn, inherited bt
	for _, s := range samples {
		assert.Equal(t, int64(1_700_000_000_000), s.Time) // all share the sticky bt
	}
}

// TestNonNumericRecordsDropped: booleans, strings, opaque data, and sum-only records are not
// measurements (ADR-016). Only the two numeric v records survive.
func TestNonNumericRecordsDropped(t *testing.T) {
	payload := []byte(`[
      {"bn":"/3303/0/","bt":1700000000,"n":"5700","v":21.5},
      {"n":"5850","vb":true},
      {"n":"5750","vs":"pump-a"},
      {"n":"5751","vd":"AQID"},
      {"n":"5852","s":42},
      {"n":"5601","v":20.1}
    ]`)
	samples, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Equal(t, "/3303/0/5700", samples[0].Name)
	assert.Equal(t, "/3303/0/5601", samples[1].Name)
}

// TestNonFiniteValueDropped: a v+bv that overflows to +Inf must never become a sample (the
// resolver's ParseFloat would accept "+Inf" into the TSDB).
func TestNonFiniteValueDropped(t *testing.T) {
	payload := []byte(`[
      {"bn":"/3303/0/","bt":1700000000,"bv":1e308,"n":"5700","v":1e308},
      {"n":"5601","v":5}
    ]`)
	samples, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, "/3303/0/5601", samples[0].Name)
	assert.Equal(t, 1e308+5, samples[0].Value)
}

// TestRelativeOrAbsentTimeStampsReceiptClock: a resolved time below 2^28 (relative) or with no
// bt/t at all falls back to the receipt clock rather than persisting device-relative or 1970.
func TestRelativeOrAbsentTimeStampsReceiptClock(t *testing.T) {
	payload := []byte(`[
      {"bn":"/3303/0/","n":"5700","v":21.5},
      {"n":"5601","v":20.1,"t":100}
    ]`)
	samples, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Equal(t, fixedNowMs, samples[0].Time) // no time at all
	assert.Equal(t, fixedNowMs, samples[1].Time) // resolved 100 < 2^28 → relative → receipt
}

// TestNameNormalization: a double slash from a bn/n split collapses to one series, and an
// empty resolved name is dropped.
func TestNameNormalization(t *testing.T) {
	payload := []byte(`[
      {"bn":"/3303/0/","bt":1700000000,"n":"/5700","v":21.5},
      {"bn":"","n":"","v":9.9}
    ]`)
	samples, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, "/3303/0/5700", samples[0].Name) // "/3303/0/" + "/5700" collapsed
}

// TestUnsupportedVersionRejectsPack: any bver other than 10 drops the whole pack.
func TestUnsupportedVersionRejectsPack(t *testing.T) {
	payload := []byte(`[{"bver":5,"bn":"/3303/0/","bt":1700000000,"n":"5700","v":21.5}]`)
	_, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	assert.ErrorIs(t, err, ErrUnsupportedSenmlVersion)

	ok := []byte(`[{"bver":10,"bn":"/3303/0/","bt":1700000000,"n":"5700","v":21.5}]`)
	samples, _, err := Samples(message.AppSenmlJSON, ok, fixedNow)
	require.NoError(t, err)
	require.Len(t, samples, 1)
}

// TestUnsupportedContentFormat: only SenML-JSON decodes in L2; TLV (the 1.0 default) is a
// named follow-up and returns the typed error the caller counts+drops.
func TestUnsupportedContentFormat(t *testing.T) {
	_, _, err := Samples(message.AppLwm2mTLV, []byte("anything"), fixedNow)
	assert.ErrorIs(t, err, ErrUnsupportedContentFormat)
}

// TestWellFormedButNoMeasurements: a valid pack with only non-numeric records is an empty
// result, not an error.
func TestWellFormedButNoMeasurements(t *testing.T) {
	payload := []byte(`[{"bn":"/3342/0/","n":"5500","vb":false}]`)
	samples, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	assert.Empty(t, samples)
}

// TestMalformedJSON surfaces a decode error rather than silently emitting nothing.
func TestMalformedJSON(t *testing.T) {
	_, _, err := Samples(message.AppSenmlJSON, []byte(`{not an array`), fixedNow)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrUnsupportedContentFormat)
}

// TestAbsoluteTimeBoundary pins the exact 2^28 edge (RFC 8428 §4.5.3: ">= 2^28" is absolute):
// at the threshold the value is absolute; one below it is relative → receipt clock.
func TestAbsoluteTimeBoundary(t *testing.T) {
	at := []byte(`[{"bn":"/3303/0/","bt":268435456,"n":"5700","v":1}]`) // exactly 2^28
	s, _, err := Samples(message.AppSenmlJSON, at, fixedNow)
	require.NoError(t, err)
	require.Len(t, s, 1)
	assert.Equal(t, int64(268_435_456_000), s[0].Time)

	below := []byte(`[{"bn":"/3303/0/","bt":268435455,"n":"5700","v":1}]`) // 2^28 - 1
	s, _, err = Samples(message.AppSenmlJSON, below, fixedNow)
	require.NoError(t, err)
	require.Len(t, s, 1)
	assert.Equal(t, fixedNowMs, s[0].Time)
}

// TestHugeOrNegativeTimeStampsReceiptClock: a device-supplied bt that would overflow the
// seconds→ms int64 conversion (or a relative-past negative bt) must fall back to the receipt
// clock, never to math.MinInt64 or a 1970 stamp.
func TestHugeOrNegativeTimeStampsReceiptClock(t *testing.T) {
	huge := []byte(`[{"bn":"/3303/0/","bt":1e17,"n":"5700","v":1}]`)
	s, _, err := Samples(message.AppSenmlJSON, huge, fixedNow)
	require.NoError(t, err)
	require.Len(t, s, 1)
	assert.Equal(t, fixedNowMs, s[0].Time)

	overflow := []byte(`[{"bn":"/3303/0/","bt":1.7e308,"n":"5700","v":1,"t":1.7e308}]`) // bt+t → +Inf
	s, _, err = Samples(message.AppSenmlJSON, overflow, fixedNow)
	require.NoError(t, err)
	require.Len(t, s, 1)
	assert.Equal(t, fixedNowMs, s[0].Time)

	neg := []byte(`[{"bn":"/3303/0/","bt":-5000,"n":"5700","v":1}]`)
	s, _, err = Samples(message.AppSenmlJSON, neg, fixedNow)
	require.NoError(t, err)
	require.Len(t, s, 1)
	assert.Equal(t, fixedNowMs, s[0].Time)
}

// TestFractionalSecondRounds pins that a sub-millisecond fraction is rounded, not truncated.
func TestFractionalSecondRounds(t *testing.T) {
	payload := []byte(`[{"bn":"/3303/0/","bt":1700000000,"n":"5700","v":1,"t":0.0006}]`)
	s, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	require.Len(t, s, 1)
	assert.Equal(t, int64(1_700_000_000_001), s[0].Time) // 1700000000.0006*1000 = ...000.6 → round up
}

// TestTrailingSlashIsSameSeries: a trailing slash on a resolved name must not fork the series.
func TestTrailingSlashIsSameSeries(t *testing.T) {
	payload := []byte(`[
      {"bn":"/3303/0/5700","bt":1700000000,"v":1},
      {"bn":"/3303/0/5700/","v":2}
    ]`)
	s, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	require.Len(t, s, 2)
	assert.Equal(t, "/3303/0/5700", s[0].Name)
	assert.Equal(t, "/3303/0/5700", s[1].Name) // trailing slash stripped → one series
}

// TestBaseVersionRejectedFromLaterRecord: bver is sticky, so an unsupported version first
// appearing in a LATER record still rejects the whole pack (including the records before it).
func TestBaseVersionRejectedFromLaterRecord(t *testing.T) {
	payload := []byte(`[
      {"bn":"/3303/0/","bt":1700000000,"n":"5700","v":1},
      {"bver":5,"n":"5601","v":2}
    ]`)
	_, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	assert.ErrorIs(t, err, ErrUnsupportedSenmlVersion)
}

// TestBaseValueResetMidPack: a later record setting a new bv overrides the running base value
// for it and subsequent records (sticky-override).
func TestBaseValueResetMidPack(t *testing.T) {
	payload := []byte(`[
      {"bn":"/3303/0/","bt":1700000000,"bv":100,"n":"5700","v":1},
      {"bv":200,"n":"5601","v":2},
      {"n":"5602","v":3}
    ]`)
	s, _, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	require.Len(t, s, 3)
	assert.Equal(t, 101.0, s[0].Value) // bv 100
	assert.Equal(t, 202.0, s[1].Value) // bv reset to 200
	assert.Equal(t, 203.0, s[2].Value) // inherits 200
}

// A pack with more numeric records than MaxSamplesPerNotify is capped at the cap, with the
// overflow reported as the truncation count — the per-message DoS bound that keeps one Notify
// from flooding the store and that floors the sample limiter's burst.
func TestSamplesCappedAtMaxPerNotify(t *testing.T) {
	// Build a pack of cap+50 numeric records under one base name.
	var b strings.Builder
	b.WriteString(`[{"bn":"/3303/0/","n":"0","v":0}`)
	for i := 1; i < MaxSamplesPerNotify+50; i++ {
		fmt.Fprintf(&b, `,{"n":"%d","v":%d}`, i, i)
	}
	b.WriteString(`]`)

	samples, truncated, err := Samples(message.AppSenmlJSON, []byte(b.String()), fixedNow)
	require.NoError(t, err)
	assert.Len(t, samples, MaxSamplesPerNotify, "output capped at the per-Notify maximum")
	assert.Equal(t, 50, truncated, "the 50 records past the cap are reported dropped")
	// The kept samples are the FIRST cap records, in order (the tail is what is dropped).
	assert.Equal(t, "/3303/0/0", samples[0].Name)
	assert.Equal(t, float64(MaxSamplesPerNotify-1), samples[MaxSamplesPerNotify-1].Value)
}

// A pack at or below the cap is never truncated (the common case): truncated is 0.
func TestSamplesNotTruncatedUnderCap(t *testing.T) {
	payload := []byte(`[{"bn":"/3303/0/","n":"5700","v":21.5},{"n":"5601","v":20.1}]`)
	samples, truncated, err := Samples(message.AppSenmlJSON, payload, fixedNow)
	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Equal(t, 0, truncated)
}
