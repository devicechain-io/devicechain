// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The audit journal's actor filter is ILIKE, which only PostgreSQL speaks, so it is
// asked of a real server rather than of the SQLite the unit tests run on.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./rdb/... -run AuditActorFilter -v
package rdb

import (
	"context"
	"testing"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
)

type auditActorFilterWidget struct {
	ID uint `gorm:"primaryKey"`
}

// The actor filter is a literal, case-insensitive substring search: `%`, `_` and the
// escape character `\` in the filter text are characters to find, never wildcards.
func TestAuditActorFilterIsLiteral(t *testing.T) {
	ctx := context.Background()
	host, port := itServer(t)
	const instance = "rdbauditactorfilter"
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))

	mgr := &RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "audit-actor-filter"},
		Migrations: []*gormigrate.Migration{{
			ID: "20261010000000",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&auditActorFilterWidget{}, &AuditEvent{})
			},
		}},
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": "postgres", "password": "postgres",
				"sslMode": "disable",
			},
		},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})

	const tenant = "actor-filter"
	sys := mgr.DB(core.WithSystemContext(ctx))
	require.NoError(t, sys.Where("tenant_id = ?", tenant).Delete(&AuditEvent{}).Error)
	now := time.Now()
	for _, actor := range []string{
		"50% done", "500 done",
		"line_a", "linexa",
		`C:\ops`, "C:ops",
		`a\%b`, "a%b",
		"SuperUser@example",
	} {
		require.NoError(t, sys.Create(&AuditEvent{
			OccurredTime: now, TenantId: tenant, Category: "auth", Operation: "login", Actor: actor,
		}).Error)
	}

	actors := func(filter string) []string {
		page, err := mgr.AuditEvents(core.WithTenant(ctx, tenant), AuditEventSearchCriteria{
			Pagination: Pagination{PageNumber: 1, PageSize: 50},
			Actor:      &filter,
		})
		require.NoError(t, err)
		out := make([]string, 0, len(page.Results))
		for _, r := range page.Results {
			out = append(out, r.Actor)
		}
		return out
	}
	assert.ElementsMatch(t, []string{"50% done"}, actors("50%"))
	assert.ElementsMatch(t, []string{"line_a"}, actors("e_a"))
	assert.ElementsMatch(t, []string{`C:\ops`}, actors(`C:\ops`))
	assert.ElementsMatch(t, []string{`a\%b`}, actors(`a\%`))
	// The counterweights: still a substring search, and still case-insensitive.
	assert.ElementsMatch(t, []string{"line_a", "linexa"}, actors("line"))
	assert.ElementsMatch(t, []string{"SuperUser@example"}, actors("superuser"))
}
