// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"time"

	"context"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
)

// okPublisher stores nothing and acknowledges every submission at once, on the caller's
// goroutine. It is the publisher of a test whose subject is something other than the publish.
type okPublisher struct{}

func (okPublisher) Submit(_ context.Context, _ []messaging.Message, done func(error)) { done(nil) }
func (okPublisher) Settle(context.Context) error                                      { return nil }

// wirePublisher stores what is submitted through an adapter.EventWriter, synchronously, so
// a test that reads what the shipped receive path WOULD publish still can.
type wirePublisher struct{ w adapter.EventWriter }

func (p wirePublisher) Submit(ctx context.Context, msgs []messaging.Message, done func(error)) {
	done(p.w.WriteMessages(ctx, msgs...))
}
func (wirePublisher) Settle(context.Context) error { return nil }

// newTestClient is NewClient for a test that is not about publishing: a client with an
// ingester gets a publisher that acknowledges at once.
func newTestClient(source config.SparkplugSource, broker Broker, ingester SampleIngester, limiter SampleLimiter,
	now func() time.Time, metrics Metrics) *Client {
	c := NewClient(source, broker, ingester, limiter, now, metrics)
	if ingester != nil {
		c.SetPublisher(okPublisher{})
	}
	return c
}
