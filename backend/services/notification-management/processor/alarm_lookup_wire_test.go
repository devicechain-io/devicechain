// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/devicechain-io/dc-notification-management/model"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

func TestAlarmLifecycleLookupDocumentMatchesDeviceManagement(t *testing.T) {
	sdl, err := os.ReadFile("../../device-management/graphql/schema.graphql")
	require.NoError(t, err)
	schema, err := gql.ParseSchema(string(sdl), nil, gql.UseFieldResolvers())
	require.NoError(t, err)
	for _, err := range schema.Validate(alarmLifecyclesDocument) {
		if err.Rule != "VariablesOfCorrectType" {
			t.Errorf("document rejected: %v", err)
		}
	}
}

func TestOrphanSweepLifecycleWire(t *testing.T) {
	for _, mode := range []string{"active", "missing", "old-replica", "partial-error", "invalid-state", "missing-result"} {
		t.Run(mode, func(t *testing.T) {
			n, _, api, ctx := pagedThenDue(t, namedAlarm())
			now := time.Now().UTC().Truncate(time.Microsecond)
			old := now.Add(-2 * OrphanAlarmHorizon)
			require.NoError(t, api.RDB.DB(ctx).Model(&model.NotificationState{}).Where("1 = 1").Updates(map[string]any{"created_at": old, "cycle_raised_at": old}).Error)
			states, err := api.OpenNotificationStates(ctx)
			require.NoError(t, err)
			require.Len(t, states, 1)
			token := states[0].AlarmToken
			mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "service-token", ExpiresAt: now.Add(time.Hour).Unix()})
			}))
			defer mint.Close()
			host, port, err := net.SplitHostPort(mint.Listener.Addr().String())
			require.NoError(t, err)
			portNumber, err := strconv.Atoi(port)
			require.NoError(t, err)
			var request struct {
				Query     string
				Variables map[string]any
			}
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				switch mode {
				case "old-replica":
					_, _ = w.Write([]byte(`{"errors":[{"message":"Cannot query field alarmLifecycles on type Query"}]}`))
				case "partial-error":
					_, _ = w.Write([]byte(`{"data":{"alarmLifecycles":[]},"errors":[{"message":"read failed"}]}`))
				case "missing-result":
					_, _ = w.Write([]byte(`{"data":{}}`))
				default:
					rows := []map[string]string{}
					if mode != "missing" {
						state := "ACTIVE"
						if mode == "invalid-state" {
							state = "UNKNOWN"
						}
						rows = append(rows, map[string]string{"token": token, "state": state})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"alarmLifecycles": rows}})
				}
			}))
			defer peer.Close()
			client := svcclient.New(config.UserManagementConfiguration{Hostname: host, InternalPort: uint32(portNumber)}, "secret", "notification-management", []string{"alarm:read"})
			n.AlarmLookup = NewAlarmLookup(client, peer.URL)
			err = n.SweepOrphans(ctx, now)
			if mode == "active" || mode == "missing" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, `query($tokens: [String!]!) { alarmLifecycles(tokens: $tokens) { token state } }`, request.Query)
			require.Equal(t, map[string]any{"tokens": []any{token}}, request.Variables)
			var stored model.NotificationState
			require.NoError(t, api.RDB.DB(ctx).First(&stored, states[0].ID).Error)
			require.Equal(t, mode == "missing", stored.ClearedAt.Valid)
		})
	}
}
