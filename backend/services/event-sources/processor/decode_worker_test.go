// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	"github.com/devicechain-io/dc-event-sources/model"
)

// The device segment in the events topic is broker-verified — a device may only
// publish to its own — so it must be checked against the device the PAYLOAD claims
// to be. Without this the segment is decorative: nothing else reads it, and under
// deviceAuthMode `optional`/`disabled` the body is trusted verbatim, so device A
// publishing on its own topic with a body naming device B is persisted as B's.
func TestCheckDeviceMatchesTransport(t *testing.T) {
	cases := []struct {
		name        string
		topicDevice string
		bodyDevice  string
		wantErr     bool
	}{
		{"agreeing identities pass", "sensor-001", "sensor-001", false},
		{"a body impersonating another device is rejected", "sensor-001", "sensor-002", true},
		{"a transport with no device identity is not checked", "", "sensor-002", false},
		{"a body with no device is left to the resolver", "sensor-001", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := rawMessage{tenant: "acme", device: tc.topicDevice}
			event := &model.UnresolvedEvent{Device: tc.bodyDevice}
			err := checkDeviceMatchesTransport(raw, event)
			if tc.wantErr && err == nil {
				t.Fatal("expected a mismatch to be rejected")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
		})
	}
}

// deviceFromTopic must match ONLY the documented events shape, so an unrelated topic
// does not yield a token that would then be compared against a body it has nothing
// to do with.
func TestDeviceFromTopic(t *testing.T) {
	cases := map[string]string{
		"inst-1/acme/devices/sensor-001/events": "sensor-001",
		"inst-1/acme/events":                    "",
		"inst-1/acme/devices/sensor-001":        "",
		"inst-1/acme/devices/sensor-001/other":  "",
		"inst-1/acme/other/sensor-001/events":   "",
	}
	for topic, want := range cases {
		if got := deviceFromTopic(topic); got != want {
			t.Errorf("deviceFromTopic(%q) = %q, want %q", topic, got, want)
		}
	}
}
