// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
)

// The wire from configuration to enforcement.
//
// 🔴 EVERY OTHER TEST OF THE CEILING PASSES ITS OWN VALUE STRAIGHT TO THE DECODER, so all of
// them pass whether or not createDecoder ever reads the configuration. Replace the argument at
// the one production construction site with a literal and nothing else in the suite notices —
// the ceiling would then be un-tunable, and an operator who raised it for a genuinely
// high-volume fleet would watch the old value keep refusing.
func TestTheConfiguredCeilingReachesTheDecoder(t *testing.T) {
	saved := Configuration
	t.Cleanup(func() { Configuration = saved })

	source := config.EventSource{
		Id:      "src",
		Type:    "mqtt",
		Decoder: config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
	}

	// An explicit operator value must be the one enforced — and it is deliberately NOT the
	// platform default, so a decoder that ignored the configuration would be caught rather
	// than accidentally agreeing.
	const operatorValue = 37
	if operatorValue == config.DefaultMaxReadingsPerMessage {
		t.Fatal("pick a value that differs from the default, or this test cannot fail")
	}
	Configuration = &config.EventSourcesConfiguration{MaxReadingsPerMessage: operatorValue}
	d, err := createDecoder(source)
	if err != nil {
		t.Fatalf("createDecoder: %v", err)
	}
	if got := d.(*processor.JsonDecoder).MaxReadings; got != operatorValue {
		t.Fatalf("decoder enforces %d, operator configured %d", got, operatorValue)
	}

	// And an unset one arrives as the platform default, because the load path defaults it.
	Configuration = config.NewEventSourcesConfiguration()
	d, err = createDecoder(source)
	if err != nil {
		t.Fatalf("createDecoder: %v", err)
	}
	if got := d.(*processor.JsonDecoder).MaxReadings; got != config.DefaultMaxReadingsPerMessage {
		t.Fatalf("decoder enforces %d, want the platform default %d", got, config.DefaultMaxReadingsPerMessage)
	}
}
