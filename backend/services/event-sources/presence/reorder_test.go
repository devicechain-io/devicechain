// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"fmt"
	"testing"
	"time"

	corepresence "github.com/devicechain-io/dc-microservice/presence"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func disconnectAdvisoryFor(device string, startNano, stopNano int64) []byte {
	return []byte(fmt.Sprintf(
		`{"client":{"client_id":"inst-1:acme:%s","client_type":"mqtt","start":%q,"stop":%q},"reason":"Client Closed"}`,
		device, time.Unix(0, startNano).UTC().Format(time.RFC3339Nano),
		time.Unix(0, stopNano).UTC().Format(time.RFC3339Nano)))
}

// fold applies emitted transitions in the given order the way device-state's merge does
// (device-state/model/api.go): presence.Decide on the stored (session, time, active), and an
// ordered transition overwrites all three.
func fold(events []heldEmit, order []int) corepresence.Prior {
	var p corepresence.Prior
	for _, i := range order {
		ev := events[i].ev
		claim := corepresence.ClaimDisconnected
		if ev.Connected {
			claim = corepresence.ClaimConnected
		}
		d := corepresence.Decide(p, corepresence.Incoming{SessionId: ev.SessionId, OccurredAt: ev.OccurredAt, Claim: claim})
		if d.Ordered {
			p = corepresence.Prior{SessionId: ev.SessionId, Time: ev.OccurredAt, HasTime: true, Connected: ev.Connected}
		}
	}
	return p
}

func permutations(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, rest := range permutations(n - 1) {
		for at := 0; at <= len(rest); at++ {
			p := append(append(append([]int{}, rest[:at]...), n-1), rest[at:]...)
			out = append(out, p)
		}
	}
	return out
}

// 🔴 THE ASYNC TAP KEEPS NO ORDER ACROSS ITS TWO SUBJECTS, AND IT DOES NOT NEED TO. Connects
// and disconnects arrive on two subscriptions whose delivery goroutines hand their transitions
// to one writer in whichever order they get there, so the stream can carry a device's
// disconnect ahead of the connect it ends, or an old connection's death behind its
// successor's birth. That was already true of the synchronous tap (two goroutines), and
// device-state merges with parallel writers and no per-device order besides. What makes it
// safe is that a broker transition carries no compare-and-set, so presence.Decide is a max
// over (session, time) with death winning a tie: every order of the same transitions ends in
// the same state. Pinned here on the transitions the tap actually emits (through Handle and
// the advisory mapping, not hand-built events), over EVERY order of each scenario.
//
// The completions are run in REVERSE submission order too: a connect whose acknowledgement
// lands after its own disconnect's still counts both and still wakes once. The wake is a hint
// that command-delivery re-checks against the projection, so a late one is harmless.
func TestTapTransitionsConvergeInEveryStreamOrder(t *testing.T) {
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).UnixNano()
	sec := int64(time.Second)
	for _, tc := range []struct {
		name      string
		advisory  []func(*Tap)
		session   int64
		connected bool
	}{
		{
			name: "a flap: connect, its disconnect, a reconnect",
			advisory: []func(*Tap){
				func(tap *Tap) { tap.Handle(context.Background(), true, connectAdvisoryFor("d", base)) },
				func(tap *Tap) { tap.Handle(context.Background(), false, disconnectAdvisoryFor("d", base, base+sec)) },
				func(tap *Tap) { tap.Handle(context.Background(), true, connectAdvisoryFor("d", base+2*sec)) },
			},
			session: base + 2*sec, connected: true,
		},
		{
			name: "two full sessions",
			advisory: []func(*Tap){
				func(tap *Tap) { tap.Handle(context.Background(), true, connectAdvisoryFor("d", base)) },
				func(tap *Tap) { tap.Handle(context.Background(), false, disconnectAdvisoryFor("d", base, base+sec)) },
				func(tap *Tap) { tap.Handle(context.Background(), true, connectAdvisoryFor("d", base+2*sec)) },
				func(tap *Tap) {
					tap.Handle(context.Background(), false, disconnectAdvisoryFor("d", base+2*sec, base+3*sec))
				},
			},
			session: base + 2*sec, connected: false,
		},
		{
			// The loser of a client-id takeover dies AFTER the winner was born: its death must
			// not take the winner offline, whichever lands first.
			name: "a takeover",
			advisory: []func(*Tap){
				func(tap *Tap) { tap.Handle(context.Background(), true, connectAdvisoryFor("d", base)) },
				func(tap *Tap) { tap.Handle(context.Background(), true, connectAdvisoryFor("d", base+sec)) },
				func(tap *Tap) {
					tap.Handle(context.Background(), false, disconnectAdvisoryFor("d", base, base+2*sec))
				},
			},
			session: base + sec, connected: true,
		},
		{
			// A connection born and killed at one instant is net-dead.
			name: "a connect and its disconnect at one instant",
			advisory: []func(*Tap){
				func(tap *Tap) { tap.Handle(context.Background(), true, connectAdvisoryFor("d", base)) },
				func(tap *Tap) { tap.Handle(context.Background(), false, disconnectAdvisoryFor("d", base, base)) },
			},
			session: base, connected: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			waker := &spyWaker{}
			held := &heldAsync{}
			m, emitted, failed := asyncMetrics()
			tap := NewTap(testInstance, "mqtt1", failingEmitter{fmt.Errorf("the sync path must not be used")}, nil, m).
				WithWaker(waker).WithAsyncEmitter(held)
			for _, a := range tc.advisory {
				a(tap)
			}
			require.Equal(t, len(tc.advisory), held.pending(), "every advisory must be queued")

			held.mu.Lock()
			events := append([]heldEmit(nil), held.held...)
			held.mu.Unlock()
			orders := permutations(len(events))
			for _, order := range orders {
				got := fold(events, order)
				assert.Equal(t, uint64(tc.session), got.SessionId, "stream order %v ended on the wrong session", order)
				assert.Equal(t, tc.connected, got.Connected, "stream order %v ended in the wrong state", order)
			}

			for i := len(events) - 1; i >= 0; i-- {
				held.finish(t, i, nil)
			}
			connects := 0
			for _, e := range events {
				if e.ev.Connected {
					connects++
				}
			}
			assert.Len(t, waker.seen(), connects, "each acknowledged connect wakes once, in any completion order")
			assert.Equal(t, float64(len(events)), testutil.ToFloat64(emitted.WithLabelValues(labelConnected))+
				testutil.ToFloat64(emitted.WithLabelValues(labelDisconnected)))
			assert.Zero(t, testutil.ToFloat64(failed))
		})
	}
}

// The asynchronous path keeps the synchronous one's clock-skew instrument: a session id that
// went backwards for a device is counted when the advisory is handled, and still emitted.
func TestAsyncTapCountsARegressedSession(t *testing.T) {
	held := &heldAsync{}
	regressed := prometheus.NewCounter(prometheus.CounterOpts{Name: "r"})
	tap := NewTap(testInstance, "mqtt1", failingEmitter{fmt.Errorf("the sync path must not be used")}, nil,
		Metrics{RegressedSessions: regressed}).WithAsyncEmitter(held)
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).UnixNano()
	tap.Handle(context.Background(), true, connectAdvisoryFor("d", base+int64(time.Second)))
	tap.Handle(context.Background(), true, connectAdvisoryFor("d", base))
	assert.Equal(t, 1.0, testutil.ToFloat64(regressed), "a backwards session on the async path went uncounted")
	assert.Equal(t, 2, held.pending(), "a regressed session is still emitted: the projection decides")
}
