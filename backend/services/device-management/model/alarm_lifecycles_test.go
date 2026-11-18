// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAlarmLifecyclesPreserveWithholdingAndTenant(t *testing.T) {
	api := newWithholdingTestApi(t)
	raiseWithPosition(t, api, "fence", "p/fence", 10, verdict(true))
	raiseWithPosition(t, api, "pending", "p/pending", 10, nil)
	locator := caller(auth.AlarmRead, auth.LocationRead)
	fence := loadAlarm(t, api, locator, "fence")
	pending := loadAlarm(t, api, locator, "pending")
	require.NotNil(t, fence)
	require.NotNil(t, pending)
	require.Nil(t, pending.PositionDerived)
	otherCtx := core.WithTenant(context.Background(), "other")
	raiseEdge(t, api, otherCtx, "other", "p/temp", "MAJOR", 10)
	other := loadAlarm(t, api, otherCtx, "other")
	require.NotNil(t, other)
	ctx := auth.WithClaims(core.WithTenant(context.Background(), "t1"),
		&auth.Claims{TokenType: auth.TokenTypeService, Tenant: "t1", Authorities: []string{"alarm:read"}})
	tokens := []string{fence.Token, pending.Token, other.Token, "missing"}
	// Load-bearing for other alarm:read-only service clients: ordinary reads still withhold.
	hidden, err := api.AlarmsByToken(ctx, tokens)
	require.NoError(t, err)
	require.Empty(t, hidden)
	found, err := api.AlarmLifecycles(ctx, tokens)
	require.NoError(t, err)
	require.ElementsMatch(t, []AlarmLifecycle{{Token: fence.Token, State: "ACTIVE"}, {Token: pending.Token, State: "ACTIVE"}}, found)
	require.NoError(t, api.RDB.DB(core.WithTenant(context.Background(), "t1")).Model(&Alarm{}).Where("token = ?", fence.Token).Update("state", "CLEARED").Error)
	found, err = api.AlarmLifecycles(ctx, []string{fence.Token})
	require.NoError(t, err)
	require.Equal(t, []AlarmLifecycle{{Token: fence.Token, State: "CLEARED"}}, found)
	found, err = api.AlarmLifecycles(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, found)
	_, err = api.AlarmLifecycles(ctx, make([]string, rdb.MaxLookupKeys+1))
	require.Error(t, err)
	_, err = api.AlarmLifecycles(context.Background(), []string{fence.Token})
	require.ErrorIs(t, err, core.ErrNoTenant)
}
