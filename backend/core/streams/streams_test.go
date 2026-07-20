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
	// Responses are deliberately tenant-scoped: one subject, one consumer,
	// correlated by command token. Marking them per-device would split the
	// consumer's filter and strand every response.
	if IsPerDevice(CommandResponses) {
		t.Errorf("%q is tenant-scoped, not per-device", CommandResponses)
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

// The cap must be declared on the capture stream and on nothing else — an
// accidental cap on a stream meant to track its tier would pin it below the
// ceiling an operator sized the deployment with, and nothing would report it.
func TestMaxBytesCapIsDeclaredOnlyWhereIntended(t *testing.T) {
	if got := MaxBytesCapFor(DeviceEventsCapture); got != deviceEventsCaptureMaxBytesCap {
		t.Errorf("MaxBytesCapFor(%q) = %d, want %d: the capture stream is capped because the "+
			"Hot tier ceiling does not fit the disk budget",
			DeviceEventsCapture, got, int64(deviceEventsCaptureMaxBytesCap))
	}
	// The cap must stay under the free space the default budget actually has. That
	// is enforced end-to-end by config's reservation tests (and, at the far smaller
	// --compact size, by dcctl's); this pins the intent at the declaration, where
	// the number is chosen.
	if deviceEventsCaptureMaxBytesCap > 384<<20 {
		t.Errorf("capture cap %d B exceeds the 384 MiB the default budget leaves free above "+
			"its headroom floor; see config.TestBudgetLeavesHeadroomForUnaccountedStreams",
			int64(deviceEventsCaptureMaxBytesCap))
	}
	for _, s := range All {
		if s.Suffix == DeviceEventsCapture {
			continue
		}
		if got := MaxBytesCapFor(s.Suffix); got != 0 {
			t.Errorf("stream %q declares a ceiling cap of %d; only the capture stream should, "+
				"since every other stream is meant to track its tier's ceiling", s.Suffix, got)
		}
	}
}

func TestSuffixesCoversAll(t *testing.T) {
	if got, want := len(Suffixes()), len(All); got != want {
		t.Errorf("Suffixes() returned %d entries for %d declared streams", got, want)
	}
}
