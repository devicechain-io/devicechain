// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package lwm2mpath classifies LwM2M resource paths that must never be reachable through the
// generic, hand-typed lwm2m.execute command.
//
// It lives in core because the same question is asked at more than one gate — device-management's
// enqueue gate (single, batch and REACT all pass through it) and the lwm2m-ingest executor that
// would put the request on the wire — and two copies of a path normaliser are how those gates come
// to disagree about what "the same resource" means.
package lwm2mpath

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	// ExecuteCommand is the generic path-addressed Execute command key.
	ExecuteCommand = "lwm2m.execute"

	// deviceObjectID is the LwM2M Device object; factoryResetResourceID its Factory Reset resource.
	deviceObjectID           = 3
	factoryResetResourceID   = 5
	factoryResetResourceSlot = 2 // index of the resource id in object/instance/resource
)

// IsFactoryResetPath reports whether path addresses resource 5 (Factory Reset) of ANY instance of
// object 3 (Device) — or anything beneath it. It is deliberately more eager than the wire format:
// the numeric segments are compared by value, so /3/0/5, 3/0/5, /3/0/5/, /3/00/5 and /3/7/5 are one
// answer, and an unparseable path is simply "not this resource" (the executor refuses malformed
// paths on its own). Over-refusing a spelling a device would never accept costs nothing; missing a
// spelling it would accept is the failure this exists to prevent.
func IsFactoryResetPath(path string) bool {
	var segs []uint64
	for _, s := range strings.Split(strings.TrimSpace(path), "/") {
		if s == "" {
			continue
		}
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return false
		}
		segs = append(segs, n)
	}
	return len(segs) > factoryResetResourceSlot &&
		segs[0] == deviceObjectID && segs[factoryResetResourceSlot] == factoryResetResourceID
}

// HasAmbiguousPath reports whether a command payload carries more than one key that names the
// path under Go's case-insensitive JSON key matching (for example "path" and "Path"). Decoders
// disagree about which one wins once a store has re-ordered the keys, so such a payload is refused
// rather than guessed at. A payload that is not a JSON object is not ambiguous (it is malformed,
// which the executor rejects separately).
func HasAmbiguousPath(payload []byte) bool {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(payload, &keys); err != nil {
		return false
	}
	n := 0
	for k := range keys {
		if strings.EqualFold(k, "path") {
			n++
		}
	}
	return n > 1
}

// IsRefusedRawExecute reports whether a command named commandKey with the given payload is a raw
// lwm2m.execute that must be refused: one addressing the Factory Reset resource, or one whose path
// is ambiguous (see HasAmbiguousPath). A payload that is not a JSON object with a string path is
// not this command (the executor rejects it separately).
func IsRefusedRawExecute(commandKey string, payload []byte) bool {
	if commandKey != ExecuteCommand {
		return false
	}
	if HasAmbiguousPath(payload) {
		return true
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return false
	}
	return IsFactoryResetPath(p.Path)
}
