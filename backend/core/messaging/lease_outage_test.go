// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// These tests put a lease through the broker behaviours a rolling or single-server
// broker restart produces, against a real embedded server:
//
//   - "connected, JetStream gone". nats-server's lame-duck mode shuts JetStream down
//     while clients are still connected. srv.ShutdownJetStream reproduces exactly that
//     (jetStreamOff). It is used only where JetStream does not come back in the test:
//     srv.EnableJetStream races the server's own API dispatch for any client request
//     in flight (the race detector reports it inside nats-server), and a retrying
//     release is a request in flight by design.
//   - "the server restarted". down stops the server and up starts a new one on the same
//     port and store directory, which brings the lease bucket back with its entries
//     (pinned by TestARestartKeepsTheLeaseBucket, which every verdict below depends
//     on). The clients are connected with ReconnectBufSize(-1), so a write made while
//     the server is down fails at once and is never buffered and flushed on
//     reconnect: with the default buffer, a single attempt made during the outage
//     would land on reconnect after its caller gave up on it, and a one-attempt release
//     would pass the very test meant to show it does not wait for the broker.
//   - "applied, reply lost". A write lands on the server and the client never sees the
//     acknowledgement. An interposer that applies the real write and then returns
//     nats.ErrTimeout reproduces it.
//
// The one exception is TestReleaseWithinDoesNotRetryOnceItsWindowClosesDuringTheBackoff,
// which runs against downKV, a broker-free fake that never answers a delete: it needs a
// validity window that closes within one backoff, and a real broker cannot be made to
// time a 20 ms window out deterministically.

// outageBroker is one embedded server that a test can stop and start again on the
// same port and store directory.
type outageBroker struct {
	srv     *natsserver.Server
	port    int
	dir     string
	clients []*nats.Conn
}

