// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventLog is one ordered record shared by the bucket and the sources, so a test can
// assert what happened BEFORE what.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (l *eventLog) index(e string) int {
	for i, got := range l.snapshot() {
		if got == e {
			return i
		}
	}
	return -1
}

func (l *eventLog) count(e string) int {
	n := 0
	for _, got := range l.snapshot() {
		if got == e {
			n++
		}
	}
	return n
}

// fakeBucket stands in for the lease bucket: Create-if-absent per partition, like the KV.
type fakeBucket struct {
	mu     sync.Mutex
	holder map[string]*fakeLease
	// grantAlways makes every Acquire succeed: the negative control for "one owner".
	grantAlways bool
	log         *eventLog
}

func newFakeBucket(log *eventLog) *fakeBucket {
	return &fakeBucket{holder: map[string]*fakeLease{}, log: log}
}

func (b *fakeBucket) leasesFor(pod string) SourceLeases { return fakeLeases{b, pod} }

func (b *fakeBucket) held(partition string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.holder[partition] != nil
}

func (b *fakeBucket) leaseOf(partition string) *fakeLease {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.holder[partition]
}

type fakeLeases struct {
	b   *fakeBucket
	pod string
}

func (f fakeLeases) Acquire(partition string) (SourceLease, error) {
	f.b.mu.Lock()
	defer f.b.mu.Unlock()
	if f.b.holder[partition] != nil && !f.b.grantAlways {
		return nil, messaging.ErrLeaseHeld
	}
	l := &fakeLease{b: f.b, pod: f.pod, partition: partition, lose: make(chan struct{}), lost: make(chan struct{})}
	l.held.Store(true)
	f.b.holder[partition] = l
	return l, nil
}

type fakeLease struct {
	b              *fakeBucket
	pod, partition string
	// lose makes KeepAlive report the lease definitively gone (ErrNotHolder).
	lose     chan struct{}
	loseOnce sync.Once
	// lost is the Holder's watch seeing another pod take the source.
	lost     chan struct{}
	lostOnce sync.Once
	held     atomic.Bool
}

func (l *fakeLease) expire() {
	l.held.Store(false)
	l.loseOnce.Do(func() { close(l.lose) })
}

func (l *fakeLease) takenOver() {
	l.held.Store(false)
	l.lostOnce.Do(func() { close(l.lost) })
}

func (l *fakeLease) KeepAlive(ctx context.Context, _ time.Duration) error {
	select {
	case <-ctx.Done():
		return nil
	case <-l.lose:
		return messaging.ErrNotHolder
	}
}

func (l *fakeLease) WatchHolder(context.Context) (SourceHolder, error) { return fakeHolder{l}, nil }

func (l *fakeLease) Release() error {
	l.b.mu.Lock()
	if l.b.holder[l.partition] == l {
		delete(l.b.holder, l.partition)
	}
	l.b.mu.Unlock()
	l.b.log.add("release:" + l.pod)
	return nil
}

type fakeHolder struct{ l *fakeLease }

func (h fakeHolder) Held() bool            { return h.l.held.Load() }
func (h fakeHolder) Lost() <-chan struct{} { return h.l.lost }

// fakeSource is one term's source. It records start and stop in the shared log.
type fakeSource struct {
	pod, clientID string
	log           *eventLog
	startErr      error
	// blockStart makes Start wait for its context, as a slow broker would.
	blockStart bool
	owns       func() bool
}

func (s *fakeSource) ClientID() string                        { return s.clientID }
func (s *fakeSource) Initialize(ctx context.Context) error    { return s.ExecuteInitialize(ctx) }
func (s *fakeSource) ExecuteInitialize(context.Context) error { return nil }
func (s *fakeSource) Start(ctx context.Context) error         { return s.ExecuteStart(ctx) }
func (s *fakeSource) ExecuteStart(ctx context.Context) error {
	if s.blockStart {
		s.log.add("start-waiting:" + s.pod)
		<-ctx.Done()
		return ctx.Err()
	}
	if s.startErr != nil {
		return s.startErr
	}
	s.log.add("start:" + s.pod)
	return nil
}
func (s *fakeSource) Stop(ctx context.Context) error { return s.ExecuteStop(ctx) }
func (s *fakeSource) ExecuteStop(context.Context) error {
	s.log.add("stop:" + s.pod)
	return nil
}
func (s *fakeSource) Terminate(ctx context.Context) error    { return s.ExecuteTerminate(ctx) }
func (s *fakeSource) ExecuteTerminate(context.Context) error { return nil }

