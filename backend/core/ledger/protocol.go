// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

// DriverProtocol is the lowest engine protocol that may drive an operation. Every driver claim,
// reconciler pass, deadline pass and retention pass works only operations whose protocol is at
// most its own build's SupportedDriverProtocol, so a pod of an older release never works an
// operation a newer release defined.
type DriverProtocol int32

// SupportedDriverProtocol is the highest protocol this build's engine can drive.
const SupportedDriverProtocol DriverProtocol = 1

// ProtocolFeatures names what an operation uses that an older engine would mishandle silently.
// Each field is true only for a capability that exists in a release that defines protocol 2: an
// older engine would post such an operation without maintaining its counters, ignore its wave
// gates, or settle a multi-key line by single-key rules.
type ProtocolFeatures struct {
	Counters bool
	Waves    bool
	Snapshot bool
	MultiKey bool
}

// RequiredProtocol is the ONE place an operation's protocol is decided. A creator must call it
// and store the answer; nothing may default it, because a defaulted protocol is a wrong one the
// first time a release adds a feature. The column has no default and refuses less than 1.
func RequiredProtocol(f ProtocolFeatures) DriverProtocol {
	if f.Counters || f.Waves || f.Snapshot || f.MultiKey {
		return 2
	}
	return 1
}

// Drivable reports whether an engine of this build may drive an operation of protocol p.
func Drivable(p DriverProtocol) bool { return p >= 1 && p <= SupportedDriverProtocol }
