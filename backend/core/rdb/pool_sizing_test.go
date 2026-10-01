// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The policy as values, including the rows database/sql would hide: it clamps an idle
// count above the open count itself, so only the policy's own answer shows whether the
// clamp is ours.
func TestPoolSizing(t *testing.T) {
	for _, tc := range []struct {
		cfgOpen, cfgIdle int
		open, idle       int
	}{
		{0, 0, 20, 20},     // nothing configured: the default pool, all of it kept
		{30, 0, 30, 30},    // a configured pool, all of it kept
		{1, 0, 1, 1},       // the smallest pool
		{12, 4, 12, 4},     // an explicit idle count is honoured
		{12, 40, 12, 12},   // ...up to the open count
		{-3, -1, 20, 20},   // negative is unset, never passed to database/sql
		{0, 25, 20, 20},    // an idle count above the DEFAULT open count is clamped too
		{20, 20, 20, 20},   // explicit and equal
		{48, 0, 48, 48},    // a raised pool keeps all of it
		{5, -2, 5, 5},      // negative idle on a configured pool
		{100, 99, 100, 99}, // explicit, just below
	} {
		t.Run(fmt.Sprintf("open=%d,idle=%d", tc.cfgOpen, tc.cfgIdle), func(t *testing.T) {
			open, idle := poolSizing(tc.cfgOpen, tc.cfgIdle)
			assert.Equal(t, [2]int{tc.open, tc.idle}, [2]int{open, idle})
		})
	}
}
