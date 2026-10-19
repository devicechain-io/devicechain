// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"strings"
	"testing"
	"time"
)

// receivedMsg is msg for tenant acme / profile p@1 with the event's ProcessedTime and the message's
// broker append time chosen by the test.
func receivedMsg(t *testing.T, seq uint64, device, value string, occurred, processed, appended time.Time) messaging.Message {
	t.Helper()
	ev := &resolvedevent.ResolvedEvent{
		Source:              "http1",
		SourceDeviceToken:   device,
		ProfileVersionToken: "p@1",
		OccurredTime:        occurred,
		ProcessedTime:       processed,
		EventType:           esmodel.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: occurred,
			Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: value}},
		}}},
	}
	b, err := resolvedevent.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, nil)
	m.StreamSeq = seq
	m.AppendTime = appended
	return m
}

// The preview reads each message's receipt the way the live loop does: a threshold match dated
// behind the frontier raises when its message was stored promptly after the platform received it
// — a device whose clock lags the fleet — and is set aside, and reported, when it was stored a
// redelivery's AckWait later. The preview runs with no lateness, so a receipt budget tied to the
// lateness would set both aside and the preview would say the lagging device never fires.
func TestPreviewThresholdReadsTheMessagesReceipt(t *testing.T) {
	wall := base.Add(30 * time.Minute)
	behind := base.Add(time.Minute)
	op := &fakeOpener{msgs: []messaging.Message{
		receivedMsg(t, 1, "live", "20", wall, wall, wall),
		receivedMsg(t, 2, "lagging", "90", behind, wall, wall.Add(time.Second)),
		receivedMsg(t, 3, "redelivered", "90", behind, behind, wall.Add(time.Minute)),
	}}
	res, err := Run(context.Background(), op, "resolved-events", thresholdReg(t), "acme", "p@1", window(), 0, 0, 0, nil, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Firings) != 1 || res.Firings[0].Series != "lagging" || !res.Firings[0].Raise || !res.Firings[0].OccurredAt.Equal(behind) {
		t.Fatalf("want one RAISE for the lagging device at %v, got %+v", behind, res.Firings)
	}
	if !strings.Contains(res.Degraded, "1 reading") {
		t.Fatalf("degraded = %q, want it to report the redelivered reading set aside", res.Degraded)
	}
}
