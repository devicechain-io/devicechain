// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "testing"

// bodyCredentialOf reads functionalAreas.event-sources.config.capture.bodyCredential out of
// release values, or "" and false when any step of the path is absent.
func bodyCredentialOf(vals map[string]interface{}) (string, bool) {
	node := interface{}(vals)
	for _, key := range []string{"functionalAreas", "event-sources", "config", "capture", "bodyCredential"} {
		m, ok := node.(map[string]interface{})
		if !ok {
			return "", false
		}
		if node, ok = m[key]; !ok {
			return "", false
		}
	}
	s, ok := node.(string)
	return s, ok
}

func aReleaseBridgingWithStrip() map[string]interface{} {
	return map[string]interface{}{
		"functionalAreas": map[string]interface{}{
			"event-sources": map[string]interface{}{
				"config": map[string]interface{}{
					"capture": map[string]interface{}{"bodyCredential": "strip", "publishWindow": 512},
				},
				"profiler": map[string]interface{}{"enabled": true},
			},
		},
	}
}

func TestMqttBodyCredentialFlagValuesAreValidated(t *testing.T) {
	for _, ok := range []string{"", "refuse", "strip"} {
		if got, err := ParseMqttBodyCredential(ok); err != nil || got != ok {
			t.Errorf("ParseMqttBodyCredential(%q) = %q, %v", ok, got, err)
		}
	}
	for _, bad := range []string{"Strip", "stripped", "optional", "true"} {
		if _, err := ParseMqttBodyCredential(bad); err == nil {
			t.Errorf("ParseMqttBodyCredential(%q) accepted a value event-sources would refuse to load", bad)
		}
	}
}

// The flag reaches the area's values, beside whatever else the area's block already holds.
func TestTheFlagSetsEventSourcesCaptureBodyCredential(t *testing.T) {
	for _, v := range []string{"strip", "refuse"} {
		vals := map[string]interface{}{"functionalAreas": map[string]interface{}{
			"event-sources": map[string]interface{}{"config": map[string]interface{}{"contention": map[string]interface{}{"manualFloor": 1}}},
		}}
		applyMqttBodyCredential(vals, &State{MqttBodyCredential: v})
		if got, _ := bodyCredentialOf(vals); got != v {
			t.Errorf("bodyCredential = %q, want %q", got, v)
		}
		cfg := vals["functionalAreas"].(map[string]interface{})["event-sources"].(map[string]interface{})["config"].(map[string]interface{})
		if _, kept := cfg["contention"]; !kept {
			t.Error("setting the flag dropped another key of the area's config")
		}
	}
}

// helmValues is the real composition: the flag must come out of it, and only when given.
func TestHelmValuesWritesTheFlagOnlyWhenGiven(t *testing.T) {
	st := profileState(false, false, "default")("dci-strip")
	if _, present := bodyCredentialOf(helmValues(st)); present {
		t.Error("helmValues wrote capture.bodyCredential though the flag was not given: an upgrade would reset a carried value")
	}
	st.MqttBodyCredential = "strip"
	if got, _ := bodyCredentialOf(helmValues(st)); got != "strip" {
		t.Errorf("helmValues bodyCredential = %q, want strip", got)
	}
}

// 🔴 THE REASON THE PATH IS CARRIED. An upgrade that omits the flag must keep the release's
// strip: the fleet it bridges is still sending credentials, and dropping it would put the
// refusal back with no one having asked.
func TestAnUpgradeWithoutTheFlagKeepsTheCarriedStrip(t *testing.T) {
	st := profileState(false, false, "default")("dci-strip")
	st.Evolving = true
	vals, _, err := releaseValues(t.Context(), chartForTest(t), st, aReleaseBridgingWithStrip())
	if err != nil {
		t.Fatalf("composing the release values: %v", err)
	}
	if got, _ := bodyCredentialOf(vals); got != "strip" {
		t.Errorf("bodyCredential after an upgrade without the flag = %q, want strip carried", got)
	}
	// Only that leaf: the rest of the area's block is not carried, and the profiler is off.
	areas := vals["functionalAreas"].(map[string]interface{})
	es := areas["event-sources"].(map[string]interface{})
	if _, ok := es["profiler"]; ok {
		t.Error("the area's profiler block was carried")
	}
	capture := es["config"].(map[string]interface{})["capture"].(map[string]interface{})
	if _, ok := capture["publishWindow"]; ok {
		t.Error("carrying bodyCredential also carried a sibling setting dcctl does not own")
	}
}

// An explicit --mqtt-body-credential=refuse overrides the carried strip: this is the step
// that ends the bridge, and it must not be silently undone by the carry.
func TestAnExplicitRefuseOverridesTheCarriedStrip(t *testing.T) {
	st := profileState(false, false, "default")("dci-strip")
	st.Evolving = true
	st.MqttBodyCredential = "refuse"
	vals, _, err := releaseValues(t.Context(), chartForTest(t), st, aReleaseBridgingWithStrip())
	if err != nil {
		t.Fatalf("composing the release values: %v", err)
	}
	if got, _ := bodyCredentialOf(vals); got != "refuse" {
		t.Errorf("bodyCredential = %q, want the explicit refuse to win over the carried strip", got)
	}
}

// ...and an explicit strip works on an instance that held nothing.
func TestAnExplicitStripOnAReleaseThatHeldNothing(t *testing.T) {
	st := profileState(false, false, "default")("dci-strip")
	st.Evolving = true
	st.MqttBodyCredential = "strip"
	vals, _, err := releaseValues(t.Context(), chartForTest(t), st, map[string]interface{}{})
	if err != nil {
		t.Fatalf("composing the release values: %v", err)
	}
	if got, _ := bodyCredentialOf(vals); got != "strip" {
		t.Errorf("bodyCredential = %q, want strip", got)
	}
	// And nothing is invented for a release that never held it, flag absent.
	st.MqttBodyCredential = ""
	vals, _, err = releaseValues(t.Context(), chartForTest(t), st, map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if v, present := bodyCredentialOf(vals); present {
		t.Errorf("an upgrade invented bodyCredential %q for a release that held none", v)
	}
}
