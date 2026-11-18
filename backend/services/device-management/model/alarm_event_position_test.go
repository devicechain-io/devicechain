// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAlarmEventCopiesPositionVerdict(t *testing.T) {
	yes, no := true, false
	for _, verdict := range []*bool{&yes, &no, nil} {
		e := newAlarmStateChangeEvent(&Alarm{PositionDerived: verdict}, AlarmEventCleared, "", time.Now())
		require.Equal(t, verdict, e.PositionDerived)
	}
}
