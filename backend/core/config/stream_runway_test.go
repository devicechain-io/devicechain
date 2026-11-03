// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
)

func runwayConfig(rate int64, minutes int) *NatsConfiguration {
	c := NewDefaultInstanceConfiguration()
	c.Infrastructure.Nats.StreamDesignRate = rate
	c.Infrastructure.Nats.StreamRunwayMinutes = minutes
	c.ApplyDefaults()
	return &c.Infrastructure.Nats
}

// A document that never named a design rate keeps exactly the ceilings it had: nothing about an
// upgrade raises a reservation the operator did not ask to raise.
func TestAnUnsetDesignRateLeavesTheCeilingsAlone(t *testing.T) {
	n := runwayConfig(0, 0)
	for _, s := range streams.RunwaySuffixes() {
		if got := n.StreamMaxBytesFor(s); got != DefaultStreamMaxBytes {
			t.Errorf("%s: %d B without a design rate, want the tier %d B", s, got, DefaultStreamMaxBytes)
		}
		if got := n.StreamMaxMsgsFor(s); got != DefaultStreamMaxMsgs {
			t.Errorf("%s: %d messages without a design rate, want %d", s, got, DefaultStreamMaxMsgs)
		}
		if got := n.StreamRunwayFor(s); got != 0 {
			t.Errorf("%s: reports a %s runway with no design rate", s, got)
		}
	}
	if got := n.RunwayReservation(); got != 0 {
		t.Errorf("the runway adds %d B to a reservation with no design rate", got)
	}
}

// The chart's stated rate and window are met by the flat ceiling: a default install keeps its
// reservation, and the runway it reports is the real one.
func TestTheChartDefaultsKeepTheFlatCeiling(t *testing.T) {
	n := runwayConfig(DefaultStreamDesignRate, DefaultStreamRunwayMinutes)
	for _, s := range streams.RunwaySuffixes() {
		if got := n.StreamMaxBytesFor(s); got != DefaultStreamMaxBytes {
			t.Errorf("%s: %d B at the default rate, want the unchanged tier %d B", s, got, DefaultStreamMaxBytes)
		}
		if got := n.StreamRunwayFor(s); got < DefaultStreamRunwayMinutes*time.Minute {
			t.Errorf("%s: holds %s at %d events/s, less than the %d minutes asked for",
				s, got, DefaultStreamDesignRate, DefaultStreamRunwayMinutes)
		}
	}
	if got := n.RunwayReservation(); got != 0 {
		t.Errorf("the default runway adds %d B to the reservation", got)
	}
}

// The target-rate case this exists for: 20,000 events a second held for 15 minutes. The old
// flat ceiling held resolved-events for under three minutes; the sized one must hold the
// runway, on bytes AND on message count, and only the streams that carry one message per
// event are touched.
func TestTheRunwayIsHonouredAtTheTargetRate(t *testing.T) {
	const rate, minutes = 20_000, 15
	n := runwayConfig(rate, minutes)

	for _, s := range streams.RunwaySuffixes() {
		per := streams.RunwayBytesPerMessageFor(s)
		wantBytes := int64(rate) * minutes * 60 * per
		if got := n.StreamMaxBytesFor(s); got != wantBytes {
			t.Errorf("%s: ceiling %d B, want rate x runway x %d B/event = %d B", s, got, per, wantBytes)
		}
		if got, want := n.StreamMaxMsgsFor(s), int64(rate)*minutes*60; got != want {
			t.Errorf("%s: %d messages, want %d: the count ceiling would cut the runway short", s, got, want)
		}
		if got := n.StreamRunwayFor(s); got < minutes*time.Minute {
			t.Errorf("%s: holds %s at %d events/s, want at least %d minutes", s, got, rate, minutes)
		}
	}

	// The negative control: the flat ceiling, the arithmetic this replaces, falls short.
	flat := DefaultStreamMaxBytes / (int64(rate) * streams.RunwayBytesPerMessageFor(streams.ResolvedEvents))
	if flat >= minutes*60 {
		t.Errorf("a flat %d B ceiling already holds %d s at %d events/s: the scenario proves nothing",
			DefaultStreamMaxBytes, flat, rate)
	}

	// A stream whose volume does not follow the event rate keeps its tier.
	for _, s := range []string{streams.DerivedEvents, streams.ConnectorDispatch, streams.DeviceRoster} {
		if got := n.StreamMaxBytesFor(s); got != n.tierMaxBytesFor(s) {
			t.Errorf("%s: %d B, want its tier %d B: it is not sized by the event rate", s, got, n.tierMaxBytesFor(s))
		}
	}
	if n.RunwayReservation() <= 0 {
		t.Error("a 20,000 events/s runway adds nothing to the reservation")
	}
}

// The byte floor stays a floor: a runway smaller than the tier is not shrunk to it, which is
// what keeps a deliberately roomy ceiling roomy.
func TestTheByteCeilingIsAFloorUnderTheRunway(t *testing.T) {
	c := NewDefaultInstanceConfiguration()
	c.Infrastructure.Nats.StreamMaxBytes = 4 << 30
	c.Infrastructure.Nats.StreamDesignRate = 100
	c.ApplyDefaults()
	if got := c.Infrastructure.Nats.StreamMaxBytesFor(streams.ResolvedEvents); got != 4<<30 {
		t.Errorf("resolved-events got %d B, want the 4 GiB floor", got)
	}
}

// A runway outside what can be held is refused at load, naming the key, rather than clamped.
func TestValidateRejectsAnUnholdableRunway(t *testing.T) {
	cases := map[string]struct {
		rate    int64
		minutes int
		want    string
	}{
		"negative rate":    {rate: -1, minutes: 15, want: "streamDesignRate"},
		"absurd rate":      {rate: MaxStreamDesignRate + 1, minutes: 15, want: "streamDesignRate"},
		"negative window":  {rate: 1000, minutes: -5, want: "streamRunwayMinutes"},
		"beyond age limit": {rate: 1000, minutes: MaxStreamRunwayMinutes + 1, want: "streamRunwayMinutes"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := NewDefaultInstanceConfiguration()
			cfg.Infrastructure.Nats.StreamDesignRate = tc.rate
			cfg.Infrastructure.Nats.StreamRunwayMinutes = tc.minutes
			cfg.ApplyDefaults()
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error naming %s", err, tc.want)
			}
		})
	}
	ok := NewDefaultInstanceConfiguration()
	ok.Infrastructure.Nats.StreamDesignRate = 20_000
	ok.Infrastructure.Nats.StreamRunwayMinutes = 15
	ok.ApplyDefaults()
	if err := ok.Validate(); err != nil {
		t.Errorf("a valid runway was refused: %v", err)
	}
}
