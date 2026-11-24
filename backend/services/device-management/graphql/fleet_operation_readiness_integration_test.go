// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// Run on the real Postgres integration database. The migration chain installs the production
// guard, so the test first reads it as ENFORCED, then takes it apart and builds the lookalikes
// readiness must not accept; cleanup replays the migration, which restores the production guard
// exactly (the test owns its own database, so nothing else observes the gap).
func TestFleetOperationReadinessPostgres(t *testing.T) {
	api := newReadinessPostgresApi(t)
	db := api.RDB.Database
	ctx := core.WithTenant(context.Background(), "readiness")
	ctx = auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{"fleet:operate"}})
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	parsed := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	probe := func() string {
		res := parsed.Exec(ctx, readinessQuery, "", nil)
		require.Empty(t, res.Errors)
		var out struct {
			Readiness struct {
				ProtocolVersion       int
				Enforcement           string
				ExecutableKinds       []string
				OrphanedAttributeRows int
			} `json:"fleetOperationReadiness"`
		}
		require.NoError(t, json.Unmarshal(res.Data, &out))
		require.Equal(t, 1, out.Readiness.ProtocolVersion)
		require.Equal(t, []string{}, out.Readiness.ExecutableKinds)
		require.Zero(t, out.Readiness.OrphanedAttributeRows)
		return out.Readiness.Enforcement
	}
	fn := `"device-management".` + model.FleetOperationEnforcementFunction
	trigger := model.FleetOperationEnforcementTrigger
	table := `"device-management".entity_attributes`
	other := `"device-management".readiness_test_other`
	wrong := `"device-management".readiness_test_wrong`
	cleanup := func() {
		for _, target := range []string{table, other} {
			require.NoError(t, db.Exec(`DROP TRIGGER IF EXISTS `+trigger+` ON `+target).Error)
		}
		require.NoError(t, db.Exec(`DROP FUNCTION IF EXISTS `+fn+`()`).Error)
		require.NoError(t, db.Exec(`DROP FUNCTION IF EXISTS `+wrong+`()`).Error)
	}
	require.NoError(t, db.Exec(`CREATE TABLE `+other+` (id bigint)`).Error)
	t.Cleanup(func() {
		cleanup()
		require.NoError(t, db.Exec(`DROP TABLE `+other).Error)
		require.NoError(t, schema.NewAttributeWriterProtocolSchema().Migrate(db), "restore the production guard")
		require.Equal(t, "ENFORCED", probe())
	})
	// The production guard, as the chain installed it.
	require.Equal(t, "ENFORCED", probe())
	cleanup()
	require.Equal(t, "NOT_INSTALLED", probe())
	require.NoError(t, db.Exec(`CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $dcap$`+schema.AttributeWriterProtocolFunctionSource+`$dcap$`).Error)
	require.NoError(t, db.Exec(`CREATE FUNCTION `+wrong+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF TG_OP = 'DELETE' THEN RETURN OLD; END IF; RETURN NEW; END; $$`).Error)
	install := func(target, function, events string) {
		require.NoError(t, db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION %s()`, trigger, events, target, function)).Error)
		require.NoError(t, db.Exec(`ALTER TABLE `+target+` ENABLE ALWAYS TRIGGER `+trigger).Error)
	}
	install(table, fn, "INSERT OR UPDATE OR DELETE")
	require.Equal(t, "ENFORCED", probe())
	for _, mode := range []string{"DISABLE", "ENABLE", "ENABLE REPLICA"} {
		require.NoError(t, db.Exec(`ALTER TABLE `+table+` `+mode+` TRIGGER `+trigger).Error)
		require.Equal(t, "DISABLED", probe(), mode)
	}
	require.NoError(t, db.Exec(`DROP TRIGGER `+trigger+` ON `+table).Error)
	require.Equal(t, "NOT_INSTALLED", probe())
	for _, tc := range []struct{ target, function, events string }{
		{table, wrong, "INSERT OR UPDATE OR DELETE"},
		{other, fn, "INSERT OR UPDATE OR DELETE"},
		{table, fn, "INSERT OR UPDATE"},
		{table, fn, "INSERT OR DELETE"},
		{table, fn, "UPDATE OR DELETE"},
	} {
		install(tc.target, tc.function, tc.events)
		require.Equal(t, "NOT_INSTALLED", probe())
		require.NoError(t, db.Exec(`DROP TRIGGER `+trigger+` ON `+tc.target).Error)
	}
	// A statement-level trigger never sees a row, so it is not enforcement either.
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT OR UPDATE OR DELETE ON %s FOR EACH STATEMENT EXECUTE FUNCTION %s()`, trigger, table, fn)).Error)
	require.NoError(t, db.Exec(`ALTER TABLE `+table+` ENABLE ALWAYS TRIGGER `+trigger).Error)
	require.Equal(t, "NOT_INSTALLED", probe())
	require.NoError(t, db.Exec(`DROP TRIGGER `+trigger+` ON `+table).Error)
	// The right NAME with another body is not the shipped function: readiness hashes the body.
	install(table, fn, "INSERT OR UPDATE OR DELETE")
	require.Equal(t, "ENFORCED", probe())
	require.NoError(t, db.Exec(`CREATE OR REPLACE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`).Error)
	require.Equal(t, "NOT_INSTALLED", probe())
	require.NoError(t, db.Exec(`DROP TRIGGER `+trigger+` ON `+table).Error)
	// A conditional trigger is not enforcement: WHEN (false) keeps the function from ever running,
	// and the catalog cannot judge a condition.
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT OR UPDATE OR DELETE ON %s FOR EACH ROW WHEN (false) EXECUTE FUNCTION %s()`, trigger, table, fn)).Error)
	require.NoError(t, db.Exec(`ALTER TABLE `+table+` ENABLE ALWAYS TRIGGER `+trigger).Error)
	require.NotEqual(t, "ENFORCED", probe())
	require.NoError(t, db.Exec(`DROP TRIGGER `+trigger+` ON `+table).Error)
}

// A separate integration database prevents test-only expected trigger names from
// interfering with the model protocol suite. Missing/unreachable Postgres fails.
func newReadinessPostgresApi(t *testing.T) *model.Api {
	t.Helper()
	env := func(name, fallback string) string {
		if value := os.Getenv(name); value != "" {
			return value
		}
		return fallback
	}
	port, err := strconv.Atoi(env("DC_IT_PGPORT", "5432"))
	require.NoError(t, err)
	host, user, password := env("DC_IT_PGHOST", "localhost"), env("DC_IT_PGUSER", "postgres"), env("DC_IT_PGPASSWORD", "postgres")
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), host, port, user, password, "dcreadiness", ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: "dcreadiness", FunctionalArea: "device-management"},
		Migrations:   schema.Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type:          "postgres95",
			Configuration: map[string]interface{}{"hostname": host, "port": port, "username": user, "password": password},
		},
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()))
	t.Cleanup(func() {
		db, err := mgr.Database.DB()
		require.NoError(t, err)
		require.NoError(t, db.Close())
	})
	return model.NewApi(mgr)
}
