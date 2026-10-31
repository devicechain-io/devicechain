// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"database/sql/driver"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/jackc/pgx/v5/pgconn"
)

const metricHeld = "devicechain_eventmanagement_persist_store_held_events"

// gauge reads an unlabelled gauge, or -1 when it is not registered.
func (r *batchRig) gauge(t *testing.T, name string) float64 {
	t.Helper()
	families, err := r.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name && len(f.GetMetric()) == 1 && f.GetMetric()[0].GetGauge() != nil {
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}
	return -1
}

func probeTimings(t *testing.T, min, max time.Duration) {
	t.Helper()
	omin, omax := holdTuning.ProbeMin, holdTuning.ProbeMax
	holdTuning.ProbeMin, holdTuning.ProbeMax = min, max
	t.Cleanup(func() { holdTuning.ProbeMin, holdTuning.ProbeMax = omin, omax })
}

func fastProbes(t *testing.T) { probeTimings(t, 5*time.Millisecond, 20*time.Millisecond) }

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// lastDelivery builds n messages that have already spent their whole delivery budget, so a
// disposition that consults it gives up on them.
func (r *batchRig) lastDelivery(t *testing.T, n int) []messaging.Message {
	t.Helper()
	msgs := make([]messaging.Message, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, consumed(t, r.acks, i, fenceCostTenant, messaging.MaxDeliver, batchEvent(i, true, batchT0)))
	}
	return msgs
}

// runAsync starts the worker on msgs and returns a channel closed when it finishes.
func (r *batchRig) runAsync(msgs []messaging.Message) chan struct{} {
	done := make(chan struct{})
	go func() { defer close(done); r.run(msgs) }()
	return done
}

func (r *batchRig) finishWithin(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the writer did not finish")
	}
}

// requireAllStoredOnce asserts n events stored and each acknowledged exactly once, none
// reported failed, and the held gauge back at zero.
func (r *batchRig) requireAllStoredOnce(t *testing.T, n int) {
	t.Helper()
	if e, m, _ := rowCounts(t, r.db); e != int64(n) || m != int64(3*n) {
		t.Errorf("stored %d events / %d measurements; want %d / %d", e, m, n, 3*n)
	}
	acked := r.acks.acked()
	for i := 0; i < n; i++ {
		if acked[i] != 1 {
			t.Errorf("message %d acknowledged %d times; want 1", i, acked[i])
		}
	}
	if f := r.reported(); len(f) != 0 {
		t.Errorf("reported %d failures; want none", len(f))
	}
	if g := r.gauge(t, metricHeld); g != 0 {
		t.Errorf("held gauge = %v after recovery; want 0", g)
	}
}

// THE REGRESSION THIS FILE EXISTS FOR: a store outage longer than the delivery budget used
// to turn every in-flight event into an acknowledged-then-reported-failed one. Here every
// message arrives on its LAST delivery (NumDelivered == MaxDeliver), the store refuses
// writes for a while, and nothing may be acked or reported failed until the store takes
// them -- then every message is stored and acked exactly once. Run at batch sizes 8 (the
// grouped path) and 1 (the per-message path), which hold in different places.
func TestAStoreOutageHoldsEventsEvenOnTheirLastDeliveryAndStoresThemOnRecovery(t *testing.T) {
	fastProbes(t)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"disk full, SQLSTATE 53100", &pgconn.PgError{Code: "53100", Message: "could not extend file: No space left on device"}},
		{"too many connections, SQLSTATE 53300", &pgconn.PgError{Code: "53300", Message: "too many connections"}},
		{"read-only transaction, SQLSTATE 25006", &pgconn.PgError{Code: "25006", Message: "cannot execute INSERT in a read-only transaction"}},
		{"io error, SQLSTATE 58030", &pgconn.PgError{Code: "58030", Message: "could not write block"}},
		{"connection exception, SQLSTATE 08006, and the probe fails too", &pgconn.PgError{Code: "08006", Message: "connection failure"}},
		{"server shutting down, SQLSTATE 57P01, and the probe fails too", &pgconn.PgError{Code: "57P01", Message: "admin shutdown"}},
		{"a bad connection, and the probe fails too", fmt.Errorf("injected: %w", driver.ErrBadConn)},
	} {
		for _, batch := range []int{8, 1} {
			t.Run(fmt.Sprintf("%s, batch %d", tc.name, batch), func(t *testing.T) {
				r := newBatchRig(t, batch)
				r.api.storeDown.Store(true)
				r.api.storeErr = tc.err
				done := r.runAsync(r.lastDelivery(t, 8))

				// A writer holds one batch and reads no further: batch 1 holds one event, the
				// other seven wait in the channel.
				held := float64(batch)
				waitUntil(t, "the writer to hold its batch", func() bool { return r.gauge(t, metricHeld) == held })
				txs := r.api.txs.Load()
				waitUntil(t, "probes against the down store", func() bool { return r.api.txs.Load() >= txs+3 })
				if n := len(r.acks.snapshot()); n != 0 {
					t.Fatalf("%d events acknowledged while the store was down; want none", n)
				}
				if f := r.reported(); len(f) != 0 {
					t.Fatalf("%d events reported failed while the store was down; want none", len(f))
				}

				r.api.storeDown.Store(false) // the volume was expanded
				r.finishWithin(t, done)
				r.requireAllStoredOnce(t, 8)
			})
		}
	}
}

