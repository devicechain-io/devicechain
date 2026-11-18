// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAuditActorBoundOnAdminWire(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&rdb.AuditEvent{}))
	svc := admin.NewService(iam.NewStore(&rdb.RdbManager{Database: db}), 0, 0, nil)
	ctx := context.WithValue(adminCtx(string(auth.AuditRead)), ContextAdminKey, svc)
	schema := gqlcore.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	res := schema.Exec(ctx, `query($actor: String!) {
		auditEvents(criteria: {pageNumber: 1, pageSize: 10, actor: $actor}) { results { id } }
	}`, "", map[string]any{"actor": strings.Repeat("\u00e9", 128) + "a"})
	require.Len(t, res.Errors, 1)
	require.Equal(t, "LIMIT_EXCEEDED", res.Errors[0].Extensions["code"])
}
