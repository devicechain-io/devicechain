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
	h := &pinHarness{now: time.Unix(1_800_000_000, 0), live: live, up: true}
	ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	pin, err := NewIssuerPin(IssuerPinConfig{
		Gather: func(context.Context) ([]string, error) {
			h.pings.Add(1)
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.gatherErr != nil {
				return nil, h.gatherErr
			}
			return append([]string(nil), h.live...), nil
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
func (h *pinHarness) setLive(ids ...string) {
	h.mu.Lock()
	h.live = ids
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
	h.setLive(a, b)
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
	h.setLive(a, b)
	h.advance(500 * time.Millisecond)
	require.Equal(t, pinPending, h.pin.Check(context.Background(), b, b), "inside the interval: refused as pending (not learned yet), not failed open")
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
	const n = 200
	forged := make([]string, n)
	for i := range forged {
		forged[i] = newServerID(t)
	}
	verdicts := make([]pinVerdict, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			verdicts[i] = h.pin.Check(context.Background(), forged[i], forged[i])
		}()
	}
	wg.Wait() // asserted here, on the test goroutine, never inside the workers
	unknown, pending := 0, 0
	for _, v := range verdicts {
		switch v {
		case pinUnknown:
			unknown++
		case pinPending:
			pending++
		default:
			t.Fatalf("a forged request got verdict %d", v)
		}
	}
	require.EqualValues(t, 2, h.pings.Load(), "200 forged requests inside one interval amplified into more than one $SYS ping")
	require.Equal(t, 1, unknown, "exactly the request that pinged finds nothing; the rest are not-yet-learned")
	require.Equal(t, n-1, pending)
	require.GreaterOrEqual(t, h.refreshCount(pinRefreshRateLimited), float64(n-1))
}

func TestIssuerPinAFailedPingIsPendingNotAForgery(t *testing.T) {
	a, b := newServerID(t), newServerID(t)
	h := newPinHarness(t, a)
	h.learn()
	h.mu.Lock()
	h.gatherErr = errors.New("sys account hiccup")
	h.mu.Unlock()
	h.advance(10 * time.Second)
	require.Equal(t, pinPending, h.pin.Check(context.Background(), b, b),
		"a request refused for want of a working ping was counted as a forgery")
}

func TestIssuerPinMismatchedClaimedServerIsRefusedEvenForALiveIssuer(t *testing.T) {
	a, b := newServerID(t), newServerID(t)
	h := newPinHarness(t, a, b)
	h.learn()
	require.Equal(t, pinUnknown, h.pin.Check(context.Background(), a, b),
		"a request signed by one live server but naming another was granted")
	require.Equal(t, pinUnknown, h.pin.Check(context.Background(), "", ""))
}

func TestIssuerPinAnAnswerNeverShrinksTheSetAServerAgesOutOnItsOwnClock(t *testing.T) {
	a, b := newServerID(t), newServerID(t)
	h := newPinHarness(t, a, b)
	h.learn()
	// Only a answers from now on (b is slow, or the ping rode a freshly booted pod).
	h.setLive(a)
	h.advance(40 * time.Second)
	h.pin.refresh(context.Background(), true)
	require.ElementsMatch(t, []string{a, b}, h.pin.Members(), "an answer that heard only one server evicted the other")
	require.Equal(t, pinPinned, h.pin.Check(context.Background(), b, b))

	// b stays silent past the max age while a keeps answering: only b ages out.
	h.advance(60 * time.Second)
	h.pin.refresh(context.Background(), true)
	require.ElementsMatch(t, []string{a}, h.pin.Members(), "a server not heard for the max age stayed pinned")
	require.Equal(t, pinPinned, h.pin.Check(context.Background(), a, a), "the server that kept answering was dropped with it")
	h.advance(10 * time.Second)
	require.Equal(t, pinUnknown, h.pin.Check(context.Background(), b, b))
}

func TestIssuerPinAnIdNotHeardForTheMaxAgeIsNotPinnedEvenIfTheSnapshotIsFresh(t *testing.T) {
	// The per-id clock is read at lookup too, not only when a refresh prunes: between a refresh that
	// stamped a and the next one, b's own stamp may have run out.
	a, b := newServerID(t), newServerID(t)
	h := newPinHarness(t, a, b)
	h.learn()
	h.setLive(a)
	h.advance(60 * time.Second)
	h.pin.refresh(context.Background(), true) // a restamped, b 60s old: still believed
	require.Equal(t, pinPinned, h.pin.Check(context.Background(), b, b))
	h.advance(40 * time.Second) // b now 100s old (> 90s); snapshot is 40s old (fresh)
	h.mu.Lock()
	h.gatherErr = errors.New("quiet")
	h.mu.Unlock()
	require.NotEqual(t, pinPinned, h.pin.Check(context.Background(), b, b), "an id past the max age was believed on its neighbour's freshness")
}

func TestIssuerPinConstructorRefusesWhatCouldNotWork(t *testing.T) {
	_, err := NewIssuerPin(IssuerPinConfig{Connected: func() bool { return true }})
	require.Error(t, err)
	_, err = NewIssuerPin(IssuerPinConfig{Gather: func(context.Context) ([]string, error) { return nil, nil }})
	require.Error(t, err)
	_, err = NewIssuerPin(IssuerPinConfig{
		Gather:    func(context.Context) ([]string, error) { return nil, nil },
		Connected: func() bool { return true }, RefreshInterval: time.Second, MinRefreshInterval: time.Minute})
	require.Error(t, err)
}
