// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/eventtime"
)

// An omitted wire format is defaulted to legacy in this release, by ApplyDefaults and at the
// point of use alike, and both read the one definition of the default.
func TestEventWireFormatDefaultsToLegacy(t *testing.T) {
	c := validLoggingDoc("")
	if f, err := c.Infrastructure.Messaging.EventWireFormat(); err != nil || f != eventtime.WireLegacy {
		t.Fatalf("an omitted format reads as %v, %v; want legacy", f, err)
	}
	c.ApplyDefaults()
	if c.Infrastructure.Messaging.WireFormat != "legacy" {
		t.Fatalf("an omitted format defaulted to %q, want legacy", c.Infrastructure.Messaging.WireFormat)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("the defaulted document does not validate: %v", err)
	}
	if got := NewDefaultInstanceConfiguration().Infrastructure.Messaging.WireFormat; got != "legacy" {
		t.Fatalf("the default instance configuration writes %q, want legacy", got)
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
