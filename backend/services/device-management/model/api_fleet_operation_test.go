// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
)

// fleetFixture is a database with devices d1 to dN in tenant acme and "other1" in tenant globex.
func fleetFixture(t *testing.T, devices int) (*Api, context.Context) {
	t.Helper()
	db := sqlitetest.OpenDSN(t, sqlitetest.MemoryDSN(t), nil)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&Device{}, &DeviceType{}, &FleetOperation{}, &FleetOperationRuntime{},
		&FleetOperationContract{}, &FleetOperationLine{}, &FleetOperationEvent{}))
	api := NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")
	seed := func(c context.Context, token string, n int) {
		dt := &DeviceType{}
		dt.Token = token + "-type"
		require.NoError(t, db.WithContext(c).Create(dt).Error)
		for i := 1; i <= n; i++ {
			d := &Device{DeviceTypeId: dt.ID}
			d.Token = fmt.Sprintf("%s%d", token, i)
			require.NoError(t, db.WithContext(c).Create(d).Error)
		}
	}
	seed(ctx, "d", devices)
	seed(core.WithTenant(context.Background(), "globex"), "other", 1)
	return api, ctx
}

func ptr32(v int32) *int32  { return &v }
func sptr(s string) *string { return &s }

func setReq(token string, devices ...string) *FleetOperationCreateRequest {
	return &FleetOperationCreateRequest{Token: token, Name: "n-" + token, Kind: FleetKindConfigSet,
		ConfigSet:    &FleetConfigSetIntent{Key: "site", ValueType: "STRING", Value: sptr("north")},
		DeviceTokens: devices}
}

func requireFleetCode(t *testing.T, err error, code string) {
	t.Helper()
	var fe *FleetOperationError
	require.True(t, errors.As(err, &fe), "want a FleetOperationError, got %v", err)
	require.Equal(t, code, fe.Code)
}

// openTheEngine lets create accept config.set for the length of one test; the registry keeps the
// door shut until the engine ships.
func openTheEngine(t *testing.T) {
	t.Helper()
	prev := fleetKindExecutable
	fleetKindExecutable = func(string) bool { return true }
	t.Cleanup(func() { fleetKindExecutable = prev })
}

func TestPlanNormalizesResolvesAndRefusesWithoutWriting(t *testing.T) {
	api, ctx := fleetFixture(t, 3)
	plan, err := api.PlanFleetOperation(ctx, setReq("t", " d2 ", "d1", "d2", "nope", "other1", "", "d3"))
	require.NoError(t, err)
	// d2 is trimmed and repeated once (dropped); order follows the first occurrence.
	got := []string{}
	for _, l := range plan.Lines {
		got = append(got, fmt.Sprintf("%d:%s:%s", l.Ordinal, l.Token, l.Code))
	}
	require.Equal(t, []string{"1:d2:", "2:d1:", "3:nope:TARGET_NOT_FOUND", "4:other1:TARGET_NOT_FOUND", "5::TARGET_NOT_FOUND", "6:d3:"}, got)
	require.Equal(t, 3, plan.Admitted)
	require.Equal(t, 3, plan.Refused)
	require.Equal(t, map[string]int{"TARGET_NOT_FOUND": 3}, plan.RefusalCounts)
	// A device of another tenant is refused exactly as an unknown token is.
	require.Equal(t, plan.Refusals[0].Code, plan.Refusals[1].Code)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, plan.RequestDigest)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, plan.PlanDigest)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, plan.ContractDigest)
	// Writes nothing.
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Count(&n).Error)
	require.Zero(t, n)
}

