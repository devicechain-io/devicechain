// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// These tests pin the bucket's own clock: every admission is charged at
// at = min(max(when, mark), now), and a retune, a creation and a cancelled wait all
// happen on that same clock. Each drives the limiter through its public entry points
// with a frozen l.now, and asserts how many admissions came out.

// frozenAt returns a limiter over a constant ceiling whose clock is frozen at now.
func frozenAt(now time.Time, rps float64, burst int) *TenantRateLimiter {
	l := NewTenantRateLimiter(constLimit(rps, burst))
	l.now = func() time.Time { return now }
	return l
}

// A redelivered (or otherwise re-sent) tail of a drain carries OLDER times than the
// bucket has already been charged at. Fed to the token bucket directly, each one
// rewinds its clock and the next forward step re-accrues the gap — so the whole tail
// is admitted a second time. Charged at the mark it finds the tokens already spent.
func TestAllowAtNeverRewindsTheBucket(t *testing.T) {
	const rps, burst = 50, 100
	base := time.Unix(1_000_000, 0)
	l := frozenAt(base.Add(61*time.Second), rps, burst)

	sent := func(i int) time.Time { return base.Add(time.Duration(i) * 20 * time.Millisecond) }
	for i := 0; i < 3000; i++ { // 60 s at exactly 50/s: compliant, all admitted
		if !l.AllowAt("acme", sent(i)) {
			t.Fatalf("a compliant drain was shed at message %d", i)
		}
	}
	readmitted := 0
	for i := 2500; i < 3000; i++ { // the last 500 again, at their original times
		if l.AllowAt("acme", sent(i)) {
			readmitted++
		}
	}
	// Charged at the mark (the last send time), only the tokens the compliant stream left
	// in the bucket remain: at most one burst. Unclamped, all 500 are re-admitted.
	if readmitted > burst {
		t.Errorf("re-sent tail admitted %d of 500; at most one burst (%d) may be", readmitted, burst)
	}
}

// A ceiling that changes while a backlog drains on send times. Retuned at NOW, each
// change jumps the bucket's clock forward and refills it, and the next (older)
// admission spends the refill — a burst minted per change. Retuned at the admission
// time, the change mints nothing.
func TestRetuneMidDrainDoesNotMint(t *testing.T) {
	const burst = 100
	base := time.Unix(1_000_000, 0)
	calls := 0
	l := NewTenantRateLimiter(func(string) (float64, int) {
		// 200 messages per second of send time; the ceiling flips once per second.
		calls++
		if (calls/200)%2 == 0 {
			return 100, burst
		}
		return 101, burst
	})
	now := base.Add(time.Hour)
	l.now = func() time.Time { return now }

	admitted := 0
	for i := 0; i < 12000; i++ { // a 2x flood: 200/s of send time for 60 s
		if l.AllowAt("acme", base.Add(time.Duration(i)*5*time.Millisecond)) {
			admitted++
		}
	}
	// The budget is one burst plus the rate over the 60 s span: between 100+100·60 and
	// 100+101·60. Retuned at now, every flip mints a burst and the flood passes whole.
	if admitted < burst+100*60-50 || admitted > burst+101*60+1 {
		t.Errorf("2x flood across ceiling flips admitted %d; want within [%d, %d]",
			admitted, burst+100*60-50, burst+101*60+1)
	}
}

// Two processing-time timelines ten minutes apart, interleaved on one tenant. Once the
// later timeline has been seen, every earlier-timeline admission is charged at the mark,
// so the pair together admit what one timeline over its own span would, plus the one
// burst the earlier timeline had before the later one first arrived.
func TestTheMarkBoundsInterleavedTimelines(t *testing.T) {
	const rps, burst = 10, 10
	early := time.Unix(1_000_000, 0)
	late := early.Add(10 * time.Minute)
	l := frozenAt(late.Add(time.Minute), rps, burst)

	admitted := 0
	for i := 0; i < 1000; i++ { // each timeline floods at 1000/s for 1 s
		step := time.Duration(i) * time.Millisecond
		if l.AllowAt("acme", early.Add(step)) {
			admitted++
		}
		if l.AllowAt("acme", late.Add(step)) {
			admitted++
		}
	}
	// burst (early, before the jump) + burst + rps·1 s (the late timeline's span).
	bound := burst + burst + rps*1 + 1
	if admitted > bound {
		t.Errorf("interleaved timelines admitted %d of 2000; bound is %d", admitted, bound)
	}
}

