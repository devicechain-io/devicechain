// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// TestAbandonedFinalDeliveryIsDeadLettered is the defect this package's assembly closes: a
// message whose every delivery ran out with NO outcome — a pod stopped mid-handling, a handler
// that ran past its window — used to leave no record anywhere. The broker stopped redelivering
// it, no arm ran (an arm runs only when a handler REACHES its final-delivery branch), and it aged
// out of its stream unrecorded.
//
// It is driven through service.New, the assembly every adopting service uses, with nothing wired
// by hand: a service that builds a reader through its Spec gets the record without asking.
//
// The letter is read back with the raw JetStream client and asserted on literals, so what it
// checks is what an operator would find on the stream, not what this package's own types say.
//
// 🔴 THE PULLER KEEPS PULLING AFTER THE FIFTH DELIVERY. The broker sends its max-delivery
// advisory on the NEXT pull of the durable after the final delivery's ack window expires — not
// from a timer — so a test that stopped reading would be testing a broker that never speaks.
func TestAbandonedFinalDeliveryIsDeadLettered(t *testing.T) {
	ephemeralProbes(t)
	host, port := startEmbeddedNats(t)

	ms := testMicroservice(t)
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	ms.Readiness.MarkReadyWithoutAuthSurface()

	var reader messaging.MessageReader
	svc := New(ms, Spec{Nats: &NatsSpec{OnCreate: func(n *messaging.NatsManager) error {
		r, err := n.NewReader(streams.RaiseAlarm)
		reader = r
		return err
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.Initialize(ctx))
	svc.Nats.SetAckWaitForTesting(t, time.Second)
	require.NoError(t, svc.Start(ctx))
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = svc.Stop(stopCtx)
		_ = svc.Terminate(stopCtx)
	})

	js, err := svc.Nats.Conn().JetStream()
	require.NoError(t, err)
	subject := messaging.ScopedSubject(ms.InstanceId, "acme", streams.RaiseAlarm)
	body := []byte(`{"alarmKey":"overheat","raise":true}`)
	pub := &nats.Msg{Subject: subject, Data: body, Header: nats.Header{}}
	pub.Header.Set(messaging.HeaderCorrelationID, "corr-abandoned")
	ack, err := js.PublishMsg(pub)
	require.NoError(t, err)

	// Five deliveries, none acked: every one is abandoned exactly as a pod killed mid-handling
	// abandons it.
	for i := 1; i <= messaging.MaxDeliver; i++ {
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		msg, err := reader.ReadMessage(rctx)
		rcancel()
		require.NoError(t, err, "delivery %d never arrived", i)
		require.Equal(t, i, msg.NumDelivered)
	}
	// ...and then keep a puller on the durable, as a live replica would.
	pullCtx, stopPulling := context.WithCancel(ctx)
	var pulling sync.WaitGroup
	pulling.Add(1)
	go func() {
		defer pulling.Done()
		for pullCtx.Err() == nil {
			if m, err := reader.ReadMessage(pullCtx); err == nil {
				_ = m.Ack()
			}
		}
	}()
	defer func() { stopPulling(); pulling.Wait() }()

	deadStream := messaging.StreamName(ms.InstanceId, streams.DeadLetters)
	var held uint64
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		info, err := js.StreamInfo(deadStream)
		if err == nil {
			held = info.State.Msgs
			if held > 0 {
				break
			}
		} else if !errors.Is(err, nats.ErrStreamNotFound) {
			require.NoError(t, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.EqualValues(t, 1, held,
		"a message whose five deliveries all ran out with no outcome must be dead-lettered exactly once")
	// A short settle, then the count again: a second letter would mean the two record paths
	// did not dedupe.
	time.Sleep(1500 * time.Millisecond)
	info, err := js.StreamInfo(deadStream)
	require.NoError(t, err)
	require.EqualValues(t, 1, info.State.Msgs, "the abandoned message was lettered more than once")

	raw, err := js.GetMsg(deadStream, info.State.FirstSeq)
	require.NoError(t, err)
	var letter struct {
		Kind        string `json:"kind"`
		Reason      string `json:"reason"`
		Source      string `json:"source"`
		Attempts    int    `json:"attempts"`
		Subject     string `json:"subject"`
		Sequence    uint64 `json:"sequence"`
		Correlation string `json:"correlation"`
		Payload     []byte `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(raw.Data, &letter))
	require.Equal(t, "detection-action", letter.Kind)
	require.Equal(t, "no-outcome", letter.Reason)
	require.Equal(t, ms.FunctionalArea, letter.Source)
	require.Equal(t, messaging.MaxDeliver, letter.Attempts)
	require.Equal(t, subject, letter.Subject)
	require.Equal(t, ack.Sequence, letter.Sequence)
	require.Equal(t, "corr-abandoned", letter.Correlation)
	require.Equal(t, body, letter.Payload, "raise-alarm is a cold stream, so its letter carries the original")
	require.Equal(t, messaging.ScopedSubject(ms.InstanceId, "acme", streams.DeadLetters), raw.Subject)

	stream := messaging.StreamName(ms.InstanceId, streams.RaiseAlarm)
	durable := messaging.DurableName(ms.InstanceId, ms.FunctionalArea, streams.RaiseAlarm)
	require.Equal(t, "mdl."+stream+"."+durable+".1", raw.Header.Get(nats.MsgIdHdr),
		"the letter's dedup id must be the one an in-handler arm derives for the same delivery")
	require.EqualValues(t, 1, ack.Sequence)
}
