// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package streams

import (
	"slices"
	"testing"
)

func TestForwardersInto(t *testing.T) {
	for suffix, area := range map[string]string{InboundEvents: "event-sources", ResolvedEvents: "device-management"} {
		if !slices.Contains(ForwardersInto(suffix), area) {
			t.Errorf("ForwardersInto(%q) does not contain %q", suffix, area)
		}
	}
	if got := ForwardersInto("unknown"); len(got) != 0 {
		t.Errorf("unknown suffix forwarders = %v", got)
	}
}
