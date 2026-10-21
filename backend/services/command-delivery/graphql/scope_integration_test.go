// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Members limited to part of the tenant (ADR-081), through the real schema on a real
// PostgreSQL: the scope predicate is `scope_anchors && ARRAY[...]::text[]`, which sqlite
// cannot evaluate, so every assertion about what a scoped member SEES lives here.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./graphql/ -run Scope -v
package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	acmeAnchor   = "customer:acme"
	globexAnchor = "customer:globex"
)

func scopeItEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ownership is the fixture's device-management: which devices exist and who owns them.
type ownership map[string][]string

func (o ownership) DeviceScopeAnchors(_ context.Context, tokens []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, t := range tokens {
		if a, ok := o[t]; ok {
			out[t] = append([]string{}, a...)
		}
	}
	return out, nil
}

// gate answers the enqueue gate from the same ownership, with an optional canned refusal
// for every device (a device-management that would say COMMAND_NOT_IN_VOCABULARY).
type gate struct {
	owners ownership
	refuse *model.EnqueueRejected
}

func (g gate) ValidateEnqueue(_ context.Context, device, _ string, _ []byte) ([]string, error) {
	anchors, ok := g.owners[device]
	if !ok {
		return nil, &model.EnqueueRejected{Code: "DEVICE_NOT_FOUND", Reason: fmt.Sprintf("device %q does not exist", device)}
	}
	if g.refuse != nil {
		return append([]string{}, anchors...), g.refuse
	}
	return append([]string{}, anchors...), nil
}

