// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package streams

import "testing"

// The backpressure declarations, pinned as a literal for the reason ReplayCovered's are: a
// name added to BackpressureReaders can stop every tenant's ingest, so adding one must fail
// here and send its author to the field's comment. Each reader must read the stream, must
// not be replay-covered (its unread position is a checkpoint no backlog shows), and the
// stream must be one a platform writer publishes to with a tenant in hand (Hot, tenant
// shape): a capture stream's producer is the broker, which no refusal can reach.
func TestBackpressureIsDeclaredOnlyWhereUnreadLossIsDataLoss(t *testing.T) {
	type pair struct{ suffix, area string }
	var got []pair
	for _, s := range All {
		for _, a := range s.BackpressureReaders {
			got = append(got, pair{s.Suffix, a})
			in := false
			for _, r := range s.Areas {
				in = in || r == a
			}
			if !in {
				t.Errorf("stream %q gates on area %q, which is not in its Areas", s.Suffix, a)
			}
			if ReplayCoveredBy(s.Suffix, a) {
				t.Errorf("stream %q gates on %q, whose unread position is its checkpoint, not its backlog", s.Suffix, a)
			}
		}
		if len(s.BackpressureReaders) > 0 && (s.Tier != Hot || s.Shape != ShapeTenant) {
			t.Errorf("stream %q applies backpressure but is not a Hot tenant-shaped stream a writer can be refused on", s.Suffix)
		}
	}
	want := []pair{{InboundEvents, "device-management"}, {ResolvedEvents, "event-management"}}
	if len(got) != len(want) {
		t.Fatalf("backpressure declarations = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("backpressure declarations = %v, want %v", got, want)
		}
	}
	for _, s := range []string{InboundEvents, ResolvedEvents} {
		if !AppliesBackpressure(s) {
			t.Errorf("AppliesBackpressure(%q) = false; it does not read the declaration", s)
		}
	}
	if r := BackpressureReadersFor(ResolvedEvents); len(r) != 1 || r[0] != "event-management" {
		t.Errorf("BackpressureReadersFor(resolved-events) = %v, want [event-management]", r)
	}
	for _, s := range []string{DeviceEventsCapture, CommandResponses, FailedEvents, FailedDecode, AlarmEvents,
		"a-suffix-nobody-declared"} {
		if AppliesBackpressure(s) {
			t.Errorf("%q must not apply backpressure", s)
		}
	}
}

// The forwarding table is what makes a hop PAUSE rather than fail its publish, and NewReader
// applies it from the reader's area, so it is pinned here as the whole of that wiring. Every
// forward must go into a stream that applies backpressure (otherwise there is no gate to park
// on), and the forwarding area must read the source and write the target (both Areas).
func TestForwardsIsTheIngestChain(t *testing.T) {
	type hop struct{ from, area, into string }
	var got []hop
	for _, s := range All {
		for area, into := range s.Forwards {
			got = append(got, hop{s.Suffix, area, into})
			if !AppliesBackpressure(into) {
				t.Errorf("%s on %q forwards into %q, which applies no backpressure", area, s.Suffix, into)
			}
			for _, sfx := range []string{s.Suffix, into} {
				in := false
				for _, r := range bySuffix[sfx].Areas {
					in = in || r == area
				}
				if !in {
					t.Errorf("%s forwards %q into %q but is not in %q's Areas", area, s.Suffix, into, sfx)
				}
			}
		}
	}
	want := map[hop]bool{
		{InboundEvents, "device-management", ResolvedEvents}:  true,
		{DeviceEventsCapture, "event-sources", InboundEvents}: true,
	}
	if len(got) != len(want) {
		t.Fatalf("forwards = %v, want %v", got, want)
	}
	for _, h := range got {
		if !want[h] {
			t.Fatalf("forwards = %v, want %v", got, want)
		}
	}
	if into := ForwardsInto(InboundEvents, "device-management"); into != ResolvedEvents {
		t.Errorf("ForwardsInto(inbound-events, device-management) = %q, want %q", into, ResolvedEvents)
	}
	if into := ForwardsInto(DeviceEventsCapture, "event-sources"); into != InboundEvents {
		t.Errorf("ForwardsInto(capture, event-sources) = %q, want %q", into, InboundEvents)
	}
	for _, area := range []string{"device-state", "event-management", "event-processing"} {
		if into := ForwardsInto(ResolvedEvents, area); into != "" {
			t.Errorf("%s does not forward resolved-events anywhere, got %q", area, into)
		}
	}
	if into := ForwardsInto(InboundEvents, "event-sources"); into != "" {
		t.Errorf("event-sources writes inbound-events, it does not read and forward it; got %q", into)
	}
}
