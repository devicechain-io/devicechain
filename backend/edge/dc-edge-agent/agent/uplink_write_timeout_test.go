// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"testing"
	"time"
)

// The uplink is the loaded publisher: at paho's default of no write timeout a write to a dead
// peer blocks for good and the ping timeout is never checked. The built options must bound it.
func TestUplinkOptionsBoundWritesAndStateKeepAlive(t *testing.T) {
	opts := testUplink(t).clientOptions()
	if opts.WriteTimeout != mqttWriteTimeout || opts.WriteTimeout <= 0 {
		t.Fatalf("WriteTimeout = %v, want %v", opts.WriteTimeout, mqttWriteTimeout)
	}
	if got := time.Duration(opts.KeepAlive) * time.Second; got != mqttKeepAlive {
		t.Fatalf("KeepAlive = %v, want %v", got, mqttKeepAlive)
	}
}
