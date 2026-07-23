// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// jsonNull is the literal a JSON serializer writes for an omitted optional field.
// A device that emits `"occurredTime": null` has the key PRESENT with value null,
// which a naive presence check would treat as "device set it" and skip minting —
// yet the cloud decoder unmarshals a null into a nil *string and then defaults
// occurredTime to time.Now() at EACH decode, re-randomising the dedup key on every
// redelivery. So for idempotency purposes a null value is ABSENT.
var jsonNull = []byte("null")

// stampIdempotency makes one captured event's JSON payload exactly-once-safe at the
// cloud, and returns the bytes to forward.
//
// The cloud dedups on the device-level partial unique index
// (tenant_id, alt_id, occurred_time) WHERE alt_id IS NOT NULL. Both altId and
// occurredTime are device-OPTIONAL, so when the device omitted them this splices in
// values that are IDENTICAL on every redelivery and after a restart — the only way
// the cloud can recognise a redelivered event as the same event:
//
//   - altId  = "edge:<installId>:<streamEpoch>:<streamSeq>". streamSeq and streamEpoch
//     are frozen when the message was first stored (see forward) so the mint is
//     replay-stable; installId + epoch keep it collision-free across agents and
//     across a stream delete/recreate (MAJOR-4).
//   - occurredTime = the message's stream store-time (frozen at first durable local
//     receipt), formatted RFC3339Nano UTC. Stamping is REQUIRED even when altId was
//     device-set: a device-set altId with no occurredTime still re-randomises the key
//     via the cloud's time.Now() default.
//
// Device-set values are never overwritten (the device's own idempotency intent wins).
// Only the JSON decoder carries this contract, so a payload that is not a JSON object
// (array, scalar, non-JSON binary) is forwarded VERBATIM — exactly-once is a
// per-decoder (JSON) property; other payloads get at-least-once. altId/occurredTime
// are the only fields touched; every other field's bytes pass through unchanged
// (json.RawMessage), so credentials (ADR-014) and payload survive intact.
func stampIdempotency(raw []byte, installId, streamEpoch string, streamSeq uint64, storedAt time.Time) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		// Not a JSON object (array/scalar/binary/malformed) — forward verbatim.
		return raw, nil
	}
	if fields == nil {
		// A literal JSON `null` unmarshals into a map with NO error but leaves the map
		// nil — writing to it below would panic, and because the event is never acked
		// that panic redelivers first on restart and crash-loops the whole spool. Treat
		// it as a non-object and forward verbatim (the cloud decoder rejects it anyway).
		return raw, nil
	}

	changed := false
	if absent(fields["altId"], hasKey(fields, "altId")) {
		altId, err := json.Marshal(fmt.Sprintf("edge:%s:%s:%d", installId, streamEpoch, streamSeq))
		if err != nil {
			return nil, err
		}
		fields["altId"] = altId
		changed = true
	}
	if absent(fields["occurredTime"], hasKey(fields, "occurredTime")) {
		ts, err := json.Marshal(storedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		fields["occurredTime"] = ts
		changed = true
	}
	if !changed {
		// Device supplied both — forward the original bytes untouched (also avoids a
		// needless key-reordering re-marshal).
		return raw, nil
	}
	return json.Marshal(fields)
}

// hasKey reports whether the parsed object carried the key at all.
func hasKey(fields map[string]json.RawMessage, key string) bool {
	_, ok := fields[key]
	return ok
}

// absent reports whether a field should be treated as unset for minting: the key was
// missing, or present with a literal null value (MAJOR-1).
func absent(raw json.RawMessage, present bool) bool {
	if !present {
		return true
	}
	return bytes.Equal(bytes.TrimSpace(raw), jsonNull)
}
