// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 🔴 A FIXTURE BUILT FROM THE CONSTANT CANNOT CATCH THE CONSTANT MOVING. Every assertion
// about the settle window used to be written as `SettleWindow + jitter`, which agrees with
// SettleWindow wherever it goes — including to zero, where the drain's first pass runs
// immediately and a bring-up racing its own broker roll releases the whole fleet before
// the run that would have fixed it has finished. The numbers below are also PUBLISHED —
// the device-presence concept page and the edge-services deployment page in both locales,
// and the chart's values.yaml all quote them — so they are pinned the way a reader checks
// them: against a literal.

// TestTheSettleWindowIsTheTwoMinutesEveryPageQuotes.
func TestTheSettleWindowIsTheTwoMinutesEveryPageQuotes(t *testing.T) {
	require.Equal(t, 2*time.Minute, SettleWindow,
		"the settle window is published as two minutes; move the prose with the number")
}

// TestTheDrainRateIsThe25PerSecondEveryPageQuotes. The pace is the difference between a
// background repair and a fleet-wide durable-write burst that every tenant on the instance
// pays for at once, and it is quoted as 25/s in the deployment page in both locales.
func TestTheDrainRateIsThe25PerSecondEveryPageQuotes(t *testing.T) {
	require.Equal(t, 25, demoteRate,
		"the drain pace is published as 25 devices a second")
}

// TestTheRateWaiterIsBuiltAtThatRate is the other half: a pinned constant that nothing
// reads would be a number in a comment. The waiter must actually be paced by it, with a
// burst of one so a long idle stretch cannot bank tokens and release a fleet in a second.
func TestTheRateWaiterIsBuiltAtThatRate(t *testing.T) {
	w := NewRateWaiter()
	require.NotNil(t, w.limiter)
	require.Equal(t, float64(demoteRate), float64(w.limiter.Limit()))
	require.Equal(t, 1, w.limiter.Burst(),
		"a burst above one lets an idle drain release a fleet in a single second")
}
