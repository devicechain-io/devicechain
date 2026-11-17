// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-notification-management/model"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

func TestNotificationHistoryWithholdsPosition(t *testing.T) {
	ctx := newWireCtx(t)
	api := apiOf(t, ctx)
	db := api.RDB.DB(ctx)
	require.NoError(t, db.AutoMigrate(&model.NotificationAttempt{}))
	for token, flag := range map[string]string{"geo": "true", "plain": "false", "legacy": "null"} {
		row := &model.NotificationState{AlarmToken: token, AlarmKey: "k"}
		require.NoError(t, json.Unmarshal([]byte(`{"PositionDerived":`+flag+`}`), row))
		require.NoError(t, db.Create(row).Error)
		require.NoError(t, db.Create(&model.NotificationAttempt{AlarmToken: token}).Error)
	}
	// An attempt without a surviving state cannot be classified and is withheld too.
	require.NoError(t, db.Create(&model.NotificationAttempt{AlarmToken: "orphan"}).Error)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	query := `{
	 notificationStates(criteria: {pageNumber: 1, pageSize: 10}) { results { alarmToken } }
	 notificationStatesByAlarmToken(alarmTokens: ["geo", "plain", "legacy"]) { alarmToken }
	 notificationAttempts(criteria: {pageNumber: 1, pageSize: 10}) { results { alarmToken } }
	}`
	for _, location := range []bool{false, true} {
		authorities := []string{string(auth.NotificationRead)}
		if location {
			authorities = append(authorities, string(auth.LocationRead))
		}
		caller := auth.WithClaims(ctx, &auth.Claims{Authorities: authorities})
		res := schema.Exec(caller, query, "", nil)
		require.Empty(t, res.Errors)
		var data struct {
			NotificationStates             struct{ Results []struct{ AlarmToken string } }
			NotificationStatesByAlarmToken []struct{ AlarmToken string }
			NotificationAttempts           struct{ Results []struct{ AlarmToken string } }
		}
		require.NoError(t, json.Unmarshal(res.Data, &data))
		wantStates, wantAttempts := 1, 1
		if location {
			wantStates, wantAttempts = 3, 4
		}
		require.Len(t, data.NotificationStates.Results, wantStates, "location=%v: %s", location, res.Data)
		require.Len(t, data.NotificationStatesByAlarmToken, wantStates)
		require.Len(t, data.NotificationAttempts.Results, wantAttempts)
		if !location {
			require.Equal(t, "plain", data.NotificationStates.Results[0].AlarmToken)
			require.Equal(t, "plain", data.NotificationStatesByAlarmToken[0].AlarmToken)
			require.Equal(t, "plain", data.NotificationAttempts.Results[0].AlarmToken)
		}
	}
}
