// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	mu      sync.Mutex
	failing bool
	writes  []storedWrite
	samples []storedSample
	trims   int
	// during runs inside a health write, to model a tally that changes while it is being written.
	during func()
}

type storedWrite struct {
	tenant string
	rows   []model.DecoderHealthWrite
}
type storedSample struct {
	tenant string
	s      model.DecoderSampleWrite
}

func (f *fakeStore) WriteDecoderHealth(ctx context.Context, rows []model.DecoderHealthWrite) (int, int, error) {
	tenant, _ := core.TenantFromContext(ctx)
	if f.during != nil {
		f.during()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return 0, 0, errors.New("db away")
	}
	f.writes = append(f.writes, storedWrite{tenant, append([]model.DecoderHealthWrite(nil), rows...)})
	return len(rows), 0, nil
}

func (f *fakeStore) TrimDecoderHealth(ctx context.Context, before time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trims++
	return nil
}

func (f *fakeStore) StoreDecoderSample(ctx context.Context, s model.DecoderSampleWrite, _ time.Time) (bool, error) {
	tenant, _ := core.TenantFromContext(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return false, errors.New("db away")
	}
	f.samples = append(f.samples, storedSample{tenant, s})
	return true, nil
}

// last is the newest written row for (tenant, profile, version).
func (f *fakeStore) last(tenant, profile string, version int32) (model.DecoderHealthWrite, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.writes) - 1; i >= 0; i-- {
		if f.writes[i].tenant != tenant {
			continue
		}
		for _, r := range f.writes[i].rows {
			if r.ProfileToken == profile && r.Version == version {
				return r, true
			}
		}
	}
	return model.DecoderHealthWrite{}, false
}

func failure(tenant string, dt uint, pv string) DecodeObservation {
	return DecodeObservation{Tenant: tenant, DeviceTypeId: dt, Device: "dev-1", ProfileVersionToken: pv,
		Outcome: DecodeFailed, Reason: payloaddecode.ReasonEvalError, Error: "decode_failed: eval_error: boom",
		Uplink: &esmodel.UnresolvedIntegrationUplinkPayload{Frame: []byte{0x00, 0xD7}}}
}

func success(tenant, pv string, outcome DecodeOutcome) DecodeObservation {
	return DecodeObservation{Tenant: tenant, DeviceTypeId: 7, ProfileVersionToken: pv, Outcome: outcome}
}

// Every uplink lands in exactly one bucket, and the four sum to the uplinks offered.
func TestEveryUplinkLandsInExactlyOneBucket(t *testing.T) {
	c := newFakeClock()
	st := &fakeStore{}
	a := newDecodeAccountant(st, "pod-a", nil, c.now)
	const failures = 1000
	var recorded, suppressed int
	for i := 0; i < failures; i++ {
		if a.ObserveDecode(failure("acme", 7, "meter@3")) {
			recorded++
		} else {
			suppressed++
		}
	}
	for i := 0; i < 30; i++ {
		a.ObserveDecode(success("acme", "meter@3", DecodeDecoded))
	}
	for i := 0; i < 5; i++ {
		a.ObserveDecode(success("acme", "meter@3", DecodeIgnored))
	}
	require.Equal(t, DecodeRecordBurst, recorded, "rows written to failed-events are bounded by the budget")
	require.NoError(t, a.Flush(context.Background()))

	r, found := st.last("acme", "meter", 3)
	require.True(t, found)
	require.EqualValues(t, recorded, r.Failed, "failed counts exactly what was recorded")
	require.EqualValues(t, suppressed, r.Suppressed, "suppressed is NOT also failed")
	require.EqualValues(t, 30, r.Decoded)
	require.EqualValues(t, 5, r.Ignored)
	require.EqualValues(t, failures+30+5, r.Decoded+r.Ignored+r.Failed+r.Suppressed, "decoded + ignored + failed + suppressed == offered")
	require.Equal(t, "decode_failed: eval_error: boom", r.LastError)
	require.NotNil(t, r.LastFailureAt)
}

func TestAFlushWritesAbsoluteValuesSoARetryLandsOnTheSameRow(t *testing.T) {
	c := newFakeClock()
	st := &fakeStore{}
	a := newDecodeAccountant(st, "pod-a", nil, c.now)
	for i := 0; i < 3; i++ {
		a.ObserveDecode(success("acme", "meter@1", DecodeDecoded))
	}
	require.NoError(t, a.Flush(context.Background()))

	// The commit may have landed with its reply lost: the replica does not know, so the next pass
	// writes what the tally is NOW. It must be the cumulative value, not the 2 more that happened.
	a.ObserveDecode(success("acme", "meter@1", DecodeDecoded))
	a.ObserveDecode(success("acme", "meter@1", DecodeDecoded))
	require.NoError(t, a.Flush(context.Background()))
	r, _ := st.last("acme", "meter", 1)
	require.EqualValues(t, 5, r.Decoded, "absolute for (replica, window), never an increment")
	require.Equal(t, "pod-a", r.Replica)
	require.Equal(t, c.now().Truncate(time.Minute), r.WindowStart)
}

