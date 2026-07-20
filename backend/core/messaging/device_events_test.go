// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/streams"
)

// The device-events shape and the internal subject shape must be DISJOINT, and
// this is the invariant that makes them so: an internal subject is
// "{instance}.{tenant}.{suffix}[.{device}]", so the only way one could ever match
// "{instance}.*.devices.*.events" is a suffix literally named "devices".
//
// Asserting it here means a future stream called "devices" fails this test
// instead of quietly reopening the ingest loop, where the symptom is a tenant
// silently metered twice and no error anywhere.
func TestNoStreamSuffixCollidesWithDeviceEventsShape(t *testing.T) {
	for _, s := range streams.All {
		if s.Suffix == SegmentDevices {
			t.Errorf("stream suffix %q collides with the device-events subject shape: "+
				"its subjects would be ingested as device telemetry and metered against the tenant", s.Suffix)
		}
		// A suffix containing a dot adds segments, which could push an internal
		// subject to the same length as the device-events shape. Check it in the
		// form it is actually PUBLISHED in — a per-device suffix carries an extra
		// device segment, so checking only the tenant-scoped form would miss the
		// longer subject that is the one at risk of matching.
		subject := ScopedSubject("inst", "acme", s.Suffix)
		if IsPerDeviceSuffix(s.Suffix) {
			subject = DeviceScopedSubject("inst", "acme", s.Suffix, "sensor-001")
		}
		if matchesDeviceEvents(subject) {
			t.Errorf("stream suffix %q yields subject %q, which the gateway subscription matches", s.Suffix, subject)
		}
	}
}

// matchesDeviceEvents reports whether a concrete subject would be delivered by
// DeviceEventsWildcard. This is a test-local check of OUR shape arithmetic; that
// the BROKER agrees is proven separately against a live MQTT gateway in
// event-sources (TestGatewaySubscriptionExcludesInternalSubjects).
func matchesDeviceEvents(subject string) bool {
	parts := strings.Split(subject, ".")
	return len(parts) == DeviceEventsSegmentCount &&
		parts[DeviceEventsDevicesIndex] == SegmentDevices &&
		parts[DeviceEventsEventsIndex] == SegmentEvents
}

// The wildcard must match the subject the grant is minted from. If these drifted,
// devices would be authorized to publish where the gateway does not listen — an
// outage with no error on either side, since the broker accepts the publish and
// simply delivers it to nobody.
func TestDeviceEventsWildcardMatchesGrantedSubject(t *testing.T) {
	subject := DeviceEventsSubject("inst", "acme", "sensor-001")
	if !matchesDeviceEvents(subject) {
		t.Fatalf("granted subject %q is not matched by %q", subject, DeviceEventsWildcard("inst"))
	}
	if got, want := subject, "inst.acme.devices.sensor-001.events"; got != want {
		t.Errorf("device events subject = %q, want %q", got, want)
	}
	if got, want := DeviceEventsWildcard("inst"), "inst.*.devices.*.events"; got != want {
		t.Errorf("device events wildcard = %q, want %q", got, want)
	}
}

// The MQTT topic form must be the NATS gateway's documented mapping of the subject
// form: "." to "/", "*" to "+", ">" to "#".
func TestSubjectToMqttTopic(t *testing.T) {
	cases := map[string]string{
		"inst.*.devices.*.events":           "inst/+/devices/+/events",
		"inst.acme.devices.d1.events":       "inst/acme/devices/d1/events",
		"inst.acme.device-commands.d1":      "inst/acme/device-commands/d1",
		"inst.*.>":                          "inst/+/#",
		"inst.acme.connector-dispatch.dead": "inst/acme/connector-dispatch/dead",
	}
	for subject, want := range cases {
		if got := SubjectToMqttTopic(subject); got != want {
			t.Errorf("SubjectToMqttTopic(%q) = %q, want %q", subject, got, want)
		}
	}
}
