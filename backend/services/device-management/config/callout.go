// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "fmt"

const (
	// DefaultCalloutJitMaxInFlight is how many registrations on first connect (a device whose
	// certificate pins nothing yet) one replica works on at once. Registrations against one
	// profile serialize on its row at roughly 5 to 10 ms each, so a few slots are enough to keep
	// that row busy; more only queue for it. They are carved out of the callout's own in-flight
	// bound, so a wave of first connects can never take the places reconnecting devices need.
	DefaultCalloutJitMaxInFlight = 16
	// MaxCalloutJitMaxInFlight bounds the slots from above: each holds a pooled database
	// connection for its transaction, and the callout's whole bound is 256.
	MaxCalloutJitMaxInFlight = 128
)

// CalloutConfiguration is the fixed configuration of the device auth callout's certificate
// registration. Unknown keys are refused when the document loads, like every other key.
type CalloutConfiguration struct {
	// JitMaxInFlight is how many registrations on first connect one replica works on at once;
	// the next is refused (callout_x509_total{result="jit_busy"}) and the device backs off and
	// retries. A pinned certificate, a token or a password never waits on it. 1 to 128.
	// Default 16.
	JitMaxInFlight int
	// JitSourceTrusted turns on a per-source limit on registrations, keyed on the client address
	// the broker saw (the provisioning sourceBurst and sourceEvery size the bucket). Set it only
	// when the MQTT Service preserves client addresses (externalTrafficPolicy: Local, or a kind
	// NodePort): behind a source NAT it would key every device on one address and refuse a fleet
	// for one device's retries. Default false, in which case sustained refusals are alerted on
	// (DeviceX509JitRefusedSustained), not throttled by address.
	JitSourceTrusted bool
}

// ApplyDefaults fills what the document left out.
func (c *CalloutConfiguration) ApplyDefaults() {
	if c.JitMaxInFlight == 0 {
		c.JitMaxInFlight = DefaultCalloutJitMaxInFlight
	}
}

// Validate refuses a bound outside its range. Nothing is clamped.
func (c CalloutConfiguration) Validate() error {
	if c.JitMaxInFlight < 1 || c.JitMaxInFlight > MaxCalloutJitMaxInFlight {
		return fmt.Errorf("callout.jitMaxInFlight must be between 1 and %d, got %d", MaxCalloutJitMaxInFlight, c.JitMaxInFlight)
	}
	return nil
}
