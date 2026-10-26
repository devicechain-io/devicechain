// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Two properties of integrations that only a real PostgreSQL can show: the RESTRICT foreign key
// that refuses deleting a device type an integration is scoped to, and the per-tenant lock that
// makes the integrations ceiling exact under concurrent creates. SQLite has neither foreign keys
// nor concurrent writers, so the unit tests can state neither.
//
// Tagged `integration`, run like the geofence TOCTOU test beside it:
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run Integration -v
package model

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPostgresIntegrationApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := NewApi(newPostgresRdbManager(t))
	api.EnrollmentKeys = testEnrollmentKeys
	api.DeviceSecretKey = testSecretKey
	// A tenant of its own each run: the server outlives the test and tokens are unique per tenant.
	ctx := core.WithTenant(context.Background(), fmt.Sprintf("it-int-%d", time.Now().UnixNano()))
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"})
	require.NoError(t, err)
	return api, ctx
}

// A scoping that lands AFTER DeleteDeviceType's count is refused by the database, and the
// refusal is answered as the same REFERENCE_VIOLATION the count gives. The count is bypassed
// here on purpose, by deleting the way DeleteDeviceType's last statement does.
func TestIntegrationScopedAfterTheCountIsRefusedByTheForeignKey(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	_, err := api.CreateIntegration(ctx, &IntegrationCreateRequest{
		Token: "chirp-1", Type: string(IntegrationChirpStackV4), Enabled: true, DeviceTypeTokens: []string{"dt"}})
	require.NoError(t, err)

	_, err = api.hardDeleteByToken(ctx, &DeviceType{}, "dt")
	require.Error(t, err, "the database let a scoped device type go")
	answered := inUseOn(err, "device type \"dt\" is still referenced")
	require.ErrorIs(t, answered, ErrEntityInUse)
	class, ok := integrity.Refused(answered)
	require.True(t, ok)
	assert.Equal(t, integrity.ClassReference, class)

	types, err := api.DeviceTypesByToken(ctx, []string{"dt"})
	require.NoError(t, err)
	assert.Len(t, types, 1, "the refused delete removed nothing")
}

// Concurrent creates at the ceiling cannot both land: the per-tenant lock makes the count the
// insert is checked against the count it lands on.
func TestIntegrationsCeilingIsExactUnderConcurrentCreates(t *testing.T) {
	api, ctx := newPostgresIntegrationApi(t)
	api.IntegrationsCeiling = ceilingStub{n: 1}

	const racers = 8
	var wg sync.WaitGroup
	results := make(chan error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := api.CreateIntegration(ctx, &IntegrationCreateRequest{
				Token: fmt.Sprintf("racer-%d", i), Type: string(IntegrationChirpStackV4), Enabled: true,
				DeviceTypeTokens: []string{"dt"}})
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
		_, isLimit := limit.As(err)
		assert.True(t, isLimit, "a loser must be refused by the ceiling, got %v", err)
	}
	assert.Equal(t, 1, landed, "the ceiling is 1")
}
