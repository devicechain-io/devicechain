// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func admissionFixture(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api, ctx := fleetFixture(t, 1)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	return api, ctx
}

func requireAdmissionQuota(t *testing.T, err error, quota string) {
	t.Helper()
	requireFleetCode(t, err, FleetCodeQuotaExceeded)
	var fe *FleetOperationError
	require.True(t, errors.As(err, &fe))
	require.Equal(t, quota, fe.Quota)
}

func restoreAdmissionBudgets(t *testing.T) {
	t.Helper()
	previous := fleetAdmissionBudgets
	t.Cleanup(func() { fleetAdmissionBudgets = previous })
}

func admissionCall(api *Api, ctx context.Context, op *FleetOperation, resume bool) (*FleetOperation, error) {
	if resume {
		return api.ResumeFleetOperation(ctx, op.Token, op.ContractDigest, nil, alice)
	}
	return api.StartFleetOperation(ctx, op.Token, op.ContractDigest, alice)
}

func admissionCandidate(t *testing.T, api *Api, ctx context.Context, resume bool) *FleetOperation {
	t.Helper()
	op := readyOp(t, api, ctx, "candidate", FleetSafetyRequest{}, "d1")
	if resume {
		var err error
		op, err = api.StartFleetOperation(ctx, op.Token, op.ContractDigest, alice)
		require.NoError(t, err)
		op, err = api.PauseFleetOperation(ctx, op.Token, alice.Name)
		require.NoError(t, err)
	}
	return op
}

func TestAdmissionOrphansGateIsTenantScopedAndFailsClosed(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, condition := range []string{"attribute", "generation", "unreadable"} {
			t.Run(map[bool]string{false: "start", true: "resume"}[resume]+"/"+condition, func(t *testing.T) {
				armed(t)
				api, ctx := admissionFixture(t)
				db := api.RDB.DB(ctx)
				op := admissionCandidate(t, api, ctx, resume)
				other := core.WithTenant(ctx, "globex")
				row := &EntityAttribute{EntityType: "device", EntityId: 999999, Scope: "SERVER", AttrKey: "orphan", ValueType: "STRING"}
				require.NoError(t, api.RDB.DB(other).Create(row).Error)
				// Another tenant's leftover rows cannot block this caller.
				got, err := admissionCall(api, ctx, op, resume)
				require.NoError(t, err)
				require.Equal(t, FleetDispatchRunning, got.Dispatch)
				if resume {
					op, err = api.PauseFleetOperation(ctx, op.Token, alice.Name)
					require.NoError(t, err)
				} else {
					_, err = api.AbortFleetOperation(ctx, op.Token, alice.Name)
					require.NoError(t, err)
					op = readyOp(t, api, ctx, "blocked", FleetSafetyRequest{}, "d1")
				}
				switch condition {
				case "attribute":
					row.ID, row.TenantId = 0, ""
					require.NoError(t, db.Create(row).Error)
					// Soft deletion still leaves a physical orphan.
					require.NoError(t, db.Delete(row).Error)
				case "generation":
					require.NoError(t, db.Create(&AttributeGeneration{DeviceId: 999999, Scope: "SERVER", AttrKey: "orphan", Generation: 1}).Error)
				case "unreadable":
					require.NoError(t, db.Migrator().DropTable(&AttributeGeneration{}))
				}
				before := events(t, api, ctx, op)
				_, err = admissionCall(api, ctx, op, resume)
				requireFleetCode(t, err, FleetCodeNotActivated)
				require.EqualError(t, err, "orphaned device attribute rows remain; the sweep removes them")
				still, err := api.fleetOperationByToken(ctx, op.Token)
				require.NoError(t, err)
				require.Equal(t, op.Dispatch, still.Dispatch)
				require.Equal(t, op.ExecutionEpoch, still.ExecutionEpoch)
				require.Equal(t, before, events(t, api, ctx, op))
			})
		}
	}
}

