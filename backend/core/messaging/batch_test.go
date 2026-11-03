// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/writerbatch"
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

// At the largest batch a writer may be configured with, CollectBatch takes that many
// whole: nothing between the setting and the batch trims it. A writer running at the cap
// commits twice as few transactions as one silently held to half of it, and nothing else
// about the batches it writes would show the difference.
func TestCollectBatchTakesAWholeBatchAtTheWriterCap(t *testing.T) {
	max := writerbatch.MaxSize
	ch := filled(2*max+1, true)
	for i := 0; i < 2; i++ {
		batch, open := CollectBatch(ch, max, 0, admitAll)
		if !open || len(batch) != max {
			t.Fatalf("batch %d holds %d messages open=%v; want %d, open", i, len(batch), open, max)
		}
	}
	batch, open := CollectBatch(ch, max, 0, admitAll)
	if open || len(batch) != 1 {
		t.Fatalf("last batch holds %d messages open=%v; want 1, closed", len(batch), open)
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

// ---------------------------------------------------------------------------------------
// Collector: serialized collection (CollectRaw)
//
// Every wait here is bounded, so a token that is never released turns into a failure at
// the bound and not into a hung suite.

// msgN is a message CollectRaw's tests can tell apart: its Subject is its index.
func msgN(i int) Message { return Message{Subject: strconv.Itoa(i)} }

func indexOf(m Message) int {
	n, _ := strconv.Atoi(m.Subject)
	return n
}

// Ten writers draining a trickle of one message a millisecond, each with a 10 ms linger and
// a 5 ms fake commit. With one collector the batches fill at the whole rate; each writer
// lingering on its own splits the trickle ten ways (about 1.5 a batch).
func TestCollectorGivesOneTrickleToOneBatch(t *testing.T) {
	const writers, total = 10, 400
	in := make(chan Message, 8)
	c := &Collector{}
	var batches, messages atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]Message, 0, 32)
			for {
				batch, open := CollectRaw(c, in, buf, 32, 10*time.Millisecond, nil)
				if len(batch) > 0 {
					batches.Add(1)
					messages.Add(int64(len(batch)))
					time.Sleep(5 * time.Millisecond) // the commit
				}
				if !open {
					return
				}
			}
		}()
	}
	start := time.Now()
	for i := 0; i < total; i++ {
		// An even trickle: message i is due i milliseconds in.
		if d := time.Until(start.Add(time.Duration(i) * time.Millisecond)); d > 0 {
			time.Sleep(d)
		}
		in <- msgN(i)
	}
	close(in)
	waitOrFail(t, &wg, 10*time.Second)
	if messages.Load() != total {
		t.Fatalf("collected %d of %d messages", messages.Load(), total)
	}
	mean := float64(messages.Load()) / float64(batches.Load())
	if mean < 8 {
		t.Errorf("mean batch = %.1f over %d batches; want at least 8 (one trickle, one batch)", mean, batches.Load())
	}
}

// The token is held only while a batch is RECEIVED. Writer A's commit blocks until another
// writer has collected: were the token held across A's commit, B could never collect, A's
// commit would never end, and the wait below would time out.
func TestCollectorIsNotHeldAcrossACommit(t *testing.T) {
	in := make(chan Message, 4)
	c := &Collector{}
	in <- msgN(0)
	in <- msgN(1)
	var collections atomic.Int64
	otherCollected := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]Message, 0, 1)
			batch, _ := CollectRaw(c, in, buf, 1, 0, nil)
			if len(batch) != 1 {
				t.Errorf("a writer collected %d messages; want 1", len(batch))
				return
			}
			if collections.Add(1) == 1 {
				// The "commit": ends only once the other writer has collected.
				select {
				case <-otherCollected:
				case <-time.After(5 * time.Second):
					t.Error("the commit was still waiting for another writer to collect: the token was held across it")
				}
				return
			}
			close(otherCollected)
		}()
	}
	waitOrFail(t, &wg, 10*time.Second)
}

// Closing the channel while a writer lingers releases everyone: the lingering writer
// returns what it holds with open=false, and each writer waiting for the token takes it in
// turn, sees the close and returns.
func TestCollectorReleasesOnClose(t *testing.T) {
	const writers = 6
	in := make(chan Message, 4)
	c := &Collector{}
	in <- msgN(0)
	type result struct {
		n    int
		open bool
	}
	results := make(chan result, writers)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			batch, open := CollectRaw(c, in, make([]Message, 0, 8), 8, time.Minute, nil)
			results <- result{len(batch), open}
		}()
	}
	// One writer has the message and is lingering; the rest wait for the token.
	for deadline := time.Now().Add(5 * time.Second); len(in) > 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(in)
	waitOrFail(t, &wg, 5*time.Second)
	close(results)
	held := 0
	for r := range results {
		if r.open {
			t.Errorf("a writer returned open=true after the channel closed")
		}
		held += r.n
	}
	if held != 1 {
		t.Errorf("the writers returned %d messages between them; want the 1 sent", held)
	}
}

