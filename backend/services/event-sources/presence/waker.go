// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"fmt"
	"sync"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Waker tells command-delivery that a device has come back, so the commands withheld
// for it can return to the delivery queue.
//
// It exists because command-delivery cannot know. The presence gate withholds a command
// when a transport reports its device absent, and the transport that owns the connection
// is the only thing that learns of the return at the instant it happens. Without a wake,
// the only way out of HELD is command-delivery's periodic reconcile pass — which walks
// the whole withheld set a page at a time, so a device's backlog can wait minutes after
// it is back. The wake makes that a round trip.
type Waker interface {
	// Wake asks for one device's withheld commands to be released. It does not block on
	// the release itself.
	Wake(tenant, deviceToken string)
}

// Releaser performs the release. Narrow on purpose so the queue can be tested with no
// HTTP and no command-delivery.
type Releaser interface {
	ReleaseHeld(ctx context.Context, tenant, deviceToken string) (int, error)
}

// WakerMetrics measures a path whose failures are all silent.
type WakerMetrics struct {
	// Requested counts wakes accepted onto the queue.
	Requested prometheus.Counter
	// Dropped counts wakes discarded because the queue was full. 🔑 THIS IS A LATENCY
	// SIGNAL, NOT AN ERROR RATE — see the drop policy on Wake. A standing rate means
	// held commands are waiting for the reconcile pass instead of the wake, which is
	// slower but still correct.
	Dropped prometheus.Counter
	// Failed counts release calls that errored. Same reading: the reconciler covers it.
	Failed prometheus.Counter
	// Released counts commands actually returned to the queue, which is the only
	// number here that says the feature is doing anything at all.
	Released prometheus.Counter
}

func incrCounter(c prometheus.Counter, n float64) {
	if c != nil {
		c.Add(n)
	}
}

// wakeRequest is one device's return.
type wakeRequest struct {
	tenant      string
	deviceToken string
}

// queueingWaker is a bounded queue drained by a small pool of workers.
type queueingWaker struct {
	releaser Releaser
	queue    chan wakeRequest
	metrics  WakerMetrics
	wg       sync.WaitGroup
	stop     chan struct{}
	stopOnce sync.Once
}

// WakerQueueDepth and WakerWorkers size the queue.
//
// 🔴 SIZED FOR A RECONNECT STORM, WHICH IS THE ONLY LOAD THAT MATTERS HERE. Steady state
// is a trickle — devices connect one at a time and most have nothing withheld. The shape
// to survive is every device reconnecting at once: a broker restart, a network partition
// healing, or a site coming back on line in the morning. That arrives as a burst of
// advisories on the tap's own consumer path, and the wake must not turn each one into a
// blocking round trip to another service.
//
// The worker count is small deliberately. Each wake is a single indexed UPDATE, so more
// workers would mostly add concurrent writers to one table.
const (
	WakerQueueDepth = 1024
	WakerWorkers    = 4
)

// NewWaker starts a waker and returns it with its stop function.
func NewWaker(releaser Releaser, metrics WakerMetrics) (Waker, func()) {
	w := &queueingWaker{
		releaser: releaser,
		queue:    make(chan wakeRequest, WakerQueueDepth),
		metrics:  metrics,
		stop:     make(chan struct{}),
	}
	for i := 0; i < WakerWorkers; i++ {
		w.wg.Add(1)
		go w.run()
	}
	return w, w.Stop
}

// Wake enqueues a release, dropping it if the queue is full.
//
// 🔑 DROPPING IS A DELIBERATE DESIGN CHOICE AND IT IS ONLY SAFE BECAUSE THE RECONCILER
// EXISTS. A dropped wake does not lose the command: the row stays HELD, and
// command-delivery's reconcile pass releases it on a later sweep. So the cost of a drop
// is latency — minutes instead of seconds — and the cost of NOT dropping would be
// backpressure onto the broker-advisory consumer, where it would delay presence itself.
// Presence being late is worse than a command being late, because presence is what the
// gate reads to make the decision in the first place.
//
// It never blocks and never returns an error, for the same reason: the caller is the
// advisory path, and there is nothing useful it could do with either.
func (w *queueingWaker) Wake(tenant, deviceToken string) {
	select {
	case w.queue <- wakeRequest{tenant: tenant, deviceToken: deviceToken}:
		incrCounter(w.metrics.Requested, 1)
	default:
		incrCounter(w.metrics.Dropped, 1)
	}
}

// Stop drains the workers. Safe to call more than once.
func (w *queueingWaker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stop)
		w.wg.Wait()
	})
}

func (w *queueingWaker) run() {
	defer w.wg.Done()
	for {
		select {
		case <-w.stop:
			return
		case req := <-w.queue:
			w.release(req)
		}
	}
}

// release performs one wake. Every failure is logged at debug and counted, never
// retried: a retry loop here would hold a worker against an outage while the queue
// behind it filled and started dropping, converting one service's unavailability into
// lost wakes for every OTHER device. The reconciler is the retry.
func (w *queueingWaker) release(req wakeRequest) {
	ctx := core.WithTenant(context.Background(), req.tenant)
	released, err := w.releaser.ReleaseHeld(ctx, req.tenant, req.deviceToken)
	if err != nil {
		incrCounter(w.metrics.Failed, 1)
		log.Debug().Err(err).Str("tenant", req.tenant).Str("device", req.deviceToken).
			Msg("Could not release a returning device's withheld commands; the periodic reconcile will.")
		return
	}
	if released == 0 {
		return
	}
	incrCounter(w.metrics.Released, float64(released))
	log.Info().Str("tenant", req.tenant).Str("device", req.deviceToken).Int("commands", released).
		Msg("A device came back; its withheld commands were returned to the delivery queue.")
}

// graphqlReleaser calls command-delivery's releaseHeldCommands mutation.
type graphqlReleaser struct {
	client GraphQLClient
	url    string
}

// GraphQLClient is the svcclient seam.
type GraphQLClient interface {
	Query(ctx context.Context, url, tenant, query string, vars map[string]any, out any) error
}

// NewGraphQLReleaser binds a releaser to command-delivery's GraphQL endpoint.
func NewGraphQLReleaser(client GraphQLClient, url string) Releaser {
	return &graphqlReleaser{client: client, url: url}
}

const releaseMutation = `mutation($deviceToken: String!) {
  releaseHeldCommands(deviceToken: $deviceToken)
}`

func (r *graphqlReleaser) ReleaseHeld(ctx context.Context, tenant, deviceToken string) (int, error) {
	if tenant == "" {
		return 0, fmt.Errorf("a wake needs a tenant")
	}
	var resp struct {
		ReleaseHeldCommands int32 `json:"releaseHeldCommands"`
	}
	if err := r.client.Query(ctx, r.url, tenant, releaseMutation,
		map[string]any{"deviceToken": deviceToken}, &resp); err != nil {
		return 0, err
	}
	return int(resp.ReleaseHeldCommands), nil
}
