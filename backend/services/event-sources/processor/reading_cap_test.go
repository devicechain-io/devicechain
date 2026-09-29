// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventlimit"
)

// entriesBody builds one inbound message of the given kind carrying n entries, each with a
// single reading.
func entriesBody(kind string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		switch kind {
		case "Measurement":
			fmt.Fprintf(&b, `{"measurements":{"temperature%d":"%d"}}`, i, i)
		case "Location":
			fmt.Fprintf(&b, `{"latitude":"33.7%02d","longitude":"-84.3%02d"}`, i%100, i%100)
		case "Alert":
			fmt.Fprintf(&b, `{"type":"t%d","level":1}`, i)
		}
	}
	return fmt.Sprintf(`{"device":"d1","eventType":"%s","payload":{"entries":[%s]}}`, kind, b.String())
}

// wideEntryBody builds ONE measurement entry carrying n metric keys — the shape that makes
// the entry count and the reading count diverge.
func wideEntryBody(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"metric%d":"%d"`, i, i)
	}
	return fmt.Sprintf(`{"device":"d1","eventType":"Measurement","payload":{"entries":[{"measurements":{%s}}]}}`, b.String())
}

// The per-event reading limit. Every reading in a message becomes a stored row, a projection
// write and an evaluation on the single DETECT goroutine every tenant shares, so the
// message-rate limiter — which charges ONE token however much a message carries — is not on
// its own a bound on what one message costs. On the JSON transports one message is one event,
// so a message over the limit is refused.
func TestAnOversizedMessageIsRefused(t *testing.T) {
	const max = eventlimit.MaxReadingsPerEvent
	decoder := NewJsonDecoder(nil)
	for _, kind := range []string{"Measurement", "Location", "Alert"} {
		t.Run(kind, func(t *testing.T) {
			// At the limit is legal. The bound is inclusive, and a test that only showed
			// the refusal could not tell an off-by-one limit from a correct one.
			if _, _, err := decoder.Decode([]byte(entriesBody(kind, max)), time.Time{}); err != nil {
				t.Fatalf("%d readings is AT the limit and must decode: %v", max, err)
			}
			_, _, err := decoder.Decode([]byte(entriesBody(kind, max+1)), time.Time{})
			if err == nil {
				t.Fatalf("%d readings is over the %d limit and must be refused", max+1, max)
			}
			// The sentinel is what lets the caller count an oversized message apart from
			// malformed JSON; without it the refusal is invisible in the metrics.
			if !errors.Is(err, model.ErrTooManyReadings) {
				t.Fatalf("refusal must wrap ErrTooManyReadings, got %v", err)
			}
			// The device is told the count, the limit and the remedy — on HTTP this text
			// is the 400 body, so it is written for whoever fixes the firmware.
			for _, want := range []string{"257 readings", "limit of 256 readings per event", "split it across messages", "not truncated"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message should contain %q, got: %v", want, err)
				}
			}
		})
	}
}

// 🔴 THE LIMIT COUNTS READINGS, NOT ENTRIES, and one wide entry is exactly why. A measurement
// entry carries an unbounded map, so a single entry can hold 30 000 metric keys: one entry,
// comfortably inside the 1 MiB body ceiling, and still 30 000 stored rows across 30 000
// series. Counting entries would wave that through while claiming to bound the fan-out.
func TestOneWideEntryIsCountedByItsReadings(t *testing.T) {
	decoder := NewJsonDecoder(nil)
	if _, _, err := decoder.Decode([]byte(wideEntryBody(256)), time.Time{}); err != nil {
		t.Fatalf("256 keys in one entry is at the limit and must decode: %v", err)
	}
	_, _, err := decoder.Decode([]byte(wideEntryBody(257)), time.Time{})
	if err == nil {
		t.Fatal("ONE entry holding 257 metric keys is 257 readings and must be refused")
	}
	if !errors.Is(err, model.ErrTooManyReadings) {
		t.Fatalf("want ErrTooManyReadings, got %v", err)
	}
	if !strings.Contains(err.Error(), "257 readings") {
		t.Errorf("the message must report readings, not entries: %v", err)
	}
}

// Readings accumulate ACROSS entries too, so a message cannot get under the limit by
// spreading the same volume over more entries.
func TestReadingsAccumulateAcrossEntries(t *testing.T) {
	decoder := NewJsonDecoder(nil)
	var a, b strings.Builder
	for i := 0; i < 128; i++ {
		fmt.Fprintf(&a, `,"a%d":"%d"`, i, i)
	}
	for i := 0; i < 129; i++ {
		fmt.Fprintf(&b, `,"b%d":"%d"`, i, i)
	}
	body := fmt.Sprintf(`{"device":"d1","eventType":"Measurement","payload":{"entries":[
		{"measurements":{%s}},
		{"measurements":{%s}}]}}`, a.String()[1:], b.String()[1:])
	_, _, err := decoder.Decode([]byte(body), time.Time{})
	if err == nil {
		t.Fatal("two entries of 128 and 129 keys is 257 readings and must be refused")
	}
	if !strings.Contains(err.Error(), "257 readings") {
		t.Errorf("want the summed count, got: %v", err)
	}
}

// 🔴 The refusal must be WHOLE. A limit that truncated would hand the device a 202 for a
// message that was silently cut short — data loss wearing a success code, undetectable from
// either end. This is not implied by the refusal tests above: a truncating implementation
// returns no error at all.
func TestAnOversizedMessageIsNotTruncated(t *testing.T) {
	decoder := NewJsonDecoder(nil)
	_, payload, err := decoder.Decode([]byte(entriesBody("Measurement", 300)), time.Time{})
	if err == nil {
		t.Fatal("an oversized message must fail, not succeed with fewer readings")
	}
	if payload != nil {
		t.Fatalf("a refused message must yield no payload, got %+v", payload)
	}
}

// A relationship is one reading, and it reaches the one check in Decode like every other
// kind — the counterweight to the refusals above.
func TestARelationshipPassesTheLimit(t *testing.T) {
	decoder := NewJsonDecoder(nil)
	body := `{"device":"d1","eventType":"NewRelationship","payload":{"relationshipType":"r","targetType":"device","target":"t"}}`
	_, payload, err := decoder.Decode([]byte(body), time.Time{})
	if err != nil {
		t.Fatalf("a relationship is one reading and must decode: %v", err)
	}
	if p, ok := payload.(*model.UnresolvedNewRelationshipPayload); !ok || p.Target != "t" {
		t.Fatalf("want the relationship payload, got %#v", payload)
	}
}
