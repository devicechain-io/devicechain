// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
)

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
)

// CommandEnqueueVerdict is the answer to "may this command be enqueued to this
// device?" — the ADR-043 decision 3 enqueue gate, evaluated at the owner of the
// command vocabulary.
//
// A rejection is carried as a verdict (Allowed=false + Code + Reason), NOT as an
// error: the caller must be able to tell "the command is invalid" (the API client's
// fault, and safe to relay verbatim) apart from "the check could not be
// performed" (a transport/availability failure, which the caller must fail closed
// on and must NOT relay — it would leak in-cluster topology). Errors from this
// method therefore mean only the latter.
type CommandEnqueueVerdict struct {
	// Allowed reports whether the command may be enqueued.
	Allowed bool
	// Code classifies a rejection for a machine; empty when Allowed. It is the field
	// a caller branches on — Reason is for a human and its wording is free to change.
	Code EnqueueRejectionCode
	// Reason explains a rejection in terms the API client can act on; empty when
	// Allowed. It names only tenant-visible things (the device token, the command
	// key, the offending parameter) — never a service, host, or internal id.
	Reason string
}

// CommandVocabulary is the set of commands a device currently accepts — the
// device-facing read of the same published vocabulary the enqueue gate decides
// against (ADR-043 decision 3).
//
// It exists so the console can OFFER what the gate will ACCEPT. Before it, the
// only published-vocabulary surface was ValidateCommandEnqueue, which is
// ask-don't-list: a user could discover a command key only by guessing it and
// being rejected.
type CommandVocabulary struct {
	// DeviceExists reports whether the token resolved to a live device. False
	// makes the other two fields meaningless.
	DeviceExists bool
	// Constrained reports whether the profile restricts which commands may be
	// sent. When false the gate accepts ANY command key (decision 4), so an
	// unconstrained device is NOT a device with nothing to send — it is a device
	// whose vocabulary is open. Callers must not infer the former from an empty
	// Commands list; that is exactly the reading this field exists to prevent.
	Constrained bool
	// Commands is the published vocabulary, empty when not Constrained. These are
	// snapshot copies frozen at publish, not the draft rows they were captured
	// from — the draft may since have been edited or deleted.
	Commands []*CommandDefinition
}

// DeviceCommandVocabulary resolves device → type → the profile's active PUBLISHED
// command vocabulary (ADR-043 decision 3 / ADR-045).
//
// ValidateCommandEnqueue is built on this rather than resolving the vocabulary
// itself, so that what the console lists and what the gate enforces cannot drift:
// a device offered a command it would then be rejected for is worse than no
// listing at all, because it moves the failure from "I guessed wrong" to "the
// platform lied to me".
func (api *Api) DeviceCommandVocabulary(ctx context.Context, deviceToken string) (*CommandVocabulary, error) {
	devices, err := api.DevicesByToken(ctx, []string{deviceToken})
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return &CommandVocabulary{DeviceExists: false}, nil
	}

	definitions, err := api.CommandDefinitionsByDeviceType(ctx, devices[0].DeviceTypeId)
	if err != nil {
		return nil, err
	}
	// Constrained is derived HERE and only here, from the same list the gate then
	// matches against, so the two readings are the same reading.
	return &CommandVocabulary{
		DeviceExists: true,
		Constrained:  len(definitions) > 0,
		Commands:     definitions,
	}, nil
}

// allowedVerdict is the accept answer.
func allowedVerdict() *CommandEnqueueVerdict { return &CommandEnqueueVerdict{Allowed: true} }

// rejectedVerdict is the reject answer: a machine-readable code plus a client-safe
// reason. The code is a REQUIRED positional argument rather than an optional extra so
// that adding a rejection forces a decision about how a caller is meant to classify
// it — an uncoded rejection would silently reach a branching caller as "unclassified"
// and be handled by whatever its default is.
func rejectedVerdict(code EnqueueRejectionCode, format string, args ...any) *CommandEnqueueVerdict {
	return &CommandEnqueueVerdict{Allowed: false, Code: code, Reason: fmt.Sprintf(format, args...)}
}

// ValidateCommandEnqueue is the single enqueue gate of ADR-043 decision 3: it
// resolves device → its type → its profile's currently-active PUBLISHED command
// vocabulary → the definition matching commandKey, and validates the payload
// against that definition's parameter schema.
//
// It answers all three of decision 3's rejections in ONE hop — the target device
// not existing, an unknown command key, and a payload that violates the schema —
// because they are one question ("may this actuation be enqueued?") and because
// resolving the device is a prerequisite of the other two. Splitting them would
// resolve the device twice and open a window in which the device or its profile
// changes between the checks.
//
// The vocabulary read is the PUBLISHED snapshot (CommandDefinitionsByDeviceType),
// not the draft: what a device accepts is what was published, so validating
// against an unpublished draft would accept commands the device will reject.
//
// Strictness follows ADR-043 decision 3 + 4 exactly, and the distinction matters:
//
//   - device not found (or soft-deleted)   → REJECT
//   - profile declares NO command vocabulary → ALLOW, free-form (decision 4: an
//     absent or not-yet-published profile keeps accepting ad-hoc commands during
//     migration; this is NOT a silent skip of validation, it is the documented
//     backward path)
//   - vocabulary declared, key not in it   → REJECT (unknown command)
//   - definition found                     → ValidateCommandPayload decides; a
//     definition with an empty schema accepts any well-formed payload
//
// Blanket strictness would break every device whose profile is unpublished or
// carries no definitions — which pre-GA is most of them.
func (api *Api) ValidateCommandEnqueue(ctx context.Context, deviceToken string, commandKey string, payload []byte) (*CommandEnqueueVerdict, error) {
	vocab, err := api.DeviceCommandVocabulary(ctx, deviceToken)
	if err != nil {
		return nil, err
	}
	if !vocab.DeviceExists {
		return rejectedVerdict(RejectDeviceNotFound, "device %q does not exist", deviceToken), nil
	}
	// Decision 4 backward path: no declared vocabulary ⇒ free-form, as today.
	if !vocab.Constrained {
		return allowedVerdict(), nil
	}

	var matched *CommandDefinition
	for _, def := range vocab.Commands {
		if def != nil && def.CommandKey == commandKey {
			matched = def
			break
		}
	}
	if matched == nil {
		return rejectedVerdict(RejectCommandNotInVocabulary,
			"device %q accepts no command %q", deviceToken, commandKey), nil
	}

	if err := ValidateCommandPayload(matched, payload); err != nil {
		// ValidateCommandPayload's message already names the command key and the
		// offending parameter, and nothing else — safe to relay to the client.
		return rejectedVerdict(RejectPayloadSchemaViolation, "%s", err.Error()), nil
	}
	return allowedVerdict(), nil
}
