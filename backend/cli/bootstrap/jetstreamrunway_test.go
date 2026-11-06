// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
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
	if err := checkJetStreamReservation(n, 14<<30, "test", true); err != nil {
		t.Fatalf("the shipped defaults do not fit the default store: %v", err)
	}

	n.StreamDesignRate, n.StreamRunwayMinutes = 20_000, 15
	err := checkJetStreamReservation(n, 14<<30, "90% of its 16Gi JetStream volume", true)
	if err == nil {
		t.Fatal("a 20,000 events/s, 15-minute runway fit a 14Gi store: the check cannot refuse")
	}
	for _, want := range []string{"outage runway", "20000 events a second", "15 minutes", "streamDesignRate", "dcctl upgrade <provider> <instance> --values", "at least 34Gi"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	// The 33Gi the published sizing table names still holds the 20,000/s, 15-minute runway: a KV
	// bucket that declares its own small ceiling must not push the sum past it.
	have, derr := derivedMaxFileStore("33Gi")
	if derr != nil {
		t.Fatal(derr)
	}
	t.Logf("reservation %d MiB, 33Gi derives %d MiB", jetStreamReservation(n)>>20, have>>20)
	if err := checkJetStreamReservation(n, have, "33Gi", true); err != nil {
		t.Errorf("the documented 33Gi no longer holds the 20,000/s runway: %v", err)
	}
	if err := checkJetStreamReservation(n, 40<<30, "a grown store", true); err != nil {
		t.Errorf("a store grown to hold the runway was refused: %v", err)
	}

	// Negative control: over budget WITHOUT the runway, the message is the plain one.
	plain := config.NewDefaultInstanceConfiguration()
	plain.ApplyDefaults()
	err = checkJetStreamReservation(&plain.Infrastructure.Nats, 1<<30, "test", true)
	if err == nil || strings.Contains(err.Error(), "outage runway") {
		t.Errorf("an over-budget instance with no runway growth blamed the runway: %v", err)
	}
}

// The volume the remedy names is the one whose DERIVED ceiling (90% of the magnitude, floored)
// holds the reservation, not the raw difference: a 29 GiB reservation needs 33Gi, not 29Gi.
func TestTheRequiredVolumeIsDerivedFromTheNinetyPercentFloor(t *testing.T) {
	t.Parallel()
	for gi := int64(1); gi <= 200; gi++ {
		n := requiredVolumeGi(gi << 30)
		have, err := derivedMaxFileStore(fmt.Sprintf("%dGi", n))
		if err != nil || have < gi<<30 {
			t.Fatalf("%dGi for a %d GiB reservation derives only %d B", n, gi, have)
		}
		if smaller, _ := derivedMaxFileStore(fmt.Sprintf("%dGi", n-1)); n > 1 && smaller >= gi<<30 {
			t.Fatalf("%dGi is not the smallest volume for %d GiB: %dGi also holds it", n, gi, n-1)
		}
	}
}

// The runway is named only when it is what tipped the sum over. An instance that did not fit
// without it is told to grow or lower its ceilings, with no mention of a runway it never asked
// for beyond the tier; a hand-set max_file_store gets no volume size to name.
func TestTheRunwayIsNamedOnlyWhenItTippedTheSum(t *testing.T) {
	t.Parallel()
	cfg := config.NewDefaultInstanceConfiguration()
	cfg.ApplyDefaults()
	n := &cfg.Infrastructure.Nats
	n.StreamDesignRate, n.StreamRunwayMinutes = 20_000, 15
	// A ceiling below even the shipped reservation: the runway did not tip it.
	err := checkJetStreamReservation(n, 1<<30, "tiny", true)
	if err == nil || strings.Contains(err.Error(), "outage runway asks") {
		t.Errorf("a store too small for the baseline blamed the runway: %v", err)
	}
	err = checkJetStreamReservation(n, 14<<30, "hand set", false)
	if err == nil || strings.Contains(err.Error(), "at least") && strings.Contains(err.Error(), "Gi, which is what holds") {
		t.Errorf("a hand-set max_file_store was given a volume size: %v", err)
	}
}

// With fail-closed MQTT the gateway's message store is sized from the capture ceiling, so the
// runway's share of the reservation counts it twice.
func TestFailClosedCountsTheMessageStoreInTheRunwayShare(t *testing.T) {
	t.Parallel()
	cfg := config.NewDefaultInstanceConfiguration()
	cfg.ApplyDefaults()
	n := &cfg.Infrastructure.Nats
	n.StreamDesignRate, n.StreamRunwayMinutes = 20_000, 15
	off := n.RunwayReservation()
	n.MqttFailClosed = true
	on := n.RunwayReservation()
	capture := n.StreamMaxBytesFor("device-events-capture") - config.DefaultStreamMaxBytes
	if on-off != capture {
		t.Errorf("fail-closed added %d B to the runway share, want the capture extra %d B", on-off, capture)
	}
}
