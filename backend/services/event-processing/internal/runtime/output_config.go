// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import "fmt"

// OutputConfig bounds how many derived events a checkpoint has waiting for the broker at once.
//
// A checkpoint publishes its buffered detections before it saves the snapshot. With
// MaxInFlight 1 that is one publish at a time, each waiting for its acknowledgement before the
// next is sent: the serial path. Above 1, detections of different series overlap their waits,
// at most one unacknowledged publish per series, so a series' detections still reach the broker
// in the order the engine produced them.
type OutputConfig struct {
	// MaxInFlight is the most publishes awaiting the broker's acknowledgement at once.
	MaxInFlight int
	// MaxBufferedBytes bounds the payload bytes of those publishes together: the bytes sent and
	// not yet acknowledged. An acknowledged payload is released at once, so it holds none, and
	// preparation is lazy, so at most one prepared-unsent payload sits beside the bound. A single
	// derived event larger than the bound is sent on its own, with nothing beside it, rather than never.
	MaxBufferedBytes int64
}

// Defaults, and the bounds Validate enforces.
//
// 🔴 THE DEFAULT WINDOW IS 1, THE SERIAL PATH, ON PURPOSE. Overlapping publishes is measured to
// pay off only when the broker's acknowledgement is slow relative to everything else a
// checkpoint does, and that has not yet been shown on a replicated cluster at a fixed resource
// budget. Raise it to measure; the default moves only with that evidence.
const (
	DefaultMaxInFlight      = 1
	DefaultMaxBufferedBytes = 4 << 20

	// MaxMaxInFlight is the same ceiling the messaging layer puts on a pipelined writer's window.
	MaxMaxInFlight      = 1024
	MinMaxBufferedBytes = 64 << 10
	MaxMaxBufferedBytes = 256 << 20
)

// DefaultOutputConfig is the shipped configuration.
func DefaultOutputConfig() OutputConfig {
	return OutputConfig{MaxInFlight: DefaultMaxInFlight, MaxBufferedBytes: DefaultMaxBufferedBytes}
}

// Validate refuses a configuration outside the bounds.
func (c OutputConfig) Validate() error {
	switch {
	case c.MaxInFlight < 1 || c.MaxInFlight > MaxMaxInFlight:
		return fmt.Errorf("derived publishing in-flight limit must be between 1 and %d, got %d", MaxMaxInFlight, c.MaxInFlight)
	case c.MaxBufferedBytes < MinMaxBufferedBytes || c.MaxBufferedBytes > MaxMaxBufferedBytes:
		return fmt.Errorf("derived publishing buffered bytes must be between %d and %d, got %d",
			MinMaxBufferedBytes, MaxMaxBufferedBytes, c.MaxBufferedBytes)
	}
	return nil
}
