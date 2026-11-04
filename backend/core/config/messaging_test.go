// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"strings"
	"testing"

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

// The envelope producer switches exist and are off. Resolved may be turned on (device-management
// produces it); Inbound has no producer, so turning it on is refused, naming the key: a switch
// that was accepted and did nothing would read as a setting that works.
func TestEnvelopeProducerSwitchesAreOffAndOnlyResolvedCanBeTurnedOn(t *testing.T) {
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
	for key, set := range map[string]func(*InstanceConfiguration){
		"infrastructure.messaging.envelopes.inbound": func(c *InstanceConfiguration) { c.Infrastructure.Messaging.Envelopes.Inbound = true },
	} {
		c := validLoggingDoc("")
		c.ApplyDefaults()
		set(c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s turned on: %v; want a refusal naming the key", key, err)
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
