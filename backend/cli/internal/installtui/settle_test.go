// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"testing"
	"time"
)

// fakeKeys is an input clock the test moves by hand, stamped as the stdin gate's reader
// stamps it: "read" is a key read from the terminal, whatever the event loop is doing.
type fakeKeys struct {
	at    time.Duration
	clock *inputClock
}

func newFakeKeys() *fakeKeys {
	f := &fakeKeys{at: time.Hour}
	f.clock = &inputClock{readerStamps: true, now: func() time.Duration { return f.at }}
	return f
}

// advance moves the clock on by d.
func (f *fakeKeys) advance(d time.Duration) { f.at += d }

// read records n keys read now by the terminal's reader.
func (f *fakeKeys) read() { f.clock.stamp() }

// 🔴 A KEY READ AT THE CLOCK'S VERY FIRST INSTANT IS NOT IN THE FUTURE. A coarse monotonic
// clock (a Windows runner's) can read 0 since the clock's base for a while; a stamp taken
// then must read back as 0, not as a moment after "now", or no quiet period ever passes
// since it and the screen never settles. This is the failure the tui-windows job hit:
// the hand-over, stamped by the key handler on a Windows console, never armed.
func TestAStampAtTheClocksFirstInstantIsNotInTheFuture(t *testing.T) {
	old, oldPoll := reviewSettle, settlePoll
	t.Cleanup(func() { reviewSettle, settlePoll = old, oldPoll })
	reviewSettle, settlePoll = 0, 0
	for _, readerStamps := range []bool{false, true} {
		c := &inputClock{readerStamps: readerStamps, now: func() time.Duration { return 0 }}
		c.stamp()
		if last, ok := c.lastInput(); !ok || last != 0 {
			t.Fatalf("a stamp at 0 reads back %v (%v)", last, ok)
		}
		g := settleGate{clock: c}
		ask := g.enter()
		if ask == nil {
			t.Fatal("no settle ask")
		}
		c.stamp() // a key, at the same instant
		if g.settled(settleMsg{gen: g.gen, token: g.token}); !g.armed {
			t.Fatalf("readerStamps=%v: a zero settle period did not arm with every reading at 0", readerStamps)
		}
		if !readerStamps {
			if ok, _ := g.admit(); !ok {
				t.Fatal("a key at the arming's instant was refused")
			}
		}
	}
}
