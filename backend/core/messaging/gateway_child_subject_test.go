// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import "testing"

func TestParseGatewayChildSubject(t *testing.T) {
	cases := []struct {
		name, subject, leaf string
		gateway, child      string
		ok                  bool
	}{
		{"commands leaf", "i.t.gateways.gw.devices.c1.commands", SegmentCommands, "gw", "c1", true},
		{"responses leaf", "i.t.gateways.gw.devices.c1.responses", SegmentResponses, "gw", "c1", true},
		{"wrong leaf", "i.t.gateways.gw.devices.c1.events", SegmentCommands, "", "", false},
		{"own-device shape", "i.t.devices.c1.events", SegmentEvents, "", "", false},
		{"notices", "i.t.gateways.gw.notices", SegmentNotices, "", "", false},
		{"empty child", "i.t.gateways.gw.devices..commands", SegmentCommands, "", "", false},
		{"empty gateway", "i.t.gateways..devices.c1.commands", SegmentCommands, "", "", false},
		{"missing devices segment", "i.t.gateways.gw.xxxxxxx.c1.commands", SegmentCommands, "", "", false},
		{"missing gateways segment", "i.t.xxxxxxxx.gw.devices.c1.commands", SegmentCommands, "", "", false},
		{"too long", "i.t.gateways.gw.devices.c1.commands.x", SegmentCommands, "", "", false},
	}
	for _, c := range cases {
		g, d, ok := ParseGatewayChildSubject(c.subject, c.leaf)
		if g != c.gateway || d != c.child || ok != c.ok {
			t.Errorf("%s: ParseGatewayChildSubject(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
				c.name, c.subject, c.leaf, g, d, ok, c.gateway, c.child, c.ok)
		}
	}
}
