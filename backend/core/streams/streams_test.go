// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package streams

import "testing"

// The declaration is only worth centralizing if it cannot be quietly incomplete.
// These tests pin the properties that make "add an entry to All" sufficient.

func TestAllIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range All {
		if s.Suffix == "" {
			t.Error("a stream declares an empty suffix")
		}
		if seen[s.Suffix] {
			// A duplicate would be counted twice by the budget, inflating the
			// disk floor — which looks like conservatism rather than a bug.
			t.Errorf("suffix %q is declared twice", s.Suffix)
		}
		seen[s.Suffix] = true
		if s.Why == "" {
			// The tier is a disk-sizing decision. Recording what drives the
			// volume is what lets the next person re-evaluate it instead of
			// inheriting it.
			t.Errorf("suffix %q declares no rationale for its tier", s.Suffix)
		}
	}
}

// A suffix built by concatenation is the one a constant scan cannot find, and it
// reserves disk exactly like any other stream. Anything DeadLetter can produce
// for a declared base must therefore be declared too.
func TestDerivedSuffixesAreDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, s := range All {
		declared[s.Suffix] = true
	}
	// Only connector-dispatch has a dead-letter stream today. If another base
	// gains one, it belongs in All and in this list — which is the point: the
	// derived name has to be written down somewhere that a reader will find.
	for _, base := range []string{ConnectorDispatch} {
		if got := DeadLetter(base); !declared[got] {
			t.Errorf("DeadLetter(%q) = %q, which is not declared in All; "+
				"a derived stream reserves disk like any other and would be "+
				"missing from the budget", base, got)
		}
	}
	if ConnectorDispatchDead != DeadLetter(ConnectorDispatch) {
		t.Errorf("ConnectorDispatchDead = %q but DeadLetter(%q) = %q; the "+
			"constant and the builder must agree or one of them is a lie",
			ConnectorDispatchDead, ConnectorDispatch, DeadLetter(ConnectorDispatch))
	}
}

// The fail-safe direction: an unclassified suffix must land on the LARGER
// ceiling. Over-reserving disk is visible and cheap; under-bounding a busy
// stream silently evicts live data via DiscardOld and looks like nothing.
func TestTierForFailsSafeToHot(t *testing.T) {
	if got := TierFor("a-suffix-nobody-declared"); got != Hot {
		t.Errorf("TierFor(unknown) = %v, want Hot — an unclassified stream must "+
			"over-reserve rather than silently under-buffer", got)
	}
	if got := TierFor(InboundEvents); got != Hot {
		t.Errorf("TierFor(%q) = %v, want Hot", InboundEvents, got)
	}
	if got := TierFor(DeviceRoster); got != Cold {
		t.Errorf("TierFor(%q) = %v, want Cold", DeviceRoster, got)
	}
}

func TestIsPerDevice(t *testing.T) {
	if !IsPerDevice(DeviceCommands) {
		t.Errorf("%q addresses an individual device and must report PerDevice", DeviceCommands)
	}
	// Responses are per-device for the same reason commands are: the device token in
	// the subject is what the broker grant confines a device to, and therefore the only
	// AUTHENTICATED statement of who answered. A tenant-scoped response subject let any
	// device in a tenant settle any other device's command.
	//
	// 🔴 THIS TEST USED TO ASSERT THE OPPOSITE, with a reason that was not true: that
	// per-device "would split the consumer's filter and strand every response". It does
	// not — StreamSubject appends the wildcard level for this shape, so one filter still
	// covers every device, exactly as it already did for DeviceCommands.
	if !IsPerDevice(CommandResponses) {
		t.Errorf("%q carries the responding device's token and must report PerDevice", CommandResponses)
	}
	// An unknown suffix must not claim to be per-device, or its stream would be
	// created with a wildcard level no publish ever fills.
	if IsPerDevice("a-suffix-nobody-declared") {
		t.Error("an unknown suffix must not report PerDevice")
	}
}

func TestShapeOf(t *testing.T) {
	cases := map[string]Shape{
		InboundEvents:       ShapeTenant,
		DeviceCommands:      ShapeTenantDevice,
		DeviceEventsCapture: ShapeDeviceEvents,
		// An unknown suffix must default to the ordinary tenant shape. The other two
		// shapes each imply a subject level no publish would ever fill, so guessing
		// either for an undeclared suffix builds a stream nothing lands in.
		"a-suffix-nobody-declared": ShapeTenant,
	}
	for suffix, want := range cases {
		if got := ShapeOf(suffix); got != want {
			t.Errorf("ShapeOf(%q) = %v, want %v", suffix, got, want)
		}
	}
}

