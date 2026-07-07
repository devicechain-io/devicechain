// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	nats "github.com/nats-io/nats.go"
)

// streamFillRatio takes the higher of the byte- and message-fill fractions, and
// treats a non-positive (unlimited) ceiling as 0 so an unbounded dimension never
// drives a near-full warning.
func TestStreamFillRatio(t *testing.T) {
	info := func(bytes uint64, maxBytes int64, msgs uint64, maxMsgs int64) *nats.StreamInfo {
		return &nats.StreamInfo{
			State:  nats.StreamState{Bytes: bytes, Msgs: msgs},
			Config: nats.StreamConfig{MaxBytes: maxBytes, MaxMsgs: maxMsgs},
		}
	}
	cases := []struct {
		name string
		in   *nats.StreamInfo
		want float64
	}{
		{"empty stream", info(0, 1<<30, 0, 5_000_000), 0},
		{"bytes dominate", info(900, 1000, 1, 1000), 0.9},
		{"msgs dominate", info(1, 1000, 800, 1000), 0.8},
		{"bytes unlimited falls back to msgs", info(1<<40, -1, 500, 1000), 0.5},
		{"both unlimited is zero", info(1<<40, 0, 1<<40, -1), 0},
		{"at ceiling", info(1000, 1000, 0, 1000), 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamFillRatio(tc.in); got != tc.want {
				t.Errorf("streamFillRatio = %v, want %v", got, tc.want)
			}
		})
	}
}
