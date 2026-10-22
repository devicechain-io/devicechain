// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "fmt"

// The two values of event-sources' capture.bodyCredential, and the flag that sets it.
const (
	MqttBodyCredentialRefuse = "refuse"
	MqttBodyCredentialStrip  = "strip"
)

// ParseMqttBodyCredential validates --mqtt-body-credential. Empty is "not given" and is NOT
// the same as "refuse": an upgrade that was not asked leaves a carried value alone, while
// "refuse" given explicitly overrides one.
func ParseMqttBodyCredential(v string) (string, error) {
	switch v {
	case "", MqttBodyCredentialRefuse, MqttBodyCredentialStrip:
		return v, nil
	}
	return "", fmt.Errorf("%q is not one of %q or %q", v, MqttBodyCredentialRefuse, MqttBodyCredentialStrip)
}

// applyMqttBodyCredential writes the flag into functionalAreas.event-sources.config.capture.bodyCredential,
// leaving every other key of the area's block alone. Nothing is written when the flag was not
// given, which is what lets an upgrade carry the release's value forward instead of resetting it.
func applyMqttBodyCredential(vals map[string]interface{}, st *State) {
	if st.MqttBodyCredential == "" {
		return
	}
	node := vals
	for _, key := range []string{"functionalAreas", "event-sources", "config", "capture"} {
		next, ok := node[key].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			node[key] = next
		}
		node = next
	}
	node["bodyCredential"] = st.MqttBodyCredential
}