// A cap may only be declared where it is intended, which is now the max-delivery capture
// alone. An accidental cap on a stream meant to track its tier pins it below the ceiling an
// operator sized the deployment with, and nothing reports it.
//
// 🔴 THE DEVICE-EVENTS CAPTURE STREAM IS NAMED HERE ON PURPOSE. It carried a 256 MiB cap, and
// a perf run lost 7.2% of its PUBACKed MQTT events when the backlog crossed it, with no
// setting able to raise it: streamMaxBytes moved the tier and the cap stayed.
// Its ceiling is the whole margin between devices publishing faster than event-sources drains
// and losing events the devices were told were accepted, so it must stay the operator's to set.
func TestMaxBytesCapIsDeclaredOnlyWhereIntended(t *testing.T) {
	if got := MaxBytesCapFor(DeviceEventsCapture); got != 0 {
		t.Errorf("MaxBytesCapFor(%q) = %d, want 0: the capture stream's ceiling must track the Hot "+
			"tier, the one an operator sizes, since it is all that stands between a slow drain and "+
			"losing PUBACKed device events", DeviceEventsCapture, got)
	}
	// The max-delivery capture is capped because it is empty in steady state, and its tier
	// ceiling would reserve far more than it could ever need.
	if got := MaxBytesCapFor(MaxDeliveries); got != maxDeliveriesMaxBytesCap {
		t.Errorf("MaxBytesCapFor(%q) = %d, want %d", MaxDeliveries, got, int64(maxDeliveriesMaxBytesCap))
	}
	for _, s := range All {
		if s.Suffix == MaxDeliveries {
			continue
		}
		if got := MaxBytesCapFor(s.Suffix); got != 0 {
			t.Errorf("stream %q declares a ceiling cap of %d; only the max-delivery capture should, "+
				"since every other stream is meant to track its tier's ceiling", s.Suffix, got)
		}
	}
}

// Every stream says what a letter about one of its messages is — or that there is none.
// An empty declaration is refused because the max-delivery recorder reads it for every
// stream a service consumes: "" would be a stream whose abandoned deliveries are neither
// lettered nor counted as refused, which is the silent drop the recorder exists to end.
// The VALUES are checked against the vocabulary in core/deadletter, which this leaf
// cannot import.
func TestEveryStreamDeclaresADeadLetterKind(t *testing.T) {
	for _, s := range All {
		if s.DeadLetterKind == "" {
			t.Errorf("stream %q declares no DeadLetterKind; declare its kind, or NotLettered", s.Suffix)
		}
	}
	// The sinks and the capture must never be lettered: a letter about a letter loops.
	for _, sink := range []string{DeadLetters, ConnectorDispatchDead, FailedEvents, FailedDecode, MaxDeliveries} {
		if got := DeadLetterKindFor(sink); got != NotLettered {
			t.Errorf("DeadLetterKindFor(%q) = %q, want NotLettered", sink, got)
		}
	}
	if got := DeadLetterKindFor("a-suffix-nobody-declared"); got != "" {
		t.Errorf("DeadLetterKindFor(unknown) = %q, want \"\" so a caller refuses it", got)
	}
}

// A verbatim copy is a real stream the writer ensures and the budget counts, so it must be
// declared — and it must itself be unlettered, or a give-up on the copy would be copied.
func TestVerbatimCopyNamesADeclaredStream(t *testing.T) {
	copies := 0
	for _, s := range All {
		if s.VerbatimCopy == "" {
			continue
		}
		copies++
		if !IsDeclared(s.VerbatimCopy) {
			t.Errorf("stream %q names verbatim copy %q, which is not declared", s.Suffix, s.VerbatimCopy)
		}
		if DeadLetterKindFor(s.VerbatimCopy) != NotLettered {
			t.Errorf("verbatim copy %q must be NotLettered", s.VerbatimCopy)
		}
	}
	if got := VerbatimCopyFor(ConnectorDispatch); got != ConnectorDispatchDead {
		t.Errorf("VerbatimCopyFor(%q) = %q, want %q", ConnectorDispatch, got, ConnectorDispatchDead)
	}
	if copies != 1 {
		t.Errorf("%d streams declare a verbatim copy; want exactly connector-dispatch", copies)
	}
}

// A replay-covered area silences the only record of its durable's give-ups, so the claim is
// held to three things this leaf can check: the area actually reads the stream (it holds a
// durable reader), the stream is one whose give-ups would otherwise be lettered (a NotLettered stream
// has nothing to silence, and naming an area there would read as a claim nobody made), and
// the set is exactly the one the tree has proven. The last is deliberately a literal: adding a
// name must fail here and send its author to write the proof the field's comment asks for.
func TestReplayCoveredIsDeclaredOnlyWhereProven(t *testing.T) {
	type pair struct{ suffix, area string }
	var got []pair
	for _, s := range All {
		for _, a := range s.ReplayCovered {
			got = append(got, pair{s.Suffix, a})
			in := false
			in = ReadsAs(s.Suffix, a, ReadDurable) || ReadsAs(s.Suffix, a, ReadAckFloor)
			if !in {
				t.Errorf("stream %q declares area %q replay-covered, but %q has no durable reader on it", s.Suffix, a, a)
			}
			if s.DeadLetterKind == NotLettered {
				t.Errorf("stream %q is NotLettered, so declaring %q replay-covered on it silences nothing", s.Suffix, a)
			}
		}
	}
	want := []pair{{ResolvedEvents, "event-processing"}}
	same := len(got) == len(want)
	for i := 0; same && i < len(got); i++ {
		same = got[i] == want[i]
	}
	if !same {
		t.Errorf("replay-covered declarations = %v, want %v; a new one needs a test that exhausts the "+
			"durable's deliveries on a real broker and shows its checkpoint still covers every message", got, want)
	}
	if !ReplayCoveredBy(ResolvedEvents, "event-processing") {
		t.Error("ReplayCoveredBy does not read the declaration")
	}
	for _, other := range []string{"device-state", "event-management"} {
		if ReplayCoveredBy(ResolvedEvents, other) {
			t.Errorf("%s reads resolved-events through an ordinary durable; its give-ups must be lettered", other)
		}
	}
	if ReplayCoveredBy("a-suffix-nobody-declared", "event-processing") {
		t.Error("an undeclared suffix must not read as replay-covered")
	}
}

