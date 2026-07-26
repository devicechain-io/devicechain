// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

// The size this guard compares against must come from the SHIPPED artifact.
//
// A restated constant keeps comparing against the old volume the moment the real
// one moves — which is exactly the event the guard exists to notice, so the one
// change that must trip it is the one that would silently stop it working.
func TestJetStreamStorageIsReadFromTheShippedTofu(t *testing.T) {
	got := embeddedJetStreamStorageDefault()
	if got == "" {
		t.Fatal("could not read nats_jetstream_storage out of the embedded variables.tf: " +
			"the guard can no longer see the volume it is checking against, so it would " +
			"wave through the upgrade break it exists to catch")
	}
	if !strings.HasSuffix(got, "Gi") {
		t.Errorf("nats_jetstream_storage default is %q, which is not a Gi quantity; the "+
			"comparison below parses it as a Kubernetes resource.Quantity", got)
	}

	// And the compact preset must use ITS volume, not the default one — compact
	// deliberately provisions a much smaller PV, so comparing it against the
	// shipped default would report a spurious conflict on every compact upgrade.
	st := &State{Compact: true}
	if want := compact.JetStreamStorage; jetStreamStorageFor(st) != want {
		t.Errorf("compact bootstrap would check against %q, want the compact volume %q",
			jetStreamStorageFor(st), want)
	}
	if jetStreamStorageFor(&State{}) != got {
		t.Errorf("a default bootstrap checks against %q, want the shipped default %q",
			jetStreamStorageFor(&State{}), got)
	}
	if compact.JetStreamStorage == got {
		t.Fatal("the compact volume equals the default, so the two branches above are " +
			"indistinguishable and this test proves nothing about which one is used")
	}
}