// pod is one event-sources pod's owned source, with everything it reported.
type pod struct {
	name  string
	owned *OwnedMqttSource

	mu       sync.Mutex
	owner    *bool
	notOwner int
	fails    []error
	built    int
	// startErrs, when set, is the Start error of each TERM in turn (the probe is not a term).
	startErrs  []error
	blockStart bool
	last       *fakeSource
}

func (p *pod) isOwner() (owner, reported bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owner == nil {
		return false, false
	}
	return *p.owner, true
}

func (p *pod) failures() []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]error(nil), p.fails...)
}

func (p *pod) builds() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.built
}

func (p *pod) lastOwns() func() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last.owns
}

func newPod(t *testing.T, name string, b *fakeBucket) *pod {
	t.Helper()
	p := &pod{name: name}
	owned, err := NewOwnedMqttSource("ext", "inst-1", name,
		func(clientID string, owns func() bool) (TermSource, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.built++
			src := &fakeSource{pod: name, clientID: clientID, log: b.log, owns: owns, blockStart: p.blockStart}
			if term := p.built - 2; term >= 0 && term < len(p.startErrs) {
				src.startErr = p.startErrs[term]
			}
			p.last = src
			return src, nil
		},
		func() (SourceLeases, error) { return b.leasesFor(name), nil },
		OwnerHooks{
			Fail: func(err error) {
				p.mu.Lock()
				defer p.mu.Unlock()
				p.fails = append(p.fails, err)
			},
			SetOwner: func(source string, owned bool) {
				p.mu.Lock()
				defer p.mu.Unlock()
				p.owner = &owned
			},
			NotOwner: func(string) {
				p.mu.Lock()
				defer p.mu.Unlock()
				p.notOwner++
			},
		})
	require.NoError(t, err)
	p.owned = owned
	return p
}

func (p *pod) start(t *testing.T) error {
	t.Helper()
	require.NoError(t, p.owned.Initialize(context.Background()))
	return p.owned.Start(context.Background())
}

func (p *pod) stop(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.owned.Stop(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not stop within 10s", p.name)
	}
}

// fastOwnerRetries shortens the standby intervals for the length of one test.
func fastOwnerRetries(t *testing.T) {
	savedRetry, savedBackoff := ownerRetryInterval, ownerFailedStartBackoff
	ownerRetryInterval, ownerFailedStartBackoff = 20*time.Millisecond, 60*time.Millisecond
	t.Cleanup(func() { ownerRetryInterval, ownerFailedStartBackoff = savedRetry, savedBackoff })
}

// readers counts the pods whose source is running: started and not since stopped.
func readers(log *eventLog, pods ...*pod) int {
	n := 0
	for _, p := range pods {
		if log.count("start:"+p.name) > log.count("stop:"+p.name) {
			n++
		}
	}
	return n
}

// The defect this type exists for: with a client id per pod, every pod's session would be
// given every message, and this path has no id to store a duplicate once. So exactly one
// pod reads.
func TestOnlyOnePodReadsAnExternalSource(t *testing.T) {
	fastOwnerRetries(t)
	log := &eventLog{}
	b := newFakeBucket(log)
	a, bb := newPod(t, "pod-a", b), newPod(t, "pod-b", b)
	require.NoError(t, a.start(t))
	require.NoError(t, bb.start(t))
	t.Cleanup(func() { a.stop(t); bb.stop(t) })

	time.Sleep(100 * time.Millisecond) // several standby retries
	assert.Equal(t, 1, readers(log, a, bb), "events %v", log.snapshot())
	aOwns, aReported := a.isOwner()
	bOwns, bReported := bb.isOwner()
	assert.True(t, aReported && bReported, "both pods must report, the standby as 0")
	assert.True(t, aOwns != bOwns, "exactly one pod may report owning the source: a=%v b=%v", aOwns, bOwns)
	assert.Equal(t, "devicechain:inst-1:ext:pod-a", a.owned.ClientID())
	assert.Equal(t, "devicechain:inst-1:ext:pod-b", bb.owned.ClientID())

	t.Run("control: a bucket that grants every pod makes both read", func(t *testing.T) {
		log := &eventLog{}
		b := newFakeBucket(log)
		b.grantAlways = true
		a, bb := newPod(t, "pod-a", b), newPod(t, "pod-b", b)
		require.NoError(t, a.start(t))
		require.NoError(t, bb.start(t))
		t.Cleanup(func() { a.stop(t); bb.stop(t) })
		assert.Equal(t, 2, readers(log, a, bb), "the reader count cannot see a second reader")
	})
}