func TestPlanDigestsAreDeterministicAndBindWhatTheyShould(t *testing.T) {
	api, ctx := fleetFixture(t, 3)
	a, err := api.PlanFleetOperation(ctx, setReq("t", "d1", "d2", "d3"))
	require.NoError(t, err)
	b, err := api.PlanFleetOperation(ctx, setReq("other-token-same-request", "d1", "d2", "d3"))
	require.NoError(t, err)
	require.Equal(t, a.RequestDigest, b.RequestDigest, "the operation token is not part of the request")
	require.Equal(t, a.PlanDigest, b.PlanDigest)
	require.Equal(t, a.ContractDigest, b.ContractDigest)

	// The request digest is order-insensitive (it identifies the request); the plan digest is the
	// ordered plan, so the contract moves with the order.
	c, err := api.PlanFleetOperation(ctx, setReq("t", "d3", "d1", "d2"))
	require.NoError(t, err)
	require.Equal(t, a.RequestDigest, c.RequestDigest)
	require.NotEqual(t, a.PlanDigest, c.PlanDigest)
	require.NotEqual(t, a.ContractDigest, c.ContractDigest)

	// Intent and safety move every digest they must.
	other := setReq("t", "d1", "d2", "d3")
	other.ConfigSet.Value = sptr("south")
	d, err := api.PlanFleetOperation(ctx, other)
	require.NoError(t, err)
	require.NotEqual(t, a.RequestDigest, d.RequestDigest)
	require.NotEqual(t, a.ContractDigest, d.ContractDigest)
	slower := setReq("t", "d1", "d2", "d3")
	slower.Safety.MaxPostsPerSecond = ptr32(5)
	e, err := api.PlanFleetOperation(ctx, slower)
	require.NoError(t, err)
	require.NotEqual(t, a.RequestDigest, e.RequestDigest)
	require.Equal(t, a.PlanDigest, e.PlanDigest, "the plan does not depend on the safety policy")
	require.NotEqual(t, a.ContractDigest, e.ContractDigest)
	// A refused line changes the plan: an unknown token is not the same plan as a known one.
	f, err := api.PlanFleetOperation(ctx, setReq("t", "d1", "d2", "zzz"))
	require.NoError(t, err)
	require.NotEqual(t, a.PlanDigest, f.PlanDigest)
}

func TestPlanRefusalsAreCappedPerCodeButCountedInFull(t *testing.T) {
	api, ctx := fleetFixture(t, 1)
	tokens := []string{"d1"}
	for i := 0; i < MaxFleetPlanRefusals+25; i++ {
		tokens = append(tokens, fmt.Sprintf("missing-%d", i))
	}
	plan, err := api.PlanFleetOperation(ctx, setReq("t", tokens...))
	require.NoError(t, err)
	require.Equal(t, MaxFleetPlanRefusals+25, plan.RefusalCounts["TARGET_NOT_FOUND"])
	require.Len(t, plan.Refusals, MaxFleetPlanRefusals)
	require.Equal(t, MaxFleetPlanRefusals+25, plan.Refused)
}

func TestPlanRefusesWhatItCannotPlanAndNeverTruncates(t *testing.T) {
	api, ctx := fleetFixture(t, 1)
	many := make([]string, ledger.MaxLinesPerOperation+1)
	for i := range many {
		many[i] = fmt.Sprintf("x%d", i)
	}
	_, err := api.PlanFleetOperation(ctx, setReq("t", many...))
	requireFleetCode(t, err, FleetCodeOperationTooLarge)
	// Negative control: exactly the ceiling is planned, in full.
	plan, err := api.PlanFleetOperation(ctx, setReq("t", many[:ledger.MaxLinesPerOperation]...))
	require.NoError(t, err)
	require.Len(t, plan.Lines, ledger.MaxLinesPerOperation)

	_, err = api.PlanFleetOperation(ctx, setReq("t", strings.Repeat("a", ledger.MaxRequestBytes+1)))
	requireFleetCode(t, err, FleetCodeInputTooLarge)

	for name, mutate := range map[string]func(*FleetOperationCreateRequest){
		"no devices":       func(r *FleetOperationCreateRequest) { r.DeviceTokens = nil },
		"no configSet":     func(r *FleetOperationCreateRequest) { r.ConfigSet = nil },
		"empty key":        func(r *FleetOperationCreateRequest) { r.ConfigSet.Key = " " },
		"bad value type":   func(r *FleetOperationCreateRequest) { r.ConfigSet.ValueType = "BLOB" },
		"no value":         func(r *FleetOperationCreateRequest) { r.ConfigSet.Value = nil },
		"unset with value": func(r *FleetOperationCreateRequest) { r.ConfigSet.Unset = true },
		"rate zero":        func(r *FleetOperationCreateRequest) { r.Safety.MaxPostsPerSecond = ptr32(0) },
		"rate over cap": func(r *FleetOperationCreateRequest) {
			r.Safety.MaxPostsPerSecond = ptr32(ledger.TenantPostsPerSecond + 1)
		},
		"deadline too short": func(r *FleetOperationCreateRequest) { r.Safety.SettleDeadlineSeconds = ptr32(1) },
		"threshold zero":     func(r *FleetOperationCreateRequest) { r.Safety.FailureThreshold = ptr32(0) },
	} {
		req := setReq("t", "d1")
		mutate(req)
		_, err := api.PlanFleetOperation(ctx, req)
		require.Error(t, err, name)
		requireFleetCode(t, err, FleetCodeInvalidRequest)
	}
	req := setReq("t", "d1")
	req.Kind = "ota.assign"
	_, err = api.PlanFleetOperation(ctx, req)
	requireFleetCode(t, err, FleetCodeKindNotExecutable)

	// An unset is a valid intent without a value.
	unset := setReq("t", "d1")
	unset.ConfigSet = &FleetConfigSetIntent{Key: "site", Unset: true}
	_, err = api.PlanFleetOperation(ctx, unset)
	require.NoError(t, err)
}

