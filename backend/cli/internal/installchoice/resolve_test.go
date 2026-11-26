// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installchoice

import (
	"reflect"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

func TestResolverPresenceAndPresets(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		c := Defaults()
		c.Build, c.Compact = true, true
		c.Given["build"], c.Given["compact"] = true, true
		c.Given["no-tls"], c.Given["no-monitoring"] = explicit, explicit
		c.Given["mqtt-gateway"], c.Given["relational-store-max-wal-mib"] = explicit, explicit
		p, err := c.Resolve("dev")
		if err != nil {
			t.Fatal(err)
		}
		if p.Options.NoTLS != !explicit || p.Options.NoMonitoring != !explicit {
			t.Fatal("compact lost explicit false")
		}
		if (p.Options.MQTTGateway != nil) != explicit || (p.Options.RelationalStoreMaxWALMiB != nil) != explicit {
			t.Fatal("false/zero was confused with omitted")
		}
		if !reflect.DeepEqual(c, p.Choices()) {
			t.Fatal("resolution changed original choices or presence")
		}
	}
	c := Defaults()
	c.Dev = true
	c.Given["dev"] = true
	p, err := c.Resolve("dev")
	if err != nil || !p.Options.BuildImages || !p.Options.AssumeYes {
		t.Fatalf("dev failed to imply build and yes: %v", err)
	}
}

func TestResolverRefusesContradictions(t *testing.T) {
	for _, c := range []Choices{
		{Dev: true, Given: Presence{"build": true}},
		{Dev: true, ImageVersion: "", Given: Presence{"version": true}},
		{Size: bootstrap.SizeCompact, Given: Presence{"compact": true}},
		{Compact: true, Size: bootstrap.SizeBase},
		{NoTLS: true, Given: Presence{"no-tls": true}},
		{MaxConnections: 50},
	} {
		if _, err := c.Resolve("dev"); err == nil {
			t.Fatal("contradictory choice accepted")
		}
	}
}
