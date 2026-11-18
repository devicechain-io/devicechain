// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"math"
	"testing"
	"time"
)

func TestClampLifetimeLargeSeconds(t *testing.T) {
	r := &Registry{
		minLifetime: 60 * time.Second,
		defaultLife: time.Hour,
		maxLife:     24 * time.Hour,
	}
	for _, tt := range []struct {
		name    string
		seconds int64
		want    time.Duration
	}{
		{"negative", -1, time.Hour},
		{"absent", 0, time.Hour},
		{"one", 1, 60 * time.Second},
		{"below minimum", 59, 60 * time.Second},
		{"minimum", 60, 60 * time.Second},
		{"in range", 300, 300 * time.Second},
		{"maximum", 86400, 24 * time.Hour},
		{"above maximum", 86401, 24 * time.Hour},
		{"max int32", math.MaxInt32, 24 * time.Hour},
		{"duration overflow", 9300000000, 24 * time.Hour},
		{"max int", int64(math.MaxInt), 24 * time.Hour},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.seconds > int64(math.MaxInt) {
				t.Skip("lifetime does not fit in int on this architecture")
			}
			if got := r.clampLifetime(int(tt.seconds)); got != tt.want {
				t.Fatalf("clampLifetime(%d) = %v, want %v", tt.seconds, got, tt.want)
			}
		})
	}
}
