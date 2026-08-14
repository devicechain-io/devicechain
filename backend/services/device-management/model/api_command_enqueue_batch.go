// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
)

// BatchEnqueueRefusal names one device a batch enqueue gate refused, and why. It is the
// same (code, reason) pair CommandEnqueueVerdict carries, plus the device it is about —
// because a batch answers about many devices at once, so a verdict with no subject is
// unattributable.
//
// 🔴 THE BATCH GATE REPORTS ONLY REFUSALS, NOT VERDICTS. A permitted device produces no
// entry at all, which makes the normal answer for a healthy fleet the EMPTY LIST — the
// difference between a response that scales with the fleet and one that scales with the
// refusals. A caller reconstructs "allowed" as "asked about, and not named here".
type BatchEnqueueRefusal struct {
	// DeviceToken is the device this refusal is about, exactly as the caller spelled
	// it in the request.
	DeviceToken string
	// Code classifies the refusal for a machine — the same vocabulary
	// ValidateCommandEnqueue produces, never a batch-specific one.
	Code EnqueueRejectionCode
	// Reason explains it in client-safe terms: it names only the device token, the
	// command key and the offending parameter — never a service, host or internal id.
	Reason string
}

// deviceLookupChunk bounds how many tokens go into one `token IN (...)` predicate.
//
// The batch is bounded upstream by the caller's held-command headroom (10,000 by
// default), and a single ten-thousand-element IN list is a large query to plan and a
// large statement to log. Chunking costs a handful of extra round trips on the largest
// batches and nothing at all on ordinary ones, which is the right trade for a gate on
// the enqueue path.
const deviceLookupChunk = 1000

// ValidateCommandEnqueueBatch decides, for MANY devices at once, whether one command
// with one payload may be enqueued to each — the fan-out counterpart of
// ValidateCommandEnqueue, and the gate a fleet-wide command write runs through.
//
// 🔑 IT IS AFFORDABLE BECAUSE THE VERDICT IS A FUNCTION OF THE DEVICE TYPE, NOT THE
// DEVICE. Every device in a batch receives the same command key and the same payload, so
// once a type's published vocabulary is resolved, the decision for every device of that
// type is already made. A ten-thousand-device batch spanning three device types resolves
// the vocabulary THREE times, not ten thousand — which is what a loop over
// ValidateCommandEnqueue would cost, one network round trip at a time from
// command-delivery.
//
// The device lookup is bulk but not free: DevicesByToken preloads DeviceType, so each
// chunk is two queries rather than one, and this gate reads only DeviceTypeId. It is a
// handful of extra queries across a whole batch, kept because a lean private lookup would
// be a second way to resolve a device token — but it is two per chunk, not one, and the
// arithmetic above should not be read as claiming otherwise.
//
// It agrees with ValidateCommandEnqueue by CONSTRUCTION, not by inspection: both call
// decideAgainstVocabulary on a vocabulary built by vocabularyOf. That matters more than
// the performance, because the failure mode of two independent implementations is a
// command the single-device gate accepts and the batch gate refuses — an operator seeing
// one device work and the same command to the same device inside a batch fail, with
// nothing in either message to explain it.
//
// Errors mean the check could not be PERFORMED (a database failure) and nothing about the
// request; the caller must fail closed on them and must not relay them. A refusal is a
// decided verdict and rides in the returned slice. This is the same split
// ValidateCommandEnqueue makes, for the same reason.
//
// Duplicate tokens in the request are collapsed: a device named twice is asked about once
// and, if refused, named once. Refusals come back in the order the caller first named
// each device, so the answer is deterministic and diffable rather than map-ordered.
func (api *Api) ValidateCommandEnqueueBatch(ctx context.Context, deviceTokens []string,
	commandKey string, payload []byte) ([]*BatchEnqueueRefusal, error) {
	refusals := make([]*BatchEnqueueRefusal, 0)
	ordered := dedupeTokens(deviceTokens)
	if len(ordered) == 0 {
		return refusals, nil
	}

	// One bulk lookup per chunk. A token absent from the result did not resolve to a
	// live device in this tenant — it never existed, or it was deleted — which is
	// exactly RejectDeviceNotFound, decided by absence rather than by a second query.
	byToken := make(map[string]*Device, len(ordered))
	for start := 0; start < len(ordered); start += deviceLookupChunk {
		end := start + deviceLookupChunk
		if end > len(ordered) {
			end = len(ordered)
		}
		found, err := api.DevicesByToken(ctx, ordered[start:end])
		if err != nil {
			return nil, err
		}
		for _, device := range found {
			if device != nil {
				byToken[device.Token] = device
			}
		}
	}

	// Resolve each distinct device type ONCE, memoized for the whole batch. The zero
	// value of a missing map entry is not usable here (a decision that allows is also
	// the zero value), so presence is tracked by the map itself.
	decisions := make(map[uint]vocabularyDecision)
	for _, token := range ordered {
		device, exists := byToken[token]
		if !exists {
			refusals = append(refusals, &BatchEnqueueRefusal{
				DeviceToken: token,
				Code:        RejectDeviceNotFound,
				Reason:      deviceNotFoundReason(token),
			})
			continue
		}

		decision, resolved := decisions[device.DeviceTypeId]
		if !resolved {
			definitions, err := api.CommandDefinitionsByDeviceType(ctx, device.DeviceTypeId)
			if err != nil {
				return nil, err
			}
			decision = decideAgainstVocabulary(vocabularyOf(definitions), commandKey, payload)
			decisions[device.DeviceTypeId] = decision
		}
		if decision.allowed() {
			continue
		}
		verdict := decision.verdictFor(token)
		refusals = append(refusals, &BatchEnqueueRefusal{
			DeviceToken: token,
			Code:        verdict.Code,
			Reason:      verdict.Reason,
		})
	}
	return refusals, nil
}

// dedupeTokens returns the distinct tokens in first-seen order.
//
// Order is preserved rather than sorted because the caller's order is meaningful — it is
// the order a partially-admitted batch admits in — so an answer sorted here would have to
// be re-sorted there, and the two orderings could disagree.
//
// 🔴 AN EMPTY TOKEN IS KEPT, NOT DROPPED, AND THE DIFFERENCE IS A CONTRACT HOLE. This
// gate reports refusals only, so a caller reads "asked about, and not named here" as
// ALLOWED. Silently discarding "" therefore answered *allowed* for a token that can never
// be enqueued to — the one reading that is both wrong and unrecoverable, because nothing
// in the response says the token was ever seen. Keeping it costs nothing: "" is absent
// from any bulk lookup result, so it falls out as DEVICE_NOT_FOUND through the ordinary
// path, which is exactly what it is.
func dedupeTokens(tokens []string) []string {
	seen := make(map[string]struct{}, len(tokens))
	ordered := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, already := seen[token]; already {
			continue
		}
		seen[token] = struct{}{}
		ordered = append(ordered, token)
	}
	return ordered
}
