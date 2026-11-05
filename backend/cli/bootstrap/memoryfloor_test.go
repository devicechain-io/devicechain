// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "testing"

// Round 29 on GKE measured two shipped memory limits sitting within one rung of the footprint:
//
//   - event-processing with an event-envelope switch on holds the detection duplicate guard:
//     265-269 MiB resident at 4,500-6,000 events/s and 388 MiB at 12,000 (68-85 MiB with the
//     switches off), against a 384Mi limit whose GOMEMLIMIT is 288 MiB. The chart now raises
//     the limit to 768Mi (GOMEMLIMIT 576 MiB) while either switch is on.
//   - device-management caches the whole fleet in every replica: Go heap 154 MiB at 6,000
//     devices and 193-218 MiB at 12,000-16,000, against a 256Mi limit whose GOMEMLIMIT is
//     192 MiB. The chart now floors it at 512Mi (GOMEMLIMIT 384 MiB).
//
// Both are floors (memoryLimitFloor, memoryLimitFloorWithEnvelopes): the operator's own
// limit on the area, or a larger top-level one, is used as written.

func envelopes(resolved, inbound bool) map[string]interface{} {
	return map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{
		"infrastructure": map[string]interface{}{"messaging": map[string]interface{}{
			"envelopes": map[string]interface{}{"resolved": resolved, "inbound": inbound},
		}},
	}}}
}

func areaOf(t *testing.T, vals map[string]interface{}, area string) renderedContainer {
	t.Helper()
	c, ok := byArea(t, renderContainers(t, vals))[area]
	if !ok {
		t.Fatalf("%s did not render: nothing below checked it", area)
	}
	return c
}

func TestEventProcessingMemoryLimitFollowsTheEnvelopeSwitches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		vals      map[string]interface{}
		wantLimit string
		wantGoMem int64
	}{
		{"no instance config", nil, "384Mi", 288},
		{"both switches off", envelopes(false, false), "384Mi", 288},
		{"resolved on", envelopes(true, false), "768Mi", 576},
		{"inbound on", envelopes(false, true), "768Mi", 576},
		{"both on", envelopes(true, true), "768Mi", 576},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := areaOf(t, tc.vals, "event-processing")
			if c.memoryLimit != tc.wantLimit || mib(t, c.goMemLimit) != tc.wantGoMem {
				t.Errorf("limit %q GOMEMLIMIT %q, want %s and %dMiB", c.memoryLimit, c.goMemLimit, tc.wantLimit, tc.wantGoMem)
			}
			// The request never moves: only the limit does, so scheduling is unchanged.
			if c.requests["memory"] != "128Mi" {
				t.Errorf("requests.memory %q, want 128Mi", c.requests["memory"])
			}
		})
	}
}

func TestEventProcessingEnvelopeFloorYieldsToTheOperator(t *testing.T) {
	t.Parallel()

	t.Run("an area limit of the operator's own is used as written", func(t *testing.T) {
		t.Parallel()
		vals := envelopes(true, true)
		vals["functionalAreas"] = map[string]interface{}{"event-processing": map[string]interface{}{
			"resources": map[string]interface{}{"limits": map[string]interface{}{"memory": "512Mi"}}}}
		c := areaOf(t, vals, "event-processing")
		if c.memoryLimit != "512Mi" || mib(t, c.goMemLimit) != 384 {
			t.Errorf("limit %q GOMEMLIMIT %q, want the operator's 512Mi and 384MiB", c.memoryLimit, c.goMemLimit)
		}
	})
	t.Run("a larger top-level limit is used as written", func(t *testing.T) {
		t.Parallel()
		vals := envelopes(true, true)
		vals["resources"] = map[string]interface{}{
			"requests": map[string]interface{}{"cpu": "100m", "memory": "128Mi"},
			"limits":   map[string]interface{}{"cpu": "500m", "memory": "2Gi"},
		}
		c := areaOf(t, vals, "event-processing")
		if c.memoryLimit != "2Gi" || mib(t, c.goMemLimit) != 1536 {
			t.Errorf("limit %q GOMEMLIMIT %q, want 2Gi and 1536MiB", c.memoryLimit, c.goMemLimit)
		}
	})
	t.Run("an explicit GOMEMLIMIT is used as written", func(t *testing.T) {
		t.Parallel()
		vals := envelopes(true, true)
		vals["functionalAreas"] = map[string]interface{}{"event-processing": map[string]interface{}{
			"goMemLimit": "640MiB"}}
		c := areaOf(t, vals, "event-processing")
		if c.memoryLimit != "768Mi" || c.goMemLimit != "640MiB" {
			t.Errorf("limit %q GOMEMLIMIT %q, want 768Mi and the operator's 640MiB", c.memoryLimit, c.goMemLimit)
		}
	})
	t.Run("the floor can be set by the operator", func(t *testing.T) {
		t.Parallel()
		vals := envelopes(true, true)
		vals["functionalAreas"] = map[string]interface{}{"event-processing": map[string]interface{}{
			"memoryLimitFloorWithEnvelopes": "1Gi"}}
		c := areaOf(t, vals, "event-processing")
		if c.memoryLimit != "1Gi" || mib(t, c.goMemLimit) != 768 {
			t.Errorf("limit %q GOMEMLIMIT %q, want 1Gi and 768MiB", c.memoryLimit, c.goMemLimit)
		}
	})
}

