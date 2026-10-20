// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import "fmt"

// OutputConfig bounds how many derived events a checkpoint has waiting for the broker at once.
//
// A checkpoint publishes its buffered detections before it saves the snapshot. With
// MaxInFlight 1 that is one publish at a time, each waiting for its acknowledgement before the
// next is sent: the serial path. Above 1 (the default is 32), detections of different series
// overlap their waits, at most one unacknowledged publish per series, so a series' detections
// still reach the broker in the order the engine produced them.
type OutputConfig struct {
	// MaxInFlight is the most publishes awaiting the broker's acknowledgement at once.
	MaxInFlight int
	// MaxBufferedBytes bounds the payload bytes held for publishing together: every payload that is
	// prepared and not yet acknowledged (in flight, waiting for room, or kept from an earlier pass
	// for a retry). An acknowledged payload is released at once, so it holds none.
	// Nothing is prepared while the count is over the bound and a publish is in flight, so it is
	// exceeded by the item that crossed it, waiting for room, and by at most one payload more: with
	// nothing in flight the next item is prepared and sent whatever the count. That is how a single
	// derived event larger than the bound is sent on its own, with nothing beside it, rather than
	// never.
	MaxBufferedBytes int64
}

// Defaults, and the bounds Validate enforces.
//
// The default window is 32. On a replicated cluster under firing-heavy load (about 8k events/s,
// about 2.5k detections/s offered) a window of 32 applied 7,658 events/s and published 2,441
// detections/s, with the backlog peaking at 993 and the watermark lag at 5.1 s; a window of 1
// applied 1,656 events/s and published 528 detections/s, with the backlog reaching 1.06M and the
// lag 139 s. Neither setting showed an ordering or duplicate violation. A window of 1 is the
// serial path and stays valid. The byte budget is unchanged: 4 MiB is 128 KiB per slot at 32,
// far above a detection's payload.
const (
	DefaultMaxInFlight      = 32
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
