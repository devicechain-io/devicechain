// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// A22. AN INTEGRATION UPLINK IS NEVER WRITTEN WHERE A PRE-DECODER DEVICE-MANAGEMENT READS.
//
// A device-management built before payload decoders unmarshals every message on inbound-events
// before any resolver guard runs; an event type it does not know is dead-lettered as Invalid and
// acked on delivery 1, never redelivered to a newer replica. So the carrier goes to a stream and a
// durable only a decoder-capable build creates, and an old replica never sees it. This asserts the
// publish the route is wired to, not the route's own tests, which hand it a publish of their own.

type recordingWriter struct {
	mu   sync.Mutex
	msgs []messaging.Message
	err  error
}

func (w *recordingWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, msgs...)
	return w.err
}

func (w *recordingWriter) WriteToDevice(context.Context, string, ...messaging.Message) error {
	return nil
}
func (w *recordingWriter) HandleResponse(error) {}

func (w *recordingWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.msgs)
}

func TestAnIntegrationUplinkIsWrittenToItsOwnStreamAndNeverToInboundEvents(t *testing.T) {
	sealingFixture(t)
	savedInbound, savedUplinks := InboundEventsWriter, IntegrationUplinksWriter
	t.Cleanup(func() { InboundEventsWriter, IntegrationUplinksWriter = savedInbound, savedUplinks })
	inbound, uplinks := &recordingWriter{}, &recordingWriter{}
	InboundEventsWriter, IntegrationUplinksWriter = inbound, uplinks

	uplink := integrationEvent()
	uplink.EventType = eventtype.IntegrationUplink
	require.NoError(t, onIntegrationEventDecoded("integration:cs", "acme", uplink,
		&model.UnresolvedIntegrationUplinkPayload{Frame: []byte{1}, MaxReadings: 3}, "acme:cs:1"))
	require.Zero(t, inbound.count(), "an integration uplink was written to inbound-events, where a device-management "+
		"built before payload decoders would dead-letter it on delivery 1")
	require.Equal(t, 1, uplinks.count())

	decoded, err := esproto.UnmarshalUnresolvedEvent(uplinks.msgs[0].Value)
	require.NoError(t, err)
	require.Equal(t, eventtype.IntegrationUplink, decoded.EventType)
	require.Equal(t, "acme:cs:1", uplinks.msgs[0].DedupID)
	require.Equal(t, "cs", decoded.Integration, "the carrier keeps the route's marks")

	// The paired positive: a measurement from the same route still goes to inbound-events and
	// never to the new stream, so a decoder-less integration behaves exactly as before.
	require.NoError(t, onIntegrationEventDecoded("integration:cs", "acme", integrationEvent(),
		&model.UnresolvedMeasurementsPayload{}, "acme:cs:2"))
	require.Equal(t, 1, inbound.count())
	require.Equal(t, 1, uplinks.count())
}

// A backpressure refusal on the new stream is counted and returned like inbound-events', and a
// build that never created the writer fails loudly instead of dropping the uplink.
func TestAnIntegrationUplinkWriteFailureIsReturnedNotSwallowed(t *testing.T) {
	sealingFixture(t)
	savedUplinks := IntegrationUplinksWriter
	t.Cleanup(func() { IntegrationUplinksWriter = savedUplinks })

	uplink := integrationEvent()
	uplink.EventType = eventtype.IntegrationUplink
	payload := &model.UnresolvedIntegrationUplinkPayload{MaxReadings: 1}

	IntegrationUplinksWriter = nil
	require.Error(t, onIntegrationEventDecoded("integration:cs", "acme", uplink, payload, "a"))

	IntegrationUplinksWriter = &recordingWriter{err: messaging.ErrStreamBackpressure}
	err := onIntegrationEventDecoded("integration:cs", "acme", uplink, payload, "a")
	require.ErrorIs(t, err, messaging.ErrStreamBackpressure)
}

// The subject the writer publishes to, and the subject inbound-events captures, cannot overlap:
// the stream is declared as its own, written by event-sources and read by device-management.
func TestTheIntegrationUplinkStreamIsDeclaredAppendOnlyAndSeparately(t *testing.T) {
	var uplinks, inbound *streams.Stream
	for i := range streams.All {
		switch streams.All[i].Suffix {
		case streams.IntegrationUplinks:
			uplinks = &streams.All[i]
		case streams.InboundEvents:
			inbound = &streams.All[i]
		}
	}
	require.NotNil(t, uplinks)
	require.NotNil(t, inbound)
	require.NotEqual(t, inbound.Suffix, uplinks.Suffix)
	require.Equal(t, streams.ShapeTenant, uplinks.Shape)
	require.Equal(t, []string{"event-sources"}, uplinks.Writers)
	require.NotContains(t, uplinks.Writers, "device-management", "device-management only reads it")
	require.NotContains(t, inbound.Writers, "integration-uplinks")
	require.Equal(t, "integration-uplinks", streams.IntegrationUplinks)
}

func TestTheDecoderSeriesAreExportedAtZeroAndCarryNoTenantOrIntegration(t *testing.T) {
	_, registry := buildHttpSourceFor(t, integrationInfra())
	families, err := registry.Gather()
	require.NoError(t, err)
	byName := map[string]int{}
	for _, f := range families {
		byName[f.GetName()] = len(f.GetMetric())
		if !strings.HasPrefix(f.GetName(), "devicechain_eventsources_integration_") {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				require.NotContains(t, []string{"tenant", "integration", "device", "token"}, l.GetName(),
					"%s carries a label a sender chose before its credential was checked", f.GetName())
			}
		}
	}
	require.Equal(t, len(processor.IntegrationDecoderLookupResults), byName["devicechain_eventsources_integration_decoder_lookups_total"])
	require.Equal(t, 1, byName["devicechain_eventsources_integration_decoder_probe_unsupported_total"])
	require.Equal(t, 1, byName["devicechain_eventsources_integration_decoder_charged_readings_total"])
}