// A STATEMENT refused for lack of space (the anchors insert), with the connection and the
// transaction fine, holds the whole batch after ONE transaction. Without the hold in
// persistBatch the refusal is blamed on a message and the batch is written again message by
// message: nine transactions, not one.
func TestAStatementRefusedForSpaceHoldsTheBatchAfterOneTransaction(t *testing.T) {
	probeTimings(t, 300*time.Millisecond, 300*time.Millisecond)
	r := newBatchRig(t, 8)
	r.api.anchorDown.Store(true)
	r.api.storeErr = &pgconn.PgError{Code: "53100", Message: "No space left on device"}
	done := r.runAsync(r.lastDelivery(t, 8))
	waitUntil(t, "the writer to hold the batch", func() bool { return r.gauge(t, metricHeld) == 8 })
	if got := r.api.txs.Load(); got != 1 {
		t.Errorf("took %d transactions to hold; want 1 (nothing in the batch is to blame)", got)
	}
	if e, _, _ := rowCounts(t, r.db); e != 0 {
		t.Errorf("%d events stored while held; want none (the transaction rolled back)", e)
	}
	r.api.anchorDown.Store(false)
	r.finishWithin(t, done)
	r.requireAllStoredOnce(t, 8)
}

// A COMMIT refused for lack of space is held the same way: every statement ran and rolled
// back, nothing is to blame, one transaction.
func TestACommitRefusedForSpaceHoldsTheBatchAfterOneTransaction(t *testing.T) {
	probeTimings(t, 300*time.Millisecond, 300*time.Millisecond)
	r := newBatchRig(t, 8)
	r.api.commitDown.Store(true)
	r.api.storeErr = &pgconn.PgError{Code: "53100", Message: "No space left on device"}
	done := r.runAsync(r.lastDelivery(t, 8))
	waitUntil(t, "the writer to hold the batch", func() bool { return r.gauge(t, metricHeld) == 8 })
	if got := r.api.txs.Load(); got != 1 {
		t.Errorf("took %d transactions to hold; want 1", got)
	}
	r.api.commitDown.Store(false)
	r.finishWithin(t, done)
	r.requireAllStoredOnce(t, 8)
}

// The store goes down WHILE the batch is being sorted out: the grouped write is refused for
// a reason that names no message (a serialization failure on a statement carrying all eight),
// so the batch is written again one message at a time, and THAT write finds the store full.
// The whole batch is held after those two transactions, not written message by message.
func TestAStoreThatFillsWhileABatchIsBeingSortedOutHoldsTheBatch(t *testing.T) {
	probeTimings(t, 300*time.Millisecond, 300*time.Millisecond)
	r := newBatchRig(t, 8)
	r.api.failAnchorsFor = "dev-3"
	r.api.anchorErrFor = &pgconn.PgError{Code: "40001", Message: "injected: could not serialize"}
	r.api.anchorFailTxLimit.Store(1)
	r.api.storeErr = &pgconn.PgError{Code: "53100", Message: "No space left on device"}
	r.api.downFromTx.Store(2)
	done := r.runAsync(r.lastDelivery(t, 8))
	waitUntil(t, "the writer to hold the batch", func() bool { return r.gauge(t, metricHeld) == 8 })
	if got := r.api.txs.Load(); got != 2 {
		t.Errorf("took %d transactions to hold; want 2 (grouped, then one at a time)", got)
	}
	r.api.downFromTx.Store(0)
	r.finishWithin(t, done)
	r.requireAllStoredOnce(t, 8)
}