// scopeFixture: tenant A with an Acme device, a Globex device and a device both own; and a
// tenant B whose device carries Acme's anchor, which a tenant-A member must never see.
func scopeFixture(t *testing.T) (*model.Api, context.Context) {
	t.Helper()
	port, err := strconv.Atoi(scopeItEnv("DC_IT_PGPORT", "5432"))
	require.NoError(t, err)
	host, user, pass := scopeItEnv("DC_IT_PGHOST", "localhost"), scopeItEnv("DC_IT_PGUSER", "postgres"),
		scopeItEnv("DC_IT_PGPASSWORD", "postgres")
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, "dccmdscope", ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: "dccmdscope", FunctionalArea: "command-delivery"},
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
	for _, table := range []string{"commands", "command_batches", "device_gates"} {
		require.NoError(t, sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "command-delivery".%q CASCADE`, table)).Error)
	}
	owners := ownership{
		"acme-1":   {acmeAnchor, "area:north"},
		"globex-1": {globexAnchor},
		"shared-1": {globexAnchor, acmeAnchor},
		"orphan-1": {},
	}
	api := model.NewApi(mgr)
	api.EnqueueValidator = gate{owners: owners}
	api.DeviceAnchors = owners
	ctx := core.WithTenant(context.Background(), "A")
	ctx = context.WithValue(ctx, gqlcore.ContextRdbKey, mgr)
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	return api, ctx
}

func asOperator(ctx context.Context) context.Context {
	return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.CommandRead), string(auth.CommandWrite)}})
}

func asMember(ctx context.Context, anchors ...string) context.Context {
	return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.CommandRead), string(auth.CommandWrite)}, ScopeAnchors: anchors})
}

func run(ctx context.Context, query string, vars map[string]any) *gql.Response {
	return gql.MustParseSchema(SchemaContent, &SchemaResolver{}).Exec(ctx, query, "", vars)
}

type enqueued struct {
	CreateCommand struct {
		Command *struct {
			Token string `json:"token"`
		} `json:"command"`
		Rejection *struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
		} `json:"rejection"`
	} `json:"createCommand"`
}

func enqueueTo(t *testing.T, ctx context.Context, token, device string) enqueued {
	t.Helper()
	res := run(ctx, createCommandMutation, map[string]any{"request": map[string]any{
		"token": token, "deviceToken": device, "name": "reboot"}})
	require.Empty(t, res.Errors)
	var out enqueued
	require.NoError(t, json.Unmarshal(res.Data, &out))
	return out
}

func listed(t *testing.T, ctx context.Context) []string {
	t.Helper()
	res := run(ctx, `query { commands(criteria: {pageNumber: 1, pageSize: 100}) { results { token } } }`, nil)
	require.Empty(t, res.Errors)
	var out struct {
		Commands struct {
			Results []struct {
				Token string `json:"token"`
			} `json:"results"`
		} `json:"commands"`
	}
	require.NoError(t, json.Unmarshal(res.Data, &out))
	tokens := make([]string, 0)
	for _, r := range out.Commands.Results {
		tokens = append(tokens, r.Token)
	}
	sort.Strings(tokens)
	return tokens
}

// An Acme member reads Acme's commands — including one sent to a device Acme shares with
// Globex — and nothing else: not Globex's, not an unstamped command from before the stamp
// existed, and not another tenant's row that happens to carry Acme's anchor. The unscoped
// operator is the negative control: the fixture really does hold the rows the member
// must not see.
func TestScopedCommandsReturnsOnlyAcme(t *testing.T) {
	api, ctx := scopeFixture(t)
	op := asOperator(ctx)
	require.NotNil(t, enqueueTo(t, op, "c-acme", "acme-1").CreateCommand.Command)
	require.NotNil(t, enqueueTo(t, op, "c-globex", "globex-1").CreateCommand.Command)
	require.NotNil(t, enqueueTo(t, op, "c-shared", "shared-1").CreateCommand.Command)
	// A row from before the stamp: no anchors at all.
	legacy := &model.Command{DeviceToken: "acme-1", Name: "reboot", Status: model.CommandQueued.String()}
	legacy.Token = "c-legacy"
	require.NoError(t, api.RDB.DB(op).Create(legacy).Error)
	// Tenant B, with Acme's anchor on it.
	other := core.WithTenant(context.Background(), "B")
	foreign := &model.Command{DeviceToken: "acme-1", Name: "reboot", Status: model.CommandQueued.String(),
		ScopeAnchors: rdb.Anchors{acmeAnchor}}
	foreign.Token = "c-tenant-b"
	require.NoError(t, api.RDB.DB(other).Create(foreign).Error)

	require.Equal(t, []string{"c-acme", "c-globex", "c-legacy", "c-shared"}, listed(t, op),
		"control: the operator sees every tenant-A command")
	require.Equal(t, []string{"c-acme", "c-shared"}, listed(t, asMember(ctx, acmeAnchor)))
	require.Equal(t, []string{"c-globex", "c-shared"}, listed(t, asMember(ctx, globexAnchor)))

	// By token: a token naming someone else's command is not returned at all.
	res := run(asMember(ctx, acmeAnchor), `query($t: [String!]!) { commandsByToken(tokens: $t) { token } }`,
		map[string]any{"t": []any{"c-acme", "c-globex", "c-legacy", "c-tenant-b"}})
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"commandsByToken":[{"token":"c-acme"}]}`, string(res.Data))
}

// The stamp is the device's anchors as the gate reported them, written once at enqueue.
func TestEnqueueStampsTheDeviceAnchors(t *testing.T) {
	api, ctx := scopeFixture(t)
	require.NotNil(t, enqueueTo(t, asMember(ctx, acmeAnchor), "c-own", "acme-1").CreateCommand.Command)
	require.NotNil(t, enqueueTo(t, asOperator(ctx), "c-orphan", "orphan-1").CreateCommand.Command)
	got, err := api.CommandsByToken(asOperator(ctx), []string{"c-own", "c-orphan"})
	require.NoError(t, err)
	stamps := map[string]rdb.Anchors{}
	for _, c := range got {
		stamps[c.Token] = c.ScopeAnchors
	}
	require.Equal(t, rdb.Anchors{acmeAnchor, "area:north"}, stamps["c-own"])
	require.Equal(t, rdb.Anchors{}, stamps["c-orphan"], "an owner-less device is stamped empty, not NULL")
	require.Equal(t, []string{"c-own"}, listed(t, asMember(ctx, acmeAnchor)),
		"the member sees the command it just sent")
}

