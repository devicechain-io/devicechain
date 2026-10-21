// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// The service wires a SessionKicker exactly when it runs the MQTT auth callout, and a revocation
// reaches the wire: disabling a credential through the service's own Api sends a session-kick
// request naming the tenant and the device token, and a revocation nobody answers is an error,
// not a quiet success.
//
// Negative control: delete the SessionKicker line from buildApis and the first subtest sees
// no request; make it unconditional and the "no callout" subtest fails.
func TestTheServiceWiresSessionKicksOnlyWhereTheMqttCalloutRuns(t *testing.T) {
	host, port := startEmbeddedNats(t)
	instance := fmt.Sprintf("kickwiring%d", time.Now().UnixNano())

	build := func(t *testing.T, seed string) (*model.Api, *nats.Conn) {
		t.Helper()
		db := putest.NewSQLiteDB(t, credentialTables()...)
		nmgr := startNatsManager(t, host, port, instance)
		nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Auth = mscfg.NatsAuthConfiguration{CalloutIssuerPublic: seed}
		api, _, err := buildApis(nmgr, &rdb.RdbManager{Database: db}, dmtest.DeviceSecretKey(), config.NewDeviceManagementConfiguration())
		require.NoError(t, err)
		return api, nmgr.Conn()
	}

	t.Run("a revocation sends a kick and reports the answer", func(t *testing.T) {
		api, nc := build(t, "a-seed")
		ctx := seedBasicCredential(t, api)

		var mu sync.Mutex
		var got []messaging.SessionKickRequest
		reply := messaging.SessionKickReply{OK: true}
		sub, err := messaging.QueueSubscribeSynced(nc, messaging.SessionKickSubject(instance), messaging.SessionKickQueue,
			func(m *nats.Msg) {
				var req messaging.SessionKickRequest
				require.NoError(t, json.Unmarshal(m.Data, &req))
				mu.Lock()
				got = append(got, req)
				r := reply
				mu.Unlock()
				body, _ := json.Marshal(r)
				_ = m.Respond(body)
			})
		require.NoError(t, err)
		t.Cleanup(func() { _ = sub.Unsubscribe() })

		disable(t, ctx, api)
		mu.Lock()
		require.Equal(t, []messaging.SessionKickRequest{{Tenant: "acme", Reason: messaging.SessionKickCredentialChanged, Tokens: []string{"dev"}}}, got)
		reply = messaging.SessionKickReply{OK: false, Error: "only 1 of 2 broker servers answered"}
		mu.Unlock()

		// An answer that is not a confirmation is an error from the operation, however the
		// database already stands.
		api.SessionKicker = fastRetries{api.SessionKicker}
		_, err = api.UpdateDeviceCredential(ctx, "c-1", &model.DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.ErrorIs(t, err, model.ErrSessionsNotEnded)
		require.Contains(t, err.Error(), "only 1 of 2 broker servers answered")
	})

	t.Run("nobody answering is an error, not a success", func(t *testing.T) {
		api, _ := build(t, "a-seed")
		ctx := seedBasicCredential(t, api)
		api.SessionKicker = fastRetries{api.SessionKicker}
		_, err := api.UpdateDeviceCredential(ctx, "c-1", &model.DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.ErrorIs(t, err, model.ErrSessionsNotEnded)
		require.Contains(t, err.Error(), "no service is answering session-kick requests")
	})

	t.Run("no callout, no kicker, no error", func(t *testing.T) {
		api, _ := build(t, "")
		require.Nil(t, api.SessionKicker)
		ctx := seedBasicCredential(t, api)
		disable(t, ctx, api)
	})
}

// fastRetries shortens the requester's waits so a failing kick does not take its production
// retry schedule inside a test.
type fastRetries struct{ model.SessionKicker }

func (f fastRetries) KickDeviceSessions(ctx context.Context, tenant, reason string, tokens []string) error {
	k := f.SessionKicker.(natsSessionKicker)
	r := messaging.NewSessionKickRequester(k.nmgr.Conn(), k.instanceId)
	r.Attempts, r.Backoff, r.AttemptTimeout = 2, 10*time.Millisecond, 2*time.Second
	return r.KickDeviceSessions(ctx, tenant, reason, tokens)
}
