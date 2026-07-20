// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// A consumed message must carry the broker's APPEND TIME, because the ingest
// drain-admission gate meters a tenant against it (ADR-030 I4) and a zero would
// silently fall back to metering at now — re-introducing the exact defect I4
// removes, with every unit test still green.
//
// This is the seam those unit tests cannot see. They construct a Message and set
// AppendTime by hand, so they verify the POLICY given a correct append time and
// would pass identically if nothing ever populated it. Only a real broker can say
// whether it is populated at all, and whether it means what the policy assumes.
func TestConsumedMessageCarriesTheBrokerAppendTime(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	reader, err := nmgr.NewReader(streams.InboundEvents)
	require.NoError(t, err, "durable reader over a real JetStream stream")

	writer, err := nmgr.NewWriter(streams.InboundEvents)
	require.NoError(t, err)

	subject := ScopedSubject(nmgr.Microservice.InstanceId, "acme", streams.InboundEvents)
	require.NoError(t, writer.WriteMessages(core.WithTenant(context.Background(), "acme"),
		Message{Subject: subject, Key: []byte("k"), Value: []byte(`{"telemetry":true}`)}))
	published := time.Now()

	// Deliberately let the message SIT in the stream before reading it. Without this
	// gap the publish and the read are milliseconds apart and no assertion could tell
	// the broker's append time from the time we happened to read it — which is the one
	// distinction the whole slice rests on. A backlog is precisely a message that was
	// written long before it was read, so the test has to reproduce that separation
	// rather than assert against a window both clocks fall inside.
	const lag = 750 * time.Millisecond
	time.Sleep(lag)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err)
	read := time.Now()

	require.False(t, msg.AppendTime.IsZero(),
		"a consumed message carried NO append time; the drain gate would silently meter the "+
			"whole recovered backlog at now and shed it")

	// The append time must sit with the PUBLISH, not with the read. Anything that
	// tracks the read instead — a time.Now() at consume, a header stamped by us —
	// would meter a recovered backlog at now and shed it, so the two must be
	// distinguishable and it must be on the publish side of the gap.
	require.WithinDuration(t, published, msg.AppendTime, lag/2,
		"append time does not track the PUBLISH; a backlog would not replay on its own timeline")
	require.True(t, read.Sub(msg.AppendTime) >= lag/2,
		"append time tracks the READ rather than the write: it advanced with the %s the message "+
			"spent waiting in the stream, which is exactly what a backlog is", lag)
}

// Append times must be non-decreasing in stream order. The drain gate replays a
// backlog against this timeline, so if the broker handed back times that jumped
// backwards the token bucket would forfeit accrual on each reversal and shed a
// compliant tenant anyway — the I4 property would be false for a reason no
// hand-constructed unit test could expose.
func TestAppendTimesAreNonDecreasingInStreamOrder(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	reader, err := nmgr.NewReader(streams.InboundEvents)
	require.NoError(t, err)
	writer, err := nmgr.NewWriter(streams.InboundEvents)
	require.NoError(t, err)

	const count = 25
	subject := ScopedSubject(nmgr.Microservice.InstanceId, "acme", streams.InboundEvents)
	for i := 0; i < count; i++ {
		require.NoError(t, writer.WriteMessages(core.WithTenant(context.Background(), "acme"),
			Message{Subject: subject, Key: []byte("k"), Value: []byte(`{"telemetry":true}`)}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var last time.Time
	for i := 0; i < count; i++ {
		msg, err := reader.ReadMessage(ctx)
		require.NoError(t, err)
		require.False(t, msg.AppendTime.IsZero(), "message %d carried no append time", i)
		if i > 0 {
			require.False(t, msg.AppendTime.Before(last),
				"append time went BACKWARDS at message %d (%s < %s); a replayed backlog would "+
					"forfeit token accrual at every reversal", i, msg.AppendTime, last)
		}
		last = msg.AppendTime
		require.NoError(t, msg.Ack())
	}
}