// blame never names a message for a refusal that names none: the store's own refusals and
// connection-class errors blame nobody however many messages the statement carried, and
// anything else is still attributed (to the one message, or to nobody when it carried
// several).
func TestBlameNamesNoMessageForAStoreWideRefusal(t *testing.T) {
	g := &tenantGroup{}
	diskFull := &pgconn.PgError{Code: "53100"}
	for _, tc := range []struct {
		name    string
		carried []int
		err     error
		want    int
		plain   bool // the error comes back unwrapped
	}{
		{"disk full, one message carried", []int{4}, diskFull, -1, true},
		{"disk full, several carried", []int{1, 2}, diskFull, -1, true},
		{"connection failure, one message carried", []int{4}, fmt.Errorf("x: %w", driver.ErrBadConn), -1, false},
		{"control: a value error, one message carried", []int{4}, &pgconn.PgError{Code: "22003"}, 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, err := blame(g, tc.carried, tc.err)
			if at != tc.want {
				t.Errorf("blamed %d; want %d", at, tc.want)
			}
			if err == nil {
				t.Fatal("blame returned no error")
			}
			if tc.plain && err != tc.err {
				t.Errorf("the error was wrapped: %v", err)
			}
		})
	}
	if at, err := blame(g, []int{1, 2}, &pgconn.PgError{Code: "40001"}); at != -1 || err == nil {
		t.Errorf("control: several carried, not a store refusal: blamed %d, err %v; want -1 and the unattributed error", at, err)
	}
}

// THE DISTINCTION M2 EXISTS FOR: a connection-class error is only an outage if the store
// does not answer. Here one message's statement fails with "admin shutdown" every time -- a
// payload that crashes its backend looks like this -- while the store answers its probe. The
// message is the problem, so it takes the delivery budget: on its last delivery it is
// reported and acked, its batch-mates are stored, and nothing is held.
func TestAPoisonMessageWhoseErrorLooksLikeALostConnectionIsNotHeldWhileTheStoreAnswers(t *testing.T) {
	fastProbes(t)
	for name, e := range map[string]error{
		"admin shutdown, SQLSTATE 57P01": &pgconn.PgError{Code: "57P01", Message: "injected: admin shutdown"},
		"connection exception, 08006":    &pgconn.PgError{Code: "08006", Message: "injected: connection failure"},
		"a bad connection":               fmt.Errorf("injected: %w", driver.ErrBadConn),
	} {
		t.Run(name, func(t *testing.T) {
			r := newBatchRig(t, 8)
			r.api.failAnchorsFor = "dev-3"
			r.api.anchorErrFor = e
			var sawHeld atomic.Bool
			stop := make(chan struct{})
			go func() {
				for {
					select {
					case <-stop:
						return
					default:
						if r.gauge(t, metricHeld) > 0 {
							sawHeld.Store(true)
						}
						time.Sleep(time.Millisecond)
					}
				}
			}()
			defer close(stop)
			done := r.runAsync(r.lastDelivery(t, 8))
			r.finishWithin(t, done)
			if sawHeld.Load() {
				t.Error("events were held although the store answered its probe")
			}
			acked := r.acks.acked()
			for i := 0; i < 8; i++ {
				if acked[i] != 1 {
					t.Errorf("message %d acknowledged %d times; want 1 (stored, or given up on)", i, acked[i])
				}
			}
			if e, _, _ := rowCounts(t, r.db); e != 7 {
				t.Errorf("stored %d events; want 7", e)
			}
			if f := r.reported(); len(f) != 1 || f[0].device != "dev-3" {
				t.Errorf("reported %v; want exactly dev-3", f)
			}
		})
	}
}

// The control: a failure that is NOT a store outage still gives up on the last delivery,
// reporting the event and acking it, as before.
func TestAnErrorThatIsNotAStoreOutageStillGivesUpOnTheLastDelivery(t *testing.T) {
	fastProbes(t)
	r := newBatchRig(t, 1)
	r.api.storeDown.Store(true)
	r.api.storeErr = &pgconn.PgError{Code: "40001", Message: "injected: could not serialize"}
	r.run([]messaging.Message{consumed(t, r.acks, 0, fenceCostTenant, messaging.MaxDeliver, batchEvent(0, true, batchT0))})
	if got := r.acks.acked()[0]; got != 1 {
		t.Errorf("acknowledged %d times; want 1 (given up on at the last delivery)", got)
	}
	if f := r.reported(); len(f) != 1 {
		t.Errorf("reported %d failures; want 1", len(f))
	}
}

