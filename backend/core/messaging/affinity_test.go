// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"
)

// The census decision: the actor is the lowest id among the durable's readers, and the target
// is the server a STRICT majority of them share.
func TestDecideMove(t *testing.T) {
	const dur = "d"
	r := func(id, server string, durables ...string) censusReply {
		return censusReply{ID: id, Server: server, Durables: durables}
	}
	cases := []struct {
		name    string
		replies []censusReply
		self    string
		target  string
		actor   bool
		ok      bool
	}{
		{"alone", []censusReply{r("a", "s1", dur)}, "a", "s1", true, true},
		{"majority, self lowest", []censusReply{r("a", "s1", dur), r("b", "s1", dur), r("c", "s2", dur)}, "a", "s1", true, true},
		{"majority, self not lowest", []censusReply{r("a", "s1", dur), r("b", "s1", dur), r("c", "s2", dur)}, "c", "s1", false, true},
		{"split two ways", []censusReply{r("a", "s1", dur), r("b", "s2", dur)}, "a", "", true, false},
		{"a replica reading another durable does not count", []censusReply{r("a", "s1", dur), r("b", "s2", "other")}, "a", "s1", true, true},
		{"self not a reader", []censusReply{r("a", "s1", "other")}, "a", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target, actor, ok := decideMove(c.replies, dur, c.self)
			if target != c.target || actor != c.actor || ok != c.ok {
				t.Fatalf("decideMove = (%q, %v, %v), want (%q, %v, %v)", target, actor, ok, c.target, c.actor, c.ok)
			}
		})
	}
}

// The headless-Service guess for a server the cluster does not advertise.
func TestHeadlessURL(t *testing.T) {
	cases := []struct{ server, host, want string }{
		{"dc-nats-2", "dc-nats.dc-infra", "nats://dc-nats-2.dc-nats-headless.dc-infra:4222"},
		{"dc-nats-0", "dc-nats.dc-infra.svc.cluster.local", "nats://dc-nats-0.dc-nats-headless.dc-infra.svc.cluster.local:4222"},
		{"dc-nats-1", "dc-nats", "nats://dc-nats-1.dc-nats-headless:4222"},
		{"other-1", "dc-nats.dc-infra", ""},
		{"dc-nats-1", "127.0.0.1", ""},
		{"dc-nats-1", "localhost", ""},
		{"", "dc-nats.dc-infra", ""},
	}
	for _, c := range cases {
		if got := headlessURL(c.server, c.host, 4222); got != c.want {
			t.Errorf("headlessURL(%q, %q) = %q, want %q", c.server, c.host, got, c.want)
		}
	}
}