// A pod that stops hands the source over: it stops reading BEFORE it releases, and a
// standby picks it up.
func TestAStandbyTakesOverWhenTheOwnerStops(t *testing.T) {
	fastOwnerRetries(t)
	log := &eventLog{}
	b := newFakeBucket(log)
	a, bb := newPod(t, "pod-a", b), newPod(t, "pod-b", b)
	require.NoError(t, a.start(t))
	require.NoError(t, bb.start(t))
	t.Cleanup(func() { bb.stop(t) })
	require.Equal(t, 1, log.count("start:pod-a"))

	a.stop(t)
	stopped, released := log.index("stop:pod-a"), log.index("release:pod-a")
	require.GreaterOrEqual(t, stopped, 0, "events %v", log.snapshot())
	require.GreaterOrEqual(t, released, 0, "events %v", log.snapshot())
	assert.Less(t, stopped, released, "the owner released before it stopped reading: %v", log.snapshot())

	require.Eventually(t, func() bool { return log.count("start:pod-b") == 1 }, 3*time.Second, 5*time.Millisecond,
		"the standby never took over: %v", log.snapshot())
	owner, _ := bb.isOwner()
	assert.True(t, owner)
	owner, _ = a.isOwner()
	assert.False(t, owner)
}

// A lease can be lost two ways, and both end the term: the renewer finds the window has
// passed (ErrNotHolder), or the watch sees another pod take the source. The pod stops
// reading, releases, reports 0, and later reads again through a FRESH source.
func TestAnOwnerThatLosesItsLeaseStopsReading(t *testing.T) {
	for _, tc := range []struct {
		name string
		lose func(*fakeLease)
	}{
		{"the renewer finds the lease gone", (*fakeLease).expire},
		{"the watch sees a takeover", (*fakeLease).takenOver},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fastOwnerRetries(t)
			log := &eventLog{}
			b := newFakeBucket(log)
			a := newPod(t, "pod-a", b)
			require.NoError(t, a.start(t))
			t.Cleanup(func() { a.stop(t) })
			require.Equal(t, 2, a.builds(), "the probe and the first term")

			first := b.leaseOf(OwnershipPartition("ext"))
			require.NotNil(t, first)
			tc.lose(first)

			require.Eventually(t, func() bool { return log.index("release:pod-a") >= 0 }, 3*time.Second,
				5*time.Millisecond, "the term never ended: %v", log.snapshot())
			assert.Less(t, log.index("stop:pod-a"), log.index("release:pod-a"), "%v", log.snapshot())

			require.Eventually(t, func() bool { return log.count("start:pod-a") == 2 }, 3*time.Second,
				5*time.Millisecond, "the pod never read again: %v", log.snapshot())
			assert.Equal(t, 3, a.builds(), "the second term must build a fresh source, not restart a stopped one")
			owner, _ := a.isOwner()
			assert.True(t, owner)
		})
	}
}

// A message delivered to the source after its lease stopped being held is dropped and
// counted, through the gate the term hands the source.
func TestATermsSourceAsksTheLeaseForEveryMessage(t *testing.T) {
	log := &eventLog{}
	b := newFakeBucket(log)
	a := newPod(t, "pod-a", b)
	require.NoError(t, a.start(t))
	t.Cleanup(func() { a.stop(t) })

	owns := a.lastOwns()
	assert.True(t, owns())
	b.leaseOf(OwnershipPartition("ext")).held.Store(false)
	assert.False(t, owns())
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, 1, a.notOwner)
}

// 🔴 THE RENEWER RUNS BEFORE THE SOURCE STARTS, AND THE START RUNS UNDER THE TERM. A
// start can outlast the lease (connect and subscribe are each bounded at 30 s, the lease
// at 30 s); a start nobody renewed for would connect a second reader beside the pod that
// took the source over. Here the start waits on its context and the lease is lost
// meanwhile: the start must be abandoned, and the pod must never report owning it.
func TestAStartThatOutlivesTheLeaseIsAbandoned(t *testing.T) {
	fastOwnerRetries(t)
	log := &eventLog{}
	b := newFakeBucket(log)
	a, bb := newPod(t, "pod-a", b), newPod(t, "pod-b", b)
	require.NoError(t, a.start(t))
	bb.mu.Lock()
	bb.blockStart = true
	bb.mu.Unlock()
	require.NoError(t, bb.start(t))
	t.Cleanup(func() { bb.stop(t) })

	a.stop(t)
	require.Eventually(t, func() bool { return log.index("start-waiting:pod-b") >= 0 }, 3*time.Second,
		5*time.Millisecond, "pod-b never took over: %v", log.snapshot())
	b.leaseOf(OwnershipPartition("ext")).expire()

	require.Eventually(t, func() bool { return log.index("release:pod-b") >= 0 }, 3*time.Second,
		5*time.Millisecond, "a start that outlived its lease was not abandoned: %v", log.snapshot())
	assert.Less(t, log.index("stop:pod-b"), log.index("release:pod-b"), "%v", log.snapshot())
	owner, _ := bb.isOwner()
	assert.False(t, owner, "a pod whose start never finished reported owning the source")
	assert.Empty(t, bb.failures(), "losing a lease is not a reason to end the process")
}

