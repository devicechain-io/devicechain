// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"time"
)

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

	// The issuer pin (certificate connects are pinned to the broker's live server ids). Defaults and
	// bounds; the pin's own defaults are processor.DefaultIssuerPin*, kept equal by a test.
	DefaultCalloutIssuerPinRefresh      = 30 * time.Second
	DefaultCalloutIssuerPinMinRefresh   = 2 * time.Second
	DefaultCalloutIssuerPinGatherWindow = 750 * time.Millisecond
	MinCalloutIssuerPinRefresh          = 5 * time.Second
	MaxCalloutIssuerPinRefresh          = 10 * time.Minute
	MinCalloutIssuerPinMinRefresh       = 100 * time.Millisecond
	MinCalloutIssuerPinGatherWindow     = 100 * time.Millisecond
	// MaxCalloutIssuerPinGatherWindow sits under the callout's lookup timeout (3 s): the window is
	// spent inside a connect.
	MaxCalloutIssuerPinGatherWindow = 2 * time.Second
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
	// IssuerPinDisabled turns OFF the pin that refuses a certificate connect (username
	// {tenant}:@x509) unless its authorization request was signed by a live broker server, as the
	// service learns them over a system-account connection. The pin is on by default and fails
	// closed: with no system login configured the service refuses to start rather than run
	// without it, and while the system connection is down every certificate connect is refused.
	// Set this only to run a broker whose publish-deny on the callout subject is the sole defence
	// you want. Default false.
	IssuerPinDisabled bool
	// IssuerPinRefresh is how often the set of live server ids is re-learned. 5s to 10m, default 30s.
	// A snapshot not refreshed for three times this is not believed.
	IssuerPinRefresh string
	// IssuerPinMinRefresh is the least time between two server pings, however many requests name
	// an issuer the pin does not know (a flood of forged requests sends at most one ping per
	// this). 100ms up to IssuerPinRefresh, default 2s. It is also the longest a device can be
	// refused by a broker server that joined since the last refresh.
	IssuerPinMinRefresh string
	// IssuerPinGatherWindow is how long one ping waits for servers to answer. It ends early once
	// every server the cluster claims has answered. 100ms to 2s, default 750ms.
	IssuerPinGatherWindow string
}

// ApplyDefaults fills what the document left out.
func (c *CalloutConfiguration) ApplyDefaults() {
	if c.JitMaxInFlight == 0 {
		c.JitMaxInFlight = DefaultCalloutJitMaxInFlight
	}
	if c.IssuerPinRefresh == "" {
		c.IssuerPinRefresh = DefaultCalloutIssuerPinRefresh.String()
	}
	if c.IssuerPinMinRefresh == "" {
		c.IssuerPinMinRefresh = DefaultCalloutIssuerPinMinRefresh.String()
	}
	if c.IssuerPinGatherWindow == "" {
		c.IssuerPinGatherWindow = DefaultCalloutIssuerPinGatherWindow.String()
	}
}

// IssuerPinRefreshDuration, IssuerPinMinRefreshDuration and IssuerPinGatherWindowDuration are the
// settings parsed. Validate has already refused one that is not a duration.
func (c CalloutConfiguration) IssuerPinRefreshDuration() time.Duration {
	d, _ := time.ParseDuration(c.IssuerPinRefresh)
	return d
}
func (c CalloutConfiguration) IssuerPinMinRefreshDuration() time.Duration {
	d, _ := time.ParseDuration(c.IssuerPinMinRefresh)
	return d
}
func (c CalloutConfiguration) IssuerPinGatherWindowDuration() time.Duration {
	d, _ := time.ParseDuration(c.IssuerPinGatherWindow)
	return d
}

// Validate refuses a bound outside its range. Nothing is clamped.
func (c CalloutConfiguration) Validate() error {
	if c.JitMaxInFlight < 1 || c.JitMaxInFlight > MaxCalloutJitMaxInFlight {
		return fmt.Errorf("callout.jitMaxInFlight must be between 1 and %d, got %d", MaxCalloutJitMaxInFlight, c.JitMaxInFlight)
	}
	refresh, err := time.ParseDuration(c.IssuerPinRefresh)
	if err != nil || refresh < MinCalloutIssuerPinRefresh || refresh > MaxCalloutIssuerPinRefresh {
		return fmt.Errorf("callout.issuerPinRefresh must be a duration between %s and %s, got %q",
			MinCalloutIssuerPinRefresh, MaxCalloutIssuerPinRefresh, c.IssuerPinRefresh)
	}
	minRefresh, err := time.ParseDuration(c.IssuerPinMinRefresh)
	if err != nil || minRefresh < MinCalloutIssuerPinMinRefresh || minRefresh > refresh {
		return fmt.Errorf("callout.issuerPinMinRefresh must be a duration between %s and issuerPinRefresh (%s), got %q",
			MinCalloutIssuerPinMinRefresh, refresh, c.IssuerPinMinRefresh)
	}
	window, err := time.ParseDuration(c.IssuerPinGatherWindow)
	if err != nil || window < MinCalloutIssuerPinGatherWindow || window > MaxCalloutIssuerPinGatherWindow {
		return fmt.Errorf("callout.issuerPinGatherWindow must be a duration between %s and %s, got %q",
			MinCalloutIssuerPinGatherWindow, MaxCalloutIssuerPinGatherWindow, c.IssuerPinGatherWindow)
	}
	return nil
}
