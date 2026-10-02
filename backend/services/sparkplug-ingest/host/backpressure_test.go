// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

// A backpressure refusal is dropped at once and counted, not retried. The stream refuses
// because a reader is far behind, which outlasts the retry budget, and every retry holds
// paho's ordered receive goroutine: before, each refused message cost ingestMaxAttempts
// attempts and their backoff before it was dropped anyway.
func TestIngestSamplesDoesNotRetryBackpressure(t *testing.T) {
	failures := prometheus.NewCounter(prometheus.CounterOpts{Name: "ingest_failures"})
	refusal := fmt.Errorf("emit: %w", &messaging.BackpressureError{Stream: "s", Durable: "d", Ratio: 0.93})
	fake := &fakeIngester{err: refusal}
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h"}, Broker{}, fake, admitAllSamples{}, fixedNow,
		Metrics{IngestFailures: failures})

	c.ingestSamples("g/n", []Sample{{Name: "t", Value: 1, Time: 1}})

	assert.Equal(t, 1, fake.count(), "a backpressure refusal must be attempted once, not retried")
	assert.Equal(t, float64(1), testutil.ToFloat64(failures), "the drop is counted")
}
