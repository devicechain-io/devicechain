// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/hex"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"
	"time"
)

// legacyResolvedMeasurementHex is a resolved measurement as device-management wrote it before
// each instant became a count of nanoseconds, byte for byte (origin/main 9b680acf6; the same
// message core/resolvedevent/legacy_wire_test.go decodes). Its envelope carries the
// device's +02:00 offset.
const legacyResolvedMeasurementHex = "0a04687474701a1164657669636570756c73652d303030303122150a08637573746f6d6572120761636d652d636f18053a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030421e323032362d31302d31365430393a33303a31352e3132353030303030375a4802523f0a3d0a1b0a0474656d70120432312e351801220343656c2a06444f55424c45121e323032362d31302d31365430393a33303a31342e3938373635343332315a5a0b64657669636570756c7365620770756c7365403372156578742d64657669636570756c73652d3030303031"

// The live detection loop takes a legacy message, still on the stream when the release that
// writes counts starts, as it takes the same event in the current format: decoded, not
// skipped as poison, and advancing the shared watermark to the same instant.
func TestDetectionTakesALegacyMessageAsTheCurrentFormat(t *testing.T) {
	legacy, err := hex.DecodeString(legacyResolvedMeasurementHex)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := resolvedevent.UnmarshalResolvedEvent(legacy)
	if err != nil {
		t.Fatal(err)
	}
	current, err := resolvedevent.MarshalResolvedEventAs(ev, eventtime.WireCompact)
	if err != nil {
		t.Fatal(err)
	}

	watermarkAfter := func(body []byte) time.Time {
		ctx := context.Background()
		rp := newTestProcessor(newTestStore(t), nil, 100)
		if err := rp.restore(ctx); err != nil {
			t.Fatalf("restore: %v", err)
		}
		before := rp.engine.Watermark()
		ack := &fakeAck{}
		m := messaging.NewConsumedMessage(testSubject, body, 0, nil, ack)
		m.StreamSeq = 1
		registerPending(t, ack, 1)
		rp.handle(m)
		if got := rp.engine.LastSeq(); got != 1 {
			t.Fatalf("lastSeq = %d, want 1", got)
		}
		if rp.engine.Watermark().Equal(before) {
			t.Fatalf("the watermark did not move: the message was skipped, not evaluated")
		}
		return rp.engine.Watermark()
	}
	fromLegacy, fromCurrent := watermarkAfter(legacy), watermarkAfter(current)
	if !fromLegacy.Equal(fromCurrent) {
		t.Fatalf("a legacy message moves the watermark to %s; the current format to %s", fromLegacy, fromCurrent)
	}
}
