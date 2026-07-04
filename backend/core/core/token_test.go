// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"strings"
	"testing"
)

// The grammar admits machine-supplied identifiers (uppercase serials, VINs) but
// rejects every metacharacter that is hazardous when a token is spliced into a
// NATS subject or MQTT topic — the property both the storage guard (ADR-042) and
// the messaging subject guard (ADR-025) rely on.
func TestValidateToken(t *testing.T) {
	valid := []string{"acme-corp", "plant_07", "THERM-001", "1ABCD1234EF567890", "a", "A0"}
	for _, tok := range valid {
		if err := ValidateToken(tok); err != nil {
			t.Errorf("ValidateToken(%q) = %v, want nil", tok, err)
		}
	}

	invalid := []string{
		"",                                 // empty
		"-leading",                         // must start with a letter/digit
		"_leading",                         // must start with a letter/digit
		"acme.corp",                        // "." shifts subject segments
		"acme*",                            // "*" is a NATS wildcard
		"acme>",                            // ">" is a NATS wildcard
		"acme:corp",                        // ":" is the callout username delimiter
		"acme/corp",                        // "/" is an MQTT level separator
		"acme corp",                        // whitespace
		"acme\tcorp",                       // whitespace
		"acme+corp",                        // "+" is an MQTT wildcard
		"tenant#1",                         // "#" is an MQTT wildcard
		strings.Repeat("a", MaxTokenLen+1), // over the length bound
	}
	for _, tok := range invalid {
		if err := ValidateToken(tok); err == nil {
			t.Errorf("ValidateToken(%q) = nil, want error", tok)
		}
	}

	// The bound itself is inclusive.
	if err := ValidateToken(strings.Repeat("a", MaxTokenLen)); err != nil {
		t.Errorf("ValidateToken(len=%d) = %v, want nil", MaxTokenLen, err)
	}
}
