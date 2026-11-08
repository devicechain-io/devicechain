// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package agenttext holds the types for text an AI agent reads from the platform and is
// expected to act on.
//
// The rule is that such text is platform-authored: written by a person, at compile time,
// into the source. Text built from a device payload, a pack description, a tenant-chosen
// name or any other runtime value is data, not instruction, and an agent that reads it as
// instruction can be steered by whoever controls that value.
//
// Remediation is the text an error carries to tell the reader what to do next. It is a
// distinct string type so that a runtime string cannot be assigned to one without an
// explicit conversion, and hack/check-redlines.sh then refuses any conversion, struct
// field or assignment named Remediation whose value is not a constant expression. The
// type makes the site visible; the guard decides whether it is constant.
package agenttext

// Remediation is compile-time-constant guidance for the reader of an error. Build one only
// from a string literal or a constant: Remediation("Retry after the freeze lifts.").
// Formatting a value into it (fmt.Sprintf, concatenating a variable) fails the guard.
type Remediation string

// String returns the text.
func (r Remediation) String() string { return string(r) }
