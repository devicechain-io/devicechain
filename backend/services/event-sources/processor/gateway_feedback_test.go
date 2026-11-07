// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/messaging"
)

type recordedFeedback struct {
	mu       sync.Mutex
	accepted []string // "tenant/gateway/altId"
	refused  []messaging.GatewayNotice
}

func (r *recordedFeedback) Accepted(tenant, gateway, altId string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accepted = append(r.accepted, tenant+"/"+gateway+"/"+altId)
}

func (r *recordedFeedback) Refused(_, _ string, n messaging.GatewayNotice) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refused = append(r.refused, n)
}

func (r *recordedFeedback) snapshot() ([]string, []messaging.GatewayNotice) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.accepted...), append([]messaging.GatewayNotice(nil), r.refused...)
}

const gatewaySubject = "inst-1.acme.gateways.gw-1.devices.sensor-001.events"

// A gateway event whose payload carries an altId, as the edge agent always stamps one.
func gatewayEvent(altId string) string {
	return `{"altId":"` + altId + `",` + validEvent[1:]
}

// A gateway event durably handled is ACCEPTED, and the acceptance names the key the gateway
// stamped.
func TestAGatewayEventDurablyHandledIsAccepted(t *testing.T) {
	fb := &recordedFeedback{}
	h := newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) { s.UseGatewayFeedback(fb) })
	ack := &recordingAck{}
	h.source.handle(capturedMsg(gatewaySubject, gatewayEvent("edge:i:e:1"), 1, 1, ack))
	require.True(t, ack.settled(t))
	h.quiesce(t, 1)

	accepted, refused := fb.snapshot()
	require.Equal(t, []string{"acme/gw-1/edge:i:e:1"}, accepted)
	require.Empty(t, refused)
}

// An event shed at the message gate is REFUSED over_ceiling and never accepted: it was acked
// off the capture stream, so without the notice the gateway would believe it stored.
func TestAGatewayEventShedAtTheMessageGateIsRefusedNotAccepted(t *testing.T) {
	fb := &recordedFeedback{}
	h := newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) { s.UseGatewayFeedback(fb) })
	h.allowResult = false
	ack := &recordingAck{}
	h.source.handle(capturedMsg(gatewaySubject, gatewayEvent("edge:i:e:2"), 1, 2, ack))
	require.True(t, ack.settled(t))

	accepted, refused := fb.snapshot()
	require.Empty(t, accepted, "a shed event was reported accepted: the gateway would delete the only copy")
	require.Equal(t, []messaging.GatewayNotice{{
		AltId: "edge:i:e:2", Device: "sensor-001", Reason: messaging.GatewayNoticeReasonOverCeiling,
	}}, refused)
}

// An event shed at the READING gate is settled nil like any drop; it must still be refused and
// not accepted.
func TestAGatewayEventShedAtTheReadingGateIsRefusedNotAccepted(t *testing.T) {
	fb := &recordedFeedback{}
	h := newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) { s.UseGatewayFeedback(fb) })
	h.readings = func(string, string, time.Time, bool, Origin, int) bool { return false }
	ack := &recordingAck{}
	h.source.handle(capturedMsg(gatewaySubject, gatewayEvent("edge:i:e:3"), 1, 3, ack))
	require.True(t, ack.settled(t))

	require.Eventually(t, func() bool { _, r := fb.snapshot(); return len(r) == 1 }, 2*time.Second, time.Millisecond)
	accepted, refused := fb.snapshot()
	require.Empty(t, accepted, "a reading-stage shed was reported accepted")
	require.Equal(t, messaging.GatewayNoticeReasonOverCeiling, refused[0].Reason)
}

// A failed publish is not accepted: the capture message is left unacked and redelivered.
func TestAGatewayEventWhosePublishFailedIsNotAccepted(t *testing.T) {
	fb := &recordedFeedback{}
	h := newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) { s.UseGatewayFeedback(fb) })
	h.publishErr = errors.New("inbound-events is refusing writes")
	ack := &recordingAck{}
	h.source.handle(capturedMsg(gatewaySubject, gatewayEvent("edge:i:e:4"), 1, 4, ack))
	h.quiesce(t, 1)

	accepted, refused := fb.snapshot()
	require.Empty(t, accepted)
	require.Empty(t, refused)
}

// A device's own message, or a gateway message with no key to answer to, is owed nothing.
func TestNoAnswerIsOwedForADeviceOrAKeylessEvent(t *testing.T) {
	fb := &recordedFeedback{}
	h := newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) { s.UseGatewayFeedback(fb) })
	own := &recordingAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, 1, 5, own))
	keyless := &recordingAck{}
	h.source.handle(capturedMsg(gatewaySubject, validEvent, 1, 6, keyless))
	require.True(t, own.settled(t))
	require.True(t, keyless.settled(t))
	h.quiesce(t, 2)

	accepted, refused := fb.snapshot()
	require.Empty(t, accepted)
	require.Empty(t, refused)
}
