// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Namespace is the Sparkplug B topic namespace. Every Sparkplug topic begins
// with it, including the Host Application STATE topic.
const Namespace = "spBv1.0"

// State is the Sparkplug 3.0 Host Application STATE payload. A Host announces its
// liveness by publishing this to spBv1.0/STATE/{host_id} as a RETAINED, QoS-1
// message, and registers the OFFLINE form as its MQTT Last-Will so an ungraceful
// disconnect flips the same retained topic. This replaced the bare "ONLINE" /
// "OFFLINE" string body of Sparkplug 2.2.
//
// The Timestamp is the load-bearing field of the 3.0 change: the OFFLINE will and
// the ONLINE birth a Host publishes for the SAME connection MUST carry the SAME
// timestamp, so an edge node that receives a delayed OFFLINE can compare it to the
// ONLINE it already holds and reject the stale death instead of marking a live
// host dead. StateTopic + the shared birth timestamp in host.go enforce that pair.
type State struct {
	Online bool `json:"online"`
	// Timestamp is milliseconds since the Unix epoch (Sparkplug uses ms).
	Timestamp int64 `json:"timestamp"`
}

// StateTopic returns the Sparkplug 3.0 Host STATE topic for a host id. Note it
// lives OUTSIDE the {group} tree (spBv1.0/STATE/{host_id}, not
// spBv1.0/{group}/STATE/...), so a wrong topic means edge nodes never observe
// host presence at all — a silent total failure, which is why B4 pins it.
func StateTopic(hostID string) string {
	return Namespace + "/STATE/" + hostID
}

// Marshal encodes the STATE payload as the Sparkplug 3.0 JSON body.
func (s State) Marshal() ([]byte, error) {
	return json.Marshal(s)
}

// ParseState decodes a Sparkplug 3.0 STATE payload, failing closed on anything
// that is not exactly the {online,timestamp} JSON object. It rejects a 2.2 bare
// "ONLINE" string, an unknown field, a trailing document, AND a body that omits
// either field — {}, null, and {"online":true} all decode without error into a
// zero State{false,0}, which would be a SILENT mis-read of liveness as
// OFFLINE@epoch (exactly the failure this guard exists to prevent). Presence is
// detected via pointers so a real online:false is distinguished from an absent
// field.
func ParseState(b []byte) (State, error) {
	var raw struct {
		Online    *bool  `json:"online"`
		Timestamp *int64 `json:"timestamp"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return State{}, fmt.Errorf("STATE payload is not Sparkplug 3.0 JSON: %w", err)
	}
	if dec.More() {
		return State{}, fmt.Errorf("STATE payload has trailing data after the JSON object")
	}
	if raw.Online == nil || raw.Timestamp == nil {
		return State{}, fmt.Errorf("STATE payload must carry both online and timestamp")
	}
	return State{Online: *raw.Online, Timestamp: *raw.Timestamp}, nil
}
