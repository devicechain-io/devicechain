// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/hex"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"
	"time"
)

// Resolved events as device-management wrote them before each instant became a count of
// nanoseconds, byte for byte (origin/main 9b680acf6; the same messages
// core/resolvedevent/legacy_wire_test.go decodes): a measurement from a device with an
// external id, and a location. Their envelopes carry the device's +02:00 offset. Such
// messages are on the retained resolved stream across an upgrade.
const (
	legacyMeasurementHex = "0a04687474701a1164657669636570756c73652d303030303122150a08637573746f6d6572120761636d652d636f18053a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030421e323032362d31302d31365430393a33303a31352e3132353030303030375a4802523f0a3d0a1b0a0474656d70120432312e351801220343656c2a06444f55424c45121e323032362d31302d31365430393a33303a31342e3938373635343332315a5a0b64657669636570756c7365620770756c7365403372156578742d64657669636570756c73652d3030303031"
	legacyLocationHex    = "0a046d7174741a0a73656e736f722d3030313a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030421e323032362d31302d31365430393a33303a31352e3132353030303030375a4801522f0a2d0a0434352e3512052d37332e36221e323032362d31302d31365430393a33303a31342e3938373635343332315a5a07747261636b6572620574726b40317804"
)

// The projection lands the same rows from a legacy message as from the same event written
// in the current format: the latest value at the sample's instant, the last position, the
// device's activity time and its external id, which the presence projection keys a device
// by and which is why the resolved event still carries it.
func TestALegacyResolvedMessageProjectsAsTheCurrentFormatDoes(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "tenant1")
	// The store keeps microseconds, so that is the precision a projected instant reads back at.
	entryAt := time.Date(2026, 10, 16, 9, 30, 14, 987654321, time.UTC).Truncate(time.Microsecond)

	type projected struct {
		value      float64
		measuredAt time.Time
		positionAt time.Time
		activityAt time.Time
		externalId string
		latitude   float64
	}
	project := func(encode func([]byte) []byte) projected {
		sp := newLocationProcessor(t)
		for _, h := range []string{legacyMeasurementHex, legacyLocationHex} {
			raw, err := hex.DecodeString(h)
			if err != nil {
				t.Fatal(err)
			}
			sp.mergeOne(context.Background(), messaging.Message{Subject: locationTestSubject, Value: encode(raw)})
		}
		mx := loadProjectedMeasurement(t, sp, ctx, "devicepulse-00001", "temp")
		pulse := loadState(t, sp, ctx, "devicepulse-00001")
		pos := loadProjectedLocation(t, sp, ctx, "sensor-001")
		return projected{value: mx.Value.Float64, measuredAt: mx.OccurredTime.UTC(), positionAt: pos.OccurredTime.UTC(),
			activityAt: pulse.LastActivityTime.Time.UTC(), externalId: pulse.ExternalId, latitude: pos.Latitude.Float64}
	}

	asWritten := func(raw []byte) []byte { return raw }
	reencoded := func(raw []byte) []byte {
		ev, err := resolvedevent.UnmarshalResolvedEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		current, err := resolvedevent.MarshalResolvedEventAs(ev, eventtime.WireCompact)
		if err != nil {
			t.Fatal(err)
		}
		if len(current) >= len(raw) {
			t.Fatalf("the current format is %d bytes against the legacy %d", len(current), len(raw))
		}
		return current
	}

	fromLegacy, fromCurrent := project(asWritten), project(reencoded)
	if fromLegacy != fromCurrent {
		t.Fatalf("a legacy message projects %+v; the current format %+v", fromLegacy, fromCurrent)
	}
	if fromLegacy.value != 21.5 || !fromLegacy.measuredAt.Equal(entryAt) || !fromLegacy.positionAt.Equal(entryAt) ||
		fromLegacy.externalId != "ext-devicepulse-00001" {
		t.Fatalf("the legacy message projected %+v", fromLegacy)
	}
}
