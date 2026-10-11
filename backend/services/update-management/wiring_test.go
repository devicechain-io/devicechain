// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/devicechain-io/dc-update-management/config"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/stretchr/testify/require"
)

// callsIn returns the calls made inside the named top-level function of main.go, spelled as the
// selector they are written with ("ReportsConsumer.Start", "startSweeper").
//
// Nothing else exercises the lifecycle callbacks: they need a whole service to run, and a call
// dropped from one compiles, passes every other test, and leaves a consumer that never reads or a
// sweep that never runs. This reads what the function actually does.
func callsIn(t *testing.T, fn string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	require.NoError(t, err)
	out := map[string]bool{}
	found := false
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		found = true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.Ident:
				out[f.Name] = true
			case *ast.SelectorExpr:
				if x, ok := f.X.(*ast.Ident); ok {
					out[x.Name+"."+f.Sel.Name] = true
				}
			}
			return true
		})
	}
	require.True(t, found, "main.go has no function %s", fn)
	return out
}

func requireCalls(t *testing.T, fn string, want ...string) {
	t.Helper()
	got := callsIn(t, fn)
	for _, w := range want {
		require.True(t, got[w], "%s no longer calls %s", fn, w)
	}
}

func TestTheLifecycleStartsTheConsumerAndTheSweep(t *testing.T) {
	requireCalls(t, "afterMicroserviceStarted", "Svc.Start", "ReportsConsumer.Start", "startSweeper")
	requireCalls(t, "startSweeper", "processor.NewDeadlineSweeper", "Sweeper.Start")
	requireCalls(t, "beforeMicroserviceStopped", "Sweeper.Stop", "ReportsConsumer.Stop", "Svc.Stop")
	requireCalls(t, "beforeMicroserviceTerminated", "ReportsConsumer.Terminate", "Svc.Terminate")
	requireCalls(t, "createNatsComponents", "ReportsConsumer.Initialize", "processor.NewDesiredWriter")
	requireCalls(t, "afterMicroserviceInitialized", "buildMetrics", "registerUploadHandler", "registerDownloadHandler")
}

// startSweeper builds the sweep from the configured cadence and starts it; stopping it ends it.
func TestStartSweeperStartsTheConfiguredSweep(t *testing.T) {
	prevCfg, prevApi, prevSweeper, prevMetrics := Configuration, Api, Sweeper, ReportsMetrics
	t.Cleanup(func() { Configuration, Api, Sweeper, ReportsMetrics = prevCfg, prevApi, prevSweeper, prevMetrics })
	Configuration = config.NewUpdateManagementConfiguration()
	Configuration.Updates.SweepIntervalSeconds = 3600
	Api = &model.Api{}
	Sweeper = nil

	startSweeper()
	require.NotNil(t, Sweeper, "startSweeper started nothing")
	Sweeper.Stop()
}
