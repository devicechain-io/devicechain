// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"testing"
)

// Skip advances LastSeq and nothing else, is monotonic, refuses 0, and round-trips through a
// snapshot.
func TestEngineSkip(t *testing.T) {
	e := NewEngine(nil, 0)
	if e.Skip(0) {
		t.Fatal("Skip(0) must be refused")
	}
	before, _ := e.Snapshot()
	if !e.Skip(5) || e.LastSeq() != 5 {
		t.Fatalf("Skip(5): lastSeq=%d", e.LastSeq())
	}
	if e.Skip(5) || e.Skip(3) || e.LastSeq() != 5 {
		t.Fatal("Skip at or below LastSeq must be a no-op")
	}
	if !e.Watermark().IsZero() {
		t.Fatal("Skip must not move the watermark")
	}
	after, _ := e.Snapshot()
	if bytes.Equal(before, after) {
		t.Fatal("snapshot must record the skipped sequence")
	}
	e2, err := Restore(nil, 0, after)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if e2.LastSeq() != 5 {
		t.Fatalf("restored lastSeq = %d, want 5", e2.LastSeq())
	}
	// A message behind the skipped seq is dropped by the existing guard.
	e2.ProcessResolved(4, base, nil)
	if e2.LastSeq() != 5 || !e2.Watermark().IsZero() {
		t.Fatal("lower-seq ProcessResolved must be dropped whole")
	}
}
