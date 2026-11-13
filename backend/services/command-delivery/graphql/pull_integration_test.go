// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The HTTP pull channel on a real PostgreSQL, through the real migration chain and the real
// schema. SQLite (the unit suites) is one connection, so it cannot race two pulls and it never runs
// the migration that adds the columns; both of those are this file's job.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./graphql/ -run Pull -v
package graphql

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

type pullSet map[string]bool

func (p pullSet) PullDevices(_ context.Context, tokens []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, t := range tokens {
		if p[t] {
			out[t] = true
		}
	}
	return out, nil
}

func asMachine(ctx context.Context) context.Context {
	return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeService,
		Authorities: []string{string(auth.CommandClaim), string(auth.CommandRead), string(auth.CommandWrite)}})
}

func pullFixture(t *testing.T) (*model.Api, context.Context) {
	t.Helper()
	api, ctx := scopeFixture(t)
	api.PullChannels = pullSet{"acme-1": true}
	return api, ctx
}

func mustEnqueue(t *testing.T, api *model.Api, ctx context.Context, token string, mode *string, max *int32) {
	t.Helper()
	_, err := api.CreateCommand(ctx, &model.CommandCreateRequest{Token: token, DeviceToken: "acme-1", Name: "setRelay",
		DeliveryMode: mode, MaxAttempts: max})
	require.NoError(t, err)
}

func ptr[T any](v T) *T { return &v }

// TestPullColumnsExistAfterTheRealMigrationChain: the appended migration added every column, and a
// command round-trips them.
func TestPullColumnsExistAfterTheRealMigrationChain(t *testing.T) {
	api, ctx := pullFixture(t)
	mustEnqueue(t, api, ctx, "cols", ptr("ONEWAY"), ptr(int32(4)))
	got, err := api.CommandsByToken(ctx, []string{"cols"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "ONEWAY", got[0].DeliveryMode.String)
	require.EqualValues(t, 4, got[0].MaxAttempts.Int32)
	require.False(t, got[0].DeliveredTime.Valid)
}

// TestConcurrentPullsNeverHandOutACommandTwice is the claim's property on a real database: many
// pulls racing for one command are handed it exactly once between them.
func TestConcurrentPullsNeverHandOutACommandTwice(t *testing.T) {
	api, ctx := pullFixture(t)
	for i := 0; i < 5; i++ {
		mustEnqueue(t, api, ctx, "race-"+string(rune('a'+i)), nil, nil)
	}
	var mu sync.Mutex
	handed := map[string]int{}
	var wg sync.WaitGroup
	for p := 0; p < 12; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pulled, err := api.PullCommands(ctx, "acme-1", 0)
			require.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			for _, c := range pulled {
				handed[c.Command.Token]++
			}
		}()
	}
	wg.Wait()
	require.Len(t, handed, 5, "every command is handed out")
	for token, n := range handed {
		require.Equal(t, 1, n, "%s was handed out %d times", token, n)
	}
}

// TestTheAcceptanceExchangeThroughTheServedMutations: a device receives, acknowledges and
// responds, and deliveredTime is set; a planted dropped ack leaves it null and the outcome
// NOT_DELIVERED.
func TestTheAcceptanceExchangeThroughTheServedMutations(t *testing.T) {
	api, ctx := pullFixture(t)
	mctx := asMachine(ctx)
	mustEnqueue(t, api, ctx, "happy", nil, nil)
	mustEnqueue(t, api, ctx, "dropped", nil, ptr(int32(1)))

	res := run(mctx, `mutation { pullCommands(deviceToken: "acme-1") { token name attempt } }`, nil)
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"pullCommands":[{"token":"happy","name":"setRelay","attempt":1},{"token":"dropped","name":"setRelay","attempt":1}]}`, string(res.Data))

	res = run(mctx, `mutation { acknowledgeCommandDelivery(token: "happy", deviceToken: "acme-1") }`, nil)
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"acknowledgeCommandDelivery":"RECORDED"}`, string(res.Data))
	res = run(mctx, `mutation { respondToPulledCommand(token: "happy", deviceToken: "acme-1", success: true, payload: "on") }`, nil)
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"respondToPulledCommand":"RECORDED"}`, string(res.Data))

	// The planted defect: "dropped" is never acknowledged. Age its hand-out past the wait.
	require.NoError(t, api.RDB.DB(ctx).Model(&model.Command{}).Where("token = ?", "dropped").
		Update("sent_time", time.Now().Add(-2*model.PullAckWait)).Error)
	res = run(mctx, `mutation { pullCommands(deviceToken: "acme-1") { token } }`, nil)
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"pullCommands":[]}`, string(res.Data))

	read := run(mctx, `query { commandsByToken(tokens: ["happy","dropped"]) { token status outcome deliveredTime deliveryEvidence } }`, nil)
	require.Empty(t, read.Errors)
	got := map[string]map[string]any{}
	for _, c := range decodeCommands(t, read.Data) {
		got[c["token"].(string)] = c
	}
	require.Equal(t, "SUCCESSFUL", got["happy"]["status"])
	require.Equal(t, "SUCCEEDED", got["happy"]["outcome"])
	require.NotNil(t, got["happy"]["deliveredTime"], "deliveredTime is set for a device that received, acknowledged and responded")
	require.Equal(t, "DEVICE_ACK", got["happy"]["deliveryEvidence"])
	require.Equal(t, "FAILED", got["dropped"]["status"])
	require.Equal(t, "NOT_DELIVERED", got["dropped"]["outcome"])
	require.Nil(t, got["dropped"]["deliveredTime"], "a dropped ack leaves deliveredTime null")
}

// TestAnUnprivilegedCallerCannotPull: a tenant user token, even one holding every authority, is
// refused. The pull claims commands for a device, which is a machine's authority.
func TestAnUnprivilegedCallerCannotPull(t *testing.T) {
	api, ctx := pullFixture(t)
	mustEnqueue(t, api, ctx, "guarded", nil, nil)
	user := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{string(auth.AuthorityAll)}})
	res := run(user, `mutation { pullCommands(deviceToken: "acme-1") { token } }`, nil)
	require.NotEmpty(t, res.Errors)
	assertStatus(t, api, ctx, "guarded", "QUEUED")
}

func assertStatus(t *testing.T, api *model.Api, ctx context.Context, token, want string) {
	t.Helper()
	got, err := api.CommandsByToken(core.WithTenant(ctx, "A"), []string{token})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, want, got[0].Status)
}

func decodeCommands(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var wrapped struct {
		Commands []map[string]any `json:"commandsByToken"`
	}
	require.NoError(t, json.Unmarshal(data, &wrapped))
	return wrapped.Commands
}