func TestAdmissionResumeRecountsWithoutItselfAndRefusesLoweredLimits(t *testing.T) {
	for _, scope := range []string{"tenant", "instance"} {
		t.Run(scope, func(t *testing.T) {
			armed(t)
			restoreAdmissionBudgets(t)
			api, ctx := admissionFixture(t)
			op := admissionCandidate(t, api, ctx, true)
			occupant := *op
			occupant.ID, occupant.Token, occupant.TenantId, occupant.Dispatch = 0, "occupant", "", FleetDispatchPaused
			otherCtx := ctx
			if scope == "instance" {
				otherCtx = core.WithTenant(ctx, "globex")
				fleetAdmissionBudgets.instanceDispatching = 2
			}
			require.NoError(t, api.RDB.DB(otherCtx).Create(&occupant).Error)
			// The PAUSED candidate is already one of the two occupied slots.
			_, err := admissionCall(api, ctx, op, true)
			require.NoError(t, err, "resume at the limit excludes itself before adding one")
			op, err = api.PauseFleetOperation(ctx, op.Token, alice.Name)
			require.NoError(t, err)
			if scope == "tenant" {
				fleetAdmissionBudgets.tenantDispatching = 1
			} else {
				fleetAdmissionBudgets.instanceDispatching = 1
			}
			before := events(t, api, ctx, op)
			_, err = admissionCall(api, ctx, op, true)
			requireAdmissionQuota(t, err, scope+".dispatchingOperations")
			still, err := api.fleetOperationByToken(ctx, op.Token)
			require.NoError(t, err)
			require.Equal(t, op.ExecutionEpoch, still.ExecutionEpoch)
			require.Equal(t, FleetDispatchPaused, still.Dispatch)
			require.Equal(t, before, events(t, api, ctx, op))
		})
	}
}

func TestAdmissionEvidenceOpenBoundaryAtStartAndResume(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, scope := range []string{"tenant", "instance"} {
			t.Run(fmt.Sprintf("resume=%t/%s", resume, scope), func(t *testing.T) {
				armed(t)
				restoreAdmissionBudgets(t)
				api, ctx := admissionFixture(t)
				op := admissionCandidate(t, api, ctx, resume) // one PLANNED line
				donorCtx := ctx
				if scope == "instance" {
					donorCtx = core.WithTenant(ctx, "globex")
				}
				donor := *op
				donor.ID, donor.Token, donor.TenantId = 0, "donor", ""
				donor.Dispatch, donor.Evidence = FleetDispatchAborted, fleetEvidenceClosedText
				require.NoError(t, api.RDB.DB(donorCtx).Create(&donor).Error)
				lines := []FleetOperationLine{
					{Posting: FleetPostingPosted},
					{Posting: FleetPostingPosted, Settlement: sql.NullString{String: string(ledger.SettlementAmbiguous), Valid: true}},
					{Posting: FleetPostingPosted, Code: "TARGET_ERASED"},
					{Posting: FleetPostingPosted, Code: "TARGET_ERASED", Settlement: sql.NullString{String: string(ledger.SettlementAmbiguous), Valid: true}},
					{Posting: FleetPostingPlanned}, // another operation's plans do not count
					{Posting: FleetPostingRefused}, {Posting: FleetPostingCancelled}, {Posting: "SKIPPED"},
				}
				for _, settlement := range []string{"CONFIRMED", "FAILED", "SUPERSEDED", "RESOLVED_CONFIRMED", "RESOLVED_FAILED"} {
					lines = append(lines, FleetOperationLine{Posting: FleetPostingPosted, Settlement: sql.NullString{String: settlement, Valid: true}})
				}
				for i := range lines {
					lines[i].OperationId, lines[i].Ordinal = int64(donor.ID), int32(i+1)
				}
				require.NoError(t, api.RDB.DB(donorCtx).Create(&lines).Error)
				if scope == "tenant" {
					// A different tenant's open evidence must not enter the tenant count.
					foreign := donor
					foreign.ID, foreign.TenantId = 0, ""
					foreignCtx := core.WithTenant(ctx, "globex")
					require.NoError(t, api.RDB.DB(foreignCtx).Create(&foreign).Error)
					require.NoError(t, api.RDB.DB(foreignCtx).Create(&FleetOperationLine{OperationId: int64(foreign.ID), Ordinal: 1, Posting: FleetPostingPosted}).Error)
					fleetAdmissionBudgets.tenantEvidenceOpen = 2
				} else {
					fleetAdmissionBudgets.instanceEvidenceOpen = 2
				}
				before := events(t, api, ctx, op)
				_, err := admissionCall(api, ctx, op, resume)
				requireAdmissionQuota(t, err, scope+".evidenceOpenLines")
				still, err := api.fleetOperationByToken(ctx, op.Token)
				require.NoError(t, err)
				require.Equal(t, op.ExecutionEpoch, still.ExecutionEpoch)
				require.Equal(t, op.Dispatch, still.Dispatch)
				require.Equal(t, before, events(t, api, ctx, op))
				if scope == "tenant" {
					fleetAdmissionBudgets.tenantEvidenceOpen = 3
				} else {
					fleetAdmissionBudgets.instanceEvidenceOpen = 3
				}
				got, err := admissionCall(api, ctx, op, resume)
				require.NoError(t, err, "equal passes: two open POSTED lines plus this operation's one PLANNED line")
				require.Equal(t, FleetDispatchRunning, got.Dispatch)
			})
		}
	}
}

