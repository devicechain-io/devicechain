// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The areas a scoped caller reaches, and anchorDevices, on the real engine: the
// ReachedFromDevice predicate nests the Device rule's EXISTS inside its own edge EXISTS, all
// over the same two tables, so the statement is run on PostgreSQL rather than trusted from
// SQLite. The second test records the plan of the scoped list on a 10k-device fixture.
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'AreasReached|ScopedAreasPlan' -v
package model

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func areaTokens(t *testing.T, api *Api, ctx context.Context) []string {
	t.Helper()
	found, err := api.Areas(ctx, AreaSearchCriteria{})
	require.NoError(t, err)
	require.EqualValues(t, len(found.Results), found.Pagination.TotalRecords, "the count and the page agree")
	out := []string{}
	for _, a := range found.Results {
		out = append(out, a.Token)
	}
	sort.Strings(out)
	return out
}

func TestAreasReachedOnPostgres(t *testing.T) {
	r := newScopeAlarmRig(t)
	ctx := r.operator
	api := r.api
	_, err := api.CreateAreaType(ctx, &AreaTypeCreateRequest{Token: "floor"})
	require.NoError(t, err)
	_, err = api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{Token: "monitors"})
	require.NoError(t, err)
	for _, a := range []string{"shared", "acme-only", "globex-only", "untracked", "idle"} {
		_, err := api.CreateArea(ctx, &AreaCreateRequest{Token: a, AreaTypeToken: "floor"})
		require.NoError(t, err)
	}
	place := func(device, area, rel string) {
		_, err := api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{Token: device + "-in-" + area,
			SourceType: "device", Source: device, TargetType: "area", Target: area, RelationshipType: rel})
		require.NoError(t, err)
	}
	place("acme-pump", "shared", AssignmentRelationshipType)
	place("globex-pump", "shared", AssignmentRelationshipType)
	place("acme-pump", "acme-only", AssignmentRelationshipType)
	place("globex-pump", "globex-only", AssignmentRelationshipType)
	place("acme-pump", "untracked", "monitors")

	require.Equal(t, []string{"acme-only", "globex-only", "idle", "shared", "untracked"}, areaTokens(t, api, ctx), "positive control: unscoped sees all")
	require.Equal(t, []string{"acme-only", "shared"}, areaTokens(t, api, r.member("customer:acme")))
	require.Equal(t, []string{"globex-only", "shared"}, areaTokens(t, api, r.member("customer:globex")))
	require.Empty(t, areaTokens(t, api, r.member("customer:nobody")))
	require.Empty(t, areaTokens(t, api, r.member([]string{}...)), "no anchors reaches nothing")

	// A by-token read outside the reach is omitted.
	found, err := api.AreasByToken(r.member("customer:acme"), []string{"globex-only", "shared"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "shared", found[0].Token)

	// anchorDevices on the shared area: each customer sees its own device, the count agrees.
	dev := func(c context.Context, kind, target string) ([]string, int32) {
		res, err := api.AnchorDevices(c, AnchorDeviceSearchCriteria{TargetType: kind, Target: target, OwnershipOnly: c != ctx})
		require.NoError(t, err)
		out := []string{}
		for _, d := range res.Results {
			out = append(out, d.Token)
		}
		sort.Strings(out)
		return out, res.Pagination.TotalRecords
	}
	got, n := dev(r.member("customer:acme"), "area", "shared")
	require.Equal(t, []string{"acme-pump"}, got)
	require.EqualValues(t, 1, n)
	got, n = dev(ctx, "area", "shared")
	require.Equal(t, []string{"acme-pump", "globex-pump"}, got)
	require.EqualValues(t, 2, n)
	got, _ = dev(r.member("customer:acme"), "area", "untracked")
	require.Empty(t, got)
	got, _ = dev(ctx, "area", "untracked")
	require.Equal(t, []string{"acme-pump"}, got)

	// A sale updates visibility on the next read.
	_, err = api.RemoveEntityRelationship(ctx, "acme-pump-to-acme")
	require.NoError(t, err)
	require.Empty(t, areaTokens(t, api, r.member("customer:acme")))
}

