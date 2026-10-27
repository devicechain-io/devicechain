// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// What only a real PostgreSQL can show about device aliases and the unregistered sightings:
// the unique index holds under concurrent writers, the cap is exact when sightings race, and the
// foreign keys cascade. SQLite has one writer at a time and no foreign keys, so the unit tests can
// state none of it. The migrations under test are the real chain, not AutoMigrate of the models.
//
// Tagged `integration`, run like the integrations tests beside it:
//
//	docker run -d --name dc-g17a2-pg -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-g17a2-pg 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'Alias|Unregistered' -v
package model

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withIntegrationChecker wires the credential Checker an integration authenticates through.
func withIntegrationChecker(t *testing.T, api *Api) {
	t.Helper()
	checker, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIntegration: {Unthrottled: true},
	}, credential.WithIntegrationSecretKey(testEnrollmentKeys.Integration))
	require.NoError(t, err)
	api.IntegrationChecker = checker
}

// Racing creates of DIFFERENT devices claiming the same DevEUI, in different spellings: exactly one
// lands, every other is a CONFLICT, and the table holds one row.
func TestOnlyOneDeviceWinsAnAliasUnderConcurrentCreates(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)

	const racers = 12
	var wg sync.WaitGroup
	results := make(chan error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			value := "70B3D57ED0000001"
			if i%2 == 1 {
				value = strings.ToLower(value)
			}
			<-start
			_, err := api.CreateDevice(ctx, &DeviceCreateRequest{
				Token: fmt.Sprintf("racer-%d", i), DeviceTypeToken: "dt",
				Aliases: &[]DeviceAliasInput{{Scheme: "lorawan.deveui", Value: value}}})
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	landed := 0
	for err := range results {
		if err == nil {
			landed++
			continue
		}
		assert.True(t, conflict.Is(err), "a loser must be a CONFLICT, got %v", err)
	}
	assert.Equal(t, 1, landed, "exactly one device holds the alias")

	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceAlias{}).Count(&n).Error)
	assert.EqualValues(t, 1, n)
	// The losers left no device behind: the alias and the device land together or not at all.
	devices, err := api.Devices(ctx, DeviceSearchCriteria{})
	require.NoError(t, err)
	assert.Len(t, devices.Results, 1)
}

// A deleted device frees its aliases in the same statement (the foreign key cascades), and a
// deleted integration takes its sightings.
func TestAliasAndSightingForeignKeysCascade(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	withIntegrationChecker(t, api)
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d-1", DeviceTypeToken: "dt",
		Aliases: &[]DeviceAliasInput{{Scheme: "lorawan.deveui", Value: "70B3D57ED0000001"}}})
	require.NoError(t, err)

	// Hard-delete the row directly, bypassing DeleteDevice's explicit alias statement: the
	// database alone must free the alias.
	_, err = api.hardDeleteByToken(ctx, &Device{}, "d-1")
	require.NoError(t, err)
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceAlias{}).Count(&n).Error)
	assert.Zero(t, n, "the foreign key cascaded the alias away")
	_, err = api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d-2", DeviceTypeToken: "dt",
		Aliases: &[]DeviceAliasInput{{Scheme: "lorawan.deveui", Value: "70B3D57ED0000001"}}})
	require.NoError(t, err, "the freed alias is available to another device")

	created, err := api.CreateIntegration(ctx, &IntegrationCreateRequest{
		Token: "chirp-1", Type: string(IntegrationChirpStackV4), Enabled: true, DeviceTypeTokens: []string{"dt"}})
	require.NoError(t, err)
	_, err = api.ResolveIntegrationDevice(ctx, "chirp-1", created.IssuedToken, "lorawan.deveui", "70B3D57ED00000FF")
	require.NoError(t, err)
	require.NoError(t, api.RDB.DB(ctx).Model(&IntegrationUnregisteredDevice{}).Count(&n).Error)
	require.EqualValues(t, 1, n)
	// Delete the integration row alone, bypassing DeleteIntegration's explicit statements.
	require.NoError(t, api.RDB.DB(ctx).Exec("DELETE FROM integration_device_types WHERE integration_id = ?", created.ID).Error)
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Delete(&Integration{}, created.ID).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&IntegrationUnregisteredDevice{}).Count(&n).Error)
	assert.Zero(t, n, "the foreign key cascaded the sightings away")
}

// Sightings racing at the cap cannot overshoot it: the per-integration lock makes the count the
// insert is checked against the count it lands on. Several rounds, each on a fresh integration
// with five places left, so a lock that is missing has many chances to be caught.
func TestTheUnregisteredCapIsExactUnderConcurrentSightings(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	withIntegrationChecker(t, api)
	api.IntegrationsCeiling = ceilingStub{n: 100}

	const room, racers, rounds = 5, 48, 8
	for round := 0; round < rounds; round++ {
		token := fmt.Sprintf("chirp-%d", round)
		created, err := api.CreateIntegration(ctx, &IntegrationCreateRequest{
			Token: token, Type: string(IntegrationChirpStackV4), Enabled: true, DeviceTypeTokens: []string{"dt"}})
		require.NoError(t, err)
		rows := make([]IntegrationUnregisteredDevice, 0, MaxUnregisteredPerIntegration-room)
		for i := 0; i < MaxUnregisteredPerIntegration-room; i++ {
			rows = append(rows, IntegrationUnregisteredDevice{IntegrationId: created.ID, Scheme: "lorawan.deveui",
				Value: fmt.Sprintf("%016X", i+1), FirstSeen: created.CreatedAt, LastSeen: created.CreatedAt, Count: 1})
		}
		require.NoError(t, api.RDB.DB(ctx).CreateInBatches(&rows, 200).Error)

		var wg sync.WaitGroup
		type outcome struct {
			overflow bool
			err      error
		}
		results := make(chan outcome, racers)
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				got, err := api.ResolveIntegrationDevice(ctx, token, created.IssuedToken,
					"lorawan.deveui", fmt.Sprintf("%016X", 0xF0000000+i))
				if err != nil {
					results <- outcome{err: err}
					return
				}
				results <- outcome{overflow: got.Overflow}
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)

		stored, overflowed := 0, 0
		for r := range results {
			require.NoError(t, r.err)
			if r.overflow {
				overflowed++
			} else {
				stored++
			}
		}
		assert.Equal(t, room, stored, "round %d: exactly the room that was left is filled", round)
		assert.Equal(t, racers-room, overflowed, "round %d", round)
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Model(&IntegrationUnregisteredDevice{}).Where("integration_id = ?", created.ID).Count(&n).Error)
		assert.EqualValues(t, MaxUnregisteredPerIntegration, n, "round %d: the cap held exactly", round)
	}
}