func TestAFailedFlushKeepsTheTallyAndTheNextPassWritesIt(t *testing.T) {
	c := newFakeClock()
	st := &fakeStore{failing: true}
	a := newDecodeAccountant(st, "pod-a", nil, c.now)
	a.ObserveDecode(success("acme", "meter@1", DecodeDecoded))
	require.Error(t, a.Flush(context.Background()))
	require.Empty(t, st.writes)

	st.failing = false
	a.ObserveDecode(success("acme", "meter@1", DecodeDecoded))
	require.NoError(t, a.Flush(context.Background()))
	r, found := st.last("acme", "meter", 1)
	require.True(t, found)
	require.EqualValues(t, 2, r.Decoded)
}

func TestATallyThatChangesWhileItIsWrittenIsWrittenAgain(t *testing.T) {
	c := newFakeClock()
	st := &fakeStore{}
	a := newDecodeAccountant(st, "pod-a", nil, c.now)
	a.ObserveDecode(success("acme", "meter@1", DecodeDecoded))
	st.during = func() { a.ObserveDecode(success("acme", "meter@1", DecodeDecoded)) }
	require.NoError(t, a.Flush(context.Background()))
	st.during = nil
	require.NoError(t, a.Flush(context.Background()))
	r, _ := st.last("acme", "meter", 1)
	require.EqualValues(t, 2, r.Decoded, "the observation made mid-write is not lost to a clean flag")
}

func TestClosedWindowsAreWrittenOnceThenForgotten(t *testing.T) {
	c := newFakeClock()
	st := &fakeStore{}
	a := newDecodeAccountant(st, "pod-a", nil, c.now)
	a.ObserveDecode(success("acme", "meter@1", DecodeDecoded))
	require.NoError(t, a.Flush(context.Background()))
	c.add(2 * time.Minute)
	a.ObserveDecode(success("acme", "meter@1", DecodeDecoded))
	require.NoError(t, a.Flush(context.Background()))
	require.NoError(t, a.Flush(context.Background()))
	a.mu.Lock()
	defer a.mu.Unlock()
	require.Len(t, a.tallies, 1, "only the open window is held")
}

func TestHealthIsWrittenUnderEachTenantsOwnContext(t *testing.T) {
	c := newFakeClock()
	st := &fakeStore{}
	a := newDecodeAccountant(st, "pod-a", nil, c.now)
	a.ObserveDecode(success("acme", "meter@1", DecodeDecoded))
	a.ObserveDecode(success("globex", "meter@1", DecodeIgnored))
	a.ObserveDecode(success("globex", "meter@1", DecodeIgnored))
	require.NoError(t, a.Flush(context.Background()))
	acme, _ := st.last("acme", "meter", 1)
	globex, _ := st.last("globex", "meter", 1)
	require.EqualValues(t, 1, acme.Decoded)
	require.EqualValues(t, 0, acme.Ignored, "one tenant's tally never lands in another's rows")
	require.EqualValues(t, 2, globex.Ignored)
	require.EqualValues(t, 0, globex.Decoded)
}

func TestAFailureKeepsAtMostOneSampleEveryTenSecondsPerProfile(t *testing.T) {
	c := newFakeClock()
	st := &fakeStore{}
	a := newDecodeAccountant(st, "pod-a", nil, c.now)
	for i := 0; i < 15; i++ {
		a.ObserveDecode(failure("acme", 7, "meter@1"))
	}
	require.NoError(t, a.Flush(context.Background()))
	require.Len(t, st.samples, 1, "the sample budget, not the record budget, bounds samples")

	c.add(DecodeSampleInterval)
	a.ObserveDecode(failure("acme", 7, "meter@1"))
	require.NoError(t, a.Flush(context.Background()))
	require.Len(t, st.samples, 2)

	s := st.samples[0]
	require.Equal(t, "acme", s.tenant)
	require.Equal(t, "meter", s.s.ProfileToken)
	require.EqualValues(t, 1, s.s.Version)
	require.Equal(t, "dev-1", s.s.DeviceToken)
	require.Equal(t, "eval_error", s.s.Reason)
	var in payloaddecode.VectorInput
	require.NoError(t, json.Unmarshal(s.s.Input, &in))
	require.NotNil(t, in.FrameHex)
	require.Equal(t, "00d7", *in.FrameHex, "a sample is the uplink in the shape a vector uses")
}

