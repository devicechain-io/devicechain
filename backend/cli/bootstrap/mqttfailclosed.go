// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-k8s/functionalarea"
	"github.com/fatih/color"
)

// --mqtt-fail-closed: the fail-closed MQTT QoS 1 acknowledgement
// (instance.config.infrastructure.nats.mqttFailClosed).
//
// 🔴 THE CHOICE LIVES IN THE INSTANCE DECLARATION, NOT IN THE RELEASE'S VALUES. dcctl
// composes the instance configuration document from the chart on every run and writes
// it over the last one, so a hand edit of the document, or of the Helm values, is
// overwritten by the next `dcctl upgrade`. The declaration is the one thing an upgrade
// reads the instance's shape back from (applyDeclaration), which is why the setting is a
// field there (InstanceSpec.MQTTFailClosed) and is written into the document by
// helmValues from the State that field populates.
//
// 🔴 THREE STATES, AND THE DIFFERENCE BETWEEN TWO OF THEM IS THE POINT. nil means the
// operator never chose: nothing is written to the document and the platform's own
// default applies. true and false are decisions, recorded as such, so an instance that
// was told "off" is still told "off" by an upgrade, whatever the default is by then.
// A plain bool would record "never chose" and "chose off" identically.

// MqttFailClosedFromFlag turns a flag's (given, value) pair into the State's tri-state.
// A flag that was not given is nil, never false.
func MqttFailClosedFromFlag(given, value bool) *bool {
	if !given {
		return nil
	}
	v := value
	return &v
}

// copyBoolPtr copies a tri-state so State and declaration never alias one another.
func copyBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// sameBoolPtr reports whether two tri-states say the same thing: both unset, or both set
// to one value.
func sameBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ValidateMqttFailClosed refuses a request for fail-closed on an instance that could not
// run it. It is judged from the profile and the extra areas alone, so it can run before
// any cluster exists.
//
// The store is read by event-sources (the platform's durables over the MQTT message
// store) and guarded by device-management (its auth callout refuses new device
// connections when the store is nearly full). Every profile ships both today; what this
// refuses is an area set that does not, rather than an instance whose event-sources
// stops at startup with "no event source points at the platform broker".
func ValidateMqttFailClosed(on *bool, profile string, extraAreas []string) error {
	if on == nil || !*on {
		return nil
	}
	enabled, err := functionalarea.ResolveEnabled(profile, nil)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, a := range enabled {
		have[string(a)] = true
	}
	for _, a := range extraAreas {
		have[strings.TrimSpace(a)] = true
	}
	var missing []string
	for _, need := range []functionalarea.FunctionalArea{functionalarea.EventSources, functionalarea.DeviceManagement} {
		if !have[string(need)] {
			missing = append(missing, string(need))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("--mqtt-fail-closed needs %s, which this instance does not deploy: the platform reads "+
			"device events out of the MQTT message store (event-sources) and refuses new device connections as "+
			"it fills (device-management). Add the area with --enable-area, or leave fail-closed off",
			strings.Join(missing, " and "))
	}
	return nil
}

// applyMqttFailClosed writes the choice into infrastructure.nats.mqttFailClosed. Nothing
// is written when the operator never chose, so the chart's default stands.
func applyMqttFailClosed(natsVals map[string]interface{}, st *State) {
	if st.MqttFailClosed == nil {
		return
	}
	natsVals["mqttFailClosed"] = *st.MqttFailClosed
}

// sayMqttFailClosed reports the setting in effect, and what it trades, whenever the
// operator has made a choice. It says nothing for an instance that never chose.
func sayMqttFailClosed(st *State) {
	if st.MqttFailClosed == nil {
		return
	}
	if !*st.MqttFailClosed {
		fmt.Printf("  %s %s\n", color.WhiteString("MQTT fail-closed:"), color.GreenString("off (recorded on the instance)"))
		return
	}
	fmt.Printf("  %s %s\n", color.WhiteString("MQTT fail-closed:"), color.GreenString(
		"on: a QoS 1 publish is refused, not acknowledged, while the capture stream has no leader; the ingest ceiling is lower than with it off"))
	if !st.HA {
		fmt.Printf("  %s\n", color.YellowString(
			"  this cluster was installed without --ha: with one broker replica a node crash can lose what the file store had not yet synced, so a PUBACK is not a durability promise there"))
	}
}
