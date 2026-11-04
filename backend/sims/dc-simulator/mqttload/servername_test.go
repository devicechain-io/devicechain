// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package mqttload

import "testing"

// A broker reached by address presents the instance host recorded in the handshake as
// its SNI; with none set the URL's host is used, as before.
func TestTLSConfigPresentsTheConfiguredServerName(t *testing.T) {
	f := &Fleet{broker: "ssl://34.1.2.3:1883", cfg: Config{TLSServerName: "devicechain.local"}}
	if got := f.tlsConfig("").ServerName; got != "devicechain.local" {
		t.Errorf("SNI = %q, want devicechain.local", got)
	}
	f = &Fleet{broker: "ssl://34.1.2.3:1883"}
	if got := f.tlsConfig("").ServerName; got != "34.1.2.3" {
		t.Errorf("control: SNI = %q, want the URL host", got)
	}
}