// TestScopedAreasPlanOnTenKDevices records the plan of the scoped `areas` list on 10,000
// devices spread over 50 customers and 500 areas (spec section 8, S1). It asserts the answer
// and logs EXPLAIN ANALYZE so the PR can quote it; it fails only on a wrong answer.
func TestScopedAreasPlanOnTenKDevices(t *testing.T) {
	r := newScopeAlarmRig(t)
	ctx := r.operator
	api := r.api
	db := api.RDB.DB(ctx)

	_, err := api.CreateAreaType(ctx, &AreaTypeCreateRequest{Token: "floor"})
	require.NoError(t, err)
	const nAreas, nCustomers, nDevices = 500, 50, 10000
	areas := make([]Area, 0, nAreas)
	var at AreaType
	require.NoError(t, db.Where("token = ?", "floor").First(&at).Error)
	for i := 0; i < nAreas; i++ {
		areas = append(areas, Area{AreaTypeId: at.ID})
		areas[i].Token = fmt.Sprintf("area-%d", i)
	}
	require.NoError(t, db.CreateInBatches(&areas, 500).Error)
	var dt DeviceType
	require.NoError(t, db.Where("token = ?", "pump").First(&dt).Error)
	assigned, err := api.EnsureAssignmentType(ctx)
	require.NoError(t, err)
	devices := make([]Device, nDevices)
	for i := range devices {
		devices[i] = Device{DeviceTypeId: dt.ID}
		devices[i].Token = fmt.Sprintf("dev-%d", i)
	}
	require.NoError(t, db.CreateInBatches(&devices, 1000).Error)
	edges := make([]EntityRelationship, 0, 2*nDevices)
	for i, d := range devices {
		customer := fmt.Sprintf("cust-%d", i%nCustomers)
		area := fmt.Sprintf("area-%d", (i*7)%nAreas)
		edges = append(edges,
			EntityRelationship{SourceType: "device", SourceId: d.ID, TargetType: "customer", TargetToken: customer, RelationshipTypeId: assigned.ID},
			EntityRelationship{SourceType: "device", SourceId: d.ID, TargetType: "area", TargetToken: area, RelationshipTypeId: assigned.ID})
		edges[len(edges)-2].Token = fmt.Sprintf("c-%d", i)
		edges[len(edges)-1].Token = fmt.Sprintf("a-%d", i)
	}
	require.NoError(t, db.CreateInBatches(&edges, 1000).Error)
	require.NoError(t, db.Exec("ANALYZE").Error)

	caller := r.member("customer:cust-0")
	got := areaTokens(t, api, caller)
	// cust-0 holds every 50th device; device i sits on area (i*7)%500: 200 devices on
	// distinct areas (7 and 500 are coprime, and i = 50k gives (350k)%500 = 50*(7k%10)).
	want := map[string]bool{}
	for i := 0; i < nDevices; i += nCustomers {
		want[fmt.Sprintf("area-%d", (i*7)%nAreas)] = true
	}
	require.Len(t, got, len(want))
	for _, a := range got {
		require.True(t, want[a], "unexpected area %s", a)
	}

	explain := func(label string, c context.Context, q func(*gorm.DB) *gorm.DB) {
		stmt := q(api.RDB.DB(c).Session(&gorm.Session{DryRun: true})).Find(&[]Area{}).Statement
		require.NoError(t, stmt.Error)
		rows := []string{}
		require.NoError(t, api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).Raw("EXPLAIN (ANALYZE, BUFFERS) "+stmt.SQL.String(), stmt.Vars...).Scan(&rows).Error)
		t.Logf("%s\n%s", label, strings.Join(rows, "\n"))
	}
	explain("scoped areas list (first page of 100)", caller, func(d *gorm.DB) *gorm.DB { return d.Limit(100) })
	explain("scoped areas by token", caller, func(d *gorm.DB) *gorm.DB { return d.Where("token IN ?", []string{"area-0", "area-7", "area-14"}) })
}
