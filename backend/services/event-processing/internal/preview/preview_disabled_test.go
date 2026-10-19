// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"
	"time"
)

// presenceMsg is a resolved presence transition for device, marked as a disabled device's
// when disabled is set.
func presenceMsg(t *testing.T, seq uint64, device string, occurred time.Time, disabled bool) messaging.Message {
	t.Helper()
	b, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{
		Source: "mqtt", SourceDeviceToken: device, ProfileVersionToken: "p@1",
		OccurredTime: occurred, ProcessedTime: occurred, EventType: eventtype.StateChange,
		SourceDeviceDisabled: disabled,
		Payload:              &resolvedevent.ResolvedStateChangePayload{State: "DISCONNECTED", SessionId: 9},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, nil)
	m.StreamSeq = seq
	return m
}

// A preview replays what the live loop did, so a disabled device's presence transition is
// skipped here as it is there: not scanned, and feeding nothing. The unmarked transition is
// the control — it IS scanned, so the count is not zero for some other reason.
func TestPreviewSkipsADisabledDevicesTransition(t *testing.T) {
	for _, tc := range []struct {
		disabled bool
		want     int
	}{{false, 1}, {true, 0}} {
		op := &fakeOpener{msgs: []messaging.Message{presenceMsg(t, 1, "d1", base, tc.disabled)}}
		res, err := Run(context.Background(), op, "resolved-events", thresholdReg(t), "acme", "p@1", window(), 0, 0, 0, nil, nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Stats.EventsScanned != tc.want {
			t.Fatalf("disabled=%v: scanned %d, want %d", tc.disabled, res.Stats.EventsScanned, tc.want)
		}
	}
}
