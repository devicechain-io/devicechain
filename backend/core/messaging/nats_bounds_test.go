// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	nats "github.com/nats-io/nats.go"
)

// applyStreamBounds writes the desired ceilings and reports drift so ensureStream
// issues UpdateStream only when an existing stream's limits actually differ.
func TestApplyStreamBounds(t *testing.T) {
	want := streamBounds{maxBytes: 2 << 30, maxMsgs: 5_000_000, maxMsgSize: 8 << 20}

	t.Run("unbounded existing stream drifts and is patched", func(t *testing.T) {
		// A stream created by an older build carries -1 (unlimited) on every ceiling.
		cfg := &nats.StreamConfig{MaxBytes: -1, MaxMsgs: -1, MaxMsgSize: -1}
		if !applyStreamBounds(cfg, want) {
			t.Fatal("expected drift against an unbounded stream")
		}
		if cfg.MaxBytes != want.maxBytes || cfg.MaxMsgs != want.maxMsgs || cfg.MaxMsgSize != want.maxMsgSize {
			t.Errorf("bounds not written: %d/%d/%d", cfg.MaxBytes, cfg.MaxMsgs, cfg.MaxMsgSize)
		}
	})

	t.Run("already-bounded stream reports no drift", func(t *testing.T) {
		cfg := &nats.StreamConfig{MaxBytes: want.maxBytes, MaxMsgs: want.maxMsgs, MaxMsgSize: want.maxMsgSize}
		if applyStreamBounds(cfg, want) {
			t.Error("expected no drift when the ceilings already match")
		}
	})

	t.Run("partial drift on a single ceiling is detected", func(t *testing.T) {
		cfg := &nats.StreamConfig{MaxBytes: want.maxBytes, MaxMsgs: want.maxMsgs, MaxMsgSize: 1 << 20}
		if !applyStreamBounds(cfg, want) {
			t.Fatal("expected drift when only maxMsgSize differs")
		}
		if cfg.MaxMsgSize != want.maxMsgSize {
			t.Errorf("maxMsgSize not corrected: %d", cfg.MaxMsgSize)
		}
	})

	t.Run("other config fields are left untouched", func(t *testing.T) {
		cfg := &nats.StreamConfig{
			Name:     "keep-me",
			Subjects: []string{"a.b.c"},
			Storage:  nats.FileStorage,
			MaxBytes: -1, MaxMsgs: -1, MaxMsgSize: -1,
		}
		applyStreamBounds(cfg, want)
		if cfg.Name != "keep-me" || len(cfg.Subjects) != 1 || cfg.Subjects[0] != "a.b.c" || cfg.Storage != nats.FileStorage {
			t.Errorf("applyStreamBounds mutated a non-ceiling field: %+v", cfg)
		}
	})
}
