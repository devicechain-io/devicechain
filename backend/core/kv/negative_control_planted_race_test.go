// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package kv

import (
	"sync"
	"testing"
)

// TEMPORARY. This file is the adoption negative control for the race detector
// step added in this pull request, and the next commit removes it.
//
// It is a genuine data race: four goroutines read-modify-write the same int
// with no synchronisation. It passes the plain `go test` step, because an
// unsynchronised counter produces a wrong total rather than a failure, and it
// must fail the `-race` step with a DATA RACE report naming this file. A race
// detector nobody has watched go red is not evidence of anything.
func TestPlantedRaceForNegativeControl(t *testing.T) {
	var wg sync.WaitGroup
	n := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5000; j++ {
				n++
			}
		}()
	}
	wg.Wait()
	_ = n
}
