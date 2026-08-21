// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
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

// The per-message reading ceiling. Every reading in a message becomes a stored row, a
// projection write and an evaluation on the single DETECT goroutine every tenant shares, so
// the message-rate limiter — which charges ONE token however much a message carries — is not
// on its own a bound on what one message costs.
func TestAnOversizedMessageIsRefused(t *testing.T) {
	const max = 4
	decoder := NewJsonDecoder(nil, max)
	for _, kind := range []string{"Measurement", "Location", "Alert"} {
		t.Run(kind, func(t *testing.T) {
			// At the ceiling is legal. The bound is inclusive, and a test that only showed
			// the refusal could not tell an off-by-one ceiling from a correct one.
			if _, _, err := decoder.Decode([]byte(entriesBody(kind, max))); err != nil {
				t.Fatalf("%d readings is AT the ceiling and must decode: %v", max, err)
			}
			_, _, err := decoder.Decode([]byte(entriesBody(kind, max+1)))
			if err == nil {
				t.Fatalf("%d readings is over the %d ceiling and must be refused", max+1, max)
			}
			// The sentinel is what lets the caller count an oversized message apart from
			// malformed JSON; without it the refusal is invisible in the metrics.
			if !errors.Is(err, ErrTooManyReadings) {
				t.Fatalf("refusal must wrap ErrTooManyReadings, got %v", err)
			}
			// The device is told the count, the ceiling and the remedy — on HTTP this text
			// is the 400 body, so it is written for whoever fixes the firmware.
			for _, want := range []string{"5 readings", "4-reading", "split it across messages", "not truncated"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message should contain %q, got: %v", want, err)
				}
			}
		})
	}
}

// 🔴 THE CEILING COUNTS READINGS, NOT ENTRIES, and one wide entry is exactly why. A
// measurement entry carries an unbounded map, so a single entry can hold 30 000 metric keys:
// one entry, comfortably inside the 1 MiB body ceiling, and still 30 000 stored rows across
// 30 000 series. Counting entries would wave that through while claiming to bound the
// fan-out — the claim and the mechanism disagreeing, which is worse than having neither.
func TestOneWideEntryIsCountedByItsReadings(t *testing.T) {
	decoder := NewJsonDecoder(nil, 10)
	if _, _, err := decoder.Decode([]byte(wideEntryBody(10))); err != nil {
		t.Fatalf("10 keys in one entry is at the ceiling and must decode: %v", err)
	}
	_, _, err := decoder.Decode([]byte(wideEntryBody(11)))
	if err == nil {
		t.Fatal("ONE entry holding 11 metric keys is 11 readings and must be refused")
	}
	if !errors.Is(err, ErrTooManyReadings) {
		t.Fatalf("want ErrTooManyReadings, got %v", err)
	}
	if !strings.Contains(err.Error(), "11 readings") {
		t.Errorf("the message must report readings, not entries: %v", err)
	}
}

// Readings accumulate ACROSS entries too, so a message cannot get under the ceiling by
// spreading the same volume over more entries.
func TestReadingsAccumulateAcrossEntries(t *testing.T) {
	decoder := NewJsonDecoder(nil, 5)
	body := `{"device":"d1","eventType":"Measurement","payload":{"entries":[
		{"measurements":{"a":"1","b":"2","c":"3"}},
		{"measurements":{"d":"4","e":"5","f":"6"}}]}}`
	_, _, err := decoder.Decode([]byte(body))
	if err == nil {
		t.Fatal("two entries of three keys is six readings and must be refused at a ceiling of 5")
	}
	if !strings.Contains(err.Error(), "6 readings") {
		t.Errorf("want the summed count, got: %v", err)
	}
}

// 🔴 The refusal must be WHOLE. A ceiling that truncated would hand the device a 202 for a
// message that was silently cut short — data loss wearing a success code, undetectable from
// either end. This is not implied by the refusal tests above: a truncating implementation
// returns no error at all.
func TestAnOversizedMessageIsNotTruncated(t *testing.T) {
	decoder := NewJsonDecoder(nil, 4)
	_, payload, err := decoder.Decode([]byte(entriesBody("Measurement", 9)))
	if err == nil {
		t.Fatal("an oversized message must fail, not succeed with fewer readings")
	}
	if payload != nil {
		t.Fatalf("a refused message must yield no payload, got %+v", payload)
	}
}

// A decoder cannot be constructed that admits an unbounded message. Zero is the value a
// caller reaches for when it has nothing to say, and in a fail-closed platform that must mean
// the platform ceiling rather than "no ceiling" — the same stance the ingest rate limit takes
// on a non-positive value.
func TestANonPositiveCeilingFallsBackToThePlatformDefault(t *testing.T) {
	for _, given := range []int{0, -1} {
		d := NewJsonDecoder(nil, given)
		if d.MaxReadings != config.DefaultMaxReadingsPerMessage {
			t.Errorf("NewJsonDecoder(_, %d).MaxReadings = %d, want the platform default %d",
				given, d.MaxReadings, config.DefaultMaxReadingsPerMessage)
		}
	}
	if config.DefaultMaxReadingsPerMessage <= 0 {
		t.Fatal("the platform default is itself the ceiling of last resort and must be positive")
	}
}

// The configuration side of the same stance: an omitted or zeroed ceiling is filled with the
// platform default by ApplyDefaults, so a document that never mentions the key still meters
// every message.
func TestConfigDefaultsTheReadingCeiling(t *testing.T) {
	for _, given := range []int{0, -5} {
		c := &config.EventSourcesConfiguration{MaxReadingsPerMessage: given}
		c.ApplyDefaults()
		if c.MaxReadingsPerMessage != config.DefaultMaxReadingsPerMessage {
			t.Errorf("ApplyDefaults with %d gave %d, want %d", given, c.MaxReadingsPerMessage,
				config.DefaultMaxReadingsPerMessage)
		}
	}
	c := &config.EventSourcesConfiguration{MaxReadingsPerMessage: 25}
	c.ApplyDefaults()
	if c.MaxReadingsPerMessage != 25 {
		t.Errorf("an explicit ceiling must survive defaulting, got %d", c.MaxReadingsPerMessage)
	}
}

// The fall-through is the part of the ceiling nothing in production reaches today, which is
// exactly why it is pinned here. checkReadingCount is called for three payload kinds and
// readingCount names four; a fifth added to Decode without a case would, under the old
// `return 1`, be metered as a single reading and pass any ceiling — silently uncapped while
// the check above it still read as a guard. It must refuse instead.
func TestAnUnmeteredPayloadKindIsRefusedRatherThanCountedAsOne(t *testing.T) {
	type payloadNobodyTaughtItAbout struct{ Entries []int }

	err := checkReadingCount("mystery", &payloadNobodyTaughtItAbout{Entries: make([]int, 5000)}, 10)
	if err == nil {
		t.Fatal("an unmetered payload kind passed the ceiling; the fall-through fails OPEN")
	}
	// It must NOT be counted as an oversized batch: that counter is what tells an operator
	// their fleet is batching too large, and a code defect landing in it sends them to the
	// firmware. Same terminal outcome, different diagnosis.
	if errors.Is(err, ErrTooManyReadings) {
		t.Errorf("an unmetered kind was reported as an oversized batch: %v", err)
	}

	// The counterweight: a kind that IS named still passes, so the refusal above is the
	// fall-through and not a check that refuses everything.
	if err := checkReadingCount("relationship", &model.UnresolvedNewRelationshipPayload{}, 10); err != nil {
		t.Errorf("a relationship is one indivisible unit and must pass a ceiling of 10: %v", err)
	}
}
