// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// recordingReleaser records every wake it is asked to perform.
type recordingReleaser struct {
	mu       sync.Mutex
	calls    []wakeRequest
	tenants  []string
	released int
	err      error
	// block, when non-nil, holds every call until it is closed — the way to fill the
	// queue deterministically rather than by racing it.
	block chan struct{}
	done  chan struct{}
}

func (r *recordingReleaser) ReleaseHeld(ctx context.Context, tenant, deviceToken string) (int, error) {
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	r.calls = append(r.calls, wakeRequest{tenant: tenant, deviceToken: deviceToken})
	// 🔑 The tenant is read back off the CONTEXT, not off the argument. The argument
	// proves the caller knew the tenant; the context is what a real releaser's
	// downstream query would actually be scoped by, and they are separately settable.
	if fromCtx, ok := core.TenantFromContext(ctx); ok {
		r.tenants = append(r.tenants, fromCtx)
	}
	r.mu.Unlock()
	if r.done != nil {
		r.done <- struct{}{}
	}
	return r.released, r.err
}

func (r *recordingReleaser) seen() []wakeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]wakeRequest(nil), r.calls...)
}

// TestAWakeReachesTheReleaserWithItsTenant.
func TestAWakeReachesTheReleaserWithItsTenant(t *testing.T) {
	rel := &recordingReleaser{released: 3, done: make(chan struct{}, 1)}
	metrics := WakerMetrics{
		Requested: prometheus.NewCounter(prometheus.CounterOpts{Name: "wake_requested"}),
		Released:  prometheus.NewCounter(prometheus.CounterOpts{Name: "wake_released"}),
	}
	waker, stop := NewWaker(rel, metrics)
	defer stop()

	waker.Wake("acme", "pump-1")

	select {
	case <-rel.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the wake never reached the releaser")
	}
	if got := rel.seen(); len(got) != 1 || got[0].tenant != "acme" || got[0].deviceToken != "pump-1" {
		t.Fatalf("releaser saw %+v", got)
	}
	// 🔴 The context must carry the tenant. Without it the release query is either
	// refused by the tenant-scope callback or — far worse, if anything downstream ever
	// runs unscoped — applied across tenants.
	rel.mu.Lock()
	tenants := append([]string(nil), rel.tenants...)
	rel.mu.Unlock()
	if len(tenants) != 1 || tenants[0] != "acme" {
		t.Fatalf("the release ran without its tenant in context, got %v", tenants)
	}
	if got := testutil.ToFloat64(metrics.Released); got != 3 {
		t.Fatalf("Released = %v, want the 3 commands the releaser reported", got)
	}
}

