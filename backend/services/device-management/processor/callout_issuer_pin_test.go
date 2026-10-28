// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// pinHarness is an IssuerPin over a scripted membership, a settable clock and a settable
// connection state, counting how many pings it sent.
type pinHarness struct {
	pin *IssuerPin

	mu        sync.Mutex
	now       time.Time
	live      []string
	expected  int
	gatherErr error
	up        bool
	pings     atomic.Int64
}

func newServerID(t *testing.T) string {
	t.Helper()
	kp, err := nkeys.CreateServer()
	require.NoError(t, err)
	id, err := kp.PublicKey()
	require.NoError(t, err)
	return id
}

func newPinHarness(t *testing.T, live ...string) *pinHarness {
	t.Helper()
	h := &pinHarness{now: time.Unix(1_800_000_000, 0), live: live, expected: len(live), up: true}
	ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	pin, err := NewIssuerPin(IssuerPinConfig{
		Gather: func(context.Context) ([]string, int, error) {
			h.pings.Add(1)
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.gatherErr != nil {
				return nil, 0, h.gatherErr
			}
			return append([]string(nil), h.live...), h.expected, nil
		},
		Connected: func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.up },
		Now:       func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now },
		Metrics:   NewIssuerPinMetrics(ms),
	})
	require.NoError(t, err)
	h.pin = pin
	return h
}

func (h *pinHarness) advance(d time.Duration) { h.mu.Lock(); h.now = h.now.Add(d); h.mu.Unlock() }
func (h *pinHarness) setLive(expected int, ids ...string) {
	h.mu.Lock()
	h.live, h.expected = ids, expected
	h.mu.Unlock()
}
func (h *pinHarness) setUp(up bool) { h.mu.Lock(); h.up = up; h.mu.Unlock() }
func (h *pinHarness) refreshCount(result string) float64 {
	return testutil.ToFloat64(h.pin.metrics.Refresh.WithLabelValues(result))
}

// learn runs the first refresh Start does, without the ticker goroutine.
func (h *pinHarness) learn() { h.pin.refresh(context.Background(), true) }

func TestIssuerPinAcceptsALiveServer(t *testing.T) {
	a := newServerID(t)
	h := newPinHarness(t, a)
	h.learn()
	require.Equal(t, pinPinned, h.pin.Check(context.Background(), a, a))
	require.EqualValues(t, 1, h.pings.Load(), "a pinned issuer must not cost a ping")
}

func TestIssuerPinRefusesAnUnknownIssuerAfterTheRePingFindsNothing(t *testing.T) {
	a, forged := newServerID(t), newServerID(t)
	h := newPinHarness(t, a)
	h.learn()
	h.advance(10 * time.Second) // past the minimum interval, so the re-ping is allowed
	require.Equal(t, pinUnknown, h.pin.Check(context.Background(), forged, forged))
	require.EqualValues(t, 2, h.pings.Load(), "an unknown issuer must trigger exactly one re-ping before it is refused")
	require.NotContains(t, h.pin.Members(), forged)
}

func TestIssuerPinLearnsAServerThatJoinedSinceTheLastRefresh(t *testing.T) {
	// The rolling restart: a new server appears, and its first authorization request arrives
	// before the periodic refresh has met it.
	a, b := newServerID(t), newServerID(t)
	h := newPinHarness(t, a)
	h.learn()
	h.setLive(2, a, b)
	h.advance(5 * time.Second)
	require.Equal(t, pinPinned, h.pin.Check(context.Background(), b, b), "the new server was not learned on the re-ping")
	require.EqualValues(t, 2, h.pings.Load())
	require.ElementsMatch(t, []string{a, b}, h.pin.Members())
}

func TestIssuerPinRollingRestartLockoutIsBoundedByTheMinimumInterval(t *testing.T) {
	// A server joins just after a refresh: a request inside the minimum interval cannot trigger
	// another ping (so it is refused, fail closed), and the same request one interval later is
	// granted. The lockout is at most MinRefreshInterval, not a refresh period.
	a, b := newServerID(t), newServerID(t)
	h := newPinHarness(t, a)
	h.learn()
	h.setLive(2, a, b)
	h.advance(500 * time.Millisecond)
	require.Equal(t, pinUnknown, h.pin.Check(context.Background(), b, b), "inside the interval: refused, not failed open")
	require.EqualValues(t, 1, h.pings.Load())
	h.advance(DefaultIssuerPinMinRefreshInterval)
	require.Equal(t, pinPinned, h.pin.Check(context.Background(), b, b), "one interval later the device gets in")
}

