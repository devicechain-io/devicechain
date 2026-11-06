// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// Concurrent resolvers of one document must share one compile. The window this guards is narrow
// (a caller that missed the cache, then found the leader's in-flight marker already cleared), so
// the test drives many callers at many distinct keys on an instance of its own, touching no
// package state.
func TestFigureLRUComputesOncePerKeyUnderContention(t *testing.T) {
	const keys, callers = 400, 8
	c := newFigureLRU(keys * 2)
	var computes atomic.Int32
	var wg sync.WaitGroup
	var wrong atomic.Int32
	for k := 0; k < keys; k++ {
		k := k
		key := [32]byte{byte(k), byte(k >> 8)}
		start := make(chan struct{})
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				want := int32(k) + 1
				got := c.figures(key, func() decoderFigures {
					computes.Add(1)
					runtime.Gosched()
					return decoderFigures{maxReadings: want}
				})
				if got.maxReadings != want {
					wrong.Add(1)
				}
			}()
		}
		close(start)
	}
	wg.Wait()
	if got := wrong.Load(); got != 0 {
		t.Fatalf("%d callers received figures other than their own key's", got)
	}
	if got := computes.Load(); got != keys {
		t.Fatalf("%d computes for %d distinct keys: a document was compiled more than once", got, keys)
	}
}
