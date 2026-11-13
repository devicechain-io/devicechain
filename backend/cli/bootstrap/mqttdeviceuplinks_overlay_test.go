// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"strings"
	"testing"
)

func TestDeviceUplinksOverlayAtTheWrongPathExplainsTheChoice(t *testing.T) {
	for _, value := range []string{"false", "true", "unrecognised"} {
		t.Run(value, func(t *testing.T) {
			path := writeOverlayFile(t, "overlay.yaml", "infrastructure:\n  nats:\n    mqttDeviceUplinks: "+value+"\n")
			_, err := LoadValuesOverlay([]string{path})
			if err == nil {
				t.Fatal("a misplaced key was accepted")
			}
			want := []string{"infrastructure.nats.mqttDeviceUplinks", "instance.config.infrastructure.nats.mqttDeviceUplinks", "requested value: " + value}
			if value == "unrecognised" {
				want = append(want, "to set it, run with --mqtt-device-uplinks=true or --mqtt-device-uplinks=false")
			} else {
				want = append(want, "--mqtt-device-uplinks="+value)
			}
			for _, part := range want {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("refusal missing %q: %v", part, err)
				}
			}
			if strings.Contains(err.Error(), "not an operator-tunable value") {
				t.Errorf("generic refusal: %v", err)
			}
		})
	}
}

func TestDeviceUplinksOverlayPathControls(t *testing.T) {
	path := writeOverlayFile(t, "tunable.yaml", "instance:\n  config:\n    infrastructure:\n      nats:\n        mqttDeviceUplinks: true\n")
	if _, err := LoadValuesOverlay([]string{path}); err != nil {
		t.Fatalf("tunable path refused: %v", err)
	}
	path = writeOverlayFile(t, "owned.yaml", "image: {tag: x}\n")
	if _, err := LoadValuesOverlay([]string{path}); err == nil || !strings.Contains(err.Error(), "not an operator-tunable value") {
		t.Errorf("unrelated owned key lost its generic refusal: %v", err)
	}
	for _, path := range [][]string{{"NATS", "MQTTDeviceUplinks"}, {"other", "Nats", "mqttDeviceUplinks"}} {
		overlay := map[string]interface{}{}
		current := overlay
		for _, segment := range path[:len(path)-1] {
			child := map[string]interface{}{}
			current[segment] = child
			current = child
		}
		current[path[len(path)-1]] = false
		err := refuseOverlayKeys(overlay, "overlay")
		if err == nil || !strings.Contains(err.Error(), strings.Join(path, ".")) || !strings.Contains(err.Error(), "--mqtt-device-uplinks=false") {
			t.Errorf("misplaced key %v lost its specific refusal: %v", path, err)
		}
	}
}

func TestDeviceUplinksOverlayConflictNamesTheRecordedChoiceAndRemedy(t *testing.T) {
	for _, recorded := range []bool{true, false} {
		t.Run(fmt.Sprint(recorded), func(t *testing.T) {
			requested := !recorded
			st := &State{MqttDeviceUplinks: boolPtr(recorded), ValuesOverlay: map[string]interface{}{
				"instance": map[string]interface{}{"config": map[string]interface{}{
					"infrastructure": map[string]interface{}{"nats": map[string]interface{}{"mqttDeviceUplinks": requested}},
				}},
			}}
			err := checkDeviceUplinksOverlay(st)
			if err == nil {
				t.Fatal("a conflict was accepted")
			}
			for _, part := range []string{
				"instance.config.infrastructure.nats.mqttDeviceUplinks", fmt.Sprintf("to %t", requested),
				fmt.Sprintf("records --mqtt-device-uplinks=%t", recorded), fmt.Sprintf("To record %t, run with --mqtt-device-uplinks=%t", requested, requested),
				"a choice recorded with the flag takes precedence over the overlay", fmt.Sprintf("to keep %t, remove the key from the overlay", recorded),
			} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("refusal missing %q: %v", part, err)
				}
			}
		})
	}
}