// What the mark does NOT protect against, pinned so it is not mistaken for a bug: an
// admission far in the past followed by one at now accrues the whole gap, capped at one
// burst. The bucket's clock moved forward, which is exactly what it is for.
func TestAFarPastAdmissionThenNowAdmitsAtMostOneBurst(t *testing.T) {
	const burst = 20
	now := time.Unix(1_000_000, 0)
	l := frozenAt(now, 1, burst)

	if !l.AllowAt("acme", now.Add(-time.Hour)) {
		t.Fatal("a fresh bucket must admit")
	}
	admitted := 0
	for i := 0; i < 500; i++ {
		if l.AllowAt("acme", now) {
			admitted++
		}
	}
	if admitted != burst {
		t.Errorf("a now-dated flood after a far-past admission admitted %d; want exactly one burst (%d)",
			admitted, burst)
	}
}

// A future when is charged at now: once the burst is spent on future-dated admissions,
// nothing more is admitted until real accrual at now provides it.
func TestAFutureWhenIsChargedAtNow(t *testing.T) {
	const burst = 5
	now := time.Unix(1_000_000, 0)
	l := NewTenantRateLimiter(constLimit(1, burst))
	l.now = func() time.Time { return now }

	admitted := 0
	for i := 0; i < 50; i++ {
		if l.AllowAt("acme", now.Add(time.Duration(i+1)*time.Hour)) {
			admitted++
		}
	}
	if admitted != burst {
		t.Fatalf("future-dated admissions admitted %d; want the burst (%d)", admitted, burst)
	}
	if l.AllowAt("acme", now) {
		t.Error("the bucket was charged past now: a now admission found a token")
	}
	now = now.Add(time.Second)
	if !l.AllowAt("acme", time.Time{}) || l.AllowAt("acme", time.Time{}) {
		t.Error("one second at 1/s must accrue exactly one token")
	}
}

// admitTimeLocked is the one place the policy lives, so its order is pinned directly:
// the clamp to now is applied LAST. That matters only when the mark is ahead of now —
// the wall clock stepped back — and then the admission is charged at now rather than at
// a time that has not happened yet.
func TestAnOlderWhenAfterAFutureOneIsChargedAtNow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cases := []struct {
		name            string
		when, mark, exp time.Time
	}{
		{"zero when is now", time.Time{}, now.Add(-time.Minute), now},
		{"past when behind the mark is charged at the mark", now.Add(-time.Hour), now.Add(-time.Minute), now.Add(-time.Minute)},
		{"past when ahead of the mark is its own time", now.Add(-time.Second), now.Add(-time.Minute), now.Add(-time.Second)},
		{"future when is now", now.Add(time.Hour), now.Add(-time.Minute), now},
		{"older when after a mark ahead of now is now", now.Add(-time.Hour), now.Add(time.Minute), now},
		{"zero when after a mark ahead of now is now", time.Time{}, now.Add(time.Minute), now},
	}
	for _, c := range cases {
		b := &tenantBucket{mark: c.mark}
		if got := admitTimeLocked(b, c.when, now); !got.Equal(c.exp) {
			t.Errorf("%s: at = %v, want %v", c.name, got, c.exp)
		}
		if !b.mark.Equal(c.mark) {
			t.Errorf("%s: admitTimeLocked moved the mark", c.name)
		}
	}

	// And through the public path: a future admission leaves the mark at now, so an older
	// one that follows is charged there, not at its own (earlier) time.
	l := frozenAt(now, 1, 1)
	if !l.AllowAt("acme", now.Add(time.Hour)) {
		t.Fatal("a fresh bucket must admit")
	}
	if l.AllowAt("acme", now.Add(-time.Hour)) {
		t.Error("an older when after a future one found a token: it was charged behind now")
	}
}

// deadlineSignal is a context that reports when WaitAt has taken its admission
// decision: WaitAt reads the deadline only after it has reserved, under the lock. Its
// deadline is a fixed instant on the limiter's (frozen) clock and it is never Done, so
// the budget decision is deterministic and an admitted wait is never cut short by the
// real time the test itself takes (under -race, a thousand decisions take long enough
// to matter).
type deadlineSignal struct {
	context.Context
	deadline time.Time
	once     sync.Once
	read     chan struct{}
}

