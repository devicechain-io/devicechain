// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newBackfillApi builds an Api over in-memory sqlite holding the job table. The statements that
// need PostgreSQL (the guarded transitions, the active-job index) are the integration tests'.
func newBackfillApi(t *testing.T) (*model.Api, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.CalculatedBackfill{}))
	return model.NewApi(&rdb.RdbManager{Database: db}), db
}

func seedJob(t *testing.T, db *gorm.DB, tenant, token, profile, status string, mutate ...func(*model.CalculatedBackfill)) {
	t.Helper()
	now := time.Now().UTC()
	j := &model.CalculatedBackfill{
		Token: token, ProfileToken: profile, ProfileVersionToken: profile + "@1", ProfileVersion: 1,
		MetricKeys: json.RawMessage(`["power"]`), Plan: json.RawMessage(`{}`), Warnings: json.RawMessage(`["w"]`),
		Skipped: json.RawMessage(`{"missing_input":4,"ambiguous":2}`), Status: status, Phase: model.BackfillPhaseFill,
		RangeFrom: now.Add(-time.Hour), RangeTo: now, CreatedAt: now, UpdatedAt: now,
	}
	j.TenantId = tenant
	for _, m := range mutate {
		m(j)
	}
	require.NoError(t, db.WithContext(core.WithTenant(context.Background(), tenant)).Create(j).Error)
}

func execAs(t *testing.T, api *model.Api, tenant string, anchors []string, authorities []string, query string, vars map[string]any) *gql.Response {
	t.Helper()
	schema, err := gql.ParseSchema(SchemaContent, &SchemaResolver{})
	require.NoError(t, err)
	ctx := core.WithTenant(context.Background(), tenant)
	ctx = auth.WithClaims(ctx, &auth.Claims{Tenant: tenant, Username: "op@example.com", TokenType: auth.TokenTypeAccess,
		Authorities: authorities, ScopeAnchors: anchors})
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	return schema.Exec(ctx, query, "", vars)
}

const (
	startDoc  = `mutation($in: CalculatedMetricBackfillInput!) { startCalculatedMetricBackfill(input: $in) { token } }`
	cancelDoc = `mutation { cancelCalculatedMetricBackfill(token: "j1") { token } }`
	readDoc   = `{ calculatedMetricBackfill(token: "j1") { token status } }`
	listDoc   = `{ calculatedMetricBackfills { nodes { token } } }`
)

func startVars() map[string]any {
	return map[string]any{"in": map[string]any{"profileToken": "meter",
		"from": "2026-10-01T00:00:00Z", "to": "2026-10-02T00:00:00Z"}}
}

// Who may do what: start and cancel are device:write, read and list are event:read, and a
// customer-scoped caller is refused by all four even holding both authorities. Each refusal is
// FORBIDDEN, and the control (the right authority, unscoped) is not refused, so the refusals are
// the gate speaking and not a broken fixture.
func TestBackfillAuthorityMatrix(t *testing.T) {
	api, db := newBackfillApi(t)
	seedJob(t, db, "t1", "j1", "meter", model.BackfillSucceeded)
	write, read := string(auth.DeviceWrite), string(auth.EventRead)
	ops := []struct {
		name, query string
		vars        map[string]any
		need        string
	}{
		{"start", startDoc, startVars(), write},
		{"cancel", cancelDoc, nil, write},
		{"read", readDoc, nil, read},
		{"list", listDoc, nil, read},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			other := read
			if op.need == read {
				other = write
			}
			forbidden := func(resp *gql.Response, why string) {
				t.Helper()
				require.NotEmpty(t, resp.Errors, why)
				assert.ErrorIs(t, resp.Errors[0].ResolverError, auth.ErrForbidden, why)
			}
			forbidden(execAs(t, api, "t1", nil, []string{other}, op.query, op.vars), "the other authority does not carry it")
			forbidden(execAs(t, api, "t1", nil, nil, op.query, op.vars), "no authority")
			forbidden(execAs(t, api, "t1", []string{"customer:acme"}, []string{write, read}, op.query, op.vars),
				"a customer-scoped caller is refused whatever it holds")
			// The control: the right authority passes the gate. Whatever the operation then
			// answers, it is not a refusal of the caller.
			resp := execAs(t, api, "t1", nil, []string{op.need}, op.query, op.vars)
			for _, e := range resp.Errors {
				assert.NotErrorIs(t, e.ResolverError, auth.ErrForbidden, "control: %s", e.Message)
			}
		})
	}
	// An unauthenticated request is refused distinguishably.
	schema, err := gql.ParseSchema(SchemaContent, &SchemaResolver{})
	require.NoError(t, err)
	ctx := context.WithValue(core.WithTenant(context.Background(), "t1"), gqlcore.ContextApiKey, api)
	resp := schema.Exec(ctx, readDoc, "", nil)
	require.NotEmpty(t, resp.Errors)
	assert.ErrorIs(t, resp.Errors[0].ResolverError, auth.ErrUnauthenticated)
}