func TestASuppressedFailureStoresNoSample(t *testing.T) {
	c := newFakeClock()
	st := &fakeStore{}
	a := newDecodeAccountant(st, "pod-a", nil, c.now)
	for i := 0; i < DecodeRecordBurst; i++ {
		a.ObserveDecode(failure("acme", 7, "meter@1"))
	}
	// Device type 7 has no record tokens left; profile "other" has not spent its sample budget.
	require.False(t, a.ObserveDecode(failure("acme", 7, "other@1")))
	require.NoError(t, a.Flush(context.Background()))
	for _, s := range st.samples {
		require.NotEqual(t, "other", s.s.ProfileToken, "a failure that was not recorded is not sampled")
	}
	require.Len(t, st.samples, 1)
}

func TestAnUnattributableOutcomeStillGetsAVerdict(t *testing.T) {
	c := newFakeClock()
	st := &fakeStore{}
	a := newDecodeAccountant(st, "pod-a", nil, c.now)
	got := 0
	for i := 0; i < 50; i++ {
		if a.ObserveDecode(failure("acme", 7, "")) {
			got++
		}
	}
	require.Equal(t, DecodeRecordBurst, got, "no profile to count it against does not lift the budget")
	require.NoError(t, a.Flush(context.Background()))
	require.Empty(t, st.writes)
}

func TestHealthRowsAreTrimmedPastRetentionOncePerHour(t *testing.T) {
	c := newFakeClock()
	st := &fakeStore{}
	a := newDecodeAccountant(st, "pod-a", nil, c.now)
	a.ObserveDecode(success("acme", "meter@1", DecodeDecoded))
	require.NoError(t, a.Flush(context.Background()))
	require.Zero(t, st.trims, "not due yet")

	// Due, even though nothing was written this pass: a quiet process still expires old rows.
	c.add(61 * time.Minute)
	require.NoError(t, a.Flush(context.Background()))
	require.Equal(t, 1, st.trims)
	require.NoError(t, a.Flush(context.Background()))
	require.Equal(t, 1, st.trims, "once an hour")
}

// A redelivered failure is always recorded, is tallied as failed (the first delivery may never have
// reached a decode), and still charges the budget so a failed-events outage cannot make failures free.
func TestARedeliveredFailureIsRecordedTalliedAndChargesTheBudget(t *testing.T) {
	c := newFakeClock()
	st := &fakeStore{}
	a := newDecodeAccountant(st, "pod-a", nil, c.now)
	for i := 0; i < DecodeRecordBurst; i++ {
		require.True(t, a.ObserveDecode(failure("acme", 7, "meter@1")))
	}
	require.False(t, a.ObserveDecode(failure("acme", 7, "meter@1")), "a first delivery past the budget is suppressed")

	again := failure("acme", 7, "meter@1")
	again.Redelivered = true
	require.True(t, a.ObserveDecode(again), "its redelivery is recorded even so")
	require.NoError(t, a.Flush(context.Background()))
	r, _ := st.last("acme", "meter", 1)
	require.EqualValues(t, DecodeRecordBurst+1, r.Failed, "the redelivery is tallied as failed")
	require.EqualValues(t, 1, r.Suppressed)
}

func TestRedeliveriesDrainTheBudgetSoNewFailuresAreSuppressedNotAddedOnTop(t *testing.T) {
	c := newFakeClock()
	a := newDecodeAccountant(&fakeStore{}, "pod-a", nil, c.now)
	redo := failure("acme", 7, "meter@1")
	redo.Redelivered = true
	for i := 0; i < 3*DecodeRecordBurst; i++ {
		require.True(t, a.ObserveDecode(redo), "a redelivery is never denied")
	}
	require.False(t, a.ObserveDecode(failure("acme", 7, "meter@1")),
		"but the redeliveries spent the bucket, so a first delivery is now suppressed")
}

func TestTheStoredErrorIsBoundedOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", 400) // 800 bytes
	s := boundText(long, maxTallyError)
	require.LessOrEqual(t, len(s), maxTallyError)
	require.True(t, strings.HasPrefix(long, s))
	require.Equal(t, "ab", boundText("ab", 512))
}

func TestSplitProfileVersion(t *testing.T) {
	p, v, okk := splitProfileVersion("my@profile@12")
	require.True(t, okk)
	require.Equal(t, "my@profile", p)
	require.EqualValues(t, 12, v)
	for _, bad := range []string{"", "meter", "@3", "meter@x", "meter@"} {
		_, _, okk := splitProfileVersion(bad)
		require.False(t, okk, bad)
	}
}