func TestIssuerPinFailsClosedWhenTheSystemConnectionIsDown(t *testing.T) {
	a := newServerID(t)
	h := newPinHarness(t, a)
	h.learn()
	h.setUp(false)
	require.Equal(t, pinUnavailable, h.pin.Check(context.Background(), a, a),
		"a known server was pinned with the system connection down")
	require.EqualValues(t, 1, h.pings.Load(), "no ping can be sent over a down connection")
	h.setUp(true)
	require.Equal(t, pinPinned, h.pin.Check(context.Background(), a, a), "control: pinned again once it is back")
}

func TestIssuerPinFailsClosedWhenNothingWasEverLearned(t *testing.T) {
	a := newServerID(t)
	h := newPinHarness(t, a)
	h.mu.Lock()
	h.gatherErr = errors.New("no server answered")
	h.mu.Unlock()
	require.Equal(t, pinUnavailable, h.pin.Check(context.Background(), a, a), "granted on a membership nobody ever learned")
	require.EqualValues(t, 1, h.refreshCount(pinRefreshError))
}

func TestIssuerPinRefusesAStaleSnapshotThatCannotBeRefreshed(t *testing.T) {
	a := newServerID(t)
	h := newPinHarness(t, a)
	h.learn()
	h.mu.Lock()
	h.gatherErr = errors.New("sys account unreachable")
	h.mu.Unlock()
	h.advance(issuerPinMaxAgeIntervals*DefaultIssuerPinRefreshInterval + time.Second)
	require.Equal(t, pinUnavailable, h.pin.Check(context.Background(), a, a),
		"a snapshot older than the pin's max age vouched for a server")
}

func TestIssuerPinReRingIsRateLimitedAgainstAFloodOfForgedRequests(t *testing.T) {
	a := newServerID(t)
	h := newPinHarness(t, a)
	h.learn()
	h.advance(10 * time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		forged := newServerID(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.Equal(t, pinUnknown, h.pin.Check(context.Background(), forged, forged))
		}()
	}
	wg.Wait()
	require.EqualValues(t, 2, h.pings.Load(), "200 forged requests inside one interval amplified into more than one $SYS ping")
	require.GreaterOrEqual(t, h.refreshCount(pinRefreshRateLimited), float64(190))
}

func TestIssuerPinMismatchedClaimedServerIsRefusedEvenForALiveIssuer(t *testing.T) {
	a, b := newServerID(t), newServerID(t)
	h := newPinHarness(t, a, b)
	h.learn()
	require.Equal(t, pinUnknown, h.pin.Check(context.Background(), a, b),
		"a request signed by one live server but naming another was granted")
	require.Equal(t, pinUnknown, h.pin.Check(context.Background(), "", ""))
}

func TestIssuerPinPartialAnswerNeverEvictsACompleteOneDoes(t *testing.T) {
	a, b := newServerID(t), newServerID(t)
	h := newPinHarness(t, a, b)
	h.learn()
	h.advance(time.Minute)
	h.setLive(2, a) // b answered late: the cluster still claims two
	h.pin.refresh(context.Background(), true)
	require.ElementsMatch(t, []string{a, b}, h.pin.Members(), "a partial answer evicted a live server")
	require.EqualValues(t, 1, h.refreshCount(pinRefreshIncomplete))

	h.setLive(1, a) // the cluster now claims one: b has really gone
	h.pin.refresh(context.Background(), true)
	require.ElementsMatch(t, []string{a}, h.pin.Members(), "a complete answer left a departed server pinned")
}

func TestIssuerPinConstructorRefusesWhatCouldNotWork(t *testing.T) {
	_, err := NewIssuerPin(IssuerPinConfig{Connected: func() bool { return true }})
	require.Error(t, err)
	_, err = NewIssuerPin(IssuerPinConfig{Gather: func(context.Context) ([]string, int, error) { return nil, 0, nil }})
	require.Error(t, err)
	_, err = NewIssuerPin(IssuerPinConfig{
		Gather:    func(context.Context) ([]string, int, error) { return nil, 0, nil },
		Connected: func() bool { return true }, RefreshInterval: time.Second, MinRefreshInterval: time.Minute})
	require.Error(t, err)
}
