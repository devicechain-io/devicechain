// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// dashboard_shares on a real PostgreSQL, through the real migration chain: the foreign key
// that cascades a share with its dashboard and the CHECK on its kind are DDL the sqlite suite
// never runs, and the scope predicate is asked of the server that will answer it.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/ -v
package model

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type customersExist struct{}

func (customersExist) Missing(context.Context, string, []string) ([]string, error) { return nil, nil }

func pgApi(t *testing.T) *Api {
	t.Helper()
	port := 5432
	if raw := os.Getenv("DC_IT_PGPORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = p
	}
	host := "localhost"
	if h := os.Getenv("DC_IT_PGHOST"); h != "" {
		host = h
	}
	ctx := context.Background()
	instance := fmt.Sprintf("dashshares%d", time.Now().UnixNano())
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "dashboard-management"},
		Migrations:   Migrations,
		InstanceConfig: config.DatastoreConfiguration{Type: "postgres", Configuration: map[string]interface{}{
			"hostname": host, "port": port, "username": "postgres", "password": "postgres", "sslMode": "disable",
		}},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	api := NewApi(mgr)
	api.Customers = customersExist{}
	return api
}

func pgAuthor() context.Context {
	return auth.WithClaims(core.WithTenant(context.Background(), "t1"), &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.DashboardRead), string(auth.DashboardWrite)}})
}

func pgMember(anchors ...string) context.Context {
	return auth.WithClaims(core.WithTenant(context.Background(), "t1"), &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.DashboardRead)}, ScopeAnchors: anchors})
}

func pgDashboard(t *testing.T, api *Api, token string, customers ...string) *Dashboard {
	t.Helper()
	d, err := api.CreateDashboard(pgAuthor(), &DashboardCreateRequest{Token: token, Definition: `{"schemaVersion":1}`})
	require.NoError(t, err)
	_, _, err = api.PublishDashboard(pgAuthor(), token, nil, nil, "author", nil)
	require.NoError(t, err)
	anchors := make([]string, 0, len(customers))
	for _, c := range customers {
		anchors = append(anchors, "customer:"+c)
	}
	_, err = api.ShareDashboard(pgAuthor(), token, anchors)
	require.NoError(t, err)
	return d
}

// The scope predicate on the real server: a member lists and opens only what is shared with
// its scope; the author is the control.
func TestPostgresScopedDashboardReads(t *testing.T) {
	api := pgApi(t)
	pgDashboard(t, api, "d-acme", "acme")
	pgDashboard(t, api, "d-globex", "globex")
	pgDashboard(t, api, "d-both", "acme", "globex")

	tokens := func(ctx context.Context) []string {
		found, err := api.Dashboards(ctx, DashboardSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
		require.NoError(t, err)
		out := []string{}
		for _, d := range found.Results {
			out = append(out, d.Token)
		}
		require.Equal(t, int32(len(out)), found.Pagination.TotalRecords)
		return out
	}
	require.ElementsMatch(t, []string{"d-acme", "d-globex", "d-both"}, tokens(pgAuthor()))
	require.ElementsMatch(t, []string{"d-acme", "d-both"}, tokens(pgMember("customer:acme")))

	_, v, err := api.PublishedDashboard(pgMember("customer:acme"), "d-both")
	require.NoError(t, err)
	require.Equal(t, int32(1), v.Version)
	_, _, err = api.PublishedDashboard(pgMember("customer:acme"), "d-globex")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// A share leaves with its dashboard by the foreign key alone — here deleted by a statement
// that names no share — and the table refuses a kind no scope can name.
func TestPostgresSharesCascadeAndAreChecked(t *testing.T) {
	api := pgApi(t)
	d := pgDashboard(t, api, "d1", "acme")
	sys := api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))

	err := sys.Exec(`INSERT INTO "dashboard-management".dashboard_shares (tenant_id, dashboard_id, anchor_kind, anchor_token) VALUES (?, ?, 'asset', 'pump')`,
		"t1", d.ID).Error
	require.Error(t, err, "the table accepted a share with an asset")

	require.NoError(t, sys.Exec(`DELETE FROM "dashboard-management".dashboard_versions WHERE dashboard_id = ?`, d.ID).Error)
	require.NoError(t, sys.Exec(`DELETE FROM "dashboard-management".dashboards WHERE id = ?`, d.ID).Error)
	var n int64
	require.NoError(t, sys.Model(&DashboardShare{}).Count(&n).Error)
	require.Zero(t, n, "a share outlived its dashboard")
}
