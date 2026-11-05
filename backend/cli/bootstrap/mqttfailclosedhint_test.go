// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

// The closing hint prints for an instance that never chose and one that chose off, and is
// silent when fail-closed is on. Driven through the real bootstrap report as well as the
// helper the upgrade's closing also calls.
func TestTheMqttFailClosedHintPrintsUnlessItIsOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  *bool
		want bool
	}{
		{"never chosen", nil, true},
		{"chosen off", boolPtr(false), true},
		{"on", boolPtr(true), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := aWritableState()
			st.MqttFailClosed = tc.val
			for name, run := range map[string]func(){
				"helper": func() { sayMqttFailClosedHint(st) },
				"report": func() {
					st.Values = map[string]string{"ingressHost": "localhost", "scheme": "http"}
					if err := stepReport(t.Context(), st); err != nil {
						t.Fatal(err)
					}
				},
			} {
				out := captureStdout(t, run)
				if got := strings.Contains(out, "--mqtt-fail-closed prevents it"); got != tc.want {
					t.Errorf("%s: hint printed = %v, want %v:\n%s", name, got, tc.want, out)
				}
				if tc.want && !strings.Contains(out, "https://docs.devicechain.io/concepts/security-model#accepted-gaps") {
					t.Errorf("%s: the hint carries no docs link:\n%s", name, out)
				}
			}
		})
	}
}

// A rehearsal installs nothing, so the report says nothing about the finished instance.
func TestTheMqttFailClosedHintIsSilentOnADryRun(t *testing.T) {
	st := aWritableState()
	st.DryRun = true
	st.Values = map[string]string{"ingressHost": "localhost", "scheme": "http"}
	out := captureStdout(t, func() {
		if err := stepReport(t.Context(), st); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "--mqtt-fail-closed prevents it") {
		t.Errorf("a dry run printed the hint:\n%s", out)
	}
}