func (b *outageBroker) start(t *testing.T) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      b.port,
		JetStream: true,
		StoreDir:  b.dir,
		NoLog:     true,
		NoSigs:    true,
	})
	if err != nil {
		t.Fatalf("new embedded nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	b.srv = srv
	b.port = srv.Addr().(*net.TCPAddr).Port
}

// down stops the server and returns once every client has seen the disconnect. Until
// a client has, a write it makes goes into a socket nobody reads and waits out the
// full API timeout, instead of failing at once the way a write to a server that is
// known to be down does.
func (b *outageBroker) down(t *testing.T) {
	t.Helper()
	b.srv.Shutdown()
	b.srv.WaitForShutdown()
	deadline := time.Now().Add(5 * time.Second)
	for _, nc := range b.clients {
		for nc.IsConnected() {
			if time.Now().After(deadline) {
				t.Fatal("a client did not see the server stop")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// up starts the server again, on the same port and store directory.
func (b *outageBroker) up(t *testing.T) {
	t.Helper()
	b.start(t)
}

// kvOn opens the lease bucket on a fresh connection.
func (b *outageBroker) kvOn(t *testing.T) nats.KeyValue {
	t.Helper()
	dl, err := newManagerOn(t, b).NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	return dl.kv
}

func (b *outageBroker) url() string { return fmt.Sprintf("nats://127.0.0.1:%d", b.port) }

// newJetStreamToggleManager starts an outageBroker and connects a manager to it, the
// way newTestManager does.
func newJetStreamToggleManager(t *testing.T) (*NatsManager, *outageBroker) {
	t.Helper()
	b := &outageBroker{dir: dctest.JetStreamStoreDir(t)}
	b.start(t)
	t.Cleanup(func() { b.srv.Shutdown() })
	return newManagerOn(t, b), b
}

// newManagerOn connects a fresh client — a second replica — to the broker. It never
// buffers a write while disconnected (see the file comment) and reconnects quickly.
func newManagerOn(t *testing.T, b *outageBroker) *NatsManager {
	t.Helper()
	nc, err := nats.Connect(b.url(), nats.ReconnectBufSize(-1), nats.MaxReconnects(-1),
		nats.ReconnectWait(50*time.Millisecond))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	b.clients = append(b.clients, nc)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	return &NatsManager{
		Microservice: &core.Microservice{InstanceId: "test", FunctionalArea: "area"},
		nc:           nc,
		js:           js,
	}
}

// jetStreamOff takes JetStream away with the clients still connected: lame-duck mode.
// It is never undone in a test (see the file comment).
func jetStreamOff(t *testing.T, b *outageBroker) {
	t.Helper()
	if err := b.srv.ShutdownJetStream(); err != nil {
		t.Fatalf("ShutdownJetStream: %v", err)
	}
}

// successorView asks what a second replica, on its own connection, sees for the
// partition once the broker answers: whether the prior owner released cleanly (the
// question that decides the handover wait) and what its Acquire returns. It retries
// the pair briefly while JetStream finishes recovering its streams, and only on a
// broker error — never on an answer.
func successorView(t *testing.T, b *outageBroker, partition string) (clean bool, acquireErr error) {
	t.Helper()
	nmgr := newManagerOn(t, b)
	deadline := time.Now().Add(10 * time.Second)
	for {
		dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
		if err == nil {
			clean = dl.PriorOwnerReleasedCleanly(partition)
			_, acquireErr = dl.Acquire(partition)
			if acquireErr == nil || errors.Is(acquireErr, ErrLeaseHeld) {
				return clean, acquireErr
			}
			err = acquireErr
		}
		if time.Now().After(deadline) {
			t.Fatalf("the successor could not reach the lease bucket: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestARestartKeepsTheLeaseBucket is the NO-OP control the outage tests stand on. If
// stopping the server and starting it again lost the bucket or its entry, a successor
// would acquire after ANY release attempt, landed or not, and every "successor
// acquired" verdict below would be worthless.
func TestARestartKeepsTheLeaseBucket(t *testing.T) {
	nmgr, b := newJetStreamToggleManager(t)
	dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	b.down(t)
	b.up(t)

	clean, acquireErr := successorView(t, b, "detect:p")
	if !errors.Is(acquireErr, ErrLeaseHeld) {
		t.Fatalf("successor Acquire after a server restart with nothing released = %v, want ErrLeaseHeld: "+
			"the restart lost the entry, so no outage verdict in this file can be trusted", acquireErr)
	}
	if clean {
		t.Fatal("PriorOwnerReleasedCleanly = true with nothing released")
	}
	// Read on the successor's connection, which is up: the leader's may still be
	// reconnecting.
	entry, err := b.kvOn(t).Get(lease.key)
	if err != nil || string(entry.Value()) != lease.holder {
		t.Fatalf("lease entry after the restart = (%v, %v), want the holder %q", entry, err, lease.holder)
	}
}

// TestReleaseWithinLandsWhenTheBrokerAnswersAgain is the release a stopping owner
// makes while the broker is briefly down. One attempt (Release, which is
// what DETECT used to make) fails and leaves the entry to expire, so the successor
// reads ErrLeaseHeld and, once the entry expires, pays its handover wait.
// ReleaseWithin keeps trying until the broker answers.
func TestReleaseWithinLandsWhenTheBrokerAnswersAgain(t *testing.T) {
	nmgr, b := newJetStreamToggleManager(t)
	dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	b.down(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- lease.ReleaseWithin(ctx) }()

	time.Sleep(1500 * time.Millisecond)
	b.up(t)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReleaseWithin across a 1.5s broker outage = %v, want nil", err)
		}
		if el := time.Since(start); el > 8*time.Second {
			t.Fatalf("ReleaseWithin took %v, want it to land shortly after the broker answered", el)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("ReleaseWithin did not return")
	}

	clean, acquireErr := successorView(t, b, "detect:p")
	if acquireErr != nil {
		t.Fatalf("successor Acquire = %v, want nil: the release did not land", acquireErr)
	}
	if !clean {
		t.Fatal("PriorOwnerReleasedCleanly = false, want true: the successor would wait out its handover period")
	}
}

// TestReleaseWithinGivesUpAtItsDeadline is the negative control: an outage that
// outlasts the caller's budget ends the call at the budget, and nothing is deleted
// late — the successor still reads the hold until it expires.
func TestReleaseWithinGivesUpAtItsDeadline(t *testing.T) {
	nmgr, b := newJetStreamToggleManager(t)
	dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	b.down(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	err = lease.ReleaseWithin(ctx)
	el := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReleaseWithin past its deadline = %v, want an error wrapping context.DeadlineExceeded", err)
	}
	if el > 7*time.Second {
		t.Fatalf("ReleaseWithin returned after %v, want within its 1s budget plus one API timeout", el)
	}

	time.Sleep(time.Second)
	b.up(t)
	if _, acquireErr := successorView(t, b, "detect:p"); !errors.Is(acquireErr, ErrLeaseHeld) {
		t.Fatalf("successor Acquire after a release that gave up = %v, want ErrLeaseHeld", acquireErr)
	}
}

// countingKV counts the calls a lease makes. Every call goes to the real store.
type countingKV struct {
	nats.KeyValue
	deletes atomic.Int32
	gets    atomic.Int32
}

func (c *countingKV) Delete(key string, opts ...nats.DeleteOpt) error {
	c.deletes.Add(1)
	return c.KeyValue.Delete(key, opts...)
}

func (c *countingKV) Get(key string) (nats.KeyValueEntry, error) {
	c.gets.Add(1)
	return c.KeyValue.Get(key)
}

// TestReleaseWithinDoesNotRetryPastAnEndedWindow pins the window as the bound on the
// RETRIES, and only on them. Past the window the server's entry has expired or is
// about to, so retrying would only hold up a shutdown — but the first attempt is still
// made, because an entry of ours can outlive our window (a renewal that landed with
// its reply lost leaves the server's copy fresher than our lastRenew).
func TestReleaseWithinDoesNotRetryPastAnEndedWindow(t *testing.T) {
	t.Run("broker down: one attempt, then it stops", func(t *testing.T) {
		nmgr, b := newJetStreamToggleManager(t)
		dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
		if err != nil {
			t.Fatalf("NewDistributedLease: %v", err)
		}
		lease, err := dl.Acquire("detect:p")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		counter := &countingKV{KeyValue: lease.kv}
		lease.kv = counter
		lease.mu.Lock()
		lease.lastRenew = time.Now().Add(-DefaultLeaseTTL - time.Second)
		lease.mu.Unlock()
		jetStreamOff(t, b)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		start := time.Now()
		err = lease.ReleaseWithin(ctx)
		el := time.Since(start)
		if !errors.Is(err, ErrNotHolder) {
			t.Fatalf("ReleaseWithin with its window ended and the broker down = %v, want ErrNotHolder", err)
		}
		if n := counter.deletes.Load(); n != 1 {
			t.Fatalf("delete attempts = %d, want exactly 1: the first is unconditional and none follows "+
				"once the window has closed", n)
		}
		if el > 3*time.Second {
			t.Fatalf("ReleaseWithin took %v with its window ended, want one attempt's worth", el)
		}
	})
	t.Run("broker up: the one attempt still deletes our entry", func(t *testing.T) {
		nmgr, b := newJetStreamToggleManager(t)
		dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
		if err != nil {
			t.Fatalf("NewDistributedLease: %v", err)
		}
		lease, err := dl.Acquire("detect:p")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		counter := &countingKV{KeyValue: lease.kv}
		lease.kv = counter
		lease.mu.Lock()
		lease.lastRenew = time.Now().Add(-DefaultLeaseTTL - time.Second)
		lease.mu.Unlock()

		if err := lease.ReleaseWithin(context.Background()); err != nil {
			t.Fatalf("ReleaseWithin with its window ended and our entry still on the server = %v, want nil", err)
		}
		if n := counter.deletes.Load(); n != 1 {
			t.Fatalf("delete attempts = %d, want 1", n)
		}
		if clean, acquireErr := successorView(t, b, "detect:p"); acquireErr != nil || !clean {
			t.Fatalf("successor = (clean %v, Acquire %v), want (true, nil)", clean, acquireErr)
		}
	})
}

// downKV is a broker that never answers a delete. It records when each attempt
// started, so a test can tell whether an attempt began after a bound had closed.
type downKV struct {
	nats.KeyValue // nil: any call other than Delete panics, which is the point
	mu            sync.Mutex
	starts        []time.Time
}

func (k *downKV) Delete(string, ...nats.DeleteOpt) error {
	k.mu.Lock()
	k.starts = append(k.starts, time.Now())
	k.mu.Unlock()
	return nats.ErrTimeout
}

func (k *downKV) attempts() []time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]time.Time(nil), k.starts...)
}

// releasingLease is a held lease over kv whose window opened at lastRenew. rev is
// non-zero only so the literal looks like a real acquisition: downKV ignores it, and
// its ErrTimeout is not a refused CAS, so ownRevision (and the nil embedded KeyValue
// behind it) is never reached.
func releasingLease(kv nats.KeyValue, ttl time.Duration, lastRenew time.Time) *Lease {
	return &Lease{kv: kv, key: "detect:p", holder: "h", ttl: ttl, rev: 1, lastRenew: lastRenew}
}

// TestReleaseWithinDoesNotRetryOnceItsWindowClosesDuringTheBackoff pins the bounds as
// checked AFTER each backoff, immediately before the retry starts, and the backoff
// itself as ending when the window does. Before, both bounds were checked only before
// the backoff, so a window that closed during it still got one more delete — with the
// broker down, one more API timeout spent after the lease had expired.
func TestReleaseWithinDoesNotRetryOnceItsWindowClosesDuringTheBackoff(t *testing.T) {
	t.Run("window closes inside the first backoff: no second attempt", func(t *testing.T) {
		start := time.Now()
		const ttl = 20 * time.Millisecond // less than releaseRetryMin, so it closes mid-backoff
		windowEnd := start.Add(ttl)
		kv := &downKV{}
		lease := releasingLease(kv, ttl, start)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // ctx stays live
		defer cancel()

		err := lease.ReleaseWithin(ctx)
		el := time.Since(start)
		got := kv.attempts()
		if len(got) == 0 {
			t.Fatalf("no delete attempt was made (err=%v): the first attempt is unconditional", err)
		}
		for i := 1; i < len(got); i++ {
			if !got[i].Before(windowEnd) {
				t.Fatalf("attempt %d started %v after the window closed (attempts=%d, elapsed=%v, err=%v)",
					i+1, got[i].Sub(windowEnd), len(got), el, err)
			}
		}
		if len(got) != 1 {
			t.Fatalf("delete attempts = %d, want 1: the unconditional first, then none once the window closed", len(got))
		}
		if !errors.Is(err, ErrNotHolder) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the window-closed ErrNotHolder, not the caller's deadline", err)
		}
		if el >= releaseRetryMin {
			t.Fatalf("ReleaseWithin took %v, want it back when the %v window closed, not after the %v backoff",
				el, ttl, releaseRetryMin)
		}
	})
	t.Run("positive control: an open window and a live ctx do retry", func(t *testing.T) {
		kv := &downKV{}
		lease := releasingLease(kv, time.Hour, time.Now())
		ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
		defer cancel()
		err := lease.ReleaseWithin(ctx)
		if n := len(kv.attempts()); n < 2 {
			t.Fatalf("delete attempts = %d, want >= 2: this fake must be seen to retry, or the one-attempt "+
				"verdict above proves nothing", n)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the caller's deadline", err)
		}
	})
	t.Run("window closing mid-backoff, long after it opened: the backoff ends with it", func(t *testing.T) {
		// The window is an hour long but opened almost an hour ago, so ~20ms of it
		// remain. The backoff must be clamped to what REMAINS, not to the ttl: a clamp
		// that forgot the time already elapsed since the last renewal would sleep the
		// whole backoff here, which the case above cannot see because its window opened
		// at the start of the test.
		start := time.Now()
		kv := &downKV{}
		lease := releasingLease(kv, time.Hour, start.Add(-time.Hour+20*time.Millisecond))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // ctx stays live
		defer cancel()

		err := lease.ReleaseWithin(ctx)
		el := time.Since(start)
		if n := len(kv.attempts()); n != 1 {
			t.Fatalf("delete attempts = %d, want 1: the unconditional first, then none once the window closed", n)
		}
		if !errors.Is(err, ErrNotHolder) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the window-closed ErrNotHolder, not the caller's deadline", err)
		}
		if el >= releaseRetryMin {
			t.Fatalf("ReleaseWithin took %v, want it back when the window closed (~20ms left), not after the %v backoff",
				el, releaseRetryMin)
		}
	})
	t.Run("ctx ending mid-backoff still ends the call with ctx's error", func(t *testing.T) {
		start := time.Now()
		kv := &downKV{}
		lease := releasingLease(kv, time.Hour, start)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		err := lease.ReleaseWithin(ctx)
		el := time.Since(start)
		if n := len(kv.attempts()); n != 1 {
			t.Fatalf("delete attempts = %d, want 1", n)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		// The backoff must itself end when ctx does. Without this the call above would
		// still pass after sleeping the whole backoff and only then noticing ctx.
		if el >= releaseRetryMin {
			t.Fatalf("ReleaseWithin took %v, want it back when the 30ms ctx ended, not after the %v backoff",
				el, releaseRetryMin)
		}
	})
	t.Run("both bounds already ended: ctx's error wins, and the first attempt is still made", func(t *testing.T) {
		kv := &downKV{}
		lease := releasingLease(kv, 10*time.Millisecond, time.Now().Add(-time.Second))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := lease.ReleaseWithin(ctx)
		if n := len(kv.attempts()); n != 1 {
			t.Fatalf("delete attempts = %d, want exactly 1 (the unconditional first)", n)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled (ctx is checked before the window)", err)
		}
	})
}

// TestReleaseWithinNeverDeletesAnotherOwnersEntry is the single-holder guard on the
// retrying release. A displaced owner's release must leave the new owner's entry in
// place, and must stop at once rather than retry until its deadline.
func TestReleaseWithinNeverDeletesAnotherOwnersEntry(t *testing.T) {
	nmgr, _ := newJetStreamToggleManager(t)
	dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	a, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := dl.kv.Delete(kvKey("detect:p")); err != nil {
		t.Fatalf("simulate expiry: %v", err)
	}
	b, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("successor Acquire: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	err = a.ReleaseWithin(ctx)
	if !errors.Is(err, ErrNotHolder) {
		t.Fatalf("displaced owner's ReleaseWithin = %v, want ErrNotHolder", err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("displaced owner's ReleaseWithin took %v, want an immediate answer, not a retry to the deadline", el)
	}
	entry, err := dl.kv.Get(kvKey("detect:p"))
	if err != nil {
		t.Fatalf("the new owner's entry after the displaced owner's release: %v", err)
	}
	if string(entry.Value()) != b.holder {
		t.Fatalf("lease entry holder = %q, want the new owner's %q", entry.Value(), b.holder)
	}
}

// lostReplyKV applies the real write and then reports nats.ErrTimeout, the first N
// times each write is called: the server has the write, the client believes it failed.
type lostReplyKV struct {
	nats.KeyValue
	loseUpdates atomic.Int32
	loseDeletes atomic.Int32
	deletes     atomic.Int32
	gets        atomic.Int32
}

func (k *lostReplyKV) Update(key string, value []byte, last uint64) (uint64, error) {
	rev, err := k.KeyValue.Update(key, value, last)
	if err == nil && k.loseUpdates.Add(-1) >= 0 {
		return 0, nats.ErrTimeout
	}
	return rev, err
}

func (k *lostReplyKV) Delete(key string, opts ...nats.DeleteOpt) error {
	k.deletes.Add(1)
	err := k.KeyValue.Delete(key, opts...)
	if err == nil && k.loseDeletes.Add(-1) >= 0 {
		return nats.ErrTimeout
	}
	return err
}

func (k *lostReplyKV) Get(key string) (nats.KeyValueEntry, error) {
	k.gets.Add(1)
	return k.KeyValue.Get(key)
}

// TestARenewWhoseReplyWasLostDoesNotLoseTheLease: a renewal that landed with its reply
// lost leaves the lease one revision behind its own entry. Before the fix every later
// Renew failed its CAS against that entry (ErrKeyRevisionMismatch), so the owner lost,
// at its window end, a partition nobody else held.
func TestARenewWhoseReplyWasLostDoesNotLoseTheLease(t *testing.T) {
	nmgr, _ := newJetStreamToggleManager(t)
	dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	kv := &lostReplyKV{KeyValue: lease.kv}
	kv.loseUpdates.Store(1)
	lease.kv = kv

	if err := lease.Renew(); !errors.Is(err, nats.ErrTimeout) {
		t.Fatalf("first Renew = %v, want the lost reply's ErrTimeout", err)
	}
	if err := lease.Renew(); err != nil {
		t.Fatalf("second Renew = %v, want nil: the entry on the server carries this lease's own holder id", err)
	}
	entry, err := dl.kv.Get(lease.key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	lease.mu.Lock()
	rev := lease.rev
	lease.mu.Unlock()
	if rev != entry.Revision() {
		t.Fatalf("lease revision after the recovering Renew = %d, want the server's %d", rev, entry.Revision())
	}
	if string(entry.Value()) != lease.holder {
		t.Fatalf("entry holder = %q, want ours %q", entry.Value(), lease.holder)
	}
}

// TestAReleaseAfterALostRenewReplyStillReleases is the same lost reply seen by the
// release. Before the fix the delete failed its CAS against our own entry, and the
// successor read ErrLeaseHeld until the entry expired, then paid its handover wait.
func TestAReleaseAfterALostRenewReplyStillReleases(t *testing.T) {
	nmgr, b := newJetStreamToggleManager(t)
	dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	kv := &lostReplyKV{KeyValue: lease.kv}
	kv.loseUpdates.Store(1)
	lease.kv = kv

	if err := lease.Renew(); !errors.Is(err, nats.ErrTimeout) {
		t.Fatalf("Renew = %v, want the lost reply's ErrTimeout", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release after a lost renewal reply = %v, want nil", err)
	}
	clean, acquireErr := successorView(t, b, "detect:p")
	if acquireErr != nil {
		t.Fatalf("successor Acquire = %v, want nil", acquireErr)
	}
	if !clean {
		t.Fatal("PriorOwnerReleasedCleanly = false, want true")
	}
}

// TestAReleaseWhoseReplyWasLostLeavesACleanMarker: the delete landed and its reply was
// lost, so the retry finds nothing of ours. It reports ErrNotHolder (there was nothing
// left to delete), it does not retry that answer, and what the successor reads is the
// clean release the first attempt made.
func TestAReleaseWhoseReplyWasLostLeavesACleanMarker(t *testing.T) {
	nmgr, b := newJetStreamToggleManager(t)
	dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	kv := &lostReplyKV{KeyValue: lease.kv}
	kv.loseDeletes.Store(1)
	lease.kv = kv

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := lease.ReleaseWithin(ctx); !errors.Is(err, ErrNotHolder) {
		t.Fatalf("ReleaseWithin after a lost delete reply = %v, want ErrNotHolder", err)
	}
	if n := kv.deletes.Load(); n != 2 {
		t.Fatalf("delete attempts = %d, want 2 (the lost one and the retry that found nothing)", n)
	}
	clean, acquireErr := successorView(t, b, "detect:p")
	if acquireErr != nil || !clean {
		t.Fatalf("successor = (clean %v, Acquire %v), want (true, nil)", clean, acquireErr)
	}
}

// TestARenewNeverAdoptsAnotherOwnersRevision: the adoption reads the holder id, and a
// displaced owner's Renew must fail without writing its id over the new owner's.
func TestARenewNeverAdoptsAnotherOwnersRevision(t *testing.T) {
	nmgr, _ := newJetStreamToggleManager(t)
	dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	a, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := dl.kv.Delete(kvKey("detect:p")); err != nil {
		t.Fatalf("simulate expiry: %v", err)
	}
	b, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("successor Acquire: %v", err)
	}
	if err := a.Renew(); err == nil {
		t.Fatal("displaced owner's Renew = nil, want an error")
	}
	entry, err := dl.kv.Get(kvKey("detect:p"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(entry.Value()) != b.holder {
		t.Fatalf("entry holder after the displaced owner's Renew = %q, want the new owner's %q", entry.Value(), b.holder)
	}
}

// TestARenewDuringAnOutageMakesNoExtraRead: the adoption read follows only a refused
// CAS — an answer from a broker that is up. A Renew failing because the broker does not
// answer must not add a read, which could cost a second API timeout on the path whose
// latency DETECT's handover wait is sized against.
func TestARenewDuringAnOutageMakesNoExtraRead(t *testing.T) {
	nmgr, b := newJetStreamToggleManager(t)
	dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	counter := &countingKV{KeyValue: lease.kv}
	lease.kv = counter

	// POSITIVE CONTROL first: a zero count proves nothing until this counter has been
	// seen to count a read on this lease's path. Move our own entry past lease.rev
	// through the raw store (a renewal that landed and lost its reply), so the next
	// Renew is refused, reads once, and adopts.
	if _, err := counter.KeyValue.Update(lease.key, []byte(lease.holder), lease.rev); err != nil {
		t.Fatalf("raw Update of our own entry: %v", err)
	}
	if err := lease.Renew(); err != nil {
		t.Fatalf("Renew after our own entry moved = %v, want nil (adopted)", err)
	}
	if n := counter.gets.Load(); n != 1 {
		t.Fatalf("reads during a refused Renew = %d, want 1: the counter is not on the lease's path", n)
	}

	jetStreamOff(t, b)
	if err := lease.Renew(); err == nil {
		t.Fatal("Renew with JetStream down = nil, want an error")
	}
	if n := counter.gets.Load(); n != 1 {
		t.Fatalf("reads after a Renew the broker did not answer = %d, want still 1 (no extra read)", n)
	}
}

// staleEntry is a real entry reported at an older revision: what a read served by a
// replica that has not caught up looks like.
type staleEntry struct {
	nats.KeyValueEntry
	rev uint64
}

func (e staleEntry) Revision() uint64 { return e.rev }

type staleGetKV struct {
	nats.KeyValue
	staleRev uint64
	deletes  atomic.Int32
}

func (k *staleGetKV) Get(key string) (nats.KeyValueEntry, error) {
	e, err := k.KeyValue.Get(key)
	if err != nil {
		return nil, err
	}
	return staleEntry{KeyValueEntry: e, rev: k.staleRev}, nil
}

func (k *staleGetKV) Delete(key string, opts ...nats.DeleteOpt) error {
	k.deletes.Add(1)
	return k.KeyValue.Delete(key, opts...)
}

// TestReleaseWithinAdoptsAtMostOncePerAttempt: a read that keeps reporting our entry at
// a revision the server has moved past (a lagging replica serving a direct get) must
// not spin the release. Every attempt adopts at most once, then goes through the
// backoff and the deadline like any other unanswered attempt.
func TestReleaseWithinAdoptsAtMostOncePerAttempt(t *testing.T) {
	nmgr, _ := newJetStreamToggleManager(t)
	dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Move our own entry on behind the lease's back, so its revision is stale, and
	// make every read report the stale revision.
	if _, err := dl.kv.Update(lease.key, []byte(lease.holder), lease.rev); err != nil {
		t.Fatalf("Update: %v", err)
	}
	kv := &staleGetKV{KeyValue: lease.kv, staleRev: lease.rev}
	lease.kv = kv

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	err = lease.ReleaseWithin(ctx)
	el := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReleaseWithin against a reader stuck on a stale revision = %v, want the deadline", err)
	}
	if el > 3*time.Second {
		t.Fatalf("ReleaseWithin took %v, want about its 1s budget", el)
	}
	// 100+200+400ms of backoff fit in 1s, so at most four attempts of two deletes each.
	if n := kv.deletes.Load(); n > 10 {
		t.Fatalf("delete attempts in a 1s budget = %d, want at most ~8: the adoption is spinning", n)
	}
}

// TestHeldIsFalseWhileAReleaseIsRetrying: a release in progress is not a hold. The
// gate must read false from the first attempt, not once the retries end.
func TestHeldIsFalseWhileAReleaseIsRetrying(t *testing.T) {
	nmgr, b := newJetStreamToggleManager(t)
	dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:p")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	wctx, wcancel := context.WithCancel(context.Background())
	defer wcancel()
	holder, err := lease.WatchHolder(wctx)
	if err != nil {
		t.Fatalf("WatchHolder: %v", err)
	}
	if !holder.Held() {
		t.Fatal("Held() before the release = false, want true")
	}
	jetStreamOff(t, b)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- lease.ReleaseWithin(ctx) }()

	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("ReleaseWithin returned %v before the probe; the probe needs it still retrying", err)
	default:
	}
	if holder.Held() {
		t.Fatal("Held() while a release is retrying = true, want false")
	}
	<-done
}

// slowGetKV counts updates and holds every read for getDelay before answering it.
type slowGetKV struct {
	nats.KeyValue
	getDelay time.Duration
	updates  atomic.Int32
}

func (k *slowGetKV) Update(key string, value []byte, last uint64) (uint64, error) {
	k.updates.Add(1)
	return k.KeyValue.Update(key, value, last)
}

func (k *slowGetKV) Get(key string) (nats.KeyValueEntry, error) {
	time.Sleep(k.getDelay)
	return k.KeyValue.Get(key)
}

// TestARenewDoesNotAdoptOnceItsWindowHasClosed pins the window re-check AFTER the
// adoption read. The window is checked before the read too, but the read is a round
// trip: a reply slow enough to carry the lease past its window end must not be
// followed by the adopting Update, or a Renew overruns the window by a read PLUS an
// update rather than the one API timeout DETECT's termSlack budgets for.
//
// The positive control is the same setup with a read that answers at once: it adopts,
// which proves the refused CAS really reaches the adoption path, so the second case's
// single Update is the re-check speaking and not a path never taken.
func TestARenewDoesNotAdoptOnceItsWindowHasClosed(t *testing.T) {
	const windowLeft = 400 * time.Millisecond
	run := func(t *testing.T, getDelay time.Duration) (updates int32, renewErr error) {
		nmgr, _ := newJetStreamToggleManager(t)
		dl, err := nmgr.NewDistributedLease(DefaultLeaseTTL)
		if err != nil {
			t.Fatalf("NewDistributedLease: %v", err)
		}
		lease, err := dl.Acquire("detect:p")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		kv := &slowGetKV{KeyValue: lease.kv, getDelay: getDelay}
		lease.kv = kv
		// Our own entry moves past lease.rev (a renewal that landed and lost its
		// reply), so the next Renew's CAS is refused and it reads to adopt.
		if _, err := kv.KeyValue.Update(lease.key, []byte(lease.holder), lease.rev); err != nil {
			t.Fatalf("raw Update of our own entry: %v", err)
		}
		lease.mu.Lock()
		lease.lastRenew = time.Now().Add(-DefaultLeaseTTL + windowLeft)
		lease.mu.Unlock()
		renewErr = lease.Renew()
		return kv.updates.Load(), renewErr
	}

	t.Run("positive control: a prompt read adopts", func(t *testing.T) {
		updates, err := run(t, 0)
		if err != nil {
			t.Fatalf("Renew with %v of window left and a prompt read = %v, want nil (adopted)", windowLeft, err)
		}
		if updates != 2 {
			t.Fatalf("updates = %d, want 2 (the refused CAS and the adopting one)", updates)
		}
	})
	t.Run("a read that outlasts the window does not adopt", func(t *testing.T) {
		updates, err := run(t, 2*windowLeft)
		if err == nil {
			t.Fatal("Renew whose adoption read outlasted the window = nil, want an error")
		}
		if updates != 1 {
			t.Fatalf("updates = %d, want 1: no adopting Update may start after the window closed", updates)
		}
	})
}
