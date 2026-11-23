// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const fleetWireCreate = `mutation ($request: FleetOperationCreateRequest!) {
  createFleetOperation(request: $request) {
    token name kind dispatch evidence targetCount planDigest contractDigest contractVersion contract initiator
    safety { maxPostsPerSecond effectivePostsPerSecond settleDeadlineSeconds failureThreshold }
    progress { planned refused posted }
    evidenceCapability { reportLog brokerFailClosed }
    executor
  }
}`

const fleetWirePlan = `query ($request: FleetOperationCreateRequest!) {
  planFleetOperation(request: $request) {
    admittedCount refusedCount requestDigest planDigest contractDigest
    refusals { ordinal token code } refusalCounts { code count }
  }
}`

func fleetWireRequest(token string, devices ...string) map[string]any {
	toks := make([]any, len(devices))
	for i, d := range devices {
		toks[i] = d
	}
	return map[string]any{"request": map[string]any{
		"token": token, "name": "Move " + token, "kind": "config.set",
		"configSet":    map[string]any{"key": "site", "value": "north", "valueType": "STRING"},
		"deviceTokens": toks,
	}}
}

// fleetWireCtx is a database on a shared-cache DSN, so the create transaction and the reads that
// follow it see one database, with devices d1 and d2 in tenant acme.
func fleetWireCtx(t *testing.T, claims *auth.Claims) (context.Context, *model.Api) {
	t.Helper()
	db := sqlitetest.OpenDSN(t, sqlitetest.MemoryDSN(t), nil)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.EntityGroupFacetRef{}, &model.Device{}, &model.DeviceAlias{}, &model.DeviceType{}, &model.DeviceProfile{},
		&model.DeviceProfileVersion{}, &model.AutomationReviewPolicy{}, &model.AutomationReviewPolicyChange{}, &model.AutomationReviewBreakGlass{},
		&model.DeviceProfileActivation{}, &model.DeviceCredential{}, &model.ProvisioningProfile{},
		&model.FleetOperation{}, &model.FleetOperationRuntime{}, &model.FleetOperationContract{}, &model.FleetOperationLine{}, &model.FleetOperationEvent{}))
	api := keyedApi(db)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "sensor"})
	require.NoError(t, err)
	for _, tok := range []string{"d1", "d2"} {
		_, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: tok, DeviceTypeToken: "sensor"})
		require.NoError(t, err)
	}
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, api)
	return auth.WithClaims(ctx, claims), api
}

func person(authorities ...auth.Authority) *auth.Claims {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	return &auth.Claims{Username: "alice@example.com", TokenType: auth.TokenTypeAccess, Authorities: strs}
}

func fleetSchema() *gqlcore.Schema {
	return gqlcore.MustParseSchema(SchemaContentWithFleetOperations, &SchemaResolver{})
}

func execFleet(t *testing.T, ctx context.Context, query string, vars map[string]any) (json.RawMessage, []string) {
	t.Helper()
	res := fleetSchema().Exec(ctx, query, "", vars)
	codes := []string{}
	for _, e := range res.Errors {
		code := ""
		if e.Extensions != nil {
			code, _ = e.Extensions["code"].(string)
		}
		codes = append(codes, code+"|"+e.Message)
	}
	return res.Data, codes
}

func withEngine(t *testing.T) {
	t.Helper()
	restore := model.OpenFleetKindsForTest(func(string) bool { return true })
	t.Cleanup(restore)
}

func TestTheFleetFragmentIsNotServedUntilTheEngineIs(t *testing.T) {
	require.NotContains(t, ServedSchema(), "createFleetOperation", "an unserved engine must not be reachable")
	require.NotContains(t, ServedSchema(), "FleetOperationDispatch")
	require.NotContains(t, SchemaContent, "FleetOperationDispatch")
	// The fragment itself must parse with the base schema, and the base schema without it still does.
	require.Contains(t, SchemaContentWithFleetOperations, "createFleetOperation")
	_ = fleetSchema()
	_ = gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
}

func TestPlanAndCreateOverTheWire(t *testing.T) {
	withEngine(t)
	ctx, _ := fleetWireCtx(t, person(auth.DeviceWrite, auth.FleetOperate, auth.DeviceRead))

	data, errs := execFleet(t, ctx, fleetWirePlan, fleetWireRequest("op1", "d1", "ghost", "d2"))
	require.Empty(t, errs)
	var plan struct {
		P struct {
			AdmittedCount, RefusedCount int
			PlanDigest, ContractDigest  string
			Refusals                    []struct {
				Ordinal     int
				Token, Code string
			}
			RefusalCounts []struct {
				Code  string
				Count int
			}
		} `json:"planFleetOperation"`
	}
	require.NoError(t, json.Unmarshal(data, &plan))
	require.Equal(t, 2, plan.P.AdmittedCount)
	require.Equal(t, 1, plan.P.RefusedCount)
	require.Equal(t, "ghost", plan.P.Refusals[0].Token)
	require.Equal(t, "TARGET_NOT_FOUND", plan.P.RefusalCounts[0].Code)

	data, errs = execFleet(t, ctx, fleetWireCreate, fleetWireRequest("op1", "d1", "ghost", "d2"))
	require.Empty(t, errs)
	var created struct {
		Op struct {
			Token, Dispatch, Evidence, PlanDigest, ContractDigest, Contract, Initiator, Executor string
			TargetCount, ContractVersion                                                         int
			Safety                                                                               struct {
				MaxPostsPerSecond, EffectivePostsPerSecond, SettleDeadlineSeconds int
				FailureThreshold                                                  *int
			}
			Progress struct{ Planned, Refused, Posted int }
		} `json:"createFleetOperation"`
	}
	require.NoError(t, json.Unmarshal(data, &created))
	op := created.Op
	require.Equal(t, "READY", op.Dispatch)
	require.Equal(t, "OPEN", op.Evidence)
	require.Equal(t, "alice@example.com", op.Initiator, "the initiator is the sign-in name")
	require.Equal(t, 3, op.TargetCount)
	require.Equal(t, plan.P.PlanDigest, op.PlanDigest, "what was previewed is what was created")
	require.Equal(t, plan.P.ContractDigest, op.ContractDigest)
	require.Equal(t, 10, op.Safety.MaxPostsPerSecond)
	require.Nil(t, op.Safety.FailureThreshold)
	require.Equal(t, 2, op.Progress.Planned)
	require.Equal(t, 1, op.Progress.Refused)
	require.Equal(t, "IDLE", op.Executor)
	require.True(t, strings.Contains(op.Contract, op.PlanDigest), "the contract body binds the plan")

	// A replay returns the original; a different request under the token is a CONFLICT with its code.
	_, errs = execFleet(t, ctx, fleetWireCreate, fleetWireRequest("op1", "d2", "ghost", "d1"))
	require.Empty(t, errs)
	_, errs = execFleet(t, ctx, fleetWireCreate, fleetWireRequest("op1", "d1"))
	require.Len(t, errs, 1)
	require.True(t, strings.HasPrefix(errs[0], "CONFLICT|"), errs[0])

	// Reads need only device:read (see the authorization matrix).
	data, errs = execFleet(t, ctx, `{ fleetOperation(token: "op1") { token dispatch } fleetOperations(criteria: {pageNumber: 1, pageSize: 10}) { results { token } pagination { totalRecords } }
	  fleetOperationLines(token: "op1", criteria: {pageNumber: 1, pageSize: 10}) { results { ordinal targetToken posting code } } missing: fleetOperation(token: "nope") { token } }`, nil)
	require.Empty(t, errs)
	var reads struct {
		Op    *struct{ Token, Dispatch string }          `json:"fleetOperation"`
		Ops   struct{ Results []struct{ Token string } } `json:"fleetOperations"`
		Lines struct {
			Results []struct {
				Ordinal                    int
				TargetToken, Posting, Code *string
			}
		} `json:"fleetOperationLines"`
		Missing *struct{} `json:"missing"`
	}
	require.NoError(t, json.Unmarshal(data, &reads))
	require.Equal(t, "op1", reads.Op.Token)
	require.Len(t, reads.Ops.Results, 1)
	require.Len(t, reads.Lines.Results, 3)
	require.Equal(t, "d1", *reads.Lines.Results[0].TargetToken)
	require.Nil(t, reads.Lines.Results[1].TargetToken, "a refused input has no token to show")
	require.Equal(t, "TARGET_NOT_FOUND", *reads.Lines.Results[1].Code)
	require.Nil(t, reads.Missing)
}

func TestFleetOperationAuthorizationMatrix(t *testing.T) {
	withEngine(t)
	type tc struct {
		name   string
		claims *auth.Claims
		read   bool // may read
		write  bool // may plan and create
		code   string
	}
	for _, c := range []tc{
		{"writer holding fleet:operate", person(auth.DeviceRead, auth.DeviceWrite, auth.FleetOperate), true, true, ""},
		{"reader", person(auth.DeviceRead), true, false, "FORBIDDEN"},
		{"writer without fleet:operate", person(auth.DeviceRead, auth.DeviceWrite), true, false, "FORBIDDEN"},
		{"fleet:operate without device:write", person(auth.DeviceRead, auth.FleetOperate), true, false, "FORBIDDEN"},
		{"nothing", person(), false, false, "FORBIDDEN"},
		{"agent origin (a client id)", func() *auth.Claims {
			c := person(auth.DeviceRead, auth.DeviceWrite, auth.FleetOperate)
			c.ClientId = "agent-1"
			return c
		}(), false, false, model.FleetCodeAgentNotPermitted},
		{"agent origin (a scope)", func() *auth.Claims {
			c := person(auth.DeviceRead, auth.DeviceWrite, auth.FleetOperate)
			c.Scope = "mcp"
			return c
		}(), false, false, model.FleetCodeAgentNotPermitted},
		{"service token", func() *auth.Claims {
			c := person(auth.DeviceRead, auth.DeviceWrite, auth.FleetOperate)
			c.TokenType = auth.TokenTypeService
			return c
		}(), false, false, model.FleetCodeAgentNotPermitted},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, _ := fleetWireCtx(t, c.claims)
			_, errs := execFleet(t, ctx, `{ fleetOperations(criteria: {pageNumber: 1, pageSize: 5}) { results { token } } }`, nil)
			if c.read {
				require.Empty(t, errs)
			} else {
				require.Len(t, errs, 1)
				require.True(t, strings.HasPrefix(errs[0], c.code+"|"), errs[0])
			}
			for name, q := range map[string]string{"plan": fleetWirePlan, "create": fleetWireCreate} {
				_, errs := execFleet(t, ctx, q, fleetWireRequest("m-"+strings.ReplaceAll(c.name, " ", "-")+name, "d1"))
				if c.write {
					require.Empty(t, errs, name)
				} else {
					require.Len(t, errs, 1, name)
					require.True(t, strings.HasPrefix(errs[0], c.code+"|"), name+": "+errs[0])
				}
			}
		})
	}
	t.Run("no claims", func(t *testing.T) {
		bare := importWireCtx(t) // an API and a tenant, and no claims
		_, errs := execFleet(t, bare, `{ fleetOperations(criteria: {pageNumber: 1, pageSize: 5}) { results { token } } }`, nil)
		require.NotEmpty(t, errs)
	})
}

func TestAScopedMemberIsRefusedEverywhere(t *testing.T) {
	withEngine(t)
	scoped := person(auth.DeviceRead, auth.DeviceWrite, auth.FleetOperate)
	scoped.ScopeAnchors = []string{"customer:acme"}
	ctx, _ := fleetWireCtx(t, scoped)
	for name, q := range map[string]string{
		"list":   `{ fleetOperations(criteria: {pageNumber: 1, pageSize: 5}) { results { token } } }`,
		"one":    `{ fleetOperation(token: "x") { token } }`,
		"lines":  `{ fleetOperationLines(token: "x", criteria: {pageNumber: 1, pageSize: 5}) { results { ordinal } } }`,
		"plan":   fleetWirePlan,
		"create": fleetWireCreate,
	} {
		var vars map[string]any
		if name == "plan" || name == "create" {
			vars = fleetWireRequest("scoped", "d1")
		}
		_, errs := execFleet(t, ctx, q, vars)
		require.Len(t, errs, 1, name)
		require.True(t, strings.HasPrefix(errs[0], "FORBIDDEN|"), name+": "+errs[0])
	}
}

func TestCreateOverTheWireRefusesAKindTheBuildCannotExecute(t *testing.T) {
	// No withEngine: the registry is empty in this build, so create must say so with its code and
	// write nothing.
	ctx, api := fleetWireCtx(t, person(auth.DeviceRead, auth.DeviceWrite, auth.FleetOperate))
	_, errs := execFleet(t, ctx, fleetWireCreate, fleetWireRequest("op1", "d1"))
	require.Len(t, errs, 1)
	require.True(t, strings.HasPrefix(errs[0], "KIND_NOT_EXECUTABLE|"), errs[0])
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&model.FleetOperation{}).Count(&n).Error)
	require.Zero(t, n)
	// Planning is still allowed: it is how an operator learns what a kind would do.
	_, errs = execFleet(t, ctx, fleetWirePlan, fleetWireRequest("op1", "d1"))
	require.Empty(t, errs)
}

func TestAScopedMemberIsRefusedEvenWhenNoAuthorityIsAsked(t *testing.T) {
	scoped := person(auth.DeviceRead, auth.DeviceWrite, auth.FleetOperate)
	scoped.ScopeAnchors = []string{"customer:acme"}
	ctx, _ := fleetWireCtx(t, scoped)
	_, err := fleetCaller(ctx)
	require.ErrorIs(t, err, auth.ErrScopeUnsupported)
	// Negative control: the same caller unscoped passes with no authority asked.
	ok, _ := fleetWireCtx(t, person())
	_, err = fleetCaller(ok)
	require.NoError(t, err)
}

const fleetWireControlFields = `token dispatch pauseReason executionEpoch grantee safety { effectivePostsPerSecond maxPostsPerSecond }`

func withGuard(t *testing.T) {
	t.Helper()
	t.Cleanup(model.OpenFleetActivationForTest(func(*gorm.DB) error { return nil }))
}

func TestControlTransitionsOverTheWire(t *testing.T) {
	withEngine(t)
	withGuard(t)
	ctx, _ := fleetWireCtx(t, person(auth.DeviceRead, auth.DeviceWrite, auth.FleetOperate))
	data, errs := execFleet(t, ctx, fleetWireCreate, fleetWireRequest("c1", "d1", "d2"))
	require.Empty(t, errs)
	var created struct {
		Op struct{ ContractDigest string } `json:"createFleetOperation"`
	}
	require.NoError(t, json.Unmarshal(data, &created))
	digest := created.Op.ContractDigest

	type op struct {
		Token, Dispatch string
		PauseReason     *string
		ExecutionEpoch  int
		Grantee         *string
		Safety          struct{ EffectivePostsPerSecond, MaxPostsPerSecond int }
	}
	call := func(q string, vars map[string]any, field string) (op, []string) {
		data, errs := execFleet(t, ctx, q, vars)
		var out map[string]op
		if len(errs) == 0 {
			require.NoError(t, json.Unmarshal(data, &out))
		}
		return out[field], errs
	}
	startQ := `mutation ($t: String!, $d: String!) { startFleetOperation(token: $t, contractDigest: $d) { ` + fleetWireControlFields + ` } }`
	pauseQ := `mutation ($t: String!) { pauseFleetOperation(token: $t) { ` + fleetWireControlFields + ` } }`
	resumeQ := `mutation ($t: String!, $d: String!, $s: FleetOperationSafetyInput) { resumeFleetOperation(token: $t, contractDigest: $d, safety: $s) { ` + fleetWireControlFields + ` } }`
	abortQ := `mutation ($t: String!) { abortFleetOperation(token: $t) { ` + fleetWireControlFields + ` } }`

	_, errs = call(startQ, map[string]any{"t": "c1", "d": "sha256:nope"}, "startFleetOperation")
	require.Len(t, errs, 1)
	require.True(t, strings.HasPrefix(errs[0], "CONTRACT_DIGEST_MISMATCH|"), errs[0])

	started, errs := call(startQ, map[string]any{"t": "c1", "d": digest}, "startFleetOperation")
	require.Empty(t, errs)
	require.Equal(t, "RUNNING", started.Dispatch)
	require.Equal(t, "alice@example.com", *started.Grantee)
	require.Equal(t, 1, started.ExecutionEpoch)

	paused, errs := call(pauseQ, map[string]any{"t": "c1"}, "pauseFleetOperation")
	require.Empty(t, errs)
	require.Equal(t, "PAUSED", paused.Dispatch)
	require.Equal(t, "USER", *paused.PauseReason)

	prev, errs2 := execFleet(t, ctx, `query ($t: String!, $s: FleetOperationSafetyInput) { previewFleetOperationResume(token: $t, safety: $s) { withinContract contractDigest stepUpRequired thresholdStillExceeded } }`,
		map[string]any{"t": "c1", "s": map[string]any{"maxPostsPerSecond": 50}})
	require.Empty(t, errs2)
	require.Contains(t, string(prev), `"withinContract":false`)

	_, errs = call(resumeQ, map[string]any{"t": "c1", "d": digest, "s": map[string]any{"maxPostsPerSecond": 50}}, "resumeFleetOperation")
	require.Len(t, errs, 1)
	require.True(t, strings.HasPrefix(errs[0], "CONTRACT_WIDENED|"), errs[0])

	resumed, errs := call(resumeQ, map[string]any{"t": "c1", "d": digest, "s": map[string]any{"maxPostsPerSecond": 4}}, "resumeFleetOperation")
	require.Empty(t, errs)
	require.Equal(t, "RUNNING", resumed.Dispatch)
	require.Equal(t, 4, resumed.Safety.EffectivePostsPerSecond)
	require.Equal(t, 10, resumed.Safety.MaxPostsPerSecond)

	aborted, errs := call(abortQ, map[string]any{"t": "c1"}, "abortFleetOperation")
	require.Empty(t, errs)
	require.Equal(t, "ABORTED", aborted.Dispatch)
	_, errs = call(abortQ, map[string]any{"t": "c1"}, "abortFleetOperation")
	require.Len(t, errs, 1)
	require.True(t, strings.HasPrefix(errs[0], "ILLEGAL_TRANSITION|"), errs[0])
}

func TestStoppingNeedsOnlyDeviceWriteButStartingAndResumingNeedFleetOperate(t *testing.T) {
	withEngine(t)
	withGuard(t)
	owner, api := fleetWireCtx(t, person(auth.DeviceRead, auth.DeviceWrite, auth.FleetOperate))
	data, errs := execFleet(t, owner, fleetWireCreate, fleetWireRequest("m1", "d1"))
	require.Empty(t, errs)
	var created struct {
		Op struct{ ContractDigest string } `json:"createFleetOperation"`
	}
	require.NoError(t, json.Unmarshal(data, &created))
	digest := created.Op.ContractDigest

	asWriter := auth.WithClaims(owner, person(auth.DeviceRead, auth.DeviceWrite)) // lost fleet:operate
	asReader := auth.WithClaims(owner, person(auth.DeviceRead))
	asAgent := func() context.Context {
		c := person(auth.DeviceRead, auth.DeviceWrite, auth.FleetOperate)
		c.ClientId = "agent-1"
		return auth.WithClaims(owner, c)
	}()
	start := `mutation ($t: String!, $d: String!) { startFleetOperation(token: $t, contractDigest: $d) { dispatch } }`
	pause := `mutation ($t: String!) { pauseFleetOperation(token: $t) { dispatch } }`
	abort := `mutation ($t: String!) { abortFleetOperation(token: $t) { dispatch } }`
	resume := `mutation ($t: String!, $d: String!) { resumeFleetOperation(token: $t, contractDigest: $d) { dispatch } }`
	preview := `query ($t: String!) { previewFleetOperationResume(token: $t) { withinContract } }`

	// Without fleet:operate: cannot start, resume or preview a resume.
	for name, q := range map[string]string{"start": start, "resume": resume, "preview": preview} {
		_, errs := execFleet(t, asWriter, q, map[string]any{"t": "m1", "d": digest})
		require.Len(t, errs, 1, name)
		require.True(t, strings.HasPrefix(errs[0], "FORBIDDEN|"), name+errs[0])
	}
	// Nobody who is not a writer may stop it, an agent included.
	for name, c := range map[string]context.Context{"reader": asReader, "agent": asAgent} {
		for op, q := range map[string]string{"pause": pause, "abort": abort} {
			_, errs := execFleet(t, c, q, map[string]any{"t": "m1"})
			require.Len(t, errs, 1, name+" "+op)
		}
	}
	// Negative control: the owner starts it, and a writer who lost fleet:operate can still stop it.
	_, errs = execFleet(t, owner, start, map[string]any{"t": "m1", "d": digest})
	require.Empty(t, errs)
	_, errs = execFleet(t, asWriter, pause, map[string]any{"t": "m1"})
	require.Empty(t, errs, "an administrator who lost fleet:operate can still stop an operation")
	_, errs = execFleet(t, asWriter, abort, map[string]any{"t": "m1"})
	require.Empty(t, errs)
	var n int64
	require.NoError(t, api.RDB.DB(owner).Model(&model.FleetOperationEvent{}).Where("kind IN ?", []string{"PAUSE", "ABORT"}).Count(&n).Error)
	require.Equal(t, int64(2), n)
}
