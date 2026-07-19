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

func TestSuffixesCoversAll(t *testing.T) {
	if got, want := len(Suffixes()), len(All); got != want {
		t.Errorf("Suffixes() returned %d entries for %d declared streams", got, want)
	}
}