// TestAFullQueueDROPSRatherThanBlocking.
//
// 🔴 THE DROP IS THE DESIGN, AND IT IS ONLY SAFE BECAUSE THE RECONCILER EXISTS. Wake is
// called from the broker-advisory path. If it blocked, a slow or unavailable
// command-delivery would apply backpressure to presence itself — and presence is what
// the delivery gate READS to decide whether to withhold. Delaying it to speed up command
// release would be exactly backwards.
//
// A dropped wake loses nothing: the row stays HELD and command-delivery's reconcile pass
// releases it later. The cost is latency, which is why it is counted separately from a
// failure rather than folded into one "wake problems" number.
func TestAFullQueueDropsRatherThanBlocking(t *testing.T) {
	rel := &recordingReleaser{block: make(chan struct{})}
	metrics := WakerMetrics{
		Requested: prometheus.NewCounter(prometheus.CounterOpts{Name: "wake_req_full"}),
		Dropped:   prometheus.NewCounter(prometheus.CounterOpts{Name: "wake_drop_full"}),
	}
	waker, stop := NewWaker(rel, metrics)
	defer func() { close(rel.block); stop() }()

	// Every worker parks in the releaser, then the queue fills, then the rest drop.
	// One more than capacity plus the parked workers guarantees at least one drop
	// without depending on scheduling.
	total := WakerQueueDepth + WakerWorkers + 50
	done := make(chan struct{})
	go func() {
		for i := 0; i < total; i++ {
			waker.Wake("acme", "pump-1")
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wake blocked on a full queue; it must never block the advisory path")
	}

	dropped := testutil.ToFloat64(metrics.Dropped)
	requested := testutil.ToFloat64(metrics.Requested)
	if dropped == 0 {
		t.Fatalf("nothing was dropped from a deliberately overfilled queue (requested=%v)", requested)
	}
	if requested+dropped != float64(total) {
		t.Fatalf("every wake must be either queued or counted as dropped: requested=%v dropped=%v total=%d",
			requested, dropped, total)
	}
}

// TestAFailedReleaseIsCountedAndNotRetried.
//
// Not retried on purpose: a retry loop would hold a worker against an outage while the
// queue behind it filled and began dropping, turning one service's unavailability into
// lost wakes for every OTHER device. The reconciler is the retry.
func TestAFailedReleaseIsCountedAndNotRetried(t *testing.T) {
	rel := &recordingReleaser{err: errors.New("command-delivery unavailable"), done: make(chan struct{}, 4)}
	metrics := WakerMetrics{
		Failed:   prometheus.NewCounter(prometheus.CounterOpts{Name: "wake_failed"}),
		Released: prometheus.NewCounter(prometheus.CounterOpts{Name: "wake_released_fail"}),
	}
	waker, stop := NewWaker(rel, metrics)
	defer stop()

	waker.Wake("acme", "pump-1")
	select {
	case <-rel.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the wake never reached the releaser")
	}
	// Give a retry, if one existed, room to show up.
	time.Sleep(150 * time.Millisecond)

	if got := len(rel.seen()); got != 1 {
		t.Fatalf("a failed wake was attempted %d times; it must not be retried", got)
	}
	if got := testutil.ToFloat64(metrics.Failed); got != 1 {
		t.Fatalf("Failed = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.Released); got != 0 {
		t.Fatalf("a failed wake must not count as releasing anything, got %v", got)
	}
}

// TestNothingReleasedIsNotCounted keeps the one honest counter honest. Most connects
// have no backlog, so if a zero release incremented Released the metric would track
// connection volume and read as healthy on an instance where the wake was doing nothing.
func TestNothingReleasedIsNotCounted(t *testing.T) {
	rel := &recordingReleaser{released: 0, done: make(chan struct{}, 1)}
	metrics := WakerMetrics{Released: prometheus.NewCounter(prometheus.CounterOpts{Name: "wake_released_zero"})}
	waker, stop := NewWaker(rel, metrics)
	defer stop()

	waker.Wake("acme", "quiet-device")
	select {
	case <-rel.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the wake never reached the releaser")
	}
	if got := testutil.ToFloat64(metrics.Released); got != 0 {
		t.Fatalf("Released = %v for a device with nothing waiting", got)
	}
}

// TestStopIsIdempotent — the runtime appends it to a list of stop functions that may be
// run more than once on a messy shutdown, and a double close would panic the service on
// its way down, which is the worst moment for a new failure.
func TestStopIsIdempotent(t *testing.T) {
	_, stop := NewWaker(&recordingReleaser{}, WakerMetrics{})
	stop()
	stop()
}

// TestNilMetricsAreTolerated. The struct is assembled by literal in tests and by a
// wiring function that could legitimately pass none.
func TestNilMetricsAreTolerated(t *testing.T) {
	rel := &recordingReleaser{done: make(chan struct{}, 1)}
	waker, stop := NewWaker(rel, WakerMetrics{})
	defer stop()

	waker.Wake("acme", "pump-1")
	select {
	case <-rel.done:
	case <-time.After(2 * time.Second):
		t.Fatal("a waker with no metrics must still perform the wake")
	}
}

// fakeGQL captures what the releaser puts on the wire.
type fakeGQL struct {
	gotTenant string
	gotVars   map[string]any
	count     int32
}

func (f *fakeGQL) Query(_ context.Context, _, tenant, _ string, vars map[string]any, out any) error {
	f.gotTenant = tenant
	f.gotVars = vars
	resp, ok := out.(*struct {
		ReleaseHeldCommands int32 `json:"releaseHeldCommands"`
	})
	if !ok {
		return errors.New("unexpected out type")
	}
	resp.ReleaseHeldCommands = f.count
	return nil
}

// TestTheGraphQLReleaserSendsTheDeviceUnderItsTenant.
func TestTheGraphQLReleaserSendsTheDeviceUnderItsTenant(t *testing.T) {
	gql := &fakeGQL{count: 7}
	rel := NewGraphQLReleaser(gql, "http://command-delivery/graphql")

	released, err := rel.ReleaseHeld(context.Background(), "acme", "pump-1")
	if err != nil {
		t.Fatalf("ReleaseHeld failed: %v", err)
	}
	if released != 7 {
		t.Fatalf("released = %d, want the 7 the server reported", released)
	}
	if gql.gotTenant != "acme" {
		t.Fatalf("the call must be tenant-scoped, got %q", gql.gotTenant)
	}
	if gql.gotVars["deviceToken"] != "pump-1" {
		t.Fatalf("vars = %+v", gql.gotVars)
	}
}

// TestTheGraphQLReleaserRefusesAnEmptyTenant fails closed rather than issuing a
// tenant-less mutation, which the server would either refuse or — if anything downstream
// were ever unscoped — apply across tenants.
func TestTheGraphQLReleaserRefusesAnEmptyTenant(t *testing.T) {
	rel := NewGraphQLReleaser(&fakeGQL{}, "http://command-delivery/graphql")
	if _, err := rel.ReleaseHeld(context.Background(), "", "pump-1"); err == nil {
		t.Fatal("a wake with no tenant must be refused")
	}
}
