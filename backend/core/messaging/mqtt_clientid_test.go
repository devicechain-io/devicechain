// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strings"
	"testing"
)

// The composed shape, written out rather than derived.
//
// 🔑 EVERY EXPECTATION HERE IS A LITERAL, AND THAT IS THE POINT. The value this
// function returns is a wire contract: firmware, the docs' mosquitto invocations
// and the auth callout must all agree on it, and the only one of those three a Go
// test can reach is the callout. An expectation built by calling DeviceClientID
// would agree with whatever it produced — including a separator changed by
// accident, which would lock every device in the field out of the broker while
// the suite stayed green.
func TestDeviceClientID(t *testing.T) {
	cases := []struct {
		name, instanceId, tenant, deviceToken, want string
	}{
		{"plain", "prod1", "acme", "sensor-001", "prod1:acme:sensor-001"},
		{"hyphens throughout stay unambiguous", "prod-1", "acme-corp", "sensor-001", "prod-1:acme-corp:sensor-001"},
		{"underscores and digits", "dc_2", "plant_07", "VIN_1HGCM82633A004352", "dc_2:plant_07:VIN_1HGCM82633A004352"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeviceClientID(tc.instanceId, tc.tenant, tc.deviceToken)
			if err != nil {
				t.Fatalf("DeviceClientID(%q, %q, %q) errored: %v", tc.instanceId, tc.tenant, tc.deviceToken, err)
			}
			if got != tc.want {
				t.Errorf("DeviceClientID(%q, %q, %q) = %q, want %q",
					tc.instanceId, tc.tenant, tc.deviceToken, got, tc.want)
			}
		})
	}
}

// The separator must be one the token grammar cannot produce, or the split is a
// guess. This is the reason ":" was chosen over the hyphen the simulator had been
// using locally, so it is asserted rather than left to the comment: with a hyphen,
// ("acme-1","dev") and ("acme","1-dev") compose to the SAME id, and a purge reading
// it back either misses one tenant's sessions or deletes the other's.
func TestDeviceClientIDIsUnambiguousAcrossTenantBoundaries(t *testing.T) {
	a, err := DeviceClientID("prod1", "acme-1", "dev")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeviceClientID("prod1", "acme", "1-dev")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two different (tenant, device) pairs composed to the same client id %q: "+
			"the separator is inside the token grammar, so the id cannot be split back", a)
	}
	// And the split itself is unique: exactly one separator per field boundary, so
	// each field is recoverable with nothing to disambiguate.
	if n := strings.Count(a, ":"); n != 2 {
		t.Errorf("client id %q carries %d separators, want exactly 2", a, n)
	}
}

// 🔑 THE INSTANCE FIELD IS NOT DECORATION. MQTT sessions are keyed per ACCOUNT,
// and on a shared broker (ADR-048) two instances share the single APP account —
// so without this field the same tenant and device name in two instances would be
// ONE session, and the cross-instance takeover would be the same defect the pin
// removes, one level up.
func TestDeviceClientIDSeparatesInstances(t *testing.T) {
	a, err := DeviceClientID("prod1", "acme", "sensor-001")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeviceClientID("prod2", "acme", "sensor-001")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two instances composed the same client id %q for the same tenant and "+
			"device, so they would share one MQTT session on a shared broker", a)
	}
}

// The callout's admission test. The cases are grouped by what each one protects:
// the plain form and a device's own discriminators must be admitted (or firmware
// that splits publish and subscribe across two connections cannot connect at all),
// and everything belonging to another device, tenant or instance must not.
//
// This layer owns the FULL truth table — it is the only one where a case costs
// nothing. The callout and gateway tests assert what only they can.
//
// 🔴 "adjacent device token" is the canary: removing the separator from the prefix
// test in DeviceClientIDMatches is what fails it, and nothing else here.
func TestDeviceClientIDMatches(t *testing.T) {
	// Written out rather than composed, for the reason at the top of this file.
	const required = "prod1:acme:sensor-001"

	cases := []struct {
		name, presented string
		want            bool
	}{
		{"the plain form", "prod1:acme:sensor-001", true},
		{"the device's own discriminator", "prod1:acme:sensor-001:pub", true},
		{"another discriminator is a separate session", "prod1:acme:sensor-001:sub", true},
		{"a discriminator may itself carry separators", "prod1:acme:sensor-001:pub:2", true},

		{"adjacent device token", "prod1:acme:sensor-0011", false},
		{"adjacent tenant", "prod1:acme2:sensor-001", false},
		{"adjacent instance", "prod11:acme:sensor-001", false},
		{"another device in this tenant", "prod1:acme:sensor-002", false},
		{"another tenant's same-named device", "prod1:other:sensor-001", false},
		{"another instance's same-named device", "prod2:acme:sensor-001", false},
		{"the instance field omitted", "acme:sensor-001", false},
		{"the bare device token", "sensor-001", false},
		{"the fields transposed", "prod1:sensor-001:acme", false},
		{"a library-generated default", "mosqpub-31337-somehost", false},
		{"empty, as a raw NATS connect reports", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeviceClientIDMatches(tc.presented, required); got != tc.want {
				t.Errorf("DeviceClientIDMatches(%q, %q) = %v, want %v",
					tc.presented, required, got, tc.want)
			}
		})
	}
}

// nats-server rejects a client id containing whitespace or a NATS metacharacter at
// CONNECT parse (isValidName), BEFORE the auth callout runs — so an id this
// function composes but the broker refuses is a device that can never connect and
// never learns why. The grammar guard is what keeps that unreachable; this asserts
// it fires rather than composing.
func TestDeviceClientIDRefusesAnythingTheBrokerWouldReject(t *testing.T) {
	cases := []struct{ name, instanceId, tenant, deviceToken string }{
		{"dot in instance id", "prod.1", "acme", "sensor-001"},
		{"dot in tenant", "prod1", "acme.corp", "sensor-001"},
		{"dot in device token", "prod1", "acme", "sensor.001"},
		{"wildcard in device token", "prod1", "acme", "sensor-*"},
		{"full wildcard in tenant", "prod1", "acme>", "sensor-001"},
		{"space in device token", "prod1", "acme", "sensor 001"},
		{"separator in tenant would forge a field", "prod1", "acme:evil", "sensor-001"},
		{"empty instance id", "", "acme", "sensor-001"},
		{"empty tenant", "prod1", "", "sensor-001"},
		{"empty device token", "prod1", "acme", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeviceClientID(tc.instanceId, tc.tenant, tc.deviceToken)
			if err == nil {
				t.Fatalf("DeviceClientID(%q, %q, %q) composed %q, want a refusal",
					tc.instanceId, tc.tenant, tc.deviceToken, got)
			}
			if got != "" {
				t.Errorf("a refusal must return no id, got %q", got)
			}
		})
	}
}