// 🔴 A device outside the member's scope is answered EXACTLY as a device that does not
// exist — whatever the gate would have said about it. Here the gate would refuse the
// command as not in the vocabulary; relaying that for Globex's device would confirm the
// device exists and describe it.
func TestEnqueueToForeignDeviceIsNotFound(t *testing.T) {
	api, ctx := scopeFixture(t)
	member := asMember(ctx, acmeAnchor)

	missing := enqueueTo(t, member, "c-missing", "no-such-device").CreateCommand.Rejection
	require.NotNil(t, missing)

	foreign := enqueueTo(t, member, "c-foreign", "globex-1").CreateCommand.Rejection
	require.NotNil(t, foreign, "an Acme member enqueued to Globex's device")
	// The same answer, word for word, with only the token the caller sent substituted.
	notFound := func(device string) string {
		return fmt.Sprintf("cannot enqueue command: device %q does not exist", device)
	}
	require.Equal(t, "DEVICE_NOT_FOUND", missing.Code)
	require.Equal(t, notFound("no-such-device"), missing.Reason, "control: the gate's own missing-device answer")
	require.Equal(t, missing.Code, foreign.Code, "a foreign device must read exactly as a missing one")
	require.Equal(t, notFound("globex-1"), foreign.Reason, "a foreign device must read exactly as a missing one")

	api.EnqueueValidator = gate{owners: api.DeviceAnchors.(ownership), refuse: &model.EnqueueRejected{
		Code: "COMMAND_NOT_IN_VOCABULARY", Reason: `device "globex-1" accepts no command "reboot"`}}
	refused := enqueueTo(t, member, "c-foreign-2", "globex-1").CreateCommand.Rejection
	require.NotNil(t, refused)
	require.Equal(t, "DEVICE_NOT_FOUND", refused.Code, "the gate's verdict about a foreign device leaked")
	require.Equal(t, notFound("globex-1"), refused.Reason)

	// An orphan is no more the member's than Globex's is.
	require.Equal(t, "DEVICE_NOT_FOUND", enqueueTo(t, member, "c-orphan", "orphan-1").CreateCommand.Rejection.Code)

	// Nothing was written for any of them.
	got, err := api.CommandsByToken(asOperator(ctx), []string{"c-missing", "c-foreign", "c-foreign-2", "c-orphan"})
	require.NoError(t, err)
	require.Empty(t, got)

	// The counterweight: the same member, the same gate, its OWN device — the gate's verdict
	// is relayed as it always was.
	own := enqueueTo(t, member, "c-own", "acme-1").CreateCommand.Rejection
	require.NotNil(t, own)
	require.Equal(t, "COMMAND_NOT_IN_VOCABULARY", own.Code)
}

// A member's repeat of its own token is the idempotent replay every caller gets; a token
// held by a command the member cannot see is TOKEN_IN_USE, and the answer names nothing
// about that command.
func TestScopedEnqueueReplaysItsOwnTokenAndRefusesAnother(t *testing.T) {
	api, ctx := scopeFixture(t)
	member := asMember(ctx, acmeAnchor)
	first := enqueueTo(t, member, "c-mine", "acme-1").CreateCommand.Command
	require.NotNil(t, first)
	again := enqueueTo(t, member, "c-mine", "acme-1").CreateCommand.Command
	require.NotNil(t, again, "a member's replay of its own token was refused")
	require.Equal(t, "c-mine", again.Token)

	require.NotNil(t, enqueueTo(t, asOperator(ctx), "c-theirs", "globex-1").CreateCommand.Command)
	taken := enqueueTo(t, member, "c-theirs", "acme-1").CreateCommand.Rejection
	require.NotNil(t, taken)
	require.Equal(t, string(model.RejectTokenInUse), taken.Code)

	got, err := api.CommandsByToken(asOperator(ctx), []string{"c-mine", "c-theirs"})
	require.NoError(t, err)
	require.Len(t, got, 2, "a replay or a refused token wrote a second row")
}

// 🔴 The held-command ceiling bounds the TENANT. A member who could count only its own
// commands could enqueue past a ceiling Globex's backlog has already reached.
func TestScopedEnqueueIsBoundByTheTenantsCeiling(t *testing.T) {
	api, ctx := scopeFixture(t)
	op := asOperator(ctx)
	require.NotNil(t, enqueueTo(t, op, "g-1", "globex-1").CreateCommand.Command)
	require.NotNil(t, enqueueTo(t, op, "g-2", "globex-1").CreateCommand.Command)
	api.DefaultHeldCommandCeiling = 2
	api.DeliveryMachineryReserve = 0.0001

	refused := enqueueTo(t, asMember(ctx, acmeAnchor), "a-1", "acme-1").CreateCommand.Rejection
	require.NotNil(t, refused, "a member enqueued past the tenant's ceiling")
	require.Equal(t, string(model.RejectHeldCeilingExceeded), refused.Code)
}

