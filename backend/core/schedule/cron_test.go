// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schedule

import (
	"math"
	"strconv"
	"testing"
)

func TestCronFieldSteps(t *testing.T) {
	for _, bounds := range []struct {
		field string
		lo    int
		hi    int
	}{
		{"*", 0, 59},
		{"40-59", 40, 59},
		{"5", 5, 59},
	} {
		for _, step := range []int{1, 15, 61, math.MaxInt32, math.MaxInt - 4, math.MaxInt} {
			field := bounds.field + "/" + strconv.Itoa(step)
			t.Run(field, func(t *testing.T) {
				var want, allowed uint64
				// Scan the bounded domain independently of the parser's step loop.
				for v := bounds.lo; v <= bounds.hi; v++ {
					allowed |= uint64(1) << uint(v)
					if (v-bounds.lo)%step == 0 {
						want |= uint64(1) << uint(v)
					}
				}
				check := func(t *testing.T, got uint64) {
					t.Helper()
					if got != want {
						t.Errorf("bitset = %#x, want %#x", got, want)
					}
					if outside := got &^ allowed; outside != 0 {
						t.Errorf("bits outside [%d, %d]: %#x", bounds.lo, bounds.hi, outside)
					}
				}
				t.Run("parseField", func(t *testing.T) {
					got, err := parseField(field, cronFields[0])
					if err != nil {
						t.Fatal(err)
					}
					check(t, got)
				})
				t.Run("parseCron", func(t *testing.T) {
					got, err := parseCron(field + " * * * *")
					if err != nil {
						t.Fatal(err)
					}
					check(t, got.minute)
				})
			})
		}
	}
}
