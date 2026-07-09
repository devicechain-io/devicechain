// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

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
