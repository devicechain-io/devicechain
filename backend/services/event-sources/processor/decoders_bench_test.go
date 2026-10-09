// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// measurementsBody builds a device-shaped measurements envelope with n readings in one entry.
func measurementsBody(n int) []byte {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"metric%02d":"%d.5"`, i, 20+i)
	}
	return []byte(`{"device":"dev-0001","eventType":"Measurement","occurredTime":"2026-10-01T12:00:00Z",` +
		`"payload":{"entries":[{"occurredTime":"2026-10-01T12:00:00Z","measurements":{` + sb.String() + `}}]}}`)
}

// BenchmarkJsonDecode measures JsonDecoder.Decode on a well-formed measurements event.
func BenchmarkJsonDecode(b *testing.B) {
	jd := NewJsonDecoder(nil)
	receivedAt := time.Date(2026, 10, 1, 12, 0, 1, 0, time.UTC)
	for _, n := range []int{1, 10} {
		body := measurementsBody(n)
		b.Run(fmt.Sprintf("metrics=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				if _, _, err := jd.Decode(body, receivedAt); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
