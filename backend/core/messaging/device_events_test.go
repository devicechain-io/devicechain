// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
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

// The gateway shape is declared once and every derivation agrees with it: the subject builder,
// the capture wildcard, the parser, the MQTT topic form, and the capture stream's subject list.
func TestGatewayShapeDerivations(t *testing.T) {
	subject := GatewayEventsSubject("inst", "acme", "gw-1", "sensor-001")
	if got, want := subject, "inst.acme.gateways.gw-1.devices.sensor-001.events"; got != want {
		t.Fatalf("gateway events subject = %q, want %q", got, want)
	}
	if got, want := GatewayEventsWildcard("inst"), "inst.*.gateways.*.devices.*.events"; got != want {
		t.Errorf("gateway events wildcard = %q, want %q", got, want)
	}
	if got, want := SubjectToMqttTopic(subject), "inst/acme/gateways/gw-1/devices/sensor-001/events"; got != want {
		t.Errorf("gateway MQTT topic = %q, want %q", got, want)
	}
	if got, want := GatewayNoticesSubject("inst", "acme", "gw-1"), "inst.acme.gateways.gw-1.notices"; got != want {
		t.Errorf("notices subject = %q, want %q", got, want)
	}

	// The capture stream stores exactly the two shapes (the raw-payload one only on opt-in, below).
	got := StreamSubjects("inst", streams.DeviceEventsCapture)
	want := []string{"inst.*.devices.*.events", "inst.*.gateways.*.devices.*.events"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("capture stream subjects = %v, want %v", got, want)
	}

	// Parsing recovers who a subject speaks for, and only for the two shapes.
	cases := []struct {
		subject, gateway, device string
		ok                       bool
	}{
		{"inst.acme.devices.d1.events", "", "d1", true},
		{subject, "gw-1", "sensor-001", true},
		// Not telemetry: a gateway's other leaves, its notices, and malformed subjects.
		{GatewayDeviceSubject("inst", "acme", "gw-1", "c1", "responses"), "", "", false},
		{GatewayNoticesSubject("inst", "acme", "gw-1"), "", "", false},
		{"inst.acme.devices.d1.reports", "", "", false},
		{"inst.acme.gateways..devices.c1.events", "", "", false},
		{"inst.acme.gateways.gw-1.devices..events", "", "", false},
		{"inst.acme.devices.d1.events.extra", "", "", false},
		{"inst.acme.gateways.gw-1.things.c1.events", "", "", false},
	}
	for _, c := range cases {
		g, d, ok := ParseDeviceEventsSubject(c.subject)
		if g != c.gateway || d != c.device || ok != c.ok {
			t.Errorf("ParseDeviceEventsSubject(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.subject, g, d, ok, c.gateway, c.device, c.ok)
		}
	}
}

// A tenant purge reaches the tenant's gateway subjects in the capture stream, and only the
// tenant's. A purge of the device shape alone would leave a deleted tenant's gateway traffic on
// the broker for a successor at a reused token.
func TestTenantPurgeFiltersCoverTheGatewayShape(t *testing.T) {
	filters := TenantSubjectFilters("inst", "acme", streams.DeviceEventsCapture)
	if len(filters) != 3 || filters[0] != "inst.acme.devices.*.events" || filters[1] != "inst.acme.gateways.*.devices.*.events" ||
		filters[2] != "inst.acme.devices.*.uplink" {
		t.Fatalf("capture purge filters = %v", filters)
	}
	if got := TenantSubjectFilters("inst", "acme", streams.InboundEvents); len(got) != 1 {
		t.Errorf("an ordinary stream purges by one filter, got %v", got)
	}
}

// The store reader hands out a gateway's telemetry as well as a device's own, and nothing else.
func TestFailClosedEventsDurableForwardsBothShapes(t *testing.T) {
	var d MqttFailClosedDurable
	for _, x := range MqttFailClosedDurables("inst") {
		if x.Kind == MqttFailClosedEvents {
			d = x
		}
	}
	r := &natsReader{forwards: d.Forwards, alsoForwards: d.AlsoForwards}
	for subject, want := range map[string]bool{
		MqttMsgsSubject(DeviceEventsSubject("inst", "t1", "d1")):                          true,
		MqttMsgsSubject(GatewayEventsSubject("inst", "t1", "g1", "c1")):                   true,
		MqttMsgsSubject(GatewayEventsSubject("other", "t1", "g1", "c1")):                  false,
		MqttMsgsSubject(GatewayDeviceSubject("inst", "t1", "g1", "c1", SegmentResponses)): false,
		MqttMsgsSubject(GatewayNoticesSubject("inst", "t1", "g1")):                        false,
		MqttMsgsSubject(DeviceUplinkSubject("inst", "t1", "d1")):                          true,
		MqttMsgsSubject(DeviceUplinkSubject("other", "t1", "d1")):                         false,
	} {
		if got := r.forwardsSubject(subject); got != want {
			t.Errorf("forwardsSubject(%q) = %v, want %v", subject, got, want)
		}
	}
}

// The raw-payload shape is declared once and every derivation agrees with it: the subject builder,
// the capture wildcard, the parser and the MQTT topic form. It is a sibling of the events leaf, and
// the events parser never reads it as telemetry.
func TestDeviceUplinkShapeDerivations(t *testing.T) {
	subject := DeviceUplinkSubject("inst", "acme", "pump-1")
	if got, want := subject, "inst.acme.devices.pump-1.uplink"; got != want {
		t.Fatalf("uplink subject = %q, want %q", got, want)
	}
	if got, want := DeviceUplinkWildcard("inst"), "inst.*.devices.*.uplink"; got != want {
		t.Errorf("uplink wildcard = %q, want %q", got, want)
	}
	if got, want := SubjectToMqttTopic(subject), "inst/acme/devices/pump-1/uplink"; got != want {
		t.Errorf("uplink MQTT topic = %q, want %q", got, want)
	}

	cases := []struct {
		subject, device string
		ok              bool
	}{
		{subject, "pump-1", true},
		{"inst.acme.devices.d1.events", "", false},
		{"inst.acme.devices..uplink", "", false},
		{"inst.acme.devices.d1.uplink.extra", "", false},
		{"inst.acme.things.d1.uplink", "", false},
		{"inst.acme.gateways.gw-1.devices.c1.uplink", "", false},
	}
	for _, c := range cases {
		d, ok := ParseDeviceUplinkSubject(c.subject)
		if d != c.device || ok != c.ok {
			t.Errorf("ParseDeviceUplinkSubject(%q) = (%q, %v), want (%q, %v)", c.subject, d, ok, c.device, c.ok)
		}
	}
	// The events parser is fixed to the events leaf: a caller that does not know the uplink leaf
	// can never take a raw payload for telemetry.
	if _, _, ok := ParseDeviceEventsSubject(subject); ok {
		t.Errorf("ParseDeviceEventsSubject read %q as telemetry", subject)
	}
}

// 🔴 OFF IS THE STREAM EVERY EARLIER BUILD DECLARED. A service refuses to start on a stream whose
// subjects differ from its own expectation, so an instance that has not opted in to raw device
// payloads must declare exactly the two subjects main does, or an old pod restarting mid-roll would
// exit with a drift error. On adds the raw-payload subject and nothing else.
func TestTheCaptureStreamDeclaresTheUplinkSubjectOnlyOnOptIn(t *testing.T) {
	off := []string{"inst.*.devices.*.events", "inst.*.gateways.*.devices.*.events"}
	for name, got := range map[string][]string{
		"DeviceEventsSubjects":      DeviceEventsSubjects("inst"),
		"DeviceEventsSubjectsWith":  DeviceEventsSubjectsWith("inst", false),
		"StreamSubjects":            StreamSubjects("inst", streams.DeviceEventsCapture),
		"StreamSubjectsWith(false)": StreamSubjectsWith("inst", streams.DeviceEventsCapture, false),
	} {
		if len(got) != 2 || got[0] != off[0] || got[1] != off[1] {
			t.Errorf("%s = %v, want exactly %v", name, got, off)
		}
	}
	on := StreamSubjectsWith("inst", streams.DeviceEventsCapture, true)
	if len(on) != 3 || on[0] != off[0] || on[1] != off[1] || on[2] != "inst.*.devices.*.uplink" {
		t.Errorf("opted-in capture subjects = %v", on)
	}
	// Another stream is unaffected by the setting.
	if a, b := StreamSubjectsWith("inst", streams.InboundEvents, true), StreamSubjects("inst", streams.InboundEvents); len(a) != 1 || a[0] != b[0] {
		t.Errorf("inbound-events subjects moved with the setting: %v vs %v", a, b)
	}
}

// The manager's expectation and its capture reader's hand-out follow the setting.
func TestTheManagersCaptureSubjectsFollowTheSetting(t *testing.T) {
	ms := &core.Microservice{InstanceId: "inst"}
	nmgr := &NatsManager{Microservice: ms}
	if got := nmgr.expectedStream(streams.DeviceEventsCapture).subjects; len(got) != 2 {
		t.Errorf("off: expected stream subjects = %v", got)
	}
	ms.InstanceConfiguration.Infrastructure.Nats.MqttDeviceUplinks = true
	if got := nmgr.expectedStream(streams.DeviceEventsCapture).subjects; len(got) != 3 {
		t.Errorf("on: expected stream subjects = %v", got)
	}
}
