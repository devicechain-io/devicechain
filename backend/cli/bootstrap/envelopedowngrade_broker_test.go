// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// The downgrade guard against a broker that ENFORCES the generated permissions, reading as the
// broker administrator: the seam-based tests cannot see a grant the login lacks, and a denied read
// is a silent drop that surfaces as a timeout.

func seedEnvelope(t *testing.T, b *permtest.Broker, instance string) {
	t.Helper()
	js, err := b.Connect(t, "event-sources").JetStream()
	require.NoError(t, err)
	subject := messaging.StreamSubject(instance, streams.InboundEvents)
	for i := 0; i < 3; i++ {
		m := nats.NewMsg(subject)
		m.Data = []byte("x")
		if i == 1 {
			m.Header.Set(eventenvelope.HeaderName, eventenvelope.HeaderValue)
		}
		_, err := js.PublishMsg(m)
		require.NoError(t, err)
	}
}

func adminJS(t *testing.T, b *permtest.Broker) nats.JetStreamContext {
	t.Helper()
	nc, err := dialBrokerCtx(context.Background(), fmt.Sprintf("nats://%s:%d", b.Host, b.Port), administratorView(b))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	return js
}

func TestTheGuardReadsTheHotStreamsAsTheBrokerAdministrator(t *testing.T) {
	const instance = "envguard"
	b := permtest.Start(t, instance)
	seedEnvelope(t, b, instance)

	res, err := sampleStreams(context.Background(), adminJS(t, b), instance)
	require.NoError(t, err, "the administrator cannot read the hot streams")
	require.Equal(t, streamBounds{First: 1, Last: 3}, res.Bounds[streams.InboundEvents], "oldest/newest sequence")
	require.Contains(t, res.Sample, "inbound-events (message 2)", "the envelope in the middle was not found")
	b.RequireNoViolations(t)
}

// The negative control: without the grant the same call is a denied read, named as such, and the
// broker logs the violation, so the passing test above is the grant's doing.
func TestTheGuardNamesADeniedReadNotATimeout(t *testing.T) {
	const instance = "envguardno"
	name := messaging.StreamName(instance, streams.InboundEvents)
	b := permtest.Start(t, instance, permtest.Without("$JS.API.STREAM.MSG.GET."+name))
	seedEnvelope(t, b, instance)

	prev := envelopeReadTimeout
	envelopeReadTimeout = time.Second
	t.Cleanup(func() { envelopeReadTimeout = prev })

	_, err := sampleStreams(context.Background(), adminJS(t, b), instance)
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
	require.Contains(t, err.Error(), "STREAM.MSG.GET."+name)
	require.NotEmpty(t, b.Violations(), "the broker did not refuse the read, so it was not what failed")
}