// Because the token is held for the whole receive, every batch is a contiguous run of the
// send order, however many writers collect at once; and read in turn they are the send
// order.
func TestCollectorKeepsArrivalOrder(t *testing.T) {
	const writers, total, max = 4, 200, 7
	in := make(chan Message, total)
	for i := 0; i < total; i++ {
		in <- msgN(i)
	}
	close(in)
	c := &Collector{}
	var mu sync.Mutex
	var batches [][]int
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]Message, 0, max)
			for {
				batch, open := CollectRaw(c, in, buf, max, 0, nil)
				if len(batch) > 0 {
					idx := make([]int, len(batch))
					for i, m := range batch {
						idx[i] = indexOf(m)
					}
					mu.Lock()
					batches = append(batches, idx)
					mu.Unlock()
				}
				if !open {
					return
				}
			}
		}()
	}
	waitOrFail(t, &wg, 5*time.Second)
	sort.Slice(batches, func(i, j int) bool { return batches[i][0] < batches[j][0] })
	next := 0
	for _, b := range batches {
		for _, i := range b {
			if i != next {
				t.Fatalf("batch %v: message %d where %d was next; batches are not contiguous runs of the send order", b, i, next)
			}
			next++
		}
	}
	if next != total {
		t.Errorf("collected %d of %d messages", next, total)
	}
	if len(batches) < total/max {
		t.Errorf("%d batches; want at least %d", len(batches), total/max)
	}
}

// A writer reuses its buffer: collecting into a buffer with room costs no allocation, for a
// batch of one and for a full batch alike.
func TestCollectRawReusesItsBuffer(t *testing.T) {
	c := &Collector{}
	buf := make([]Message, 0, 32)
	received := func(Message) {}

	one := make(chan Message, 1)
	if allocs := testing.AllocsPerRun(200, func() {
		one <- msgN(1)
		batch, _ := CollectRaw(c, one, buf, 32, 0, received)
		if len(batch) != 1 {
			t.Fatalf("collected %d; want 1", len(batch))
		}
	}); allocs != 0 {
		t.Errorf("a batch of one allocated %v times; want 0", allocs)
	}

	full := make(chan Message, 32)
	m := msgN(2)
	if allocs := testing.AllocsPerRun(200, func() {
		for i := 0; i < 32; i++ {
			full <- m
		}
		batch, _ := CollectRaw(c, full, buf, 32, 10*time.Millisecond, received)
		if len(batch) != 32 {
			t.Fatalf("collected %d; want 32", len(batch))
		}
	}); allocs != 0 {
		t.Errorf("a full batch allocated %v times; want 0", allocs)
	}
}

// A nil Collector is no token at all: collection is exactly CollectBatch's, which is what a
// worker built by struct literal gets.
func TestANilCollectorIsTodaysBehaviour(t *testing.T) {
	for _, tc := range []struct {
		name   string
		n, max int
		close  bool
	}{
		{"fewer than max", 3, 8, false},
		{"more than max", 10, 4, false},
		{"closed with a partial batch", 5, 8, true},
		{"max below one", 3, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := filled(tc.n, tc.close), filled(tc.n, tc.close)
			want, wantOpen := CollectBatch(a, tc.max, 0, admitAll)
			got, gotOpen := CollectRaw(nil, b, nil, tc.max, 0, nil)
			if gotOpen != wantOpen || len(got) != len(want) {
				t.Fatalf("CollectRaw(nil) = %d messages open=%v; CollectBatch = %d open=%v",
					len(got), gotOpen, len(want), wantOpen)
			}
			for i := range got {
				if got[i].Subject != want[i] {
					t.Errorf("message %d = %q; want %q", i, got[i].Subject, want[i])
				}
			}
		})
	}
}

// received runs once per message taken, in order: it is where a writer starts its clock.
func TestCollectRawReportsEachMessageAsItIsTaken(t *testing.T) {
	in := filled(10, false)
	var seen []string
	batch, open := CollectRaw(&Collector{}, in, nil, 4, 0, func(m Message) { seen = append(seen, m.Subject) })
	if !open || len(batch) != 4 || len(seen) != 4 || seen[0] != "a" || seen[3] != "d" {
		t.Fatalf("batch = %d open=%v seen=%v; want 4 messages reported in order", len(batch), open, seen)
	}
}

// waitOrFail waits for wg, failing the test (and leaving the goroutines to the process) if
// it does not finish within d.
func waitOrFail(t *testing.T, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("writers still running after %v: a token was never released", d)
	}
}
