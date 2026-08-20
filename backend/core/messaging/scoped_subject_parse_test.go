// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import "testing"

// ParseDeviceFromScopedSubject is the inverse of DeviceScopedSubject, and it is what
// turns a broker-verified subject into the device identity an authorization check can
// use. Everything it rejects, it must reject by returning ok=false — never by returning
// a token that happens to be wrong, since a wrong token is an identity a caller will act
// on.
func TestParseDeviceFromScopedSubject(t *testing.T) {
	const suffix = SubjectCommandResponses

	t.Run("round-trips the constructor", func(t *testing.T) {
		subject := DeviceScopedSubject("inst-1", "acme", suffix, "sensor-001")
		got, ok := ParseDeviceFromScopedSubject(subject, suffix)
		if !ok || got != "sensor-001" {
			t.Fatalf("parse(%q) = %q ok=%v, want sensor-001", subject, got, ok)
		}
	})

	// 🔴 THE TENANT SEGMENT IS THE DANGEROUS NEAR-MISS. Both are non-empty tokens in the
	// same subject, so an off-by-one index returns a plausible string rather than
	// failing — and every command would then be attributed to a "device" named after the
	// tenant, which matches nothing and refuses every response in the instance.
	t.Run("returns the device, not the tenant", func(t *testing.T) {
		got, _ := ParseDeviceFromScopedSubject("inst-1.acme.command-responses.sensor-001", suffix)
		if got == "acme" {
			t.Fatal("parsed the TENANT segment as the device token")
		}
		if got != "sensor-001" {
			t.Fatalf("device = %q, want sensor-001", got)
		}
	})

	t.Run("rejects the wrong suffix", func(t *testing.T) {
		// A device-commands subject has the identical shape. Without the suffix check a
		// consumer could be handed one and read a device token out of it as though it
		// were a response.
		subject := DeviceScopedSubject("inst-1", "acme", SubjectDeviceCommands, "sensor-001")
		if got, ok := ParseDeviceFromScopedSubject(subject, suffix); ok {
			t.Fatalf("parsed %q as a command-response subject, yielding %q", subject, got)
		}
	})

	for name, subject := range map[string]string{
		"the old tenant-wide shape": "inst-1.acme.command-responses",
		"an extra segment":          "inst-1.acme.command-responses.sensor-001.extra",
		"an empty device":           "inst-1.acme.command-responses.",
		"an empty tenant":           "inst-1..command-responses.sensor-001",
		"an empty instance":         ".acme.command-responses.sensor-001",
		"empty":                     "",
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			if got, ok := ParseDeviceFromScopedSubject(subject, suffix); ok {
				t.Fatalf("parsed %q, yielding device %q; it carries no usable identity", subject, got)
			}
		})
	}
}
