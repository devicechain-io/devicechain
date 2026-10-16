// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"slices"
	"testing"
)

// Turning route compression on or off restarts every broker server, so the upgrade's
// plan says which mode it moves to rather than only that a path changed. The values
// next to it in the cluster block (the TLS secret name, the CA) stay named by path.
func TestUpgradePlanShowsRouteCompressionMode(t *testing.T) {
	before := map[string]interface{}{"config": map[string]interface{}{"cluster": map[string]interface{}{
		"enabled": true,
		"tls":     map[string]interface{}{"secretName": "dc-nats-tls"},
	}}}
	after := map[string]interface{}{"config": map[string]interface{}{"cluster": map[string]interface{}{
		"enabled": true,
		"merge":   map[string]interface{}{"compression": "s2_fast"},
		"tls":     map[string]interface{}{"secretName": "dc-nats-tls-2"},
	}}}

	on := diffValues(before, after)
	want := []string{
		"config.cluster.merge.compression (unset) -> s2_fast",
		"config.cluster.tls.secretName (changed)",
	}
	if !slices.Equal(on, want) {
		t.Errorf("turning compression on:\n got %q\nwant %q", on, want)
	}

	off := diffValues(after, before)
	if !slices.Contains(off, "config.cluster.merge.compression s2_fast -> (unset)") {
		t.Errorf("turning compression off must show the mode it leaves; got %q", off)
	}
}
