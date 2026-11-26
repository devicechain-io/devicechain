// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func TestFleetTestOperationCleanupReleasesDispatchSlot(t *testing.T) {
	for _, state := range []string{FleetDispatchRunning, FleetDispatchPaused} {
		t.Run(state, func(t *testing.T) {
			armed(t)
			api, ctx := fleetFixture(t, 2)
			otherCtx := core.WithTenant(context.Background(), "globex")
			other := readyOp(t, api, otherCtx, "owned", FleetSafetyRequest{}, "other1")
			other, err := api.StartFleetOperation(otherCtx, other.Token, other.ContractDigest, alice)
			require.NoError(t, err)
			cleanupFleetTestOperation(t, api, otherCtx, other.Token)
			var owned *FleetOperation
			t.Run("owner", func(t *testing.T) {
				requestCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				owned = readyOp(t, api, requestCtx, "owned", FleetSafetyRequest{}, "d1", "d2")
				cleanupFleetTestOperation(t, api, requestCtx, owned.Token)
				cleanupFleetTestOperation(t, api, requestCtx, owned.Token) // idempotent
				_, err := api.StartFleetOperation(requestCtx, owned.Token, owned.ContractDigest, alice)
				require.NoError(t, err)
				if state == FleetDispatchPaused {
					_, err = api.PauseFleetOperation(requestCtx, owned.Token, alice.Name)
					require.NoError(t, err)
				}
				cancel() // cleanup must work after the request has ended
			})
			got, err := api.fleetOperationByToken(ctx, owned.Token)
			require.NoError(t, err)
			require.Equal(t, FleetDispatchAborted, got.Dispatch)
			progress, err := api.FleetOperationProgressOf(ctx, got)
			require.NoError(t, err)
			require.Zero(t, progress.Planned)
			kinds := events(t, api, ctx, got)
			require.Equal(t, fleetEventAbort, kinds[len(kinds)-1], "cleanup uses the control API")
			still, err := api.fleetOperationByToken(otherCtx, other.Token)
			require.NoError(t, err)
			require.Equal(t, FleetDispatchRunning, still.Dispatch, "cleanup keeps the owning tenant")
		})
	}
}

func TestFleetTestOperationCleanupPreservesFinishedOperations(t *testing.T) {
	for _, state := range []string{FleetDispatchAborted, FleetDispatchDispatched, FleetDispatchReady} {
		t.Run(state, func(t *testing.T) {
			armed(t)
			api, ctx := fleetFixture(t, 2)
			op := readyOp(t, api, ctx, "finished", FleetSafetyRequest{}, "d1", "d2")
			require.NoError(t, api.RDB.DB(ctx).Model(op).Update("dispatch", state).Error)
			before := events(t, api, ctx, op)
			t.Run("owner", func(t *testing.T) {
				cleanupFleetTestOperation(t, api, ctx, op.Token)
				cleanupFleetTestOperation(t, api, ctx, "purged-header")
			})
			got, err := api.fleetOperationByToken(ctx, op.Token)
			require.NoError(t, err)
			require.Equal(t, state, got.Dispatch)
			require.Equal(t, op.ExecutionEpoch, got.ExecutionEpoch)
			require.Equal(t, before, events(t, api, ctx, got))
		})
	}
}

func TestFleetTestLeakGuardChecksEveryTenantAndBothDispatchingStates(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	for _, state := range []string{FleetDispatchRunning, FleetDispatchPaused} {
		otherCtx := core.WithTenant(context.Background(), "globex")
		op := readyOp(t, api, otherCtx, state, FleetSafetyRequest{}, "other1")
		require.NoError(t, api.RDB.DB(otherCtx).Model(op).Update("dispatch", state).Error)
		err := fleetTestDispatchingError(api.RDB.DB(ctx), "TestLeakingOwner/subtest")
		require.ErrorContains(t, err, "TestLeakingOwner/subtest")
		require.ErrorContains(t, err, "globex")
		require.ErrorContains(t, err, state)
		got, readErr := api.fleetOperationByToken(otherCtx, op.Token)
		require.NoError(t, readErr)
		require.Equal(t, state, got.Dispatch, "the guard must report without silently repairing")
		require.NoError(t, api.RDB.DB(otherCtx).Model(op).Update("dispatch", FleetDispatchAborted).Error)
	}
	readyOp(t, api, ctx, "ready", FleetSafetyRequest{}, "d1")
	require.NoError(t, fleetTestDispatchingError(api.RDB.DB(ctx), t.Name()), "terminal and unstarted rows do not consume dispatch slots")
	require.NoError(t, api.RDB.Database.Migrator().DropTable(&FleetOperation{}))
	require.Error(t, fleetTestDispatchingError(api.RDB.DB(ctx), t.Name()), "a failed database query must not pass the guard")
}

