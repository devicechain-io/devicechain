// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-update-management/config"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/prometheus/client_golang/prometheus"
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
	requireCalls(t, "afterMicroserviceStarted", "Svc.Start", "ReportsConsumer.Start", "RosterConsumer.Start", "DeletionConsumer.Start", "startSweeper")
	requireCalls(t, "startSweeper", "processor.NewDeadlineSweeper", "Sweeper.Start")
	requireCalls(t, "beforeMicroserviceStopped", "Sweeper.Stop", "DeletionConsumer.Stop", "RosterConsumer.Stop", "ReportsConsumer.Stop", "Svc.Stop")
	requireCalls(t, "beforeMicroserviceTerminated", "DeletionConsumer.Terminate", "RosterConsumer.Terminate", "ReportsConsumer.Terminate", "Svc.Terminate")
	requireCalls(t, "createNatsComponents", "ReportsConsumer.Initialize", "RosterConsumer.Initialize", "DeletionConsumer.Initialize", "processor.NewDesiredWriter")
	requireCalls(t, "afterMicroserviceInitialized", "buildMetrics", "registerUploadHandler", "registerDownloadHandler")
}

// startSweeper builds the sweep from the configured cadence and starts it; stopping it ends it.
// The roster durable starts at NEW facts: a first start (or a re-created durable) that replayed the
// stream would re-offer on every fact in its retention window.
func TestTheRosterReaderStartsFromNewFacts(t *testing.T) {
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)
	require.Contains(t, string(src), "nmgr.NewReader(streams.DeviceRoster, messaging.ReaderWithDeliverNew())")
}

func TestStartSweeperAndRolloutDriverRegisterPassOutcomes(t *testing.T) {
	prevCfg, prevApi, prevSweeper, prevMetrics := Configuration, Api, Sweeper, ReportsMetrics
	prevMs, prevRollouts := Microservice, Rollouts
	prevRolloutStats, prevDownloads := RolloutStats, DownloadStats
	prevSweepPass, prevRolloutPass := SweepPassMetrics, RolloutPassMetrics
	t.Cleanup(func() {
		if Sweeper != nil {
			Sweeper.Stop(context.Background())
		}
		if Rollouts != nil {
			Rollouts.Stop(context.Background())
		}
		Configuration, Api, Sweeper, ReportsMetrics = prevCfg, prevApi, prevSweeper, prevMetrics
		Microservice, Rollouts = prevMs, prevRollouts
		RolloutStats, DownloadStats = prevRolloutStats, prevDownloads
		SweepPassMetrics, RolloutPassMetrics = prevSweepPass, prevRolloutPass
	})
	Configuration = config.NewUpdateManagementConfiguration()
	Configuration.Updates.SweepIntervalSeconds = 3600
	Api = &model.Api{}
	Sweeper, Rollouts = nil, nil
	reg := prometheus.NewRegistry()
	Microservice = &core.Microservice{InstanceId: "inst", FunctionalArea: "update-management"}
	Microservice.UseMetricsRegistry(reg)
	buildMetrics()

	// Twice: the loops are built on every start, so a collector they registered themselves would
	// panic as a duplicate on the second start.
	for range 2 {
		require.NoError(t, startSweeper())
		require.NoError(t, startRolloutDriver())
		require.NotNil(t, Sweeper, "startSweeper started nothing")
		require.NotNil(t, Rollouts, "startRolloutDriver started nothing")
		require.NoError(t, Sweeper.Stop(context.Background()))
		require.NoError(t, Rollouts.Stop(context.Background()))
	}

	families, err := reg.Gather()
	require.NoError(t, err)
	for _, task := range []string{"deadline_sweep", "rollout_driver"} {
		name := "devicechain_updatemanagement_" + task + "_passes_total"
		outcomes := map[string]float64{}
		for _, family := range families {
			if family.GetName() != name {
				continue
			}
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "outcome" {
						outcomes[label.GetValue()] = metric.GetCounter().GetValue()
					}
				}
			}
		}
		require.Equal(t, map[string]float64{
			core.PassComplete: 0, core.PassPartial: 0, core.PassFailed: 0,
			core.PassSkipped: 0, core.PassCancelled: 0,
		}, outcomes, "%s must expose every outcome at zero before its first pass", name)
	}
}

func TestTheDeletionReaderReplaysRetainedFacts(t *testing.T) {
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)
	require.Contains(t, string(src), "nmgr.NewReader(streams.EntityDeleted)")
}
