// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/stretchr/testify/require"
)

func TestAlarmEventPositionVerdictIsStoredAndSticky(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedEscalatingPolicy(t, api, ctx)
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}})
	n.api, n.store = api, &fakeSecretStore{}
	at := time.Now().UTC()
	for i, flag := range []string{"true", "false", "null"} {
		e := cycleEvent(dmmodel.AlarmEventRaised, at, at.Add(time.Duration(i)*time.Second))
		require.NoError(t, json.Unmarshal([]byte(`{"PositionDerived":`+flag+`}`), e))
		wire, err := dmproto.MarshalAlarmStateChangeEvent(e)
		require.NoError(t, err)
		e, err = dmproto.UnmarshalAlarmStateChangeEvent(wire)
		require.NoError(t, err)
		require.NoError(t, n.Notify(ctx, e))
		rows, err := api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		encoded, err := json.Marshal(rows[0])
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(encoded, &fields))
		require.Equal(t, "true", string(fields["PositionDerived"]))
	}
}
