// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"bytes"
	"context"
	"encoding/hex"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// legacyResolvedMeasurementHex is a resolved measurement as device-management wrote it before
// each instant became a count of nanoseconds, byte for byte (origin/main 9b680acf6; the same
// message core/resolvedevent/legacy_wire_test.go decodes): devicepulse-00001 under
// profile version pulse@3 reports temp 21.5 at 2026-10-16T09:30:14.987654321Z, with an
// envelope carrying the device's +02:00 offset.
const legacyResolvedMeasurementHex = "0a04687474701a1164657669636570756c73652d303030303122150a08637573746f6d6572120761636d652d636f18053a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030421e323032362d31302d31365430393a33303a31352e3132353030303030375a4802523f0a3d0a1b0a0474656d70120432312e351801220343656c2a06444f55424c45121e323032362d31302d31365430393a33303a31342e3938373635343332315a5a0b64657669636570756c7365620770756c7365403372156578742d64657669636570756c73652d3030303031"

// THE PREVIEW REPLAYS HISTORY, and history spans the upgrade: a window that opens before it
// reads messages in the string form and then messages in the count form, off one retained
// stream. Both must evaluate, at their own instants: the legacy reading raises, the
// current-format one a minute later resolves.
func TestPreviewReplaysAWindowThatSpansTheFormatChange(t *testing.T) {
	replayAcrossTheFormatChange(t, func(ev *resolvedevent.ResolvedEvent) ([]byte, error) {
		return resolvedevent.MarshalResolvedEventAs(ev, eventtime.WireCompact)
	})
}

// The same window, with the later message written the way a device-management that sets
// no wireFormat writes it once the default is compact: what a retained stream holds after
// an upgrade that flips the default, legacy messages from before it and default-written
// ones after. The legacy message must still decode and evaluate.
func TestPreviewReplaysLegacyMessagesWrittenBeforeTheDefaultBecameCompact(t *testing.T) {
	require.Equal(t, eventtime.WireCompact, eventtime.ProducerWireFormat(), "the default producer format")
	replayAcrossTheFormatChange(t, resolvedevent.MarshalResolvedEvent)
}

func replayAcrossTheFormatChange(t *testing.T, writeCurrent func(*resolvedevent.ResolvedEvent) ([]byte, error)) {
	t.Helper()
	legacy, err := hex.DecodeString(legacyResolvedMeasurementHex)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := resolvedevent.UnmarshalResolvedEvent(legacy)
	if err != nil {
		t.Fatal(err)
	}
	sampleAt := time.Date(2026, 10, 16, 9, 30, 14, 987654321, time.UTC)
	later := *ev
	later.OccurredTime, later.ProcessedTime = ev.OccurredTime.Add(time.Minute), ev.ProcessedTime.Add(time.Minute)
	later.Payload = &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
		OccurredTime: sampleAt.Add(time.Minute),
		Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "temp", Value: "10"}},
	}}}
	current, err := writeCurrent(&later)
	if err != nil {
		t.Fatal(err)
	}
	// The stream must really hold both forms: the later message is not in the string form.
	asLegacy, err := resolvedevent.MarshalResolvedEventAs(&later, eventtime.WireLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(asLegacy, current) {
		t.Fatal("the later message was written in the legacy form; the window would not span the change")
	}
	at := func(seq uint64, b []byte) messaging.Message {
		m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, nil)
		m.StreamSeq = seq
		return m
	}

	thr := 20.0
	cr, err := rules0.Compile(rules0.Rule{
		ID: "acme/pulse@3/hot", Name: "hot", Type: rules0.TypeThreshold, Severity: rules0.SeverityCritical,
		When: rules0.Condition{Metric: "temp", Op: rules0.OpGt, Threshold: &thr},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "pulse@3", Compiled: cr}})

	op := &fakeOpener{msgs: []messaging.Message{at(1, legacy), at(2, current)}}
	window := TimeRange{Start: sampleAt.Add(-time.Hour), End: sampleAt.Add(time.Hour)}
	res, err := Run(context.Background(), op, "resolved-events", reg, "acme", "pulse@3", window, 0, 0, 0, nil, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.EventsScanned != 2 || len(res.Firings) != 2 {
		t.Fatalf("scanned %d, firings %+v; want both messages scanned, a raise and a resolve", res.Stats.EventsScanned, res.Firings)
	}
	if !res.Firings[0].Raise || !res.Firings[0].OccurredAt.Equal(sampleAt) || res.Firings[0].Value != 21.5 {
		t.Fatalf("firing[0] = %+v; want the legacy reading's RAISE at %s", res.Firings[0], sampleAt)
	}
	if res.Firings[1].Raise || !res.Firings[1].OccurredAt.Equal(sampleAt.Add(time.Minute)) {
		t.Fatalf("firing[1] = %+v; want the current-format reading's RESOLVE a minute later", res.Firings[1])
	}
}
