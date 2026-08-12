// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strings"
	"testing"
)

// The round trip is the primary claim: whatever DeviceClientID composes,
// ParseDeviceClientIDFor must take apart into the same three fields. Composing the
// input rather than writing it out is deliberate here (the opposite of the callout
// test's literal, which is testing a different thing) — this test's subject IS the
// relationship between the two functions, so a change to the separator must move
// both or fail here.
func TestParseDeviceClientIDRoundTrips(t *testing.T) {
	for _, tc := range []struct{ instance, tenant, device string }{
		{"prod1", "acme", "sensor-001"},
		{"prod1", "acme-1", "dev"},
		{"prod1", "acme", "1-dev"},
		{"p", "t", "d"},
	} {
		id, err := DeviceClientID(tc.instance, tc.tenant, tc.device)
		if err != nil {
			t.Fatalf("DeviceClientID(%q,%q,%q): %v", tc.instance, tc.tenant, tc.device, err)
		}
		got, err := ParseDeviceClientIDFor(tc.instance, id)
		if err != nil {
			t.Fatalf("ParseDeviceClientIDFor(%q, %q): %v", tc.instance, id, err)
		}
		if got.Tenant != tc.tenant || got.DeviceToken != tc.device || got.InstanceId != tc.instance {
			t.Errorf("parse(%q) = %+v, want instance %q tenant %q device %q",
				id, got, tc.instance, tc.tenant, tc.device)
		}
		if got.Discriminator != "" {
			t.Errorf("parse(%q) invented a discriminator %q", id, got.Discriminator)
		}
	}
}

// The pair that would collide under a naive parse. "acme-1" + "dev" and "acme" +
// "1-dev" are different tenants; only the separator keeps them apart, and getting
// this wrong attributes one tenant's device to another — the cross-tenant direction
// of the same hazard DeviceClientIDMatches guards.
func TestParseKeepsAmbiguousTenantsApart(t *testing.T) {
	a, err := DeviceClientID("prod1", "acme-1", "dev")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeviceClientID("prod1", "acme", "1-dev")
	if err != nil {
		t.Fatal(err)
	}
	pa, err := ParseDeviceClientIDFor("prod1", a)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := ParseDeviceClientIDFor("prod1", b)
	if err != nil {
		t.Fatal(err)
	}
	if pa.Tenant == pb.Tenant {
		t.Errorf("both ids parsed to tenant %q; %q and %q name different tenants", pa.Tenant, a, b)
	}
}

// A device may open a second concurrent session by appending its own discriminator,
// which DeviceClientIDMatches admits. The parse must attribute that session to the
// SAME device — a tap that dropped it would report the device's second connection as
// nobody's, and a tap that mis-split it would report it as a device named after the
// discriminator.
func TestParseAttributesADiscriminatedSessionToItsDevice(t *testing.T) {
	plain, err := DeviceClientID("prod1", "acme", "sensor-001")
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"2", "uplink", "a:b"} {
		presented := plain + ":" + suffix
		if !DeviceClientIDMatches(presented, plain) {
			t.Fatalf("fixture %q is not something the callout would admit", presented)
		}
		got, err := ParseDeviceClientIDFor("prod1", presented)
		if err != nil {
			t.Fatalf("ParseDeviceClientIDFor(%q): %v", presented, err)
		}
		if got.DeviceToken != "sensor-001" || got.Tenant != "acme" {
			t.Errorf("parse(%q) = %+v, want it attributed to acme/sensor-001", presented, got)
		}
		// The tail is kept whole. "a:b" is the case that separates a four-way split
		// from a full one: a full split would parse it as a device "a" with a
		// trailing field, which is a different device.
		if got.Discriminator != suffix {
			t.Errorf("parse(%q) discriminator = %q, want %q", presented, got.Discriminator, suffix)
		}
	}
}

// 🔴 The cross-instance refusal. On a shared broker both instances' devices are in
// the one APP account, so this id really does arrive at the other instance's tap. If
// it parsed, that instance would publish presence for a device it does not own — and
// under the delivery gate, decide whether that device is reachable.
func TestParseRefusesAnotherInstancesDevice(t *testing.T) {
	id, err := DeviceClientID("prod2", "acme", "sensor-001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDeviceClientIDFor("prod1", id); err == nil {
		t.Fatalf("instance prod1 accepted %q, which belongs to prod2", id)
	}
	// The counterweight: the refusal must be about the instance and not about the
	// shape, or it would refuse this instance's own devices too and the tap would
	// simply never fire.
	if _, err := ParseDeviceClientIDFor("prod2", id); err != nil {
		t.Errorf("instance prod2 refused its own device id %q: %v", id, err)
	}
}

// Everything that is not a device client id must be refused, because the tap sees
// every client id the account admits. The service connections are the live case: an
// internal service connects with no MQTT client id at all, and a monitoring tool with
// whatever its operator typed.
func TestParseRefusesWhatIsNotADeviceClientID(t *testing.T) {
	for _, tc := range []struct{ name, presented string }{
		{"empty", ""},
		{"no separators", "sensor-001"},
		{"one separator", "prod1:acme"},
		{"empty tenant", "prod1::sensor-001"},
		{"empty device", "prod1:acme:"},
		{"tenant with a wildcard", "prod1:ac*me:sensor-001"},
		{"device with a subject separator", "prod1:acme:sen.sor"},
		{"device with a full wildcard", "prod1:acme:>"},
		{"leading separator", ":prod1:acme:sensor-001"},
		{"whitespace in the device token", "prod1:acme:sensor 001"},
		{"over-long device token", "prod1:acme:" + strings.Repeat("x", 256)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDeviceClientIDFor("prod1", tc.presented)
			if err == nil {
				t.Fatalf("accepted %q as a device client id, yielding %+v", tc.presented, got)
			}
			if got != (DeviceClientIDParts{}) {
				t.Errorf("parts were returned alongside the error: %+v", got)
			}
		})
	}
}

// An invalid instance id is a programming error at the call site rather than
// something off the wire, and it must not silently match.
//
// 🔴 EVERY CASE HERE PRESENTS AN ID THAT AGREES WITH THE BAD INSTANCE, and that is
// the only way this test says anything. The obvious fixture — a bad expected instance
// against a WELL-FORMED id — is refused by the instance-equality test whether the
// validation exists or not, so it passes against a build with the validation deleted.
// Measured: written that way, this test survived exactly that mutation. Making the
// two agree is what forces the refusal to come from the validation.
func TestParseRefusesAnInvalidExpectedInstance(t *testing.T) {
	for _, instance := range []string{"", "pro*d1", "prod.1", "prod>1"} {
		presented := instance + ":acme:sensor-001"
		got, err := ParseDeviceClientIDFor(instance, presented)
		if err == nil {
			t.Errorf("expected-instance %q accepted its own id %q, yielding %+v — an instance id that "+
				"cannot be composed must not be parseable either", instance, presented, got)
		}
	}
}
