// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The SQL the disabled/window seam fixes added, run on a real PostgreSQL through the real
// migration chain. Every other test of it runs on in-memory sqlite, which accepts a row-value
// `(tenant_id, device_token) NOT IN (subquery)`, a negated window predicate and
// `UPDATE … RETURNING` with its own dialect rules — so a green sqlite suite says nothing about
// whether the deployed server parses, plans and answers them the same way.
//
// Tagged `integration` so it stays out of the default `go test ./...`. hack/integration-tests.sh
// runs it against a throwaway server, as it does every module's tagged suite.
package model

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func itEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newPostgresApi runs command-delivery's migration chain against a live server and returns an
// Api on it, with every table it touches emptied first: the instance id is fixed, so a server
// that has run this before still holds its rows.
func newPostgresApi(t *testing.T) *Api {
	t.Helper()
	port, err := strconv.Atoi(itEnv("DC_IT_PGPORT", "5432"))
	require.NoError(t, err, "DC_IT_PGPORT must be numeric")
	host, user, pass := itEnv("DC_IT_PGHOST", "localhost"), itEnv("DC_IT_PGUSER", "postgres"),
		itEnv("DC_IT_PGPASSWORD", "postgres")
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, "dccmdseams", ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: "dccmdseams", FunctionalArea: "command-delivery"},
		Migrations:   Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres95",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": user, "password": pass,
			},
		},
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()), "run the command-delivery migrations")
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	for _, table := range []string{"commands", "command_batches", "device_gates"} {
		require.NoError(t, sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "command-delivery".%q CASCADE`, table)).Error,
			"truncate %s", table)
	}
	return NewApi(mgr)
}

// pgCommand creates one command row and returns it.
func pgCommand(t *testing.T, api *Api, ctx context.Context, token, device string, status CommandStatus,
	after, until time.Time, window string) *Command {
	t.Helper()
	cmd := &Command{DeviceToken: device, Name: "reboot", Status: status.String()}
	if window != "" {
		cmd.DispatchAfter = sql.NullTime{Time: after, Valid: true}
		cmd.DispatchUntil = sql.NullTime{Time: until, Valid: true}
		cmd.WindowToken = sql.NullString{String: window, Valid: true}
	}
	cmd.Token = token
	require.NoError(t, api.RDB.DB(ctx).Create(cmd).Error)
	return cmd
}

func TestPostgresDrainReadExcludesADisabledDevice(t *testing.T) {
	api := newPostgresApi(t)
	a, b := core.WithTenant(context.Background(), "A"), core.WithTenant(context.Background(), "B")
	pgCommand(t, api, a, "a-off", "dev-off", CommandParked, time.Time{}, time.Time{}, "")
	pgCommand(t, api, a, "a-on", "dev-on", CommandParked, time.Time{}, time.Time{}, "")
	pgCommand(t, api, b, "b-off", "dev-off", CommandParked, time.Time{}, time.Time{}, "")

	got, err := api.DrainableCommands(a, "dev-off", 32)
	require.NoError(t, err)
	require.Equal(t, []string{"a-off"}, tokensOf(got), "control: drainable before the disable")

	_, err = api.SetDeviceDisabled(a, "dev-off", true)
	require.NoError(t, err)
	got, err = api.DrainableCommands(a, "dev-off", 32)
	require.NoError(t, err)
	assert.Empty(t, tokensOf(got), "the drain read offered a disabled device's backlog")
	got, err = api.DrainableCommands(a, "dev-on", 32)
	require.NoError(t, err)
	assert.Equal(t, []string{"a-on"}, tokensOf(got))
	got, err = api.DrainableCommands(b, "dev-off", 32)
	require.NoError(t, err)
	assert.Equal(t, []string{"b-off"}, tokensOf(got), "the same token in another tenant is not disabled")
}

func TestPostgresAConfirmAfterTheWindowClosedParks(t *testing.T) {
	api := newPostgresApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	now := time.Now()
	cmd := pgCommand(t, api, ctx, "late", "dev-1", CommandQueued, now.Add(-time.Hour), now.Add(time.Hour), "mw")
	nonce, claimed, err := api.MarkSent(ctx, cmd.ID)
	require.NoError(t, err)
	require.True(t, claimed)

	// Control: inside the window the confirm wins, and parks nothing.
	next, won, err := api.ConfirmDispatch(ctx, "late", nonce)
	require.NoError(t, err)
	require.True(t, won, "control: a confirm inside the window wins")

	require.NoError(t, api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", "late").
		Update("dispatch_until", now.Add(-time.Second)).Error)
	_, won, err = api.ConfirmDispatch(ctx, "late", next)
	require.NoError(t, err)
	require.False(t, won)
	row := commandRow(t, api, ctx, "late")
	assert.Equal(t, CommandParked.String(), row.Status, "a refused confirm outside the window left the row SENT")
	assert.False(t, row.SentTime.Valid)
	assert.Equal(t, next, row.DispatchNonce.String)
}

func TestPostgresParkRefusesACalledOffBatch(t *testing.T) {
	api := newPostgresApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	batch := seedBatchWithCommands(t, api, ctx, "fleet-pg", map[string]string{"pump-pg": CommandQueued.String()})
	row := commandRow(t, api, ctx, "cmd-pump-pg")
	nonce, claimed, err := api.MarkSent(ctx, row.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", "cmd-pump-pg").
		Updates(map[string]any{"dispatch_after": time.Now().Add(-2 * time.Hour),
			"dispatch_until": time.Now().Add(-time.Second), "window_token": "mw"}).Error)
	_, err = api.CancelCommandBatch(ctx, batch)
	require.NoError(t, err)

	require.NoError(t, api.parkOutsideWindow(ctx, "token = ? AND dispatch_nonce = ?", []any{"cmd-pump-pg", nonce}))
	assert.Equal(t, CommandSent.String(), commandRow(t, api, ctx, "cmd-pump-pg").Status,
		"a called-off batch's command was parked, which makes it claimable again")
}

// More than one chunk, so the RETURNING answer is gathered across statements.
func TestPostgresReconcileDisabledReportsExactlyWhatItEnabled(t *testing.T) {
	api := newPostgresApi(t)
	a, b := core.WithTenant(context.Background(), "A"), core.WithTenant(context.Background(), "B")
	n := gateChunk + 7
	tokens := make([]string, 0, n)
	for i := 0; i < n; i++ {
		tokens = append(tokens, fmt.Sprintf("d-%04d", i))
	}
	_, err := api.ReconcileDisabled(a, tokens, time.Now())
	require.NoError(t, err)
	_, err = api.ReconcileDisabled(b, tokens[:3], time.Now())
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)

	enabled, err := api.ReconcileDisabled(a, tokens[:1], time.Now())
	require.NoError(t, err)
	assert.ElementsMatch(t, tokens[1:], enabled, "the enable half's RETURNING answer")

	got, err := api.DisabledAmong(a, tokens)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{tokens[0]: true}, got)
	got, err = api.DisabledAmong(b, tokens[:3])
	require.NoError(t, err)
	assert.Len(t, got, 3, "another tenant's record was changed")

	// A newer fact is not overwritten, and is not reported.
	readStarted := time.Now().Add(-time.Hour)
	_, err = api.SetDeviceDisabled(a, tokens[2], true)
	require.NoError(t, err)
	enabled, err = api.ReconcileDisabled(a, tokens[:1], readStarted)
	require.NoError(t, err)
	assert.Empty(t, enabled, "a row newer than the read was enabled by an older list")
}

func TestPostgresRepointMovesAndReleasesWaitingCommands(t *testing.T) {
	api := newPostgresApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	other := core.WithTenant(context.Background(), "B")
	after := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	for i := 0; i < 4; i++ {
		pgCommand(t, api, ctx, fmt.Sprintf("c-%d", i), fmt.Sprintf("dev-%d", i), CommandQueued,
			after, after.Add(time.Hour), "plant-a")
	}
	pgCommand(t, api, ctx, "c-sent", "dev-0", CommandSent, after, after.Add(time.Hour), "plant-a")
	pgCommand(t, api, other, "b-0", "dev-0", CommandQueued, after, after.Add(time.Hour), "plant-a")

	keys, err := api.WaitingCommandKeys(ctx, "plant-a")
	require.NoError(t, err)
	require.Len(t, keys, 4, "one key per waiting device, in this tenant only, SENT excluded")

	b := after.Add(48 * time.Hour)
	moved, err := api.RepointWindow(ctx, "plant-a", "reboot", []string{"dev-0", "dev-1"},
		&DispatchHold{WindowToken: "plant-b", DispatchAfter: b, DispatchUntil: b.Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, int64(2), moved)
	released, err := api.RepointWindow(ctx, "plant-a", "reboot", []string{"dev-2"}, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(1), released)

	r0 := commandRow(t, api, ctx, "c-0")
	assert.Equal(t, "plant-b", r0.WindowToken.String)
	assert.True(t, r0.DispatchAfter.Time.Equal(b))
	r2 := commandRow(t, api, ctx, "c-2")
	assert.False(t, r2.WindowToken.Valid || r2.DispatchAfter.Valid || r2.DispatchUntil.Valid, "release cleared the hold")
	assert.Equal(t, "plant-a", commandRow(t, api, ctx, "c-3").WindowToken.String, "an unnamed device moved")
	assert.Equal(t, "plant-a", commandRow(t, api, ctx, "c-sent").WindowToken.String, "a SENT row was re-pointed")
	assert.Equal(t, "plant-a", commandRow(t, api, other, "b-0").WindowToken.String, "another tenant's row moved")
}
