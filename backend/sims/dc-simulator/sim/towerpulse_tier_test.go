// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// dcctl creates the tier the Towerpulse showcase runs at (backend/cli/sim/tier.go), and it cannot
// import this module, so the number it writes is a literal there. This test derives the number
// the scenario needs from its own manifest and holds the literal to it: a profile that gains a
// calculated metric fails here instead of as a refused publish on a live run.
//
// The parsed file lives outside this module and go's test cache does not track it; CI and the
// pre-commit sweep run with -count=1.
func TestTowerpulseTierCoversTheScenario(t *testing.T) {
	const dcctlTier = "../../../cli/sim/tier.go"
	source, err := os.ReadFile(dcctlTier)
	if err != nil {
		t.Fatalf("read %s: %v (if dcctl's tier file moved, re-point this check rather than deleting it)", dcctlTier, err)
	}

	need := 0
	for _, p := range NewTowerpulse(1, Load{}).Manifest().Profiles {
		if n := calculatedMetricCount(p); n > need {
			need = n
		}
	}
	if need == 0 {
		t.Fatal("no Towerpulse profile declares a calculated metric, so the showcase tier has no reason to exist; remove it from dcctl")
	}

	m := regexp.MustCompile(`"calculatedMetricsPerProfile":\s*(\d+),`).FindSubmatch(source)
	if m == nil {
		t.Fatalf("could not find calculatedMetricsPerProfile in %s; the check would pass without asserting anything", dcctlTier)
	}
	have, _ := strconv.Atoi(string(m[1]))
	if have != need {
		t.Errorf("dcctl's showcase tier grants calculatedMetricsPerProfile %d, but the scenario's busiest profile declares %d: "+
			"set it to exactly the number needed (too low refuses the publish; too high spends storage rows the scenario never uses)", have, need)
	}
}