func TestCreateIsRefusedWhileNoEngineCanExecuteTheKind(t *testing.T) {
	api, ctx := fleetFixture(t, 1)
	_, err := api.CreateFleetOperation(ctx, setReq("t", "d1"), "alice")
	requireFleetCode(t, err, FleetCodeKindNotExecutable)
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Count(&n).Error)
	require.Zero(t, n, "a refused create writes nothing")
}

func TestCreateWritesTheHeaderLinesContractAndEventAndIsREADY(t *testing.T) {
	openTheEngine(t)
	api, ctx := fleetFixture(t, 3)
	op, err := api.CreateFleetOperation(ctx, setReq("t1", "d1", "ghost", "d2"), "alice")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, op.Dispatch)
	require.Equal(t, "OPEN", op.Evidence)
	require.Equal(t, "alice", op.Initiator)
	require.Equal(t, int32(3), op.TargetCount)
	require.Equal(t, int32(1), op.DriverProtocol)
	require.Equal(t, int32(10), op.MaxPostsPerSecond)
	require.Equal(t, int32(1), op.ContractVersion)
	require.NotEmpty(t, op.PlanDigest)
	require.NotEmpty(t, op.ContractDigest)
	require.True(t, op.ExpiresAt.Valid)

	lines, err := api.FleetOperationLines(ctx, "t1", FleetLineSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, lines.Results, 3)
	require.Equal(t, int32(1), lines.Results[0].Ordinal)
	require.Equal(t, "d1", lines.Results[0].TargetKey)
	require.Equal(t, FleetPostingPlanned, lines.Results[0].Posting)
	require.True(t, lines.Results[0].TargetId.Valid)
	require.Equal(t, FleetPostingRefused, lines.Results[1].Posting)
	require.Equal(t, "TARGET_NOT_FOUND", lines.Results[1].Code)
	require.True(t, lines.Results[1].RefusedAtPlan)
	require.Empty(t, lines.Results[1].TargetKey, "a refused input stores no identifier")
	require.False(t, lines.Results[1].TargetId.Valid)

	var contract FleetOperationContract
	require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ?", op.ID).First(&contract).Error)
	require.Equal(t, op.ContractDigest, contract.Digest)
	require.False(t, contract.ApprovedBy.Valid, "the creator proposes; the starter approves")
	var events []FleetOperationEvent
	require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ?", op.ID).Find(&events).Error)
	require.Len(t, events, 1)
	require.Equal(t, "CREATE", events[0].Kind)
	var runtime FleetOperationRuntime
	require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ?", op.ID).First(&runtime).Error)

	progress, err := api.FleetOperationProgressOf(ctx, op)
	require.NoError(t, err)
	require.Equal(t, int32(2), progress.Planned)
	require.Equal(t, int32(1), progress.Refused)

	// The plan the operator previewed is the plan create stored.
	preview, err := api.PlanFleetOperation(ctx, setReq("t1", "d1", "ghost", "d2"))
	require.NoError(t, err)
	require.Equal(t, preview.PlanDigest, op.PlanDigest)
	require.Equal(t, preview.ContractDigest, op.ContractDigest)
}

