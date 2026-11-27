// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// A failing sweep must not stop the fast pass (it ran first and returned, before), nor
// re-run the full sweep on every five-second tick.
func TestSettlementTickFastPassSurvivesAFailingSweep(t *testing.T) {
	now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	sweeps, fasts := 0, 0
	boom := errors.New("sweep failed")
	tick := &settlementTick{
		now:   func() time.Time { return now },
		sweep: func(context.Context) error { sweeps++; return boom },
		fast:  func(context.Context) error { fasts++; return nil },
	}
	err := tick.run(context.Background())
	require.ErrorIs(t, err, boom)
	require.Equal(t, 1, fasts, "the fast pass runs although the sweep failed")
	for range 5 {
		now = now.Add(5 * time.Second)
		require.NoError(t, tick.run(context.Background()))
	}
	require.Equal(t, 1, sweeps, "a failed sweep is not retried on every tick")
	require.Equal(t, 6, fasts)
	now = now.Add(fleetSettlementSweepRetry)
	require.ErrorIs(t, tick.run(context.Background()), boom)
	require.Equal(t, 2, sweeps, "a failed sweep is retried")

	// A successful (or skipped) sweep waits the full interval.
	tick.sweep = func(context.Context) error { sweeps++; return core.ErrPassSkipped }
	now = now.Add(fleetSettlementSweepRetry)
	require.NoError(t, tick.run(context.Background()))
	require.Equal(t, 3, sweeps)
	now = now.Add(fleetSettlementSweepEvery - time.Second)
	require.NoError(t, tick.run(context.Background()))
	require.Equal(t, 3, sweeps)
	now = now.Add(time.Second)
	require.NoError(t, tick.run(context.Background()))
	require.Equal(t, 4, sweeps)
}

// The reconciler lag gauge keeps its cadence because nothing but one serial task can write it:
// MeasureFleetReconcilerLag is the only function that sets it (besides the constructor, which
// writes NaN), and the bulk-change-reconciler-lag task is its only caller. A second writer (a
// settlement pass sampling it again, say) or a second caller (a pass that also measures) would
// let two schedules interleave, so either fails here, wherever in the module it is added.
func TestReconcilerLagGaugeHasOneWriterAndOneCaller(t *testing.T) {
	source, err := os.ReadFile("main.go")
	require.NoError(t, err)
	for _, call := range []string{"initFleetSettlement()", "FleetReconcilerLag.Start(ctx)", "FleetReconcilerLag.Stop(ctx)", "FleetReconcilerLag.Terminate(ctx)"} {
		require.Contains(t, string(source), call)
	}
	writers, callers := map[string]int{}, map[string]int{}
	fset := token.NewFileSet()
	require.NoError(t, filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			where := filepath.ToSlash(path) + ":" + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "reconcilerLag":
					writers[where]++
				case "MeasureFleetReconcilerLag":
					callers[where]++
				}
				return true
			})
		}
		return nil
	}))
	require.Equal(t, map[string]int{"model/fleet_metrics.go:NewBulkChangeMetrics": 1, "model/fleet_reconciler_lag.go:MeasureFleetReconcilerLag": 2}, writers,
		"only the constructor and MeasureFleetReconcilerLag touch the reconciler lag gauge")
	require.Equal(t, map[string]int{"fleet_settlement.go:initFleetSettlement": 1}, callers,
		"only the bulk-change-reconciler-lag task measures the reconciler lag")
}
