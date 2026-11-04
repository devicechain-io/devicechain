// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// Through a REAL reader: a member republished alone out of an envelope (Dc-Retry) arrives on
// inbound-events with the deliveries it already used added to its own and with the coordinate it
// came from, and an envelope's marker survives the trip. The unit tests build a Message by hand
// and so cannot see whether the reader ever applies any of it.
func TestTheInboundReaderAppliesTheRetryHeaderAndKeepsTheEnvelopeMarker(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	reader, err := nmgr.NewReader(streams.InboundEvents)
	require.NoError(t, err)
	writer, err := nmgr.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	ctx := core.WithTenant(context.Background(), "acme")
	subject := ScopedSubject(nmgr.Microservice.InstanceId, "acme", streams.InboundEvents)

	require.NoError(t, writer.WriteMessages(ctx, Message{Subject: subject, Value: []byte("copy"),
		Headers: map[string]string{HeaderRetry: RetryHeader(4242, 2, 2)}}))
	require.NoError(t, writer.WriteMessages(ctx, Message{Subject: subject, Value: []byte("plain")}))
	require.NoError(t, writer.WriteMessages(ctx, Message{Subject: subject, Value: []byte("env"),
		Headers: map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}}))

	rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	copyMsg, err := reader.ReadMessage(rctx)
	require.NoError(t, err)
	require.Equal(t, "copy", string(copyMsg.Value))
	require.Equal(t, 3, copyMsg.NumDelivered, "its own first delivery on top of the two it had used")
	seq, idx, ok := copyMsg.SourceCoordinate()
	require.True(t, ok)
	require.Equal(t, uint64(4242), seq)
	require.Equal(t, uint32(2), idx)

	plain, err := reader.ReadMessage(rctx)
	require.NoError(t, err)
	require.Equal(t, 1, plain.NumDelivered)
	_, _, ok = plain.SourceCoordinate()
	require.False(t, ok)

	env, err := reader.ReadMessage(rctx)
	require.NoError(t, err)
	require.True(t, env.IsEnvelope(), "the envelope marker reaches the consumer")
}

// The header means something only on inbound-events: the same header on resolved-events is
// ignored, so no other stream's message can be given a different origin or a smaller budget.
func TestOtherStreamsIgnoreTheRetryHeader(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	reader, err := nmgr.NewReader(streams.ResolvedEvents)
	if err != nil {
		t.Skipf("no reader on resolved-events in the test manager: %v", err)
	}
	writer, err := nmgr.NewWriter(streams.ResolvedEvents)
	if err != nil {
		t.Skipf("no writer on resolved-events in the test manager: %v", err)
	}
	subject := ScopedSubject(nmgr.Microservice.InstanceId, "acme", streams.ResolvedEvents)
	require.NoError(t, writer.WriteMessages(core.WithTenant(context.Background(), "acme"),
		Message{Subject: subject, Value: []byte("x"), Headers: map[string]string{HeaderRetry: RetryHeader(4242, 2, 2)}}))
	rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m, err := reader.ReadMessage(rctx)
	require.NoError(t, err)
	require.Equal(t, 1, m.NumDelivered)
	_, _, ok := m.SourceCoordinate()
	require.False(t, ok)
}
