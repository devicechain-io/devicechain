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

// BenchmarkJsonDecodePaths compares Decode with the previous implementation (referenceDecode, see
// decoders_equivalence_test.go) on the three shapes that matter: an event the single-pass decode
// takes, one it declines up front (a non-ASCII reading name), and one it rejects after doing work
// (an unparseable entry time). The last two are the slow paths; they must not be slower than before.
func BenchmarkJsonDecodePaths(b *testing.B) {
	jd := NewJsonDecoder(nil)
	receivedAt := time.Date(2026, 10, 1, 12, 0, 1, 0, time.UTC)
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"metric%02d":"%d.5"`, i, 20+i)
	}
	wrap := func(entryTime, readings string) []byte {
		return []byte(`{"device":"dev-0001","eventType":"Measurement","payload":{"entries":[{"occurredTime":"` +
			entryTime + `","measurements":{` + readings + `}}]}}`)
	}
	shapes := []struct {
		name string
		body []byte
		ok   bool
	}{
		{"accepted20", wrap("2026-10-01T12:00:00Z", sb.String()), true},
		{"nonascii", wrap("2026-10-01T12:00:00Z", sb.String()+`,"température":"1"`), true},
		{"rejected", wrap("not-a-time", sb.String()), false},
	}
	impls := []struct {
		name string
		fn   func([]byte, time.Time) (interface{}, error)
	}{
		{"reference", func(p []byte, t time.Time) (interface{}, error) {
			_, v, err := referenceDecode(jd, p, t)
			return v, err
		}},
		{"decode", func(p []byte, t time.Time) (interface{}, error) { _, v, err := jd.Decode(p, t); return v, err }},
	}
	for _, s := range shapes {
		for _, impl := range impls {
			b.Run(s.name+"/"+impl.name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := impl.fn(s.body, receivedAt); (err == nil) != s.ok {
						b.Fatalf("ok=%v err=%v", s.ok, err)
					}
				}
			})
		}
	}
}
