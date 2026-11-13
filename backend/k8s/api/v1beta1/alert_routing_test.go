// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"reflect"
	"testing"
)

func TestAlertRoutingRoundTrips(t *testing.T) {
	for _, c := range []struct {
		in   AlertRouting
		want string
	}{
		{AlertRouting{}, "none"},
		{AlertRouting{Watchdog: true}, "none,watchdog"},
		{AlertRouting{Receivers: []string{AlertReceiverWebhook}}, "webhook"},
		{AlertRouting{Receivers: []string{AlertReceiverEmail, AlertReceiverWebhook}, Watchdog: true}, "email,webhook,watchdog"},
		{AlertRouting{Receivers: []string{AlertReceiverConfig, AlertReceiverConfig}}, "config"},
	} {
		got := c.in.Format()
		if got != c.want {
			t.Errorf("Format(%+v) = %q, want %q", c.in, got, c.want)
		}
		back, err := ParseAlertRouting(got)
		if err != nil {
			t.Fatalf("ParseAlertRouting(%q): %v", got, err)
		}
		if back.Routed() != c.in.Routed() || back.Watchdog != c.in.Watchdog {
			t.Errorf("round trip of %q lost information: %+v", got, back)
		}
	}
}

func TestParseAlertRoutingRefusesWhatItDoesNotKnow(t *testing.T) {
	for _, bad := range []string{"", "none,webhook", "slack", "webhook,", "watchdog", ",", "None"} {
		if got, err := ParseAlertRouting(bad); err == nil {
			t.Errorf("ParseAlertRouting(%q) = %+v, want an error: an unknown value must never read as routed", bad, got)
		}
	}
}

func TestRoutedOnlyWithAReceiver(t *testing.T) {
	r, err := ParseAlertRouting("none,watchdog")
	if err != nil {
		t.Fatal(err)
	}
	if r.Routed() {
		t.Error("a watchdog alone must not count as routing alerts to a human")
	}
	r, _ = ParseAlertRouting("email,webhook")
	if !r.Routed() || !reflect.DeepEqual(r.Receivers, []string{"email", "webhook"}) {
		t.Errorf("got %+v", r)
	}
}