const holdQuery = `query($d: String!) { deviceCommandHold(deviceToken: $d) { disabled heldCount } }`

// The hold is answered for a device the member owns now — counting every command held for
// it, not only the member's own — and NOT_FOUND for any other.
func TestScopedDeviceCommandHold(t *testing.T) {
	api, ctx := scopeFixture(t)
	op := asOperator(ctx)
	require.NotNil(t, enqueueTo(t, op, "s-1", "shared-1").CreateCommand.Command)
	legacy := &model.Command{DeviceToken: "shared-1", Name: "reboot", Status: model.CommandQueued.String()}
	legacy.Token = "s-legacy"
	require.NoError(t, api.RDB.DB(op).Create(legacy).Error)
	for _, d := range []string{"shared-1", "globex-1"} {
		_, err := api.SetDeviceDisabled(op, d, true)
		require.NoError(t, err)
	}

	res := run(asMember(ctx, acmeAnchor), holdQuery, map[string]any{"d": "shared-1"})
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"deviceCommandHold":{"disabled":true,"heldCount":2}}`, string(res.Data))

	for _, d := range []string{"globex-1", "no-such-device"} {
		res = run(asMember(ctx, acmeAnchor), holdQuery, map[string]any{"d": d})
		require.Len(t, res.Errors, 1, d)
		require.True(t, errors.Is(res.Errors[0].ResolverError, gorm.ErrRecordNotFound),
			"%s: want NOT_FOUND, got %v", d, res.Errors[0])
	}
}

// With nothing to ask who owns a device, a scoped member is refused rather than served:
// an enqueue would be written with no stamp it could see, and a hold could not be decided.
// An unscoped caller is unaffected.
func TestScopedMemberIsRefusedWhenOwnershipCannotBeAsked(t *testing.T) {
	api, ctx := scopeFixture(t)
	api.EnqueueValidator = nil
	api.DeviceAnchors = nil
	member := asMember(ctx, acmeAnchor)

	res := run(member, createCommandMutation, map[string]any{"request": map[string]any{
		"token": "c-1", "deviceToken": "acme-1", "name": "reboot"}})
	require.Len(t, res.Errors, 1)
	require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrScopeUnsupported), "%v", res.Errors[0])

	res = run(member, holdQuery, map[string]any{"d": "acme-1"})
	require.Len(t, res.Errors, 1)
	require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrScopeUnsupported), "%v", res.Errors[0])

	require.NotNil(t, enqueueTo(t, asOperator(ctx), "c-2", "acme-1").CreateCommand.Command,
		"control: an unscoped caller still enqueues with no gate")
}

// The command search's time range, through the served schema on the real engine: a member
// limited to Acme reads only Acme's commands inside the range, the range's total is exact,
// and the planner can serve one device's range from idx_commands_device_time (the search's
// predicate must be written as the index's key expression, or the index is never used).
func TestScopedCommandsTimeRangeOnPostgres(t *testing.T) {
	api, ctx := scopeFixture(t)
	op := asOperator(ctx)
	require.NotNil(t, enqueueTo(t, op, "c-acme-old", "acme-1").CreateCommand.Command)
	require.NotNil(t, enqueueTo(t, op, "c-acme-new", "acme-1").CreateCommand.Command)
	require.NotNil(t, enqueueTo(t, op, "c-shared", "shared-1").CreateCommand.Command)
	require.NotNil(t, enqueueTo(t, op, "c-globex", "globex-1").CreateCommand.Command)
	now := time.Now().UTC().Truncate(time.Second)
	// c-acme-old was queued two days ago and never sent; c-globex was sent an hour ago.
	db := api.RDB.DB(op)
	require.NoError(t, db.Model(&model.Command{}).Where("token = ?", "c-acme-old").
		Update("queued_time", now.Add(-48*time.Hour)).Error)
	require.NoError(t, db.Model(&model.Command{}).Where("token = ?", "c-globex").
		Update("sent_time", now.Add(-time.Hour)).Error)

	ranged := func(ctx context.Context, device *string, from, to time.Time) ([]string, int) {
		t.Helper()
		criteria := map[string]any{"pageNumber": 1, "pageSize": 100,
			"startTime": from.Format(time.RFC3339), "endTime": to.Format(time.RFC3339)}
		if device != nil {
			criteria["deviceToken"] = *device
		}
		res := run(ctx, `query($c: CommandSearchCriteria!) { commands(criteria: $c) {
			results { token } pagination { totalRecords } } }`, map[string]any{"c": criteria})
		require.Empty(t, res.Errors)
		var out struct {
			Commands struct {
				Results []struct {
					Token string `json:"token"`
				} `json:"results"`
				Pagination struct {
					TotalRecords int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"commands"`
		}
		require.NoError(t, json.Unmarshal(res.Data, &out))
		tokens := []string{}
		for _, r := range out.Commands.Results {
			tokens = append(tokens, r.Token)
		}
		sort.Strings(tokens)
		return tokens, out.Commands.Pagination.TotalRecords
	}
	lastTwoHours := func(ctx context.Context, device *string) []string {
		tokens, total := ranged(ctx, device, now.Add(-2*time.Hour), now.Add(time.Minute))
		require.Equal(t, len(tokens), total)
		return tokens
	}
	acme := "acme-1"
	require.Equal(t, []string{"c-acme-new", "c-globex", "c-shared"}, lastTwoHours(op, nil), "control: the operator")
	require.Equal(t, []string{"c-acme-new"}, lastTwoHours(op, &acme))
	old, _ := ranged(op, &acme, now.Add(-49*time.Hour), now.Add(-47*time.Hour))
	require.Equal(t, []string{"c-acme-old"}, old, "a command never sent is placed at its queue time")
	require.Equal(t, []string{"c-acme-new", "c-shared"}, lastTwoHours(asMember(ctx, acmeAnchor), nil))
	require.Equal(t, []string{"c-globex", "c-shared"}, lastTwoHours(asMember(ctx, globexAnchor), nil))

	bad := run(op, `query { commands(criteria: {pageNumber: 1, pageSize: 1, startTime: "yesterday"}) { results { token } } }`, nil)
	require.NotEmpty(t, bad.Errors, "a bound that is not RFC 3339 is an error, never an unbounded search")

	// The plan, over a history big enough for the planner to choose: 50 devices of tenant A with
	// 200 commands each, spread over 200 days. One device's one day is then ~1 row in 10,000, which
	// only the device-and-time index reaches without reading the device's whole history.
	require.NoError(t, db.Exec(`INSERT INTO "command-delivery".commands
		(created_at, updated_at, tenant_id, token, device_token, name, status, queued_time)
		SELECT now(), now(), 'A', 'bulk-' || g, 'bulk-dev-' || (g % 50), 'reboot', 'EXPIRED',
			now() - (g % 200) * interval '1 day'
		FROM generate_series(1, 10000) AS g`).Error)
	require.NoError(t, db.Exec(`ANALYZE "command-delivery".commands`).Error)
	var plan []string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return tx.Raw(`EXPLAIN SELECT id FROM "command-delivery".commands WHERE tenant_id = 'A' AND device_token = 'acme-1' ` +
			`AND COALESCE(sent_time, queued_time) >= now() - interval '1 day' AND COALESCE(sent_time, queued_time) <= now() ` +
			`AND deleted_at IS NULL`).Scan(&plan).Error
	}))
	require.Contains(t, fmt.Sprint(plan), "idx_commands_device_time")
	// And the range itself bounds the scan: the time test is an Index Cond, not a Filter applied to
	// every row of the device that the index found.
	bounded := false
	for _, line := range plan {
		if strings.Contains(line, "Index Cond") && strings.Contains(line, "COALESCE(sent_time, queued_time)") {
			bounded = true
		}
	}
	require.True(t, bounded, "the time range is not part of the index condition: %v", plan)
}
