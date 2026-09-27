// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"
	"time"
)

// numbered is a message CollectBatch can tell apart: its Subject is its index.
func numbered(i int) Message {
	return Message{Subject: string(rune('a' + i))}
}

func filled(n int, closeIt bool) chan Message {
	ch := make(chan Message, n)
	for i := 0; i < n; i++ {
		ch <- numbered(i)
	}
	if closeIt {
		close(ch)
	}
	return ch
}

func admitAll(m Message) (string, bool) { return m.Subject, true }

// It takes what is already waiting, up to max, and leaves the rest for the next batch.
func TestCollectBatchTakesWhatIsWaitingUpToMax(t *testing.T) {
	ch := filled(10, false)
	batch, open := CollectBatch(ch, 4, 0, admitAll)
	if !open || len(batch) != 4 || batch[0] != "a" || batch[3] != "d" {
		t.Fatalf("first batch = %v open=%v; want [a b c d] open", batch, open)
	}
	batch, open = CollectBatch(ch, 32, 0, admitAll)
	if !open || len(batch) != 6 || batch[0] != "e" {
		t.Fatalf("second batch = %v open=%v; want the remaining 6 from e, open", batch, open)
	}
	if len(ch) != 0 {
		t.Errorf("%d messages left behind", len(ch))
	}
}

// A message admit refuses joins no batch and does not count toward max: with every other
// message refused, a batch of 3 needs 6 messages.
func TestCollectBatchDoesNotCountARefusedMessage(t *testing.T) {
	ch := filled(10, false)
	seen := 0
	batch, _ := CollectBatch(ch, 3, 0, func(m Message) (string, bool) {
		seen++
		return m.Subject, (seen-1)%2 == 0
	})
	if len(batch) != 3 || batch[0] != "a" || batch[1] != "c" || batch[2] != "e" {
		t.Fatalf("batch = %v; want [a c e]", batch)
	}
	if seen != 5 {
		t.Errorf("admit saw %d messages; want 5 (a..e)", seen)
	}
}

// A closed channel ends collection with open=false and hands back what it had, which the
// writer still writes — the shutdown drain.
func TestCollectBatchReturnsThePartialBatchWhenTheChannelCloses(t *testing.T) {
	batch, open := CollectBatch(filled(3, true), 8, time.Hour, admitAll)
	if open || len(batch) != 3 {
		t.Fatalf("batch = %v open=%v; want 3 messages and open=false", batch, open)
	}
	batch, open = CollectBatch(filled(0, true), 8, 0, admitAll)
	if open || batch != nil {
		t.Fatalf("drained channel: batch = %v open=%v; want nil, false", batch, open)
	}
}

// With a linger it waits for a message arriving within it; without one it returns what is
// waiting. The same arrival 20ms later: one batch of two, against a batch of one.
func TestCollectBatchLingersOnlyWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		name   string
		linger time.Duration
		want   int
	}{{"linger", 5 * time.Second, 2}, {"control: no linger", 0, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan Message, 2)
			ch <- numbered(0)
			go func() {
				time.Sleep(20 * time.Millisecond)
				ch <- numbered(1)
			}()
			batch, open := CollectBatch(ch, 2, tc.linger, admitAll)
			if !open || len(batch) != tc.want {
				t.Errorf("batch = %v open=%v; want %d messages, open", batch, open, tc.want)
			}
		})
	}
}

// A linger ends: a lone message is returned once it runs out.
func TestCollectBatchLingerIsBounded(t *testing.T) {
	ch := filled(1, false)
	start := time.Now()
	batch, open := CollectBatch(ch, 8, 50*time.Millisecond, admitAll)
	if !open || len(batch) != 1 {
		t.Fatalf("batch = %v open=%v; want one message, open", batch, open)
	}
	if waited := time.Since(start); waited < 50*time.Millisecond || waited > 5*time.Second {
		t.Errorf("returned after %v; want the 50ms linger", waited)
	}
}

// max below 1 is a batch of one, never an empty batch that would spin the writer.
func TestCollectBatchTreatsMaxBelowOneAsOne(t *testing.T) {
	for _, max := range []int{0, -3} {
		batch, open := CollectBatch(filled(3, false), max, 0, admitAll)
		if !open || len(batch) != 1 {
			t.Errorf("max %d: batch = %v open=%v; want one message", max, batch, open)
		}
	}
}