func TestFleetProtocol2FixtureCleanupRemovesOnlyItsOwnRows(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	op := running(t, api, ctx, "original", FleetSafetyRequest{})
	cleanupFleetTestOperation(t, api, ctx, op.Token)
	var copyID uint
	t.Run("owner", func(t *testing.T) {
		requestCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		db := api.RDB.DB(requestCtx)
		copy := insertProtocol2OperationForTest(t, db, *op)
		copyID = copy.ID
		var lines []FleetOperationLine
		require.NoError(t, db.Where("operation_id = ?", op.ID).Find(&lines).Error)
		for _, line := range lines {
			line.ID, line.OperationId = 0, int64(copy.ID)
			require.NoError(t, db.Create(&line).Error)
		}
		require.NoError(t, db.Create(&FleetOperationEvent{OperationId: int64(copy.ID), Kind: fleetEventPause}).Error)
		cancel()
	})
	for _, model := range []any{&FleetOperationLine{}, &FleetOperationEvent{}, &FleetOperationContract{}, &FleetOperationRuntime{}} {
		var count int64
		require.NoError(t, api.RDB.DB(ctx).Model(model).Where("operation_id = ?", copyID).Count(&count).Error)
		require.Zero(t, count)
	}
	copy, err := api.fleetOperationByToken(ctx, op.Token+"-protocol2")
	require.NoError(t, err)
	require.Nil(t, copy, "a seeded newer operation must not keep a dispatch slot")
	original, err := api.fleetOperationByToken(ctx, op.Token)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchRunning, original.Dispatch)
	progress, err := api.FleetOperationProgressOf(ctx, original)
	require.NoError(t, err)
	require.Equal(t, int32(2), progress.Planned)
}

// The ordinary gate cannot run Postgres tests. Keep their start sites covered even
// without the integration tag; the behavioral cleanup tests use SQLite.
func TestFleetIntegrationStartsRegisterCleanup(t *testing.T) {
	files, err := filepath.Glob("*integration_test.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			starts, cleanups := 0, 0
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if fun.Sel.Name == "StartFleetOperation" {
						starts++
					}
				case *ast.Ident:
					if fun.Name == "admissionCall" {
						starts++
					}
					if fun.Name == "cleanupFleetTestOperation" {
						cleanups++
					}
				}
				return true
			})
			if starts > 0 {
				require.Positive(t, cleanups, "%s: %s starts operations without registering cleanup", path, fn.Name.Name)
			}
		}
	}
}

func TestFleetIntegrationDatabasesRegisterLeakGuard(t *testing.T) {
	for path, helper := range map[string]string{
		"geofence_toctou_integration_test.go":   "newPostgresRdbManager",
		"fleet_driver_loop_integration_test.go": "driverPGManager",
	} {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, err)
		registered := false
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != helper {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				fun, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || fun.Sel.Name != "Cleanup" {
					return true
				}
				ast.Inspect(call, func(node ast.Node) bool {
					if call, ok := node.(*ast.CallExpr); ok {
						if fun, ok := call.Fun.(*ast.Ident); ok && fun.Name == "checkFleetTestDispatching" {
							registered = true
						}
					}
					return true
				})
				return false
			})
		}
		require.True(t, registered, "%s must check for leaks during test cleanup", helper)
	}
}
