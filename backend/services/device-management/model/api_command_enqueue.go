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
	return vocabularyOf(definitions), nil
}

// vocabularyOf builds the vocabulary of a device that EXISTS, from the definition list
// its type resolved to.
//
// Constrained is derived HERE and only here, from the same list the gate then matches
// against, so the two readings are the same reading. It is a named function rather than
// a struct literal because the batch gate resolves definitions per device TYPE and would
// otherwise have to repeat the `len(definitions) > 0` derivation — and a second copy of
// that one comparison is exactly how "an unconstrained device is not a device with
// nothing to send" stops being true on one of the two paths.
func vocabularyOf(definitions []*CommandDefinition) *CommandVocabulary {
	return &CommandVocabulary{
		DeviceExists: true,
		Constrained:  len(definitions) > 0,
		Commands:     definitions,
	}
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
		return rejectedVerdict(RejectDeviceNotFound, "%s", deviceNotFoundReason(deviceToken)), nil
	}
	return decideAgainstVocabulary(vocab, commandKey, payload).verdictFor(deviceToken), nil
}

// deviceNotFoundReason is the one rejection wording both gates must share. The batch gate
// decides this case by a token's ABSENCE from a bulk result rather than by a vocabulary
// lookup, so without a shared renderer the two paths would each spell the same refusal
// their own way — and the reason is the only part of a rejection a person reads.
func deviceNotFoundReason(deviceToken string) string {
	return fmt.Sprintf("device %q does not exist", deviceToken)
}

// vocabularyDecision is the part of an enqueue verdict that depends ONLY on the device
// TYPE — the published vocabulary it resolves to, the command key, and the payload.
//
// 🔑 IT IS SPLIT OUT BECAUSE THE BATCH GATE MUST NOT RE-DERIVE IT. A batch carries one
// command key and one payload for every device in it (the whole reason it is affordable),
// so the decision varies only with the device's type — which means the batch gate can
// resolve each distinct type once and reuse the answer, instead of doing O(devices)
// vocabulary reads and O(devices) schema validations.
//
// The alternative — a second implementation of the same rules inside the batch path — is
// the failure this shape exists to prevent: the two gates would drift, and the symptom
// would be a command that the single-device gate accepts and the batch gate refuses (or
// worse, the reverse). Both paths now reach the same verdict because it is literally the
// same function, not because two pieces of code agree today.
//
// The device TOKEN is deliberately not an input: it appears only in the rendered reason,
// which is why verdictFor takes it separately. Keeping it out is what makes the decision
// reusable across every device sharing a type.
type vocabularyDecision struct {
	// code is the rejection classification, or "" when the command is allowed.
	code EnqueueRejectionCode
	// reason is the fully-rendered reason for a decision whose wording does not name
	// the device (the payload-schema case, whose message names the command key and the
	// offending parameter). Empty when the reason must be rendered per device.
	reason string
	// commandKey is retained so a per-device reason can name it.
	commandKey string
}

// decideAgainstVocabulary applies ADR-043 decision 3 + 4 to an ALREADY-RESOLVED
// vocabulary. It performs no I/O, so a caller holding one vocabulary may reuse it for
// every device of that type.
//
// The caller is responsible for the device-existence check: a vocabulary with
// DeviceExists false has no meaningful Constrained or Commands, and the two callers
// discover a missing device differently (a single lookup returning nothing, versus a
// token absent from a bulk result).
func decideAgainstVocabulary(vocab *CommandVocabulary, commandKey string, payload []byte) vocabularyDecision {
	// Decision 4 backward path: no declared vocabulary ⇒ free-form, as today.
	if !vocab.Constrained {
		return vocabularyDecision{}
	}

	var matched *CommandDefinition
	for _, def := range vocab.Commands {
		if def != nil && def.CommandKey == commandKey {
			matched = def
			break
		}
	}
	if matched == nil {
		return vocabularyDecision{code: RejectCommandNotInVocabulary, commandKey: commandKey}
	}

	if err := ValidateCommandPayload(matched, payload); err != nil {
		// ValidateCommandPayload's message already names the command key and the
		// offending parameter, and nothing else — safe to relay to the client, and
		// identical for every device of this type.
		return vocabularyDecision{code: RejectPayloadSchemaViolation, reason: err.Error(), commandKey: commandKey}
	}
	return vocabularyDecision{}
}

// allowed reports whether this decision permits the enqueue.
func (d vocabularyDecision) allowed() bool { return d.code == "" }

// verdictFor renders the decision as a single-device verdict, naming the device in the
// one reason whose wording is per-device.
func (d vocabularyDecision) verdictFor(deviceToken string) *CommandEnqueueVerdict {
	if d.allowed() {
		return allowedVerdict()
	}
	if d.reason != "" {
		return rejectedVerdict(d.code, "%s", d.reason)
	}
	return rejectedVerdict(d.code, "device %q accepts no command %q", deviceToken, d.commandKey)
}