func (c *deadlineSignal) Deadline() (time.Time, bool) {
	c.once.Do(func() { close(c.read) })
	return c.deadline, true
}

// A backlog the tenant produced at exactly its ceiling, admitted through WaitAt on its
// own timeline, passes at drain speed: every event's token accrued long ago on the
// `when` timeline, so none waits. Metered at now (Wait), the same backlog is paced out
// at the ceiling — ten seconds for these thousand events.
func TestWaitAtPassesACompliantBacklogWithoutWaiting(t *testing.T) {
	l := NewTenantRateLimiter(constLimit(100, 10))
	end := time.Now()
	start := time.Now()
	for i := 0; i < 1000; i++ {
		when := end.Add(-time.Duration(999-i) * 10 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := l.WaitAt(ctx, "acme", when)
		cancel()
		if err != nil {
			t.Fatalf("compliant backlog event %d refused: %v", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a compliant backlog took %v to admit; it must pass at drain speed", elapsed)
	}
}

// A flood metered on its own timeline is shed exactly as it would have been live: over a
// 1 s window of trigger times with a 5 s wait budget, the admissions are one burst plus
// what the rate provides over the window and the budget together. Everything else is
// refused with ErrWaitBudget, and takes no token.
func TestWaitAtShedsAFloodAsLive(t *testing.T) {
	const rps, burst = 100, 10
	now := time.Now()
	l := NewTenantRateLimiter(constLimit(rps, burst))
	l.now = func() time.Time { return now }

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
		budget   int
		other    []error
	)
	for i := 0; i < 1000; i++ {
		when := now.Add(-time.Second + time.Duration(i)*time.Millisecond)
		ctx := &deadlineSignal{Context: context.Background(), deadline: now.Add(5 * time.Second),
			read: make(chan struct{})}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := l.WaitAt(ctx, "acme", when)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				admitted++
			case errors.Is(err, ErrWaitBudget):
				budget++
			default:
				other = append(other, err)
			}
		}()
		<-ctx.read // one decision at a time, in trigger-time order
	}
	wg.Wait()

	want := burst + rps*6 // the 1 s window plus the 5 s budget
	if admitted < want-2 || admitted > want+2 {
		t.Errorf("flood admitted %d; want %d ± 2", admitted, want)
	}
	if budget != 1000-admitted || len(other) != 0 {
		first := error(nil)
		if len(other) > 0 {
			first = other[0]
		}
		t.Errorf("sheds: %d ErrWaitBudget and %d other errors (first: %v); want %d budget refusals and nothing else",
			budget, len(other), first, 1000-admitted)
	}
	if !errors.Is(ErrWaitBudget, context.DeadlineExceeded) {
		t.Error("ErrWaitBudget must wrap context.DeadlineExceeded")
	}
}

// An interrupted wait returns its token on the bucket's own timeline. Returned at the
// wall clock (Reservation.Cancel is CancelAt(time.Now())), the limiter's clock is moved
// to wherever the wall is. Here the trigger timeline runs an hour AHEAD of the wall, so
// that move is an hour backwards, and the next admission on the trigger timeline
// re-accrues the hour: a full burst minted from a drained bucket.
func TestWaitAtCancelRestoresOnTheTriggerTimeline(t *testing.T) {
	const burst = 10
	frozen := time.Now().Add(time.Hour) // the limiter's clock, an hour ahead of the wall
	l := NewTenantRateLimiter(constLimit(1, burst))
	l.now = func() time.Time { return frozen }

	for i := 0; i < burst; i++ {
		if !l.AllowAt("acme", frozen) {
			t.Fatalf("burst admission %d refused", i)
		}
	}
	// The deadline must leave room on the limiter's clock for the 1 s the token needs.
	ctx, cancel := context.WithDeadline(context.Background(), frozen.Add(time.Hour))
	done := make(chan error, 1)
	go func() { done <- l.WaitAt(ctx, "acme", frozen) }() // needs 1 s for its token
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("an interrupted wait must return ctx's error, got %v", err)
	}

	minted := 0
	for i := 0; i < 100; i++ {
		if l.AllowAt("acme", frozen) {
			minted++
		}
	}
	if minted != 0 {
		t.Errorf("after a cancelled wait, %d admissions found tokens on the drained timeline; want 0", minted)
	}
}
