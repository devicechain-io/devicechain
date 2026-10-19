// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package eventtype is the platform's closed vocabulary of event types: the discriminator a
// device event carries from the ingest transport through resolution to storage. It lives in
// core because it is part of the wire contract of the resolved-event stream (see
// resolvedevent), and so cannot belong to the service that happened to declare it first.
package eventtype

// EventType discriminates what an event's payload is.
type EventType int64

// Enumeration of event types. The numbers are WIRE VALUES (a resolved event carries one as
// an int64), so a type is only ever appended, never renumbered or removed.
//
//go:generate stringer -type=EventType
const (
	NewRelationship EventType = iota
	Location
	Measurement
	Alert
	StateChange
	CommandInvocation
	CommandResponse
)
