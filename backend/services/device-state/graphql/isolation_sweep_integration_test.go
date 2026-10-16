// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package graphql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-microservice/test/isolationsweep"
	"github.com/stretchr/testify/require"
)

func sweepEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// sweepFixture is device-state on a real PostgreSQL holding, in tenant A, the live view of
// an Acme device and of a Globex device — state, a reading and a position each, every row
// carrying the device's token — and, in tenant B, a device stamped with Acme's anchor. The
// scope predicate is an array overlap sqlite cannot evaluate, hence PostgreSQL.
func sweepFixture(t *testing.T) context.Context {
	t.Helper()
	port, err := strconv.Atoi(sweepEnv("DC_IT_PGPORT", "5432"))
	require.NoError(t, err)
	host, user, pass := sweepEnv("DC_IT_PGHOST", "localhost"), sweepEnv("DC_IT_PGUSER", "postgres"),
		sweepEnv("DC_IT_PGPASSWORD", "postgres")
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, "dcstatesweep", ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: "dcstatesweep", FunctionalArea: "device-state"},
		Migrations:   model.Migrations,
		InstanceConfig: config.DatastoreConfiguration{Type: "postgres95", Configuration: map[string]interface{}{
			"hostname": host, "port": port, "username": user, "password": pass,
		}},
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	for _, table := range []string{"device_states", "latest_measurements", "latest_locations"} {
		require.NoError(t, sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "device-state".%q`, table)).Error)
	}
	api := model.NewApi(mgr)
	at := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	seed := func(tenant, device, owner string) {
		ctx := core.WithTenant(context.Background(), tenant)
		_, err := api.MergeDeviceState(ctx, device, at, nil, model.DeviceIdentity{ExternalId: device + "-ext",
			Source: "mqtt1", Owners: model.Owners{Anchors: []string{owner}, At: at}})
		require.NoError(t, err)
		require.NoError(t, api.MergeLatestMeasurements(ctx, device, []model.LatestMeasurementInput{{
			Name: device + "-temp", Value: sql.NullFloat64{Float64: 21.5, Valid: true}, OccurredTime: at}}))
		require.NoError(t, api.MergeLatestLocations(ctx, device, []model.LatestLocationInput{{
			Latitude: sql.NullFloat64{Float64: 41.5, Valid: true}, Longitude: sql.NullFloat64{Float64: -87.5, Valid: true},
			OccurredTime: at}}))
	}
	seed("A", "acme-1", "customer:acme")
	seed("A", "globex-1", "customer:globex")
	seed("B", "b-globex-1", "customer:acme")
	ctx := core.WithTenant(context.Background(), "A")
	ctx = context.WithValue(ctx, gqlcore.ContextRdbKey, mgr)
	return context.WithValue(ctx, gqlcore.ContextApiKey, api)
}

// sweepAuthorities is every authority a device-state operation gates on, so a refusal in the
// scoped run is the scope's and not a missing grant.
var sweepAuthorities = []string{string(auth.StateRead), string(auth.LocationRead), string(auth.StateDemote)}

// TestIsolationSweep calls every query and mutation device-state serves as Acme's member,
// aimed at Globex's device, and fails on any answer that carries Globex's live view or tells
// Globex's device apart from a missing one (ADR-081). Its negative control is the same
// queries as the unscoped operator, which must surface Globex.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./graphql/ -run TestIsolationSweep -v
func TestIsolationSweep(t *testing.T) {
	ctx := sweepFixture(t)
	scoped := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: sweepAuthorities,
		ScopeAnchors: []string{"customer:acme"}})
	unscoped := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: sweepAuthorities})

	out := isolationsweep.Run(t, isolationsweep.Config{
		Schema:   gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
		Scoped:   scoped,
		Unscoped: unscoped,
		Probe:    "globex-1",
		Hidden:   []string{"globex"},
	})
	// The opted-in reads answered data rather than a refusal...
	for _, name := range []string{"query deviceStates", "query deviceStatesByDeviceToken", "query deviceStatesByExternalId",
		"query latestMeasurements", "query latestLocation", "query latestLocations"} {
		if o, ok := isolationsweep.OutcomeOf(out, name); !ok || !o.Data {
			t.Errorf("%s: %+v (reached=%v), want data for acme's member", name, o, ok)
		}
	}
	// ...and the two that stay closed refused.
	for _, name := range []string{"query assertedDeviceStates", "mutation demoteAssertedPresence"} {
		if o, ok := isolationsweep.OutcomeOf(out, name); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
			t.Errorf("%s: %+v (reached=%v), want FORBIDDEN for a scoped member", name, o, ok)
		}
	}
	// The sweep aims every by-token read at Globex's device, so "answered data" above could be
	// an empty answer; Acme's own device must come back, by token and by search.
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	for _, q := range []string{
		`query { deviceStatesByDeviceToken(deviceTokens: ["acme-1", "globex-1"]) { deviceToken } }`,
		`query { deviceStates(criteria: {pageNumber: 1, pageSize: 50}) { results { deviceToken } } }`,
		`query { latestMeasurements(deviceToken: "acme-1") { name } }`,
		`query { latestLocations(deviceTokens: ["acme-1", "globex-1"]) { deviceToken } }`,
	} {
		res := schema.Exec(scoped, q, "", nil)
		require.Empty(t, res.Errors, q)
		require.Contains(t, string(res.Data), "acme-1", q)
		require.NotContains(t, string(res.Data), "globex", q)
	}
}
