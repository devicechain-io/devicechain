// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
)

// An omitted wire format is defaulted to compact, by ApplyDefaults and at the point of use
// alike, and both read the one definition of the default.
func TestEventWireFormatDefaultsToCompact(t *testing.T) {
	c := validLoggingDoc("")
	if f, err := c.Infrastructure.Messaging.EventWireFormat(); err != nil || f != eventtime.WireCompact {
		t.Fatalf("an omitted format reads as %v, %v; want compact", f, err)
	}
	c.ApplyDefaults()
	if c.Infrastructure.Messaging.WireFormat != "compact" {
		t.Fatalf("an omitted format defaulted to %q, want compact", c.Infrastructure.Messaging.WireFormat)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("the defaulted document does not validate: %v", err)
	}
	if got := NewDefaultInstanceConfiguration().Infrastructure.Messaging.WireFormat; got != "compact" {
		t.Fatalf("the default instance configuration writes %q, want compact", got)
	}
}

// The envelope producer switches exist and are off; both may be turned on.
func TestEnvelopeProducerSwitchesAreOffAndCanBeTurnedOn(t *testing.T) {
	c := validLoggingDoc("")
	c.ApplyDefaults()
	if c.Infrastructure.Messaging.Envelopes.Resolved || c.Infrastructure.Messaging.Envelopes.Inbound {
		t.Fatal("an envelope producer switch defaulted on")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
	on := validLoggingDoc("")
	on.ApplyDefaults()
	on.Infrastructure.Messaging.Envelopes.Resolved = true
	if err := on.Validate(); err != nil {
		t.Fatalf("envelopes.resolved turned on is refused: %v", err)
	}
	on.Infrastructure.Messaging.Envelopes.Inbound = true
	if err := on.Validate(); err != nil {
		t.Fatalf("envelopes.inbound turned on is refused: %v", err)
	}
	if got := on.Infrastructure.Messaging.Envelopes.InboundLinger(); got != 5*time.Millisecond {
		t.Fatalf("the default inbound linger is %s, want 5ms", got)
	}
}

// The inbound linger is configurable up to a hard cap of 10 ms; anything outside is refused at
// startup, naming the key, and never clamped.
func TestInboundLingerIsBoundedAndRefusedOutOfRange(t *testing.T) {
	for _, ms := range []int{0, 1, 5, 10} {
		c := validLoggingDoc("")
		c.ApplyDefaults()
		c.Infrastructure.Messaging.Envelopes.InboundLingerMillis = ms
		if err := c.Validate(); err != nil {
			t.Fatalf("%d ms refused: %v", ms, err)
		}
		want := time.Duration(ms) * time.Millisecond
		if ms == 0 {
			want = 5 * time.Millisecond
		}
		if got := c.Infrastructure.Messaging.Envelopes.InboundLinger(); got != want {
			t.Fatalf("%d ms reads as %s, want %s", ms, got, want)
		}
	}
	for _, ms := range []int{-1, 11, 1000} {
		c := validLoggingDoc("")
		c.ApplyDefaults()
		c.Infrastructure.Messaging.Envelopes.InboundLingerMillis = ms
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "infrastructure.messaging.envelopes.inboundLingerMillis") {
			t.Fatalf("%d ms: %v; want a refusal naming the key", ms, err)
		}
	}
}

// Both spellings validate; anything else is refused by Validate itself, naming the key, and
// is never rewritten to the default by ApplyDefaults.
func TestEventWireFormatValidates(t *testing.T) {
	for _, name := range []string{"legacy", "compact"} {
		c := validLoggingDoc("")
		c.Infrastructure.Messaging.WireFormat = name
		c.ApplyDefaults()
		if err := c.Validate(); err != nil {
			t.Fatalf("%q refused: %v", name, err)
		}
		if c.Infrastructure.Messaging.WireFormat != name {
			t.Fatalf("%q rewritten to %q", name, c.Infrastructure.Messaging.WireFormat)
		}
	}
	for _, name := range []string{"Compact", "LEGACY", "binary", " compact"} {
		c := validLoggingDoc("")
		c.Infrastructure.Messaging.WireFormat = name
		c.ApplyDefaults()
		err := c.Validate()
		if err == nil {
			t.Fatalf("%q validated", name)
		}
		if !errors.Is(err, eventtime.ErrUnknownWireFormat) || !strings.Contains(err.Error(), "infrastructure.messaging.wireFormat") {
			t.Fatalf("%q: the refusal %v does not name the key", name, err)
		}
	}
}
