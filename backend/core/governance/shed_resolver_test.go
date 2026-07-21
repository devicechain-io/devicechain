// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import "testing"

func i32(v int32) *int32 { return &v }

// TestResolveShedPriority pins the wire fold: a valid band value passes through; null
// or out-of-band inherits the fail-safe default (never gold), the same discipline the
// ceiling fetcher applies to a non-positive value.
func TestResolveShedPriority(t *testing.T) {
	cases := []struct {
		name string
		in   *int32
		want int
	}{
		{"null inherits the fail-safe", nil, DefaultShedPriority},
		{"a valid band value passes through", i32(90), 90},
		{"the low boundary passes", i32(1), 1},
		{"the high boundary passes", i32(100), 100},
		{"zero is out of band → fail-safe", i32(0), DefaultShedPriority},
		{"above 100 is out of band → fail-safe", i32(200), DefaultShedPriority},
		{"negative is out of band → fail-safe", i32(-5), DefaultShedPriority},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveShedPriority(c.in); got != c.want {
				t.Errorf("resolveShedPriority(%v) = %d, want %d", c.in, got, c.want)
			}
		})
	}
	// The fail-safe must never band to gold — the property that keeps a slow authority
	// from letting a tenant ride through as premium.
	if ShedClassOf(resolveShedPriority(nil)) == ShedGold {
		t.Fatal("the shed-priority fail-safe bands to gold — a fail-open must not fail to never-shed")
	}
}
