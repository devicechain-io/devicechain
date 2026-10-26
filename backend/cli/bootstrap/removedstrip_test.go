// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

func previousWithBodyCredential(v string) map[string]interface{} {
	return map[string]interface{}{
		"functionalAreas": map[string]interface{}{
			"event-sources": map[string]interface{}{
				"config": map[string]interface{}{
					"capture": map[string]interface{}{"bodyCredential": v},
				},
			},
		},
	}
}

// An upgrade over a release that was bridging with strip says, once, that such events are now
// refused and names the metric; over a release that was not, it says nothing.
func TestAnUpgradeOverAStripReleasePrintsOneRefusalNotice(t *testing.T) {
	st := compactState(false)
	st.Evolving = true

	var err error
	out := captureStdout(t, func() {
		_, _, err = releaseValues(t.Context(), chartForTest(t), st, previousWithBodyCredential("strip"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "REFUSED"); n != 1 {
		t.Errorf("the notice was printed %d times, want 1: %q", n, out)
	}
	if !strings.Contains(out, "total_msg_credential_in_mqtt_body") {
		t.Errorf("the notice does not name the metric: %q", out)
	}

	for name, prev := range map[string]map[string]interface{}{
		"refuse":   previousWithBodyCredential("refuse"),
		"absent":   {},
		"no-value": nil,
	} {
		out := captureStdout(t, func() {
			_, _, err = releaseValues(t.Context(), chartForTest(t), st, prev)
		})
		if err != nil {
			t.Fatal(err)
		}
		if out != "" {
			t.Errorf("[%s] printed a notice with no strip to report: %q", name, out)
		}
	}

	// A fresh install (not evolving) has no previous release to speak of.
	fresh := compactState(false)
	out = captureStdout(t, func() {
		_, _, err = releaseValues(t.Context(), chartForTest(t), fresh, previousWithBodyCredential("strip"))
	})
	if err != nil || out != "" {
		t.Errorf("a non-upgrade printed %q (err %v)", out, err)
	}
}