// A takeover whose start is abandoned because the lease was lost under it has learned
// nothing about the broker, so it is neither the failed-start backoff nor an error: the pod
// asks again at the standby interval. The backoff here is long enough that a pod which
// took it would not ask again within the test.
func TestAStartAbandonedForALostLeaseAsksAgainAtTheStandbyInterval(t *testing.T) {
	fastOwnerRetries(t)
	ownerFailedStartBackoff = time.Hour
	log := &eventLog{}
	b := newFakeBucket(log)
	a, bb := newPod(t, "pod-a", b), newPod(t, "pod-b", b)
	require.NoError(t, a.start(t))
	bb.mu.Lock()
	bb.blockStart = true
	bb.mu.Unlock()
	require.NoError(t, bb.start(t))
	t.Cleanup(func() { bb.stop(t) })

	a.stop(t)
	require.Eventually(t, func() bool { return log.count("start-waiting:pod-b") == 1 }, 3*time.Second,
		5*time.Millisecond, "pod-b never took over: %v", log.snapshot())
	b.leaseOf(OwnershipPartition("ext")).expire()

	require.Eventually(t, func() bool { return log.count("start-waiting:pod-b") >= 2 }, 3*time.Second,
		5*time.Millisecond, "a start abandoned for a lost lease waited out the failed-start backoff: %v", log.snapshot())
	assert.Empty(t, bb.failures())
}

// The pod that wins the lease at startup fails the service's start when its source cannot
// start, as an external source always has, and leaves the lease free.
func TestAStartupStartFailureFailsTheStart(t *testing.T) {
	log := &eventLog{}
	b := newFakeBucket(log)
	boom := errors.New("connection refused")
	a := newPod(t, "pod-a", b)
	a.startErrs = []error{boom}
	err := a.start(t)
	require.ErrorIs(t, err, boom)
	assert.False(t, b.held(OwnershipPartition("ext")), "a failed start left the lease held")
	owner, _ := a.isOwner()
	assert.False(t, owner)
}

// 🔴 A TAKEOVER THAT CANNOT CONNECT DOES NOT END THE PROCESS. The broker is the operator's
// and may be down for maintenance; ending the pod would take HTTP and gateway ingest with
// it, and every pod would crash-loop in turn. It releases, reports 0, and tries again.
func TestATakeoverThatCannotConnectStandsByAgain(t *testing.T) {
	fastOwnerRetries(t)
	log := &eventLog{}
	b := newFakeBucket(log)
	a, bb := newPod(t, "pod-a", b), newPod(t, "pod-b", b)
	require.NoError(t, a.start(t))
	bb.startErrs = []error{fmt.Errorf("this event source would ingest nothing: %w", errors.New("dial tcp: connection refused"))}
	require.NoError(t, bb.start(t))
	t.Cleanup(func() { bb.stop(t) })

	a.stop(t)
	require.Eventually(t, func() bool { return log.count("release:pod-b") >= 1 }, 3*time.Second, 5*time.Millisecond,
		"the failed takeover did not release: %v", log.snapshot())
	assert.Empty(t, bb.failures(), "an unreachable broker ended the process")
	require.Eventually(t, func() bool { return log.count("start:pod-b") == 1 }, 3*time.Second, 5*time.Millisecond,
		"the pod never tried again: %v", log.snapshot())
	assert.Empty(t, bb.failures())
	owner, _ := bb.isOwner()
	assert.True(t, owner)
}

// A takeover whose broker REFUSES the subscription does end the process: every pod would be
// given the same answer, and it is what a refused re-subscribe does too.
func TestATakeoverWhoseSubscriptionIsRefusedEndsTheProcess(t *testing.T) {
	fastOwnerRetries(t)
	log := &eventLog{}
	b := newFakeBucket(log)
	a, bb := newPod(t, "pod-a", b), newPod(t, "pod-b", b)
	require.NoError(t, a.start(t))
	refused := fmt.Errorf("this event source would ingest nothing: %w", messaging.ErrSubscriptionRefused)
	bb.startErrs = []error{refused}
	require.NoError(t, bb.start(t))
	t.Cleanup(func() { bb.stop(t) })

	a.stop(t)
	require.Eventually(t, func() bool { return len(bb.failures()) == 1 }, 3*time.Second, 5*time.Millisecond,
		"a refused takeover did not end the process: %v", log.snapshot())
	assert.ErrorIs(t, bb.failures()[0], messaging.ErrSubscriptionRefused)
	assert.Eventually(t, func() bool { return !b.held(OwnershipPartition("ext")) }, time.Second, 5*time.Millisecond)
}

