// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// ApplyDefaults fills the checkpoint cadence when unset and leaves explicit values.
func TestApplyDefaults(t *testing.T) {
	c := &EventProcessingConfiguration{}
	c.ApplyDefaults()
	if c.CheckpointEvents != DefaultCheckpointEvents {
		t.Fatalf("CheckpointEvents = %d, want default %d", c.CheckpointEvents, DefaultCheckpointEvents)
	}
	if c.CheckpointIntervalSeconds != DefaultCheckpointIntervalSeconds {
		t.Fatalf("CheckpointIntervalSeconds = %d, want default %d", c.CheckpointIntervalSeconds, DefaultCheckpointIntervalSeconds)
	}

	explicit := &EventProcessingConfiguration{CheckpointEvents: 50, CheckpointIntervalSeconds: 3}
	explicit.ApplyDefaults()
	if explicit.CheckpointEvents != 50 || explicit.CheckpointIntervalSeconds != 3 {
		t.Fatalf("ApplyDefaults overwrote explicit values: %+v", explicit)
	}

	// The per-tenant state budgets default to the platform ceilings when unset (fail-safe, never
	// unlimited) and leave an explicit value untouched (ADR-023, slice 6c).
	if c.MaxRulesPerTenant != DefaultMaxRulesPerTenant || c.MaxLiveKeysPerTenant != DefaultMaxLiveKeysPerTenant {
		t.Fatalf("budgets should default to the platform ceilings; got rules=%d keys=%d", c.MaxRulesPerTenant, c.MaxLiveKeysPerTenant)
	}
	tuned := &EventProcessingConfiguration{MaxRulesPerTenant: 5, MaxLiveKeysPerTenant: 42}
	tuned.ApplyDefaults()
	if tuned.MaxRulesPerTenant != 5 || tuned.MaxLiveKeysPerTenant != 42 {
		t.Fatalf("ApplyDefaults overwrote explicit budgets: %+v", tuned)
	}

	// The outbound egress ceiling (ADR-060 SD-3) defaults to the platform ceilings when unset
	// (fail-safe, never unlimited) and leaves explicit values untouched.
	if c.OutboundMessagesPerSecond != DefaultOutboundMessagesPerSecond || c.OutboundBurst != DefaultOutboundBurst {
		t.Fatalf("outbound egress should default to the platform ceilings; got rate=%g burst=%d", c.OutboundMessagesPerSecond, c.OutboundBurst)
	}
	tunedOut := &EventProcessingConfiguration{OutboundMessagesPerSecond: 7, OutboundBurst: 9}
	tunedOut.ApplyDefaults()
	if tunedOut.OutboundMessagesPerSecond != 7 || tunedOut.OutboundBurst != 9 {
		t.Fatalf("ApplyDefaults overwrote explicit outbound ceiling: %+v", tunedOut)
	}
}

// Validate fails closed on a NEGATIVE per-tenant budget (an operator error, not an unlimited escape
// hatch — unset defaults to the platform ceiling, never unlimited).
func TestValidateRejectsNegativeBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  EventProcessingConfiguration
	}{
		{"negative rules", EventProcessingConfiguration{CheckpointEvents: 100, CheckpointIntervalSeconds: 10, MaxRulesPerTenant: -1}},
		{"negative keys", EventProcessingConfiguration{CheckpointEvents: 100, CheckpointIntervalSeconds: 10, MaxLiveKeysPerTenant: -1}},
		{"negative outbound rate", EventProcessingConfiguration{CheckpointEvents: 100, CheckpointIntervalSeconds: 10, OutboundMessagesPerSecond: -1}},
		{"negative outbound burst", EventProcessingConfiguration{CheckpointEvents: 100, CheckpointIntervalSeconds: 10, OutboundBurst: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatal("expected validation error for a negative budget, got nil")
			}
		})
	}
}

// Validate fails closed on a non-positive checkpoint cadence (a zero/negative value
// would never checkpoint or checkpoint every event, breaking ack-on-checkpoint).
func TestValidateRejectsNonPositiveCadence(t *testing.T) {
	cases := []struct {
		name string
		cfg  EventProcessingConfiguration
	}{
		{"zero events", EventProcessingConfiguration{CheckpointEvents: 0, CheckpointIntervalSeconds: 10}},
		{"negative events", EventProcessingConfiguration{CheckpointEvents: -1, CheckpointIntervalSeconds: 10}},
		{"zero interval", EventProcessingConfiguration{CheckpointEvents: 100, CheckpointIntervalSeconds: 0}},
		{"negative interval", EventProcessingConfiguration{CheckpointEvents: 100, CheckpointIntervalSeconds: -5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatal("expected validation error, got nil")
			}
		})
	}

	valid := EventProcessingConfiguration{CheckpointEvents: 100, CheckpointIntervalSeconds: 10}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

// 🔴 THE UPGRADE THIS EXISTS FOR. v0.11.0 documented maxEventFutureSkewSeconds as an
// event-processing key, in both locales. The key moved to device-management, and the
// fail-closed decode calls anything it does not recognise unknown — so without the
// retirement declaration an operator who followed our own documentation gets a DETECT
// engine that will not start, and detection is down for every tenant on the instance.
//
// It goes through core.LoadConfiguration rather than calling RetiredConfigKeys directly:
// the map is only worth anything if the loader consults it, and asserting on the map
// would pass just as happily with the interface unimplemented.
func TestRetiredSkewKeyDoesNotStopTheService(t *testing.T) {
	cfg := &EventProcessingConfiguration{}
	err := core.LoadConfiguration([]byte(`{"maxEventFutureSkewSeconds":600,"checkpointEvents":250}`), cfg)

	if err != nil {
		t.Fatalf("a document carrying the retired key must load: %v", err)
	}
	if cfg.CheckpointEvents != 250 {
		t.Errorf("settings alongside the retired key must still apply; checkpointEvents = %d, want 250", cfg.CheckpointEvents)
	}
}

// The counterweight: retiring one key must not relax the posture for any other. A typo is
// still a setting the operator believes is in force, and is still refused.
func TestRetirementDoesNotAdmitATypo(t *testing.T) {
	cfg := &EventProcessingConfiguration{}
	err := core.LoadConfiguration([]byte(`{"maxEventFutureSkewSeconds":600,"checkpontEvents":250}`), cfg)

	if err == nil {
		t.Fatal("an unknown key alongside a retired one must still fail the load closed")
	}
}

// The retired value is dropped, never honoured — there is no field for it to reach and
// nothing forwards it. A future change that wired it back into a live setting would have
// to make this test say so.
func TestRetiredSkewValueIsNotHonoured(t *testing.T) {
	cfg := &EventProcessingConfiguration{}
	if err := core.LoadConfiguration([]byte(`{"maxEventFutureSkewSeconds":600}`), cfg); err != nil {
		t.Fatalf("load: %v", err)
	}

	// Every live field must read as though the key had never been written. Watermark
	// lateness is the nearest neighbour in meaning — the other event-time knob — so it is
	// the one a careless re-wiring would most plausibly land on.
	if cfg.WatermarkLatenessSeconds != DefaultWatermarkLatenessSeconds {
		t.Errorf("watermarkLatenessSeconds = %d, want the default %d — the retired value must reach no live setting",
			cfg.WatermarkLatenessSeconds, DefaultWatermarkLatenessSeconds)
	}
}