// A misnamed field of the input is a request error, not a quietly dropped key: a start that lost
// its `to` would otherwise be indistinguishable from a correct one.
func TestBackfillInputRejectsAnUnknownField(t *testing.T) {
	api, _ := newBackfillApi(t)
	vars := startVars()
	vars["in"].(map[string]any)["deviceToken"] = "d1" // the singular, which the input does not have
	resp := execAs(t, api, "t1", nil, []string{string(auth.DeviceWrite)}, startDoc, vars)
	require.NotEmpty(t, resp.Errors)
	assert.Contains(t, resp.Errors[0].Message, "deviceToken")
}

// Counters are Float: a long job's counters pass 2^31, which a GraphQL Int cannot carry.
func TestBackfillCountersAreFloatsPastTwoToTheThirtyFirst(t *testing.T) {
	api, db := newBackfillApi(t)
	const big = int64(5_000_000_000)
	total := int64(8_000_000_000)
	seedJob(t, db, "t1", "j1", "meter", model.BackfillRunning, func(j *model.CalculatedBackfill) {
		j.Samples, j.Computed, j.Written, j.AlreadyPresent = big, big+1, big+2, big+3
		j.WindowsCompressed, j.WindowsBeyondRetention, j.WindowsRefreshed = big+4, big+5, big+6
		j.DevicesDone, j.DevicesTotal = big, &total
		j.Skipped = json.RawMessage(`{"missing_input":5000000007,"ambiguous":3,"out_of_range":0}`)
	})
	resp := execAs(t, api, "t1", nil, []string{string(auth.EventRead)}, `{ calculatedMetricBackfill(token: "j1") {
		samples computed written alreadyPresent windowsCompressed windowsBeyondRetention windowsRefreshed
		devicesDone devicesTotal progress storedInputPrecision metricKeys warnings status profileVersion
		skipped { reason count } } }`, nil)
	require.Empty(t, resp.Errors)
	var out struct {
		J struct {
			Samples, Computed, Written, AlreadyPresent                  float64
			WindowsCompressed, WindowsBeyondRetention, WindowsRefreshed float64
			DevicesDone, DevicesTotal, Progress                         float64
			StoredInputPrecision, Status                                string
			MetricKeys, Warnings                                        []string
			ProfileVersion                                              int
			Skipped                                                     []struct {
				Reason string
				Count  float64
			}
		} `json:"calculatedMetricBackfill"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &out))
	j := out.J
	assert.EqualValues(t, big, j.Samples)
	assert.EqualValues(t, big+1, j.Computed)
	assert.EqualValues(t, big+2, j.Written)
	assert.EqualValues(t, big+3, j.AlreadyPresent)
	assert.EqualValues(t, big+4, j.WindowsCompressed)
	assert.EqualValues(t, big+5, j.WindowsBeyondRetention)
	assert.EqualValues(t, big+6, j.WindowsRefreshed)
	assert.EqualValues(t, big, j.DevicesDone)
	assert.EqualValues(t, total, j.DevicesTotal)
	assert.InDelta(t, 0.625, j.Progress, 1e-9)
	assert.Equal(t, "8 decimal places", j.StoredInputPrecision)
	assert.Equal(t, []string{"power"}, j.MetricKeys)
	assert.Equal(t, []string{"w"}, j.Warnings)
	assert.Equal(t, 1, j.ProfileVersion)
	assert.Equal(t, "RUNNING", j.Status)
	require.Len(t, j.Skipped, 2, "a reason with no count is not listed")
	assert.Equal(t, "ambiguous", j.Skipped[0].Reason, "per reason, in reason order, ambiguous included")
	assert.EqualValues(t, 3, j.Skipped[0].Count)
	assert.EqualValues(t, 5000000007, j.Skipped[1].Count)
}

// Reading: a token the tenant has no job for is null, not an error; another tenant's job is the
// same null.
func TestBackfillReadIsTenantScoped(t *testing.T) {
	api, db := newBackfillApi(t)
	seedJob(t, db, "t1", "j1", "meter", model.BackfillSucceeded)
	seedJob(t, db, "t2", "j2", "meter", model.BackfillSucceeded)
	read := []string{string(auth.EventRead)}

	resp := execAs(t, api, "t1", nil, read, `{ calculatedMetricBackfill(token: "j1") { token } }`, nil)
	require.Empty(t, resp.Errors)
	assert.JSONEq(t, `{"calculatedMetricBackfill":{"token":"j1"}}`, string(resp.Data))
	for _, token := range []string{"j2", "nope"} {
		resp = execAs(t, api, "t1", nil, read, `{ calculatedMetricBackfill(token: "`+token+`") { token } }`, nil)
		require.Empty(t, resp.Errors, token)
		assert.JSONEq(t, `{"calculatedMetricBackfill":null}`, string(resp.Data), token)
	}
}

type listOut struct {
	L struct {
		Nodes      []struct{ Token string }
		NextCursor *string
	} `json:"calculatedMetricBackfills"`
}

func list(t *testing.T, api *model.Api, tenant, args string) (listOut, *gql.Response) {
	t.Helper()
	resp := execAs(t, api, tenant, nil, []string{string(auth.EventRead)},
		`{ calculatedMetricBackfills`+args+` { nodes { token } nextCursor } }`, nil)
	var out listOut
	if len(resp.Errors) == 0 {
		require.NoError(t, json.Unmarshal(resp.Data, &out))
	}
	return out, resp
}

func tokens(o listOut) []string {
	out := []string{}
	for _, n := range o.L.Nodes {
		out = append(out, n.Token)
	}
	return out
}

// The list is newest first, pages without skipping or repeating, filters by profile and status,
// refuses a page size or cursor it cannot honour, and never shows another tenant's jobs.
func TestBackfillListOrderingPagingFiltersAndIsolation(t *testing.T) {
	api, db := newBackfillApi(t)
	seedJob(t, db, "t1", "a", "meter", model.BackfillSucceeded)
	seedJob(t, db, "t1", "b", "meter", model.BackfillFailed)
	seedJob(t, db, "t1", "c", "pump", model.BackfillCancelled)
	seedJob(t, db, "t1", "d", "meter", model.BackfillRunning)
	seedJob(t, db, "t2", "x", "meter", model.BackfillRunning)

	all, _ := list(t, api, "t1", "")
	assert.Equal(t, []string{"d", "c", "b", "a"}, tokens(all), "newest first, and not tenant t2's x")
	assert.Nil(t, all.L.NextCursor)

	p1, _ := list(t, api, "t1", "(first: 3)")
	assert.Equal(t, []string{"d", "c", "b"}, tokens(p1))
	require.NotNil(t, p1.L.NextCursor)
	p2, _ := list(t, api, "t1", `(first: 3, after: "`+*p1.L.NextCursor+`")`)
	assert.Equal(t, []string{"a"}, tokens(p2))
	assert.Nil(t, p2.L.NextCursor, "the last page has no cursor")

	exact, _ := list(t, api, "t1", "(first: 4)")
	assert.Nil(t, exact.L.NextCursor, "a page that holds exactly the rest has no cursor either")

	byProfile, _ := list(t, api, "t1", `(profileToken: "meter")`)
	assert.Equal(t, []string{"d", "b", "a"}, tokens(byProfile))
	byStatus, _ := list(t, api, "t1", `(statuses: [FAILED, RUNNING])`)
	assert.Equal(t, []string{"d", "b"}, tokens(byStatus))
	other, _ := list(t, api, "t2", "")
	assert.Equal(t, []string{"x"}, tokens(other))

	for _, args := range []string{"(first: 0)", "(first: -1)", "(first: 101)", `(after: "junk")`, `(after: "0")`} {
		_, resp := list(t, api, "t1", args)
		require.NotEmpty(t, resp.Errors, args)
		assert.Equal(t, "INVALID_VALUE", resp.Errors[0].Extensions["code"], args)
	}
}
