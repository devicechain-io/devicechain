// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// startupBindReader parks the supervisor inside buildTerm, before any reader
// goroutines exist. Cancellation alone cannot finish the bind: the dependency
// must actually return before shutdown may finish joining the supervisor.
type startupBindReader struct {
	entered chan struct{}
	release chan struct{}
	unbound chan struct{}
}

func (r *startupBindReader) BindTerm() error {
	close(r.entered)
	<-r.release
	return errors.New("test: term bind unwound")
}

func (r *startupBindReader) UnbindTerm() error {
	close(r.unbound)
	return nil
}

func (*startupBindReader) ReadMessage(context.Context) (messaging.Message, error) {
	panic("a failed term bind must not start consumption")
}

func (*startupBindReader) HandleResponse(error) {}

// Unlike liveTerm, this enters through ExecuteStart and never registers work in
// supWG itself. Removing startup's Add/Done must let ExecuteStop return while the
// bind is still blocked, which is the regression this test guards.
func TestExecuteStartRegistersTheSupervisorForShutdownJoining(t *testing.T) {
	b := startOutageBroker(t)
	nmgr := b.manager(t)
	dl, err := nmgr.NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.Lease = dl
	rp.Gate = NewTermGate()
	ms := &core.Microservice{InstanceId: "startup", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	rp.metrics = NewDetectMetrics(ms)

	// Leave a real clean-release marker so startup skips termSlack without
	// replacing the production acquisition/watch/renewal/release path.
	prior, err := dl.Acquire(rp.cfg.PartitionId)
	require.NoError(t, err)
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = prior.ReleaseWithin(seedCtx)
	seedCancel()
	require.NoError(t, err)
	require.True(t, dl.PriorOwnerReleasedCleanly(rp.cfg.PartitionId))

	r := &startupBindReader{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		unbound: make(chan struct{}),
	}
	rp.ResolvedEventsReader = r
	require.NoError(t, rp.ExecuteInitialize(context.Background()))

	var releaseOnce, stopOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(r.release) }) }
	stopped := make(chan struct{})
	var stopErr error
	stop := func() {
		stopOnce.Do(func() {
			go func() {
				defer close(stopped)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				stopErr = rp.ExecuteStop(ctx)
			}()
		})
	}
	t.Cleanup(func() {
		release()
		stop()
		select {
		case <-stopped:
			require.NoError(t, stopErr)
		case <-time.After(10 * time.Second):
			t.Error("ExecuteStop did not finish after the bind was released")
		}
		select {
		case <-r.entered:
			// Also clean up the Add/Done mutant: its stop can return before
			// endTerm. Observe teardown independently of the broken wait group
			// before the fixture closes its connection and snapshot store.
			select {
			case <-r.unbound:
			case <-time.After(10 * time.Second):
				t.Error("the supervisor did not unbind the failed term")
				return
			}
			require.Eventually(t, func() bool {
				return dl.PriorOwnerReleasedCleanly(rp.cfg.PartitionId)
			}, 10*time.Second, 10*time.Millisecond, "the supervisor did not release its term")
		default:
		}
	})

	require.NoError(t, rp.ExecuteStart(context.Background()))
	select {
	case <-r.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("ExecuteStart did not reach the term-build dependency")
	}
	stop()
	select {
	case <-rp.supCtx.Done(): // proves ExecuteStop has reached cancellation
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteStop did not cancel the supervisor")
	}
	select {
	case <-stopped:
		t.Error("ExecuteStop returned while the supervisor's term bind was still blocked")
	case <-time.After(200 * time.Millisecond):
	}

	release()
	select {
	case <-stopped:
		require.NoError(t, stopErr)
	case <-time.After(10 * time.Second):
		t.Fatal("ExecuteStop did not join the supervisor after the term bind unwound")
	}
}
