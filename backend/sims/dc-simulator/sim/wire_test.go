// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/eventtype"
)

// The MQTT wire's bytes go through the platform's own JSON decoder, padded or not: the body
// must decode to exactly one measurement entry carrying the metric that was sent, and the
// padding must be inert.
func TestPaddedMeasurementDecodesThroughTheRealDecoder(t *testing.T) {
	for _, pad := range []int{0, 4096} {
		now := EventTimestamp()
		payload, err := MeasurementPayload(map[string]float64{"speed_kph": 42.5}, now, pad)
		if err != nil {
			t.Fatal(err)
		}
		body, err := MQTTEventBody(DeviceInstance{Token: "dev-1", CredentialId: "secret-cred"}, "Measurement", now, payload)
		if err != nil {
			t.Fatal(err)
		}
		if pad > 0 && len(body) < pad {
			t.Fatalf("pad %d: body is only %d bytes", pad, len(body))
		}
		event, decoded, err := processor.NewJsonDecoder(nil).Decode(body, time.Time{})
		if err != nil {
			t.Fatalf("pad %d: the REAL decoder rejected the MQTT wire's bytes: %v", pad, err)
		}
		if event.EventType != eventtype.Measurement {
			t.Fatalf("pad %d: decoded event type = %v", pad, event.EventType)
		}
		ms, ok := decoded.(*model.UnresolvedMeasurementsPayload)
		if !ok || len(ms.Entries) != 1 {
			t.Fatalf("pad %d: decoded payload %T with entries %v, want exactly 1 measurement entry", pad, decoded, decoded)
		}
		if v := ms.Entries[0].Measurements["speed_kph"]; v != "42.5" {
			t.Fatalf("pad %d: decoded speed_kph = %q, want 42.5", pad, v)
		}
	}
}