// The work-queue retention is the max-delivery capture's alone. Anything else declared so
// would lose a message the moment one reader acked it — every other stream has several
// readers, one per area, each owed its own copy.
func TestOnlyTheCaptureStreamIsWorkQueue(t *testing.T) {
	for _, s := range All {
		want := RetentionLimits
		if s.Suffix == MaxDeliveries || s.Suffix == DeviceEventsCapture {
			want = RetentionWorkQueue
		}
		if got := RetentionFor(s.Suffix); got != want {
			t.Errorf("RetentionFor(%q) = %d, want %d", s.Suffix, got, want)
		}
		if got, want := RecreatesOnRetentionChange(s.Suffix), s.Suffix == DeviceEventsCapture; got != want {
			t.Errorf("RecreatesOnRetentionChange(%q) = %v, want %v", s.Suffix, got, want)
		}
		if s.Retention == RetentionWorkQueue && len(s.Admins()) != 1 && s.Suffix == DeviceEventsCapture {
			t.Errorf("%q is a work queue and must have exactly one reading area, has %v", s.Suffix, s.Admins())
		}
	}
	if ShapeOf(MaxDeliveries) != ShapeAdvisory {
		t.Errorf("%q must be the advisory shape", MaxDeliveries)
	}
}

func TestSuffixesCoversAll(t *testing.T) {
	if got, want := len(Suffixes()), len(All); got != want {
		t.Errorf("Suffixes() returned %d entries for %d declared streams", got, want)
	}
}

// The two device-plane channels are per-device (the token in the subject is what the
// signed grant confines a device to) and Cold (volume follows state changes). They were
// dormant until the configuration delivery loop gave each a reader or writer; the dormant
// set is pinned empty so a stream that is declared before it is used is a deliberate act.
func TestDormantStreamsArePinned(t *testing.T) {
	var got []string
	for _, s := range All {
		if s.Dormant {
			got = append(got, s.Suffix)
		}
	}
	if len(got) != 0 {
		t.Fatalf("dormant streams = %v, want none: a stream that gained a reader or writer must "+
			"stop being dormant, and a new dormant one must be added here deliberately", got)
	}
	for _, suffix := range []string{DeviceDesired, DeviceReports} {
		if !IsPerDevice(suffix) {
			t.Errorf("%q must be per-device so the grant can confine a device to its own", suffix)
		}
		if TierFor(suffix) != Cold {
			t.Errorf("%q must be Cold: it is not a per-sample stream", suffix)
		}
		if RetentionFor(suffix) != RetentionLimits {
			t.Errorf("%q must use Limits retention (age and byte bounded)", suffix)
		}
	}
}

// PlatformAuthored is the whole list of streams whose dead letters keep their payload
// verbatim; every other stream's payload is redacted. Adding to this list is a deliberate
// statement that no device writes to the stream.
func TestPlatformAuthoredStreamsArePinned(t *testing.T) {
	want := map[string]bool{
		ResolvedEvents: true, DerivedEvents: true, DeviceAttribute: true, DeviceCommands: true,
		DeviceDesired: true, ConnectorDispatch: true, DetectionRulesPublished: true, DeviceRoster: true,
		GeoFenceSetManifest: true, EntityDeleted: true, AlarmEvents: true, RaiseAlarm: true, FailedEvents: true,
		DeviceOwnership: true,
	}
	got := map[string]bool{}
	for _, s := range All {
		if s.PlatformAuthored {
			got[s.Suffix] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("platform-authored streams = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] || !PlatformAuthoredFor(k) {
			t.Errorf("%q must be declared platform-authored", k)
		}
	}
	// The device-written streams never are.
	for _, k := range []string{InboundEvents, DeviceEventsCapture, CommandResponses, DeviceReports, FailedDecode, "not-declared"} {
		if PlatformAuthoredFor(k) {
			t.Errorf("%q must not be platform-authored", k)
		}
	}
	if KindPlatformAuthored("command-response") || KindPlatformAuthored("no-such-kind") || !KindPlatformAuthored("notification") {
		t.Error("KindPlatformAuthored must be true only where every stream of the kind is platform-authored")
	}
}
