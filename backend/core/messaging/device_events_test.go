// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/streams"
)

// The device-events shape and the INTERNAL subject shape must be DISJOINT, and
// this is the invariant that makes them so: an internal subject is
// "{instance}.{tenant}.{suffix}[.{device}]", so the only way one could ever match
// "{instance}.*.devices.*.events" is a suffix literally named "devices".
//
// Asserting it here means a future stream called "devices" fails this test
// instead of quietly reopening the ingest loop, where the symptom is a tenant
// silently metered twice and no error anywhere.
//
// The capture stream (ADR-030 amendment) is the one declared stream that is
// SUPPOSED to match — it exists to store exactly this shape. So the assertion is
// two-directional rather than a skip: a device-events stream must match, every
// other stream must not, and there must be exactly one of the former. A plain
// skip would let a second capture stream, or a mis-shaped ordinary one, pass
// unnoticed — and the earlier version of this test had already degraded that far
// on its own, because it rebuilt each subject by hand-dispatching on shape and so
// checked a subject the capture stream does not have.
func TestNoStreamSuffixCollidesWithDeviceEventsShape(t *testing.T) {
	captureStreams := 0
	for _, s := range streams.All {
		if s.Shape != streams.ShapeDeviceEvents && s.Suffix == SegmentDevices {
			t.Errorf("stream suffix %q collides with the device-events subject shape: "+
				"its subjects would be ingested as device telemetry and metered against the tenant", s.Suffix)
		}
		// Check the subject in the form it is actually PUBLISHED in: a suffix
		// containing a dot adds segments, and a per-device suffix carries an extra
		// device segment, so checking only the tenant-scoped form would miss the
		// longer subject that is the one at risk of matching.
		subject := ConcreteSubjectFor("inst", "acme", s.Suffix, "sensor-001")
		matches := matchesDeviceEvents(subject)
		if s.Shape == streams.ShapeDeviceEvents {
			captureStreams++
			if !matches {
				t.Errorf("capture stream %q yields subject %q, which is NOT the device-events shape: "+
					"the stream would store nothing a device ever publishes", s.Suffix, subject)
			}
			continue
		}
		if matches {
			t.Errorf("stream suffix %q yields subject %q, which the gateway subscription matches", s.Suffix, subject)
		}
	}
	if captureStreams != 1 {
		t.Errorf("found %d device-events capture streams, want exactly 1: a second one would "+
			"double-store every device publish and reserve its ceiling twice", captureStreams)
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