func TestAdmissionEvidenceCountsUseTheControlTransaction(t *testing.T) {
	armed(t)
	api, ctx := admissionFixture(t)
	op := admissionCandidate(t, api, ctx, false)
	var controlPool gorm.ConnPool
	counts := 0
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:admission-transaction", func(tx *gorm.DB) {
		if tx.Statement.Table == "fleet_operations" {
			if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); ok {
				controlPool = tx.Statement.ConnPool
			}
		}
		if tx.Statement.Table == "fleet_operation_lines" && strings.Contains(tx.Statement.SQL.String(), "settlement IS NULL") {
			counts++
			require.NotNil(t, controlPool)
			require.Same(t, controlPool, tx.Statement.ConnPool)
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove("test:admission-transaction")) })
	_, err := admissionCall(api, ctx, op, false)
	require.NoError(t, err)
	require.Equal(t, 2, counts)
}

// SQLite cannot observe PostgreSQL advisory locks. Pin the critical ordering here;
// the PG test additionally observes both locks on the counting connection itself.
func TestAdmissionEvidenceCountsFollowAdmissionLocks(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "api_fleet_operation_control.go", nil, 0)
	require.NoError(t, err)
	var lock, count token.Pos
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "fleetAdmitDispatching" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok {
				switch id.Name {
				case "fleetLock":
					lock = call.Pos()
				case "fleetEvidenceOpenCount":
					if count == token.NoPos {
						count = call.Pos()
					}
				}
			}
			return true
		})
	}
	require.NotEqual(t, token.NoPos, lock)
	require.NotEqual(t, token.NoPos, count)
	require.Less(t, lock, count, "evidence must be counted after tenant then instance K_adm")
}

func TestAdmissionOrphanScanPrecedesTheControlFence(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "api_fleet_operation_control.go", nil, 0)
	require.NoError(t, err)
	checked := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || (fn.Name.Name != "StartFleetOperation" && fn.Name.Name != "ResumeFleetOperation") {
			continue
		}
		var scan, control token.Pos
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					switch selector.Sel.Name {
					case "fleetCheckOrphans":
						scan = call.Pos()
					case "fleetControl":
						control = call.Pos()
					}
				}
			}
			return true
		})
		require.NotEqual(t, token.NoPos, scan, fn.Name.Name)
		require.NotEqual(t, token.NoPos, control, fn.Name.Name)
		require.Less(t, scan, control, "the 5s orphan scan must run before the fence: %s", fn.Name.Name)
		checked++
	}
	require.Equal(t, 2, checked)
}
