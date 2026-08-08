// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-simulator/sim"
)

// --no-command-far-end must WARN in every mode, and say something different in each.
//
// 🔴 The none case is the one this test exists for, and it is the one that went
// missing: /status reports the flag only where there is a far end to give up, so for
// a scenario that declares none this warning is the ONLY report anywhere that the
// operator passed it. Silence there is not "nothing to say" — it is a flag the
// operator believes took effect, against a run it never touched, and every
// observation that follows is read through that belief.
//
// Distinctness is asserted alongside presence because a single shared message would
// satisfy "every mode warns" while telling an operator nothing about which situation
// they are in — and the internal and external consequences differ (one far end was
// skipped, the other was never attached and its broker requirement was dropped).
func TestFarEndOptOutWarnsInEveryModeAndSaysSomethingDifferent(t *testing.T) {
	modes := []sim.CommandFarEndMode{sim.FarEndNone, sim.FarEndInternal, sim.FarEndExternal}
	seen := map[string]sim.CommandFarEndMode{}
	for _, mode := range modes {
		msg := farEndOptOutWarning(mode)
		if strings.TrimSpace(msg) == "" {
			t.Errorf("mode %q produces no warning, so --no-command-far-end passes without "+
				"comment and reads as a flag that worked", mode)
			continue
		}
		if !strings.Contains(msg, "--no-command-far-end") {
			t.Errorf("mode %q: warning %q does not name the flag it is about", mode, msg)
		}
		if other, dup := seen[msg]; dup {
			t.Errorf("modes %q and %q share the warning %q, so it cannot tell an operator "+
				"which situation they are in", other, mode, msg)
		}
		seen[msg] = mode
	}

	// The zero value reaches this function through Manifest().FarEndMode(), which
	// normalizes it — but the warning is the last line of defence for a flag that
	// otherwise goes unreported, so it must not depend on that having happened.
	if strings.TrimSpace(farEndOptOutWarning("")) == "" {
		t.Error("an unnormalized empty mode produces no warning")
	}
}
