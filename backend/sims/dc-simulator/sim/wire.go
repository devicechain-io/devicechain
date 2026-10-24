// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"fmt"
	"strings"
)

// EventTimestamp is the occurred-time stamp an emit carries (see eventTimestamp): UTC,
// truncated to the microsecond the event store keeps. A wire that stamps its own events must
// use this one, because the identity reconciliation keys an event by the stamp it sent.
func EventTimestamp() string { return eventTimestamp() }

// MeasurementPayload builds the payload of a Measurement event carrying metrics, stamped
// occurredTime, in the exact shape EmitMeasurements sends over HTTP.
//
// padBytes, when positive, adds a "pad" member of that many bytes to the payload. It exists to load-test the transport with a payload larger
// than one reading, and it is inert: the decoder reads payload.entries and ignores any other
// member (TestPaddedMeasurementDecodesThroughTheRealDecoder pins that), so the padding costs
// the wire and the broker and adds no stored row.
func MeasurementPayload(metrics map[string]float64, occurredTime string, padBytes int) (map[string]interface{}, error) {
	payload, err := jsonRoundTrip(map[string]any{"entries": []any{map[string]any{
		"measurements": wireMeasurements(metrics),
		"occurredTime": occurredTime,
	}}})
	if err != nil {
		return nil, fmt.Errorf("build measurement payload: %w", err)
	}
	if padBytes > 0 {
		payload["pad"] = strings.Repeat("x", padBytes)
	}
	return payload, nil
}
