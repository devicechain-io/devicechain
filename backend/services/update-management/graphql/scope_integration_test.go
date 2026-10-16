// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Members limited to part of the tenant (ADR-081) reading update assignments, through the real
// schema on a real PostgreSQL: the scope predicate is `scope_anchors && ARRAY[...]::text[]`,
// which sqlite cannot evaluate.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./graphql/ -run Scope -v
package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/devicechain-io/dc-update-management/schema"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// ownedDevices is the fixture's device-management: every device on the gateway profile,
// owned as the map says.
type ownedDevices map[string][]string

func (o ownedDevices) DeviceOf(_ context.Context, _ string, device string) (model.DeviceFacts, bool, error) {
	anchors, ok := o[device]
	return model.DeviceFacts{Profile: "gateway", Anchors: anchors}, ok, nil
}

func pgPlane(t *testing.T) *plane {
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
	instance := fmt.Sprintf("updatescope%d", time.Now().UnixNano())
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "update-management"},
		Migrations:   schema.Migrations,
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
	store, err := blob.New(ctx, blob.Config{Backend: blob.BackendFilesystem, Directory: t.TempDir()}, "inst")
	require.NoError(t, err)
	api := model.NewApi(mgr, store, model.Limits{
		MaxArtifactBytes: 1 << 20, ConfiguredMaxArtifactBytes: 1 << 20, MaxTenantArtifactBytes: 1 << 30,
	})
	api.Profiles = fakeProfiles{}
	api.AllowUnverified = true
	api.Desired = &sink{}
	api.Devices = ownedDevices{
		"dev-acme":   {"customer:acme", "area:north"},
		"dev-globex": {"customer:globex"},
		"dev-shared": {"customer:globex", "customer:acme"},
		"dev-orphan": {},
	}
	return &plane{t: t, api: api, schema: gql.MustParseSchema(SchemaContent, &SchemaResolver{Area: "update-management", AllowUnverifiedArtifacts: true})}
}

func (p *plane) member(anchors ...string) context.Context {
	ctx := context.WithValue(context.Background(), gqlcore.ContextApiKey, p.api)
	ctx = core.WithTenant(ctx, "acme")
	return auth.WithClaims(ctx, &auth.Claims{Tenant: "acme", TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.ArtifactRead), string(auth.ArtifactWrite)}, ScopeAnchors: anchors})
}

const scopedAssignmentsQuery = `{ updateAssignments(criteria:{pageNumber:1,pageSize:50}) {
  results { deviceToken artifact { token } attempt { state } } pagination { totalRecords } } }`

func assignedDevices(t *testing.T, p *plane, ctx context.Context) ([]string, int) {
	t.Helper()
	resp := p.exec(ctx, scopedAssignmentsQuery, nil)
	require.Empty(t, resp.Errors)
	var out struct {
		UpdateAssignments struct {
			Results []struct {
				DeviceToken string
				Artifact    struct{ Token string }
				Attempt     struct{ State string }
			}
			Pagination struct{ TotalRecords int }
		}
	}
	require.NoError(t, json.Unmarshal(resp.Data, &out))
	devices := make([]string, 0)
	for _, r := range out.UpdateAssignments.Results {
		require.Equal(t, "fw-1", r.Artifact.Token, "the joined artifact did not resolve")
		require.Equal(t, "QUEUED", r.Attempt.State, "the joined attempt did not resolve")
		devices = append(devices, r.DeviceToken)
	}
	sort.Strings(devices)
	return devices, out.UpdateAssignments.Pagination.TotalRecords
}

// An Acme member reads the assignments made to the devices Acme owned when they were made —
// including one it shares with Globex — with their artifact and attempt, and nothing else: not
// Globex's, not an owner-less device's, and a token naming another's answers null like a token
// naming nothing. The unscoped operator is the negative control.
func TestScopedAssignmentsReturnOnlyAcme(t *testing.T) {
	p := pgPlane(t)
	p.available("fw-1", []byte("image"))
	op := p.ctx("acme", auth.ArtifactRead, auth.ArtifactWrite)
	tokens := map[string]string{}
	for _, d := range []string{"dev-acme", "dev-globex", "dev-shared", "dev-orphan"} {
		resp := p.exec(op, assignMutation, map[string]any{"d": d, "a": "fw-1"})
		require.Empty(t, resp.Errors, d)
		var got struct{ AssignArtifact struct{ Token string } }
		require.NoError(t, json.Unmarshal(resp.Data, &got))
		tokens[d] = got.AssignArtifact.Token
	}

	all, total := assignedDevices(t, p, op)
	require.Equal(t, []string{"dev-acme", "dev-globex", "dev-orphan", "dev-shared"}, all, "control")
	require.Equal(t, 4, total)

	mine, total := assignedDevices(t, p, p.member("customer:acme"))
	require.Equal(t, []string{"dev-acme", "dev-shared"}, mine)
	require.Equal(t, 2, total, "the page count must be the member's, not the tenant's")

	one := `query($t: String!) { updateAssignment(token: $t) { deviceToken } }`
	resp := p.exec(p.member("customer:acme"), one, map[string]any{"t": tokens["dev-globex"]})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateAssignment":null}`, string(resp.Data))
	resp = p.exec(p.member("customer:acme"), one, map[string]any{"t": tokens["dev-acme"]})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateAssignment":{"deviceToken":"dev-acme"}}`, string(resp.Data))

	// The stamp, as written.
	var rows []model.UpdateAssignment
	require.NoError(t, p.api.RDB.DB(op).Order("device_token").Find(&rows).Error)
	stamps := map[string]rdb.Anchors{}
	for _, r := range rows {
		stamps[r.DeviceToken] = r.ScopeAnchors
	}
	require.Equal(t, rdb.Anchors{"customer:acme", "area:north"}, stamps["dev-acme"])
	require.Equal(t, rdb.Anchors{}, stamps["dev-orphan"])
}

// A member may read assignments but write none, holding artifact:write or not: assigning and
// cancelling refuse it with the scope refusal before anything is read.
func TestScopedMembersCannotAssignOrCancel(t *testing.T) {
	p := pgPlane(t)
	p.available("fw-1", bytes.Repeat([]byte("i"), 4))
	member := p.member("customer:acme")
	require.Equal(t, auth.ErrScopeUnsupported.Error(), code(p.exec(member, assignMutation, map[string]any{"d": "dev-acme", "a": "fw-1"})))
	require.Equal(t, auth.ErrScopeUnsupported.Error(), code(p.exec(member, `mutation { cancelUpdate(assignmentToken:"x"){ token } }`, nil)))
}
