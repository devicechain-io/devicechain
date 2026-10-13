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
	var segs []int
	for _, s := range strings.Split(strings.TrimSpace(path), "/") {
		if s == "" {
			continue
		}
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return false
		}
		segs = append(segs, int(n))
	}
	return len(segs) > factoryResetResourceSlot &&
		segs[0] == deviceObjectID && segs[factoryResetResourceSlot] == factoryResetResourceID
}

// IsRefusedRawExecute reports whether a command named commandKey with the given payload is a raw
// lwm2m.execute of the Factory Reset resource. A payload that is not a JSON object with a string
// path is not this command (the executor rejects it separately).
func IsRefusedRawExecute(commandKey string, payload []byte) bool {
	if commandKey != ExecuteCommand {
		return false
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return false
	}
	return IsFactoryResetPath(p.Path)
}
