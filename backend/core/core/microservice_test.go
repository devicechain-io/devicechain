// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// shutdownDrainDelay resolves the drain window from the environment, falling back
// to the default and tolerating invalid input.
func TestShutdownDrainDelay(t *testing.T) {
	tests := []struct {
		name string
		set  bool
		val  string
		want time.Duration
	}{
		{"unset uses default", false, "", defaultShutdownDrain},
		{"explicit seconds", true, "10", 10 * time.Second},
		{"zero disables drain", true, "0", 0},
		{"empty uses default", true, "", defaultShutdownDrain},
		{"invalid uses default", true, "soon", defaultShutdownDrain},
		{"negative uses default", true, "-3", defaultShutdownDrain},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(ENV_SHUTDOWN_DRAIN_SECONDS, tc.val)
			}
			assert.Equal(t, tc.want, shutdownDrainDelay())
		})
	}
}
