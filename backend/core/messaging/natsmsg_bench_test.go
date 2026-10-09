// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	"github.com/nats-io/nats.go"
)

// BenchmarkNatsMsgHeaders measures building the wire message for a publish that carries no
// producer-propagated correlation id (the generated case: every message an ingest edge
// originates) and for one that does, with and without a dedup id. It reports hdr-B/msg, the
// encoded header block as it goes on the wire and is stored (and replicated) by the stream,
// alongside ns/op and allocs/op for the construction.
//
// Run with: go test ./messaging -run '^$' -bench BenchmarkNatsMsgHeaders -benchmem -count=6
func BenchmarkNatsMsgHeaders(b *testing.B) {
	const dedup = "measurement:tenant-0001:device-000123:1760000000000000000:0"
	cases := []struct {
		name  string
		msg   Message
		dedup bool
	}{
		{"generated-cid", Message{Value: []byte("x")}, false},
		{"generated-cid/dedup", Message{Value: []byte("x"), DedupID: dedup}, true},
		{"propagated-cid", Message{Value: []byte("x")}.WithCorrelationID("0123456789abcdefABCDEF"), false},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			var nm *nats.Msg
			for i := 0; i < b.N; i++ {
				nm = natsMsg("dc.i.t.s", c.msg)
			}
			b.StopTimer()
			// The NATS/1.0 header block: status line, then one "Key: Value" line per entry, each
			// ended CRLF, then a blank line.
			const crlf = "\r\n"
			size := len("NATS/1.0"+crlf) + len(crlf)
			for k, vs := range nm.Header {
				for _, v := range vs {
					size += len(k) + len(": ") + len(v) + len(crlf)
				}
			}
			b.ReportMetric(float64(size), "hdr-B/msg")
		})
	}
}
