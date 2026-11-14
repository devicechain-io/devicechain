// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type flightAck struct {
	fakeAck
	progress func() error
}

func (a *flightAck) InProgress() error { return a.progress() }

func TestSingleTenantDispatchUsesSpareCapacity(t *testing.T) {
	srv, hits := countingServer(t)
	c := newTestConsumer(&fakeWriter{}, &fakeSecretStore{})
	require.True(t, c.inFlight.TryAcquire("acme"))
	ack := &flightAck{progress: func() error {
		t.Fatal("a lone tenant must not be throttled")
		return nil
	}}
	c.handle(context.Background(), wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL}}, ack))
	require.True(t, ack.acked)
	require.EqualValues(t, 1, hits.Load())
}

func TestThrottledDispatchKeepsDeliveryOrDeadLetters(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "resumes", true: "final_progress_failure"}[fail], func(t *testing.T) {
			srv, hits := countingServer(t)
			dead := &fakeWriter{}
			c := newTestConsumer(dead, &fakeSecretStore{})
			c.metrics = &DispatchMetrics{dispatched: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "flight_test"}, []string{"action", "outcome"})}
			require.True(t, c.inFlight.TryAcquire("acme"))
			require.True(t, c.inFlight.TryAcquire("other"))
			beats := 0
			ack := &flightAck{progress: func() error {
				beats++
				require.Zero(t, hits.Load(), "must not send before capacity is available")
				if fail {
					return errors.New("broker unavailable")
				}
				c.inFlight.Release("other")
				return nil
			}}
			msg := wireMsg(t, dispatchSubject, messaging.MaxDeliver, &connectorwire.ConnectorDispatchRequest{
				Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
				HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL}}, ack)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c.procCtx = ctx
			c.handle(ctx, msg)
			require.Positive(t, beats, "must renew AckWait on the current delivery")
			require.True(t, ack.acked)
			require.EqualValues(t, 1, testutil.ToFloat64(c.metrics.dispatched.WithLabelValues("httpCall", "throttled")))
			if fail {
				require.Zero(t, hits.Load())
				require.Len(t, dead.written(), 1)
				require.Equal(t, msg.Value, dead.written()[0].Value)
			} else {
				require.EqualValues(t, 1, hits.Load())
				require.Empty(t, dead.written())
			}
		})
	}
}
