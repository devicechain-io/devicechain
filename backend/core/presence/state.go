// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

// State is the presence state carried by a StateChange event (ADR-067): the wire vocabulary
// that Claim is derived from. A closed enum, and NOT purely a connectivity vocabulary:
// CONNECTED and DISCONNECTED are claims about the device, while DEMOTED is a claim about the
// SOURCE — it says the source is releasing custody and asserting nothing whatsoever about
// whether the device is up or down.
//
// 🔴 The distinction is load-bearing downstream. Both consumers used to derive
// connectivity by string-comparing to CONNECTED, which reads every other value —
// including one that means "no claim" — as a death. That is why the mapping onto a
// Claim is a single TOTAL function that reports its own failure
// (resolvedevent.ResolvedStateChangePayload.Claim) rather than a comparison repeated per
// consumer.
type State string

const (
	StateConnected    State = "CONNECTED"
	StateDisconnected State = "DISCONNECTED"
	StateDemoted      State = "DEMOTED"
)
