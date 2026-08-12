// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventtime

import (
	"testing"
	"time"
)

// Effective bounds a far-future device time against processed+skew, leaves a normal
// (past/near) time alone, and disables when processed is unset or skew is non-positive.
// The bool is asserted alongside every case: it drives the operator counter, so a
// correct time returned with the wrong flag would report a healthy fleet as broken (or,
// worse, a clock-poisoned one as healthy).
func TestEffectiveBounds(t *testing.T) {
	proc := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	skew := 5 * time.Minute
	for _, tc := range []struct {
		name      string
		occurred  time.Time
		processed time.Time
		skew      time.Duration
		want      time.Time
		wantBound bool
	}{
		{"far future clamps to processed+skew", proc.Add(48 * time.Hour), proc, skew, proc.Add(skew), true},
		{"within skew is untouched", proc.Add(time.Minute), proc, skew, proc.Add(time.Minute), false},
		{"exactly at the limit is untouched", proc.Add(skew), proc, skew, proc.Add(skew), false},
		{"a past reading is untouched", proc.Add(-time.Hour), proc, skew, proc.Add(-time.Hour), false},
		{"unset processed disables the bound", proc.Add(48 * time.Hour), time.Time{}, skew, proc.Add(48 * time.Hour), false},
		{"non-positive skew disables the bound", proc.Add(48 * time.Hour), proc, 0, proc.Add(48 * time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, bounded := Effective(tc.occurred, tc.processed, tc.skew)
			if !got.Equal(tc.want) {
				t.Fatalf("time: got %v, want %v", got, tc.want)
			}
			if bounded != tc.wantBound {
				t.Fatalf("bounded: got %v, want %v", bounded, tc.wantBound)
			}
		})
	}
}

// ForEntry prefers the sample's own time and falls back to the envelope, and bounds
// whichever one it picked. The last case is the one that matters: a device cannot escape
// the bound by moving its bad clock from the envelope into a per-sample field.
func TestForEntryPrefersTheSampleAndStillBounds(t *testing.T) {
	proc := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	envelope := proc.Add(-time.Minute)
	skew := 5 * time.Minute

	if got, bounded := ForEntry(nil, envelope, proc, skew); !got.Equal(envelope) || bounded {
		t.Fatalf("no sample time falls back to the envelope; got %v (bounded=%v)", got, bounded)
	}

	sample := proc.Add(-time.Hour)
	if got, bounded := ForEntry(&sample, envelope, proc, skew); !got.Equal(sample) || bounded {
		t.Fatalf("a sample time wins over the envelope; got %v (bounded=%v)", got, bounded)
	}

	// The envelope is honest, the sample is not. Bounding only the envelope would leave
	// the poisoned value to reach the projections through the entry.
	poisoned := proc.Add(72 * time.Hour)
	got, bounded := ForEntry(&poisoned, envelope, proc, skew)
	if !got.Equal(proc.Add(skew)) || !bounded {
		t.Fatalf("a poisoned sample time must be bounded; got %v (bounded=%v)", got, bounded)
	}
}
