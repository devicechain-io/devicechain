// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

// refusingPublisher answers every submission with err, on the caller's goroutine, and counts
// them: the stream's gate refusing a call, as the AsyncWriter built to refuse reports it.
type refusingPublisher struct {
	err   error
	calls atomic.Int32
}

func (p *refusingPublisher) Submit(_ context.Context, _ []messaging.Message, done func(error)) {
	p.calls.Add(1)
	done(p.err)
}
func (*refusingPublisher) Settle(context.Context) error { return nil }

// A backpressure refusal is dropped at once and counted, not retried. The stream refuses
// because a reader is far behind, which outlasts the retry budget, and a resubmission would
// only be refused again: before, each refused message cost ingestMaxAttempts attempts and
// their backoff before it was dropped anyway. The refusal now arrives from the publish, so it
// is delivered where it does: through the publisher.
func TestIngestSamplesDoesNotRetryBackpressure(t *testing.T) {
	failures := prometheus.NewCounter(prometheus.CounterOpts{Name: "ingest_failures"})
	refusal := fmt.Errorf("emit: %w", &messaging.BackpressureError{Stream: "s", Durable: "d", Ratio: 0.93})
	pub := &refusingPublisher{err: refusal}
	c := newTestClient(config.SparkplugSource{Tenant: "acme", HostId: "h"}, Broker{}, &fakeIngester{}, admitAllSamples{}, fixedNow,
		Metrics{IngestFailures: failures})
	c.SetPublisher(pub)
	c.Connect()
	t.Cleanup(c.Stop)

	c.ingestSamples("g/n", []Sample{{Name: "t", Value: 1, Time: 1}})

	assert.Equal(t, int32(1), pub.calls.Load(), "a backpressure refusal must be attempted once, not resubmitted")
	assert.Equal(t, float64(1), testutil.ToFloat64(failures), "the drop is counted")
	time.Sleep(2 * ingestRetryBackoff)
	assert.Equal(t, int32(1), pub.calls.Load(), "and it is not resubmitted after the backoff either")
}