func TestCreateIsIdempotentOnTheToken(t *testing.T) {
	openTheEngine(t)
	api, ctx := fleetFixture(t, 3)
	first, err := api.CreateFleetOperation(ctx, setReq("t1", "d1", "d2"), "alice")
	require.NoError(t, err)
	// The same request replays the original and writes nothing (even from another caller, and with
	// the tokens in another order, which is the same request).
	again, err := api.CreateFleetOperation(ctx, setReq("t1", "d2", "d1"), "bob")
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID)
	require.Equal(t, "alice", again.Initiator)
	var ops, lines int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Count(&ops).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Count(&lines).Error)
	require.Equal(t, int64(1), ops)
	require.Equal(t, int64(2), lines)

	// A different request under the same token is a conflict, and changes nothing.
	_, err = api.CreateFleetOperation(ctx, setReq("t1", "d1", "d2", "d3"), "alice")
	requireFleetCode(t, err, FleetCodeConflict)
	diff := setReq("t1", "d1", "d2")
	diff.ConfigSet.Value = sptr("south")
	_, err = api.CreateFleetOperation(ctx, diff, "alice")
	requireFleetCode(t, err, FleetCodeConflict)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Count(&lines).Error)
	require.Equal(t, int64(2), lines)

	// The token namespace is per tenant: globex may use the same token.
	globex := core.WithTenant(context.Background(), "globex")
	other, err := api.CreateFleetOperation(globex, setReq("t1", "other1"), "carol")
	require.NoError(t, err)
	require.NotEqual(t, first.ID, other.ID)
}

func TestCreateFinishesAHeaderAnEarlierCallLeftInPlanning(t *testing.T) {
	openTheEngine(t)
	api, ctx := fleetFixture(t, 2)
	req := setReq("t1", "d1", "d2")
	n, err := normalizeFleetRequest(req)
	require.NoError(t, err)
	plan, err := api.planNormalized(ctx, n)
	require.NoError(t, err)
	// The earlier call died after step 1.
	stuck, err := api.fleetCreateStepOne(ctx, "acme", req, n, plan, "alice")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchPlanning, stuck.Dispatch)
	var lines int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Count(&lines).Error)
	require.Zero(t, lines)

	// A different request under that token is still a conflict while it is PLANNING.
	_, err = api.CreateFleetOperation(ctx, setReq("t1", "d1"), "alice")
	requireFleetCode(t, err, FleetCodeConflict)

	done, err := api.CreateFleetOperation(ctx, req, "alice")
	require.NoError(t, err)
	require.Equal(t, stuck.ID, done.ID)
	require.Equal(t, FleetDispatchReady, done.Dispatch)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Count(&lines).Error)
	require.Equal(t, int64(2), lines)
	// And a third call is a plain replay: no lines are inserted twice.
	_, err = api.CreateFleetOperation(ctx, req, "alice")
	require.NoError(t, err)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Count(&lines).Error)
	require.Equal(t, int64(2), lines)
}

func TestAdmissionRefusesPastTheBudgetAndNamesIt(t *testing.T) {
	openTheEngine(t)
	api, ctx := fleetFixture(t, 2)
	// The tenant may create TenantCreatesPerHour operations an hour.
	for i := 0; i < ledger.TenantCreatesPerHour; i++ {
		_, err := api.CreateFleetOperation(ctx, setReq(fmt.Sprintf("u%d", i), "d1"), "alice")
		require.NoError(t, err, i)
	}
	_, err := api.CreateFleetOperation(ctx, setReq("one-too-many", "d1"), "alice")
	var fe *FleetOperationError
	require.True(t, errors.As(err, &fe), "%v", err)
	require.Equal(t, FleetCodeQuotaExceeded, fe.Code)
	require.Contains(t, []string{"tenant.unstartedOperations", "tenant.createsPerHour"}, fe.Quota)
	require.Equal(t, fe.Quota, fe.Extensions()["quota"])
	// Another tenant is not held back by this one's budget.
	_, err = api.CreateFleetOperation(core.WithTenant(context.Background(), "globex"), setReq("g", "other1"), "carol")
	require.NoError(t, err)
}

