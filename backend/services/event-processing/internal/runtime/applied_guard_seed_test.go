// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"testing"
	"time"
)

// The home slot depends on the per-process seed (so it cannot be ground offline), while the guard's
// answers do not.
func TestMixSeedMovesHomesButNotAnswers(t *testing.T) {
	saved := mixSeed
	t.Cleanup(func() { mixSeed = saved })

	run := func(seed uint64) (homes []int, answers []bool) {
		mixSeed = seed
		g := NewAppliedGuard(0, 0)
		now := time.Unix(1_700_000_000, 0)
		for i := uint64(1); i <= 2000; i++ {
			var d Digest
			d[0], d[1], d[7] = byte(i), byte(i>>8), byte(i*7)
			homes = append(homes, home(d.Fingerprint(), 1000003))
			answers = append(answers, g.Admit(now, d), g.Admit(now, d))
		}
		return
	}
	h1, a1 := run(1)
	h2, a2 := run(0x9e3779b97f4a7c15)
	moved := 0
	for i := range h1 {
		if h1[i] != h2[i] {
			moved++
		}
	}
	if moved < len(h1)*9/10 {
		t.Fatalf("seed moved only %d of %d home slots", moved, len(h1))
	}
	for i := range a1 {
		if a1[i] != a2[i] {
			t.Fatalf("answer %d differs across seeds", i)
		}
	}
}
