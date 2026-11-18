// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/stretchr/testify/require"
)

func TestAlarmPositionVerdictRoundTrip(t *testing.T) {
	for _, verdict := range []string{"true", "false", "null"} {
		t.Run(verdict, func(t *testing.T) {
			e := &model.AlarmStateChangeEvent{OccurredTime: time.Now().UTC(), RaisedTime: time.Now().UTC()}
			require.NoError(t, json.Unmarshal([]byte(`{"PositionDerived":`+verdict+`}`), e))
			wire, err := MarshalAlarmStateChangeEvent(e)
			require.NoError(t, err)
			got, err := UnmarshalAlarmStateChangeEvent(wire)
			require.NoError(t, err)
			encoded, err := json.Marshal(got)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(encoded, &fields))
			require.Equal(t, verdict, string(fields["PositionDerived"]))
		})
	}
}