func TestOnlyEventProcessingRaisesItsLimitWithEnvelopes(t *testing.T) {
	t.Parallel()
	off := byArea(t, renderContainers(t, envelopes(false, false)))
	on := byArea(t, renderContainers(t, envelopes(true, true)))
	for area, c := range on {
		if area == "event-processing" {
			continue
		}
		if c.memoryLimit != off[area].memoryLimit {
			t.Errorf("%s: limit %q with envelopes on, %q off: only event-processing follows the switches",
				area, c.memoryLimit, off[area].memoryLimit)
		}
	}
}

func TestDeviceManagementMemoryLimitIsFlooredAt512Mi(t *testing.T) {
	t.Parallel()

	if c := areaOf(t, nil, "device-management"); c.memoryLimit != "512Mi" || mib(t, c.goMemLimit) != 384 {
		t.Errorf("shipped: limit %q GOMEMLIMIT %q, want 512Mi and 384MiB", c.memoryLimit, c.goMemLimit)
	}
	// The envelope switches do not move it: its heap follows the fleet, not the stream format.
	if c := areaOf(t, envelopes(true, true), "device-management"); c.memoryLimit != "512Mi" {
		t.Errorf("envelopes on: limit %q, want 512Mi", c.memoryLimit)
	}
	t.Run("an area limit of the operator's own is used as written", func(t *testing.T) {
		t.Parallel()
		vals := map[string]interface{}{"functionalAreas": map[string]interface{}{"device-management": map[string]interface{}{
			"resources": map[string]interface{}{"limits": map[string]interface{}{"memory": "256Mi"}}}}}
		c := areaOf(t, vals, "device-management")
		if c.memoryLimit != "256Mi" || mib(t, c.goMemLimit) != 192 {
			t.Errorf("limit %q GOMEMLIMIT %q, want the operator's 256Mi and 192MiB", c.memoryLimit, c.goMemLimit)
		}
	})
	t.Run("a larger top-level limit is used as written", func(t *testing.T) {
		t.Parallel()
		vals := map[string]interface{}{"resources": map[string]interface{}{
			"requests": map[string]interface{}{"cpu": "100m", "memory": "128Mi"},
			"limits":   map[string]interface{}{"cpu": "500m", "memory": "1Gi"},
		}}
		c := areaOf(t, vals, "device-management")
		if c.memoryLimit != "1Gi" || mib(t, c.goMemLimit) != 768 {
			t.Errorf("limit %q GOMEMLIMIT %q, want 1Gi and 768MiB", c.memoryLimit, c.goMemLimit)
		}
	})
}

// 🔴 dcctl INSTALLS WITH instance.existingSecret, AND THE CHART CANNOT READ THE DOCUMENT IT
// MOUNTS. Under it instance.config is the chart's defaults, so a floor keyed on the envelope
// switches in instance.config would never fire for a dcctl instance: the first version of this
// fix passed every inline-config test and left a dcctl instance with envelopes on at 384Mi.
// These render the real composed install values, as dcctl applies them.
func TestEventProcessingEnvelopeFloorReachesADcctlInstall(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		on        bool
		wantLimit string
	}{
		{"envelopes off in the document", false, "384Mi"},
		{"envelopes on in the document", true, "768Mi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := composeStateForTest()
			ch := chartForTest(t)
			authoring := helmValues(st)
			inst, _ := authoring["instance"].(map[string]interface{})
			cfg, _ := inst["config"].(map[string]interface{})
			infra, _ := cfg["infrastructure"].(map[string]interface{})
			if infra == nil {
				t.Fatal("the authoring values carry no instance.config.infrastructure: nothing below would set the switch")
			}
			messaging, _ := infra["messaging"].(map[string]interface{})
			if messaging == nil {
				messaging = map[string]interface{}{}
				infra["messaging"] = messaging
			}
			messaging["envelopes"] = map[string]interface{}{"resolved": tc.on}

			doc, err := composeInstanceConfig(t.Context(), ch, authoring)
			if err != nil {
				t.Fatalf("composing: %v", err)
			}
			install, err := installValuesFor(authoring, st.Instance, doc)
			if err != nil {
				t.Fatalf("install values: %v", err)
			}
			if got := install["eventEnvelopesEnabled"]; got != tc.on {
				t.Errorf("eventEnvelopesEnabled = %v, want %v", got, tc.on)
			}
			manifest, err := renderChartClientSide(t.Context(), ch, install)
			if err != nil {
				t.Fatalf("rendering the install values: %v", err)
			}
			ep := byArea(t, containersOf(t, manifest))["event-processing"]
			if ep.memoryLimit != tc.wantLimit {
				t.Errorf("event-processing limit %q, want %s", ep.memoryLimit, tc.wantLimit)
			}
		})
	}
}
