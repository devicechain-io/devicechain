// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"fmt"
	"time"
)

// EpisodeWindow is one review interval, recorded by the simulator at its first tick.
type EpisodeWindow struct {
	Role  string    `json:"role"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// ValidateEpisode rejects even an explicitly empty flag on another manifest.
func ValidateEpisode(manifest, episode string, explicit bool) error {
	if !explicit && episode == "" {
		return nil
	}
	if manifest != "towerpulse" {
		return fmt.Errorf("--episode requires --manifest towerpulse")
	}
	if episode != "classic" && episode != "night-shift" {
		return fmt.Errorf("unknown towerpulse episode %q (known: classic, night-shift)", episode)
	}
	return nil
}
