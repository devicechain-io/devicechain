// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventenvelope_test

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/stretchr/testify/require"
)

// BenchmarkParse is the per-envelope cost the member benchmarks amortise.
func BenchmarkParse(b *testing.B) {
	for _, k := range []int{4, 16, 64} {
		b.Run(fmt.Sprintf("k%d", k), func(b *testing.B) {
			msg, err := eventenvelope.Encode(resolvedHeader(), corpusOf(b, k, loadtestResolved))
			require.NoError(b, err)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := eventenvelope.Parse(msg); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
