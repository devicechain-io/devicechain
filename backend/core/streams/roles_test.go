// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package streams

import (
	"reflect"
	"slices"
	"testing"
)

// Every area a role names must be a real one. A typo in a role is a grant to nobody that
// reads as a grant to somebody, so it is the one mistake the declaration cannot show.
func TestEveryRoleNamesAPlatformArea(t *testing.T) {
	for _, s := range All {
		for _, a := range s.Writers {
			if !IsPlatformArea(a) {
				t.Errorf("stream %q: writer %q is not a platform area", s.Suffix, a)
			}
		}
		for _, r := range s.Readers {
			if !IsPlatformArea(r.Area) {
				t.Errorf("stream %q: reader %q is not a platform area", s.Suffix, r.Area)
			}
		}
		seen := map[Reader]bool{}
		for _, r := range s.Readers {
			if seen[r] {
				t.Errorf("stream %q declares reader %+v twice", s.Suffix, r)
			}
			seen[r] = true
		}
		if len(s.Writers) != len(dedup(s.Writers)) {
			t.Errorf("stream %q declares a writer twice: %v", s.Suffix, s.Writers)
		}
	}
}

func dedup(in []string) []string {
	var out []string
	for _, a := range in {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// Somebody has to create every stream, or it does not exist on a running broker, and a stream
// that is declared but not Dormant promises that it does.
func TestEveryLiveStreamHasAnAdministrator(t *testing.T) {
	for _, s := range All {
		if !s.Dormant && len(s.Admins()) == 0 {
			t.Errorf("stream %q has no area that creates it", s.Suffix)
		}
	}
}

// The broker, not a service, writes these two. An empty writer list is the statement, so it
// is pinned: a service that starts publishing into either must declare itself here first.
func TestBrokerWrittenStreamsHaveNoServiceWriter(t *testing.T) {
	for _, suffix := range []string{DeviceEventsCapture, MaxDeliveries} {
		if w := WritersOf(suffix); len(w) != 0 {
			t.Errorf("%q is written by the broker, but declares writers %v", suffix, w)
		}
	}
}

// Known cases the roles must get right. They are chosen as the ones a mistake would be costly
// on: the ingest path (who may put a device event on the platform) and the readers that trust it.
func TestRolesMatchKnownUse(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  bool
	}{
		{"event-sources writes inbound-events", Writes(InboundEvents, "event-sources")},
		{"lwm2m-ingest writes inbound-events", Writes(InboundEvents, "lwm2m-ingest")},
		{"sparkplug-ingest writes inbound-events", Writes(InboundEvents, "sparkplug-ingest")},
		{"device-management reads inbound-events", ReadsAs(InboundEvents, "device-management", ReadDurable)},
		{"device-management writes resolved-events", Writes(ResolvedEvents, "device-management")},
		{"event-processing reads resolved-events at the floor", ReadsAs(ResolvedEvents, "event-processing", ReadAckFloor)},
		{"event-processing replays resolved-events", ReadsAs(ResolvedEvents, "event-processing", ReadEphemeral)},
		{"event-management follows resolved-events live", ReadsAs(ResolvedEvents, "event-management", ReadLive)},
		{"event-sources writes command-responses", Writes(CommandResponses, "event-sources")},
		{"command-delivery reads command-responses", ReadsAs(CommandResponses, "command-delivery", ReadDurable)},
		{"event-sources reads the capture", ReadsAs(DeviceEventsCapture, "event-sources", ReadDurable)},
		{"dashboard-management writes dead-letters (derived)", Writes(DeadLetters, "dashboard-management")},
		{"dashboard-management records max-deliveries (derived)", ReadsAs(MaxDeliveries, "dashboard-management", ReadDurable)},
		{"user-management reads dead-letters", ReadsAs(DeadLetters, "user-management", ReadDurable)},
	} {
		if !tc.got {
			t.Errorf("%s: not declared", tc.name)
		}
	}
	for _, tc := range []struct {
		name string
		got  bool
	}{
		// The things a compromise of the wrong area must not be able to claim.
		{"event-management must not write inbound-events", Writes(InboundEvents, "event-management")},
		{"event-processing must not write inbound-events", Writes(InboundEvents, "event-processing")},
		{"device-state must not write resolved-events", Writes(ResolvedEvents, "device-state")},
		{"update-management must not write command-responses", Writes(CommandResponses, "update-management")},
		{"sparkplug-ingest must not write dead-letters (it has no reader)", Writes(DeadLetters, "sparkplug-ingest")},
		{"mcp must not administer any stream", func() bool {
			for _, s := range All {
				if Administers(s.Suffix, "mcp") || Administers(s.Suffix, "ai-inference") {
					return true
				}
			}
			return false
		}()},
	} {
		if tc.got {
			t.Errorf("%s", tc.name)
		}
	}
}

// An area that only follows a stream live, or replays it, opens nothing: neither creates
// the stream, so neither is its administrator on that account.
func TestOnlyWritingAndConsumingMakeAnAdmin(t *testing.T) {
	for _, s := range All {
		for _, r := range s.Readers {
			if r.Mode.Consumes() {
				if !Administers(s.Suffix, r.Area) {
					t.Errorf("%q: %s consumes it but does not administer it", s.Suffix, r.Area)
				}
				continue
			}
			if Administers(s.Suffix, r.Area) && !Writes(s.Suffix, r.Area) && !ReadsAs(s.Suffix, r.Area, ReadDurable) &&
				!ReadsAs(s.Suffix, r.Area, ReadAckFloor) {
				t.Errorf("%q: %s only reads it %s, yet administers it", s.Suffix, r.Area, r.Mode)
			}
		}
	}
}

// The recorder areas are exactly the areas with a consuming reader on a stream that is not the
// advisory capture. The derived writers and readers of the two sink streams follow from it.
func TestRecorderAreasAreTheConsumingAreas(t *testing.T) {
	want := map[string]bool{}
	for _, s := range All {
		if s.Shape == ShapeAdvisory {
			continue
		}
		for _, r := range s.Readers {
			if r.Mode.Consumes() {
				want[r.Area] = true
			}
		}
	}
	got := RecorderAreas()
	if len(got) != len(want) {
		t.Fatalf("RecorderAreas = %v, want the %d consuming areas", got, len(want))
	}
	for _, a := range got {
		if !want[a] {
			t.Errorf("RecorderAreas names %q, which has no consuming reader", a)
		}
	}
	for _, a := range got {
		if !Writes(DeadLetters, a) || !ReadsAs(MaxDeliveries, a, ReadDurable) {
			t.Errorf("recorder area %q must write dead-letters and read max-deliveries", a)
		}
	}
	if len(WritersOf(DeadLetters)) != len(got) {
		t.Errorf("dead-letters writers %v differ from the recorder areas %v", WritersOf(DeadLetters), got)
	}
}

// Nothing in a declaration can express a stream that sources, mirrors, transforms or
// re-publishes: the Stream type has no such field. The test makes that a statement a maintainer
// meets BEFORE adding one, because the consumer-name argument for not moving consumers to an
// administrator rests on it: a pushed entry lands under its source subject only while no
// stream stores subjects outside its own.
func TestStreamDeclarationHasNoCrossStreamFields(t *testing.T) {
	for _, banned := range []string{"Sources", "Mirror", "SubjectTransform", "RePublish", "Republish"} {
		if _, ok := reflect.TypeOf(Stream{}).FieldByName(banned); ok {
			t.Errorf("streams.Stream declares %q; revisit the consumer-name decision (consumers stay with services only while no stream sources, mirrors, transforms or re-publishes)", banned)
		}
	}
}