// Shutdown must not hang behind a store that never recovers: closing Halt ends the hold,
// leaving the events UNACKED (redelivered to the next pod) and unreported.
func TestHaltEndsTheHoldAndLeavesEventsUnacked(t *testing.T) {
	fastProbes(t)
	r := newBatchRig(t, 4)
	r.api.storeDown.Store(true)
	r.api.storeErr = &pgconn.PgError{Code: "53100", Message: "No space left on device"}
	halt := make(chan struct{})
	r.ep.Halt = halt
	msgs := make([]messaging.Message, 0, 4)
	for i := 0; i < 4; i++ {
		msgs = append(msgs, consumed(t, r.acks, i, fenceCostTenant, 1, batchEvent(i, true, batchT0)))
	}
	done := r.runAsync(msgs)
	waitUntil(t, "the writer to hold the batch", func() bool { return r.gauge(t, metricHeld) == 4 })
	close(halt)
	r.finishWithin(t, done)
	if n := len(r.acks.snapshot()); n != 0 {
		t.Errorf("%d events acknowledged on halt; want none", n)
	}
	if f := r.reported(); len(f) != 0 {
		t.Errorf("%d events reported failed on halt; want none", len(f))
	}
	if g := r.gauge(t, metricHeld); g != 0 {
		t.Errorf("held gauge = %v after halt; want 0", g)
	}
}

// progressAck is a recording acknowledger that also takes progress beats.
type progressAck struct {
	recordingAck
	mu    *sync.Mutex
	beats *[]time.Time
}

func (p progressAck) InProgress() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	*p.beats = append(*p.beats, time.Now())
	return nil
}

// THE HEARTBEAT: a held message is told it is in progress AT ONCE, then whenever a third of
// AckWait (here scaled down) has passed, through every backoff wait and every probe, so its
// ack-wait never lapses and no delivery is spent. The gauge never dips to zero in between.
func TestHeldEventsAreToldTheyAreInProgressAtOnceAndOnCadence(t *testing.T) {
	// Waits (200-400 ms) much longer than the beat interval (60 ms): the old ticker-per-wait
	// would have sent nothing for the whole of a wait.
	probeTimings(t, 200*time.Millisecond, 400*time.Millisecond)
	every := holdTuning.ProgressEvery
	holdTuning.ProgressEvery = 60 * time.Millisecond
	t.Cleanup(func() { holdTuning.ProgressEvery = every })

	r := newBatchRig(t, 4)
	r.api.storeDown.Store(true)
	r.api.storeErr = &pgconn.PgError{Code: "53100", Message: "No space left on device"}
	var mu sync.Mutex
	beats := make([][]time.Time, 4)
	msgs := make([]messaging.Message, 0, 4)
	for i := 0; i < 4; i++ {
		ev := batchEvent(i, true, batchT0)
		bytes, err := resolvedevent.MarshalResolvedEvent(&ev)
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, messaging.NewConsumedMessage("instance1."+fenceCostTenant+".resolved-events", bytes, 1, nil,
			progressAck{recordingAck: recordingAck{log: r.acks, idx: i}, mu: &mu, beats: &beats[i]}))
	}

	var zeroed atomic.Bool
	stop := make(chan struct{})
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		seen := false
		for {
			select {
			case <-stop:
				return
			default:
				g := r.gauge(t, metricHeld)
				if g > 0 {
					seen = true
				} else if seen && g == 0 {
					zeroed.Store(true)
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()
	var stopOnce sync.Once
	stopPoller := func() { stopOnce.Do(func() { close(stop); <-pollDone }) }
	defer stopPoller()

	start := time.Now()
	done := r.runAsync(msgs)
	waitUntil(t, "the writer to hold the batch", func() bool { return r.gauge(t, metricHeld) == 4 })
	holdAt := time.Since(start)
	time.Sleep(900 * time.Millisecond) // at least two probes, all of them failing
	mu.Lock()
	snap := make([][]time.Time, len(beats))
	for i := range beats {
		snap[i] = append([]time.Time(nil), beats[i]...)
	}
	mu.Unlock()
	// Stop watching the gauge BEFORE the store recovers: its legitimate fall to 0 on recovery
	// is not "dropped to 0 during a probe", and a poller still running would call it one.
	stopPoller()
	r.api.storeDown.Store(false)
	r.finishWithin(t, done)

	for i, bs := range snap {
		if len(bs) < 8 {
			t.Errorf("message %d: %d beats in about 900 ms at one per 60 ms; want at least 8", i, len(bs))
			continue
		}
		if first := bs[0].Sub(start); first > holdAt+30*time.Millisecond {
			t.Errorf("message %d: first beat %v after the start, hold was at %v; it must be sent at once", i, first, holdAt)
		}
		for j := 1; j < len(bs); j++ {
			if gap := bs[j].Sub(bs[j-1]); gap > 150*time.Millisecond {
				t.Errorf("message %d: gap of %v between beats %d and %d; want at most about 60 ms", i, gap, j-1, j)
			}
		}
	}
	if zeroed.Load() {
		t.Error("the held gauge dropped to 0 during a probe")
	}
	r.requireAllStoredOnce(t, 4)
}