func TestAdmissionLineBudgetCountsPlannedLines(t *testing.T) {
	openTheEngine(t)
	api, ctx := fleetFixture(t, 1)
	// Fill the unposted-line budget with lines of a READY operation, in the table directly.
	db := api.RDB.DB(ctx)
	filler := &FleetOperation{Token: "filler", Name: "f", Kind: FleetKindConfigSet, Dispatch: FleetDispatchReady, Evidence: "OPEN",
		RequestDigest: "d", Intent: "{}", DriverProtocol: 1, TargetKind: fleetTargetKindDeviceList, TargetCount: 1, MaxPostsPerSecond: 1,
		EffPostsPerSecond: 1, SettleDeadlineS: 60, Initiator: "x", ContractVersion: 1}
	require.NoError(t, db.Create(filler).Error)
	rows := make([]FleetOperationLine, ledger.TenantUnpostedLines)
	for i := range rows {
		rows[i] = FleetOperationLine{OperationId: int64(filler.ID), Ordinal: int32(i + 1), Posting: FleetPostingPlanned}
	}
	require.NoError(t, db.CreateInBatches(&rows, 1000).Error)
	_, err := api.CreateFleetOperation(ctx, setReq("t", "d1"), "alice")
	var fe *FleetOperationError
	require.True(t, errors.As(err, &fe), "%v", err)
	require.Equal(t, "tenant.unpostedLines", fe.Quota)
	// Negative control: one line fewer in the way and the same create is admitted.
	require.NoError(t, db.Where("operation_id = ? AND ordinal <= 2", filler.ID).Delete(&FleetOperationLine{}).Error)
	_, err = api.CreateFleetOperation(ctx, setReq("t", "d1"), "alice")
	require.NoError(t, err)
}

func TestPlanRefusesALaterTokenThatNamesTheSameDevice(t *testing.T) {
	// Tokens are unique per tenant, so this cannot come from the database today; the planner still
	// dedupes on the resolved id, so the unique index on (operation, device) is never what answers.
	n, err := normalizeFleetRequest(setReq("t", "a", "b", "c"))
	require.NoError(t, err)
	plan, err := buildFleetPlan(n, map[string]uint{"a": 1, "b": 1, "c": 2})
	require.NoError(t, err)
	require.Equal(t, 2, plan.Admitted)
	require.Equal(t, map[string]int{FleetLineDuplicateTarget: 1}, plan.RefusalCounts)
	require.Nil(t, plan.Lines[1].TargetId)
	require.Equal(t, FleetLineDuplicateTarget, plan.Lines[1].Code)
	require.NotNil(t, plan.Lines[0].TargetId, "the first occurrence is the one admitted")
	// Negative control: distinct devices are all admitted.
	clean, err := buildFleetPlan(n, map[string]uint{"a": 1, "b": 3, "c": 2})
	require.NoError(t, err)
	require.Equal(t, 3, clean.Admitted)
	require.Empty(t, clean.RefusalCounts)
}

func TestPlanDigestSeparatesRefusalReasons(t *testing.T) {
	n, err := normalizeFleetRequest(setReq("t", "a", "b"))
	require.NoError(t, err)
	// In both plans the second line has no device; only the reason differs.
	notFound, err := buildFleetPlan(n, map[string]uint{"a": 1})
	require.NoError(t, err)
	duplicate, err := buildFleetPlan(n, map[string]uint{"a": 1, "b": 1})
	require.NoError(t, err)
	require.NotEqual(t, notFound.PlanDigest, duplicate.PlanDigest)
	require.NotEqual(t, notFound.ContractDigest, duplicate.ContractDigest)
}

func TestReplayIsNotRefusedByAnExhaustedBudget(t *testing.T) {
	openTheEngine(t)
	api, ctx := fleetFixture(t, 2)
	var first *FleetOperation
	for i := 0; i < ledger.TenantCreatesPerHour; i++ {
		op, err := api.CreateFleetOperation(ctx, setReq(fmt.Sprintf("b%d", i), "d1"), "alice")
		require.NoError(t, err)
		if i == 0 {
			first = op
		}
	}
	// A new operation is over budget; a replay of an existing one is not a new operation.
	_, err := api.CreateFleetOperation(ctx, setReq("new", "d1"), "alice")
	requireFleetCode(t, err, FleetCodeQuotaExceeded)
	again, err := api.CreateFleetOperation(ctx, setReq("b0", "d1"), "alice")
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID)
}

