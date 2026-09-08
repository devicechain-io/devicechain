// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"strings"
	"testing"
	"time"
)

// Kinds is what dcctl's `--kind` help offers an operator, so a value it omits is a filter
// the tool tells them does not exist. Every declared Kind must be in it, and nothing that
// is not a Kind may be.
//
// 🔴 GO CANNOT ENUMERATE A CONST GROUP, so this cannot be derived — which is exactly why
// it is asserted. The assertion is written against the VALUES rather than against a
// second copy of the list: a fixture built from allKinds would agree with allKinds no
// matter what allKinds said.
func TestKindsOffersEveryDeclaredKind(t *testing.T) {
	offered := map[string]bool{}
	for _, k := range Kinds() {
		if offered[k] {
			t.Errorf("Kinds() lists %q twice", k)
		}
		offered[k] = true
	}
	for _, declared := range []Kind{
		KindDetectionAction, KindNotification, KindCommandResponse, KindConnectorDispatch,
	} {
		if !offered[string(declared)] {
			t.Errorf("kind %q is declared but not offered by Kinds(), so dcctl's --kind help "+
				"tells an operator a real filter value does not exist", declared)
		}
	}
	if len(Kinds()) != 4 {
		t.Errorf("Kinds() has %d entries; a kind was added or removed without this test and "+
			"the dcctl help being revisited: %v", len(Kinds()), Kinds())
	}
}

// The vocabulary is a metric label and a query filter, so a value carrying a comma or a
// space would be unsplittable in the help string dcctl joins and awkward in both of the
// bounded surfaces it feeds.
func TestKindsAreSimpleTokens(t *testing.T) {
	for _, k := range Kinds() {
		if k == "" || strings.ContainsAny(k, " ,\t\n") {
			t.Errorf("kind %q is not a simple token", k)
		}
	}
}

// The new kind has to survive the write-path validation the other three do, since an
// envelope that cannot be marshalled is a dead letter that is never recorded.
func TestConnectorDispatchEnvelopeValidates(t *testing.T) {
	body, err := Marshal(Envelope{
		Kind: KindConnectorDispatch, Reason: ReasonShed, Source: "outbound-connectors",
		Summary: "an outbound connector dispatch was given up on", OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("a well-formed connector-dispatch envelope was refused: %v", err)
	}
	back, err := Unmarshal(body)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if back.Kind != KindConnectorDispatch || back.Reason != ReasonShed {
		t.Fatalf("round trip lost the kind/reason: %+v", back)
	}
}
