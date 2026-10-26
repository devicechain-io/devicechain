// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-simulator/sim"
)

// A refused connect is the ingress being unreachable: it is neither a shed nor backpressure,
// so the report names it under its own count and the refusal description includes it.
func TestConnectRefusedIsReportedUnderItsOwnName(t *testing.T) {
	snap := sim.Snapshot{Emitted: 10, ConnectRefused: 1431}
	led := ledgerOf(snap)
	if led.ConnectRefused != 1431 || led.Backpressured != 0 || led.Shed != 0 {
		t.Fatalf("ledger = %+v, want 1431 connect-refused and no shed or backpressure", led)
	}
	if want := "accepted 10, shed 0, backpressured 0, connect-refused 1431, failed 0"; led.outcome() != want {
		t.Errorf("outcome = %q, want %q", led.outcome(), want)
	}
	desc, ok := backgroundRefusals(snap)
	if !ok || !strings.Contains(desc, "1431 connects refused") {
		t.Errorf("backgroundRefusals = %q, %v; want it to count the refused connects", desc, ok)
	}
	// A refused connect never fails clean-drive: it is provably absent.
	for _, inv := range Reconcile(10, led.Failed, 10, 1) {
		if inv.Name == InvCleanDrive && !inv.Passed {
			t.Errorf("clean-drive failed over refused connects: %s", inv.Detail)
		}
	}
}

// A refused connect (nothing listening) is not shed priority for either tenant at any floor:
// the run fails as unattributable, by name, and gold is not read as shed.
func TestConnectRefusedIsUnattributable(t *testing.T) {
	for _, floor := range []int{0, 2} {
		gold := contendedGold()
		gold.ConnectRefused = 3
		invs := classifyContention(cfgFloor(floor), gold, contendedShed())
		if !invByName(t, invs, InvGoldNeverShed).Passed {
			t.Errorf("floor %d: a refused connect was read as a gold shed", floor)
		}
		found := false
		for _, inv := range invs {
			if inv.Name == InvContentionUnbackpressured && !inv.Passed && strings.Contains(inv.Detail, "connect(s) refused") {
				found = true
			}
		}
		if !found {
			t.Errorf("floor %d: refused connects did not fail the run as unattributable", floor)
		}
	}
}
