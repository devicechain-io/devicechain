// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAuditActorBoundOnTenantWire(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&rdb.AuditEvent{}))
	ctx := withAuthorities(core.WithTenant(context.Background(), "acme"), auth.AuditRead)
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, `query($actor: String!) {
		auditEvents(criteria: {pageNumber: 1, pageSize: 10, actor: $actor}) { results { id } }
	}`, "", map[string]any{"actor": strings.Repeat("a", 257)})
	require.Len(t, res.Errors, 1)
	require.Equal(t, "LIMIT_EXCEEDED", res.Errors[0].Extensions["code"])
}
