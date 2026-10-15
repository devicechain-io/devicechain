// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	"github.com/devicechain-io/dc-device-state/config"
)

// The channel between the read loop and the writers must hold at least two rounds of a full
// pool (writers x maxBatch). On main it was a constant 100 against 320.
func TestMessageChannelHoldsTwoRoundsOfAFullPool(t *testing.T) {
	for _, pc := range []config.ProjectionConfiguration{
		{Writers: 10, MaxBatch: 32},
		{Writers: 20, MaxBatch: 64},
		{Writers: 4, MaxBatch: 8},
	} {
		sp := &StateProcessor{projection: pc}
		sp.initializeWorkers()
		got := cap(sp.messages)
		close(sp.messages)
		sp.workerWG.Wait()
		if got < 2*pc.Writers*pc.MaxBatch {
			t.Errorf("%+v: capacity %d below 2 x writers x maxBatch = %d", pc, got, 2*pc.Writers*pc.MaxBatch)
		}
	}
}

func TestMessageChannelKeepsAFloorWithNoWriters(t *testing.T) {
	sp := &StateProcessor{}
	sp.initializeWorkers()
	if cap(sp.messages) != 100 {
		t.Errorf("capacity %d, want %d", cap(sp.messages), 100)
	}
}