func TestEachCreateBudgetRefusesOnItsOwn(t *testing.T) {
	openTheEngine(t)
	fill := func(api *Api, ctx context.Context) {
		for i := 0; i < ledger.TenantCreatesPerHour; i++ {
			_, err := api.CreateFleetOperation(ctx, setReq(fmt.Sprintf("f%d", i), "d1"), "alice")
			require.NoError(t, err)
		}
	}
	quota := func(err error) string {
		var fe *FleetOperationError
		require.True(t, errors.As(err, &fe), "%v", err)
		require.Equal(t, FleetCodeQuotaExceeded, fe.Code)
		return fe.Quota
	}
	t.Run("operations waiting to start", func(t *testing.T) {
		api, ctx := fleetFixture(t, 1)
		fill(api, ctx)
		// Age them out of the hourly window: only the waiting-operations budget can refuse now.
		require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Where("1 = 1").Update("created_at", time.Now().Add(-2*time.Hour)).Error)
		_, err := api.CreateFleetOperation(ctx, setReq("next", "d1"), "alice")
		require.Equal(t, "tenant.unstartedOperations", quota(err))
	})
	t.Run("creates per hour", func(t *testing.T) {
		api, ctx := fleetFixture(t, 1)
		fill(api, ctx)
		// Nothing is waiting to start any more: only the hourly rate can refuse now.
		require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Where("1 = 1").Update("dispatch", "ABORTED").Error)
		_, err := api.CreateFleetOperation(ctx, setReq("next", "d1"), "alice")
		require.Equal(t, "tenant.createsPerHour", quota(err))
	})
	t.Run("negative control: neither budget full", func(t *testing.T) {
		api, ctx := fleetFixture(t, 1)
		fill(api, ctx)
		require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Where("1 = 1").Updates(map[string]any{"dispatch": "ABORTED", "created_at": time.Now().Add(-2 * time.Hour)}).Error)
		_, err := api.CreateFleetOperation(ctx, setReq("next", "d1"), "alice")
		require.NoError(t, err)
	})
}

func TestAReplayRacingTheLastSlotIsAnsweredAsAReplayNotRefused(t *testing.T) {
	openTheEngine(t)
	api, ctx := fleetFixture(t, 1)
	req := setReq("twin", "d1")
	n, err := normalizeFleetRequest(req)
	require.NoError(t, err)
	plan, err := api.planNormalized(ctx, n)
	require.NoError(t, err)
	first, err := api.fleetCreateStepOne(ctx, "acme", req, n, plan, "alice")
	require.NoError(t, err)
	// Fill the waiting-operations budget to the brim with other operations (aged out of the hourly rate).
	db := api.RDB.DB(ctx)
	for i := 1; i < ledger.TenantUnstartedOperations; i++ {
		other := &FleetOperation{Token: fmt.Sprintf("fill%d", i), Name: "f", Kind: FleetKindConfigSet, Dispatch: FleetDispatchReady, Evidence: "OPEN",
			RequestDigest: "d", Intent: "{}", DriverProtocol: 1, TargetKind: fleetTargetKindDeviceList, TargetCount: 1, MaxPostsPerSecond: 1,
			EffPostsPerSecond: 1, SettleDeadlineS: 60, Initiator: "x", ContractVersion: 1}
		require.NoError(t, db.Create(other).Error)
	}
	// The twin arrives after the winner committed, with the budget full: it is a replay, not a refusal.
	again, err := api.fleetCreateStepOne(ctx, "acme", req, n, plan, "alice")
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID)
	// A different request under that token is still a conflict, never a quota answer.
	req2 := setReq("twin", "d1", "d1x")
	n2, err := normalizeFleetRequest(req2)
	require.NoError(t, err)
	_, err = api.fleetCreateStepOne(ctx, "acme", req2, n2, plan, "alice")
	requireFleetCode(t, err, FleetCodeConflict)
	// Negative control: a NEW token with the budget full is refused for the budget.
	req3 := setReq("brand-new", "d1")
	n3, err := normalizeFleetRequest(req3)
	require.NoError(t, err)
	_, err = api.fleetCreateStepOne(ctx, "acme", req3, n3, plan, "alice")
	requireFleetCode(t, err, FleetCodeQuotaExceeded)
}
