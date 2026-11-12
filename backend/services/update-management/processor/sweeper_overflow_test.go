// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The config refuses such a value before it gets here and panics the sweeper.
func TestAnOverflowingSweepIntervalPanicsTheSweeper(t *testing.T) {
	seconds := int64(9223372037)
	interval := time.Duration(seconds) * time.Second
	require.Negative(t, interval)
	require.PanicsWithValue(t, "processor.NewDeadlineSweeper: interval must be positive", func() {
		NewDeadlineSweeper(nil, interval, nil)
	})
}
