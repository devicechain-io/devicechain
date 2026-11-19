// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package contract

// EnqueueRejectionCode is the STABLE, machine-readable classification of an enqueue
// rejection, carried alongside the human Reason.
//
// It exists because Reason is prose: it names the device token and the offending
// parameter so a person can fix the command, which is exactly what makes it unusable
// as a branch condition. A caller that must DECIDE something — command-delivery
// relaying the rejection, REACT's dispatcher deciding whether a retry could ever
// succeed — needs a value that does not change when the wording does.
//
// 🔴 The set is derived from the rejections ValidateCommandEnqueue can actually
// produce, one code per rejectedVerdict site, and nothing else. A code for a case the
// code cannot reach is a contract promising an answer that will never be given, and
// the first caller to branch on it writes dead code that reads as coverage.
type EnqueueRejectionCode string

const (
	// RejectDeviceNotFound: the device token resolved to no live device (it never
	// existed, or it was deleted). Permanent — no retry of the same enqueue can
	// succeed while the token names nothing.
	RejectDeviceNotFound EnqueueRejectionCode = "DEVICE_NOT_FOUND"
	// RejectCommandNotInVocabulary: the device's profile declares a command
	// vocabulary and the requested key is not in its PUBLISHED version. Permanent
	// for this (device, key) pair until the profile is republished.
	RejectCommandNotInVocabulary EnqueueRejectionCode = "COMMAND_NOT_IN_VOCABULARY"
	// RejectPayloadSchemaViolation: the command key resolved, but the payload
	// violates that definition's parameter schema (a missing required parameter, a
	// wrong type, an out-of-range value). Permanent for this payload.
	RejectPayloadSchemaViolation EnqueueRejectionCode = "PAYLOAD_SCHEMA_VIOLATION"
	// RejectDeviceDisabled: the device exists but an operator has disabled it.
	// Permanent for as long as it stays disabled; a command is not held for it, so
	// re-enabling the device does not deliver what was refused.
	RejectDeviceDisabled EnqueueRejectionCode = "DEVICE_DISABLED"
	// RejectCommandDestructiveRefused: the command is a raw, hand-addressed execute of an
	// operation that wipes the device (LwM2M Factory Reset). It is refused whatever the device's
	// vocabulary says, on every path that enqueues. Permanent: the same command is refused
	// identically every time, so REACT must not retry it.
	RejectCommandDestructiveRefused EnqueueRejectionCode = "COMMAND_DESTRUCTIVE_REFUSED"
)

// RejectWindowAfterExpiry: the command is held for the device type's maintenance window and
// the next opening comes after the command's own expiry, so it could only expire unsent.
// It is refused at enqueue rather than accepted to lapse. Permanent for this expiry: a later
// expiresAt, or a window that opens sooner, would be accepted.
const RejectWindowAfterExpiry EnqueueRejectionCode = "WINDOW_AFTER_EXPIRY"