// Stop is what lets the next pod take over promptly, so it must not return before the
// source has stopped and the lease is released.
func TestStopReturnsOnlyAfterTheSourceIsStoppedAndReleased(t *testing.T) {
	log := &eventLog{}
	b := newFakeBucket(log)
	a := newPod(t, "pod-a", b)
	require.NoError(t, a.start(t))
	a.stop(t)
	events := log.snapshot()
	assert.Contains(t, events, "stop:pod-a")
	assert.Contains(t, events, "release:pod-a")
	assert.False(t, b.held(OwnershipPartition("ext")))
}

// A pod whose lease bucket cannot be reached fails its start rather than reading unowned.
func TestAnOwnedSourceWithNoLeaseBucketFailsItsStart(t *testing.T) {
	boom := errors.New("no JetStream")
	owned, err := NewOwnedMqttSource("ext", "inst-1", "pod-a",
		func(clientID string, owns func() bool) (TermSource, error) {
			return &fakeSource{pod: "pod-a", clientID: clientID, log: &eventLog{}, owns: owns}, nil
		},
		func() (SourceLeases, error) { return nil, boom },
		OwnerHooks{Fail: func(error) {}, SetOwner: func(string, bool) {}, NotOwner: func(string) {}})
	require.NoError(t, err)
	require.NoError(t, owned.Initialize(context.Background()))
	assert.ErrorIs(t, owned.Start(context.Background()), boom)
}

func TestOwnedSourceRefusesMissingHooks(t *testing.T) {
	src := func(clientID string, owns func() bool) (TermSource, error) {
		return &fakeSource{clientID: clientID, log: &eventLog{}}, nil
	}
	leases := func() (SourceLeases, error) { return nil, nil }
	hooks := OwnerHooks{Fail: func(error) {}, SetOwner: func(string, bool) {}, NotOwner: func(string) {}}
	for name, tc := range map[string]struct {
		src    NewTermSource
		leases func() (SourceLeases, error)
		hooks  func(OwnerHooks) OwnerHooks
	}{
		"no fail":      {src, leases, func(h OwnerHooks) OwnerHooks { h.Fail = nil; return h }},
		"no setOwner":  {src, leases, func(h OwnerHooks) OwnerHooks { h.SetOwner = nil; return h }},
		"no notOwner":  {src, leases, func(h OwnerHooks) OwnerHooks { h.NotOwner = nil; return h }},
		"no newSource": {nil, leases, func(h OwnerHooks) OwnerHooks { return h }},
		"no newLeases": {src, nil, func(h OwnerHooks) OwnerHooks { return h }},
	} {
		_, err := NewOwnedMqttSource("ext", "inst-1", "pod-a", tc.src, tc.leases, tc.hooks(hooks))
		assert.Error(t, err, name)
	}
	_, err := NewOwnedMqttSource("ext", "inst-1", "pod-a", src, leases, hooks)
	assert.NoError(t, err, "the counterweight: a complete set is accepted")
}

// A configuration error surfaces when the sources are built, as it did for an unowned
// source, not later when a pod takes the source over.
func TestABadPortFailsAtBuildNotAtStart(t *testing.T) {
	_, err := NewOwnedMqttSource("ext", "inst-1", "pod-a",
		func(clientID string, owns func() bool) (TermSource, error) {
			_, err := strconv.Atoi("not-a-port")
			return nil, err
		},
		func() (SourceLeases, error) { return nil, nil },
		OwnerHooks{Fail: func(error) {}, SetOwner: func(string, bool) {}, NotOwner: func(string) {}})
	assert.Error(t, err)
}

// A factory that ignores the id it is handed would put every pod on one session again; the
// constructor refuses it.
func TestAFactoryThatIgnoresTheClientIDIsRefused(t *testing.T) {
	_, err := NewOwnedMqttSource("ext", "inst-1", "pod-a",
		func(_ string, owns func() bool) (TermSource, error) {
			return &fakeSource{clientID: "devicechain", log: &eventLog{}}, nil
		},
		func() (SourceLeases, error) { return nil, nil },
		OwnerHooks{Fail: func(error) {}, SetOwner: func(string, bool) {}, NotOwner: func(string) {}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"devicechain"`)
}
