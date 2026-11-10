// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"

	"github.com/fatih/color"
)

// The device-uplinks choice lives in the Instance declaration: nil follows the chart
// default; true and false are decisions kept by later upgrades.

// MqttDeviceUplinksFromFlag turns a flag's (given, value) pair into the State's tri-state.
// A flag that was not given is nil, never false.
func MqttDeviceUplinksFromFlag(given, value bool) *bool {
	if !given {
		return nil
	}
	v := value
	return &v
}

// checkDeviceUplinksOverlay refuses a fresh overlay value that would be ignored because
// the instance already records a different flag choice. A carried value predates that
// choice and must not prevent later flagless upgrades.
func checkDeviceUplinksOverlay(st *State) error {
	path := splitPath("instance.config.infrastructure.nats.mqttDeviceUplinks")
	v, present := lookupPath(effectiveOverlay(st), path)
	overlay, isBool := v.(bool)
	if !present || !isBool || st.MqttDeviceUplinks == nil || st.MqttDeviceUplinksGiven || overlay == *st.MqttDeviceUplinks {
		return nil
	}
	if recorded, was := lookupPath(st.ValuesOverlayRecorded, path); was && recorded == overlay {
		return nil
	}
	return fmt.Errorf("the values overlay sets infrastructure.nats.mqttDeviceUplinks to %t, but this instance records --mqtt-device-uplinks=%t. "+
		"A choice recorded with the flag takes precedence over the overlay key; change it with --mqtt-device-uplinks (or --mqtt-device-uplinks=false), "+
		"or remove the key from the overlay. Nothing has been changed", overlay, *st.MqttDeviceUplinks)
}

// verifyDeviceUplinksRecorded refuses a declaration that came back without the choice this run
// asked it to keep. The API server prunes a field its CRD's schema does not declare, so a
// cluster whose Instance definition predates the field accepts the write, drops the field and
// reports success; the next flagless upgrade would then render the platform default over an
// instance the operator turned the setting on (or off) for.
func verifyDeviceUplinksRecorded(want, got *bool) error {
	if want == nil || sameBoolPtr(want, got) {
		return nil
	}
	return fmt.Errorf("this cluster's Instance definition does not record mqttDeviceUplinks (the declaration " +
		"came back without the choice this run asked it to keep), so a later upgrade would not know it. " +
		"Run `dcctl install` to move the cluster's definitions onto this release, then run this again")
}

// applyMqttDeviceUplinks writes the choice into infrastructure.nats.mqttDeviceUplinks. Nothing
// is written when the operator never chose, so the chart's default stands.
func applyMqttDeviceUplinks(natsVals map[string]interface{}, st *State) {
	if st.MqttDeviceUplinks == nil {
		return
	}
	natsVals["mqttDeviceUplinks"] = *st.MqttDeviceUplinks
}

// sayMqttDeviceUplinks reports only an explicit choice, on one line.
func sayMqttDeviceUplinks(st *State) {
	if st.MqttDeviceUplinks == nil {
		return
	}
	choice := "off (recorded on the instance)"
	if *st.MqttDeviceUplinks {
		choice = "on: a device may publish its own raw payload for its profile's decoder (recorded on the instance)"
	}
	fmt.Printf("  %s %s\n", color.WhiteString("MQTT device uplinks:"), color.GreenString(choice))
}
