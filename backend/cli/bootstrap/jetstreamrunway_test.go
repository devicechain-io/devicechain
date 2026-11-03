// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
)

// 🔴 THE OUTAGE RUNWAY IS CHECKED AGAINST THE VOLUME, AND NAMED WHEN IT IS WHAT DOES NOT FIT.
// 20,000 events a second held for 15 minutes asks the three event streams for about 20 GiB, which
// the default 14Gi store cannot hold: the install is refused before anything is written, and
// the refusal says it is the runway and by how much, not just that "the ceilings" are too big.
// The same shape without the runway must not claim it (the negative control), and a volume that
// is grown to fit passes.
func TestTheRunwayIsRefusedWhenTheVolumeCannotHoldIt(t *testing.T) {
	t.Parallel()
	cfg := config.NewDefaultInstanceConfiguration()
	cfg.ApplyDefaults()
	n := &cfg.Infrastructure.Nats
	if err := checkJetStreamReservation(n, 14<<30, "test"); err != nil {
		t.Fatalf("the shipped defaults do not fit the default store: %v", err)
	}

	n.StreamDesignRate, n.StreamRunwayMinutes = 20_000, 15
	err := checkJetStreamReservation(n, 14<<30, "90% of its 16Gi JetStream volume")
	if err == nil {
		t.Fatal("a 20,000 events/s, 15-minute runway fit a 14Gi store: the check cannot refuse")
	}
	for _, want := range []string{"outage runway", "20000 events a second", "15 minutes", "streamDesignRate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if err := checkJetStreamReservation(n, 40<<30, "a grown store"); err != nil {
		t.Errorf("a store grown to hold the runway was refused: %v", err)
	}

	// Negative control: over budget WITHOUT the runway, the message is the plain one.
	plain := config.NewDefaultInstanceConfiguration()
	plain.ApplyDefaults()
	err = checkJetStreamReservation(&plain.Infrastructure.Nats, 1<<30, "test")
	if err == nil || strings.Contains(err.Error(), "outage runway") {
		t.Errorf("an over-budget instance with no runway growth blamed the runway: %v", err)
	}
}
