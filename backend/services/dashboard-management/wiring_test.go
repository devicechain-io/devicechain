// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/devicechain-io/dc-dashboard-management/graphql"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// callsIn returns the calls made inside the named top-level function of main.go, spelled as
// the selector they are written with ("Svc.Start", "service.New").
//
// Nothing else exercises the lifecycle callbacks: they need a whole service to run, and a
// call dropped from one compiles and passes every other test. This reads what main.go
// actually does.
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
			if call, ok := n.(*ast.CallExpr); ok {
				switch f := call.Fun.(type) {
				case *ast.Ident:
					out[f.Name] = true
				case *ast.SelectorExpr:
					if x, ok := f.X.(*ast.Ident); ok {
						out[x.Name+"."+f.Sel.Name] = true
					}
				}
			}
			return true
		})
	}
	require.True(t, found, "main.go has no function %s", fn)
	return out
}

// TestMainAssemblesItsManagersThroughCoreService pins that this service drives its managers
// through core/service rather than constructing and sequencing them itself, which is how
// lifecycle order drifts between services.
func TestMainAssemblesItsManagersThroughCoreService(t *testing.T) {
	init := callsIn(t, "afterMicroserviceInitialized")
	require.True(t, init["service.New"] && init["Svc.Initialize"], "initialize no longer assembles through core/service")
	for _, hand := range []string{"rdb.NewRdbManager", "gqlcore.NewGraphQLManager", "messaging.NewNatsManager"} {
		require.False(t, init[hand], "main.go constructs a manager by hand (%s)", hand)
	}
	require.True(t, callsIn(t, "afterMicroserviceStarted")["Svc.Start"])
	require.True(t, callsIn(t, "beforeMicroserviceStopped")["Svc.Stop"])
	require.True(t, callsIn(t, "beforeMicroserviceTerminated")["Svc.Terminate"])
	require.True(t, callsIn(t, "afterRdb")["Microservice.StartInstanceAuthGate"],
		"the auth gate is no longer started before the GraphQL manager is built")
}

// TestTheGraphQLServerIsStoppedBeforeTheDatabase drives beforeMicroserviceStopped over the
// real core/service sequence. The relational manager is built but never initialized (that
// needs a live Postgres), so its stop is refused: that refusal is the end marker proving
// the stopper reached the database AFTER the GraphQL manager, whose stop is recorded.
func TestTheGraphQLServerIsStoppedBeforeTheDatabase(t *testing.T) {
	prevMs, prevSvc := Microservice, Svc
	t.Cleanup(func() { Microservice, Svc = prevMs, prevSvc })
	Microservice = &core.Microservice{
		InstanceId:     "dashboard-shutdown",
		FunctionalArea: "dashboard-shutdown",
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())

	graphqlStopped := false
	cb := core.NewNoOpLifecycleCallbacks()
	cb.Stopper.Postprocess = func(context.Context) error { graphqlStopped = true; return nil }
	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})
	gql := gqlcore.NewGraphQLManager(Microservice, cb, parsed,
		map[gqlcore.ContextKey]interface{}{}, Microservice.Readiness)
	require.NoError(t, gql.Initialize(context.Background()))
	db := rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), nil,
		Microservice.InstanceConfiguration.Persistence.Rdb, mscfg.MicroserviceDatastoreConfiguration{})
	Svc = service.FromManagers(Microservice, service.Managers{Rdb: db, GraphQL: gql})

	err := beforeMicroserviceStopped(context.Background())

	require.True(t, graphqlStopped, "the GraphQL manager was never stopped (the database was stopped first)")
	require.ErrorContains(t, err, "relational database manager",
		"beforeMicroserviceStopped did not reach the database after the GraphQL stop")
}

// TestAnInitializeFailureIsReturnedFromTheCallback pins that a manager refusing to initialize
// fails the service's initialize callback, which is what makes the process exit non-zero
// rather than run on with no database. No relational database type is configured, so the
// relational manager refuses at once without dialling anything.
func TestAnInitializeFailureIsReturnedFromTheCallback(t *testing.T) {
	prevMs, prevSvc, prevCfg := Microservice, Svc, Configuration
	t.Cleanup(func() { Microservice, Svc, Configuration = prevMs, prevSvc, prevCfg })
	Microservice = &core.Microservice{
		InstanceId:                   "dashboard-initfail",
		FunctionalArea:               "dashboard-initfail",
		Readiness:                    core.NewReadinessGate(),
		MicroserviceConfigurationRaw: []byte(`{}`),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())

	err := afterMicroserviceInitialized(context.Background())

	require.ErrorContains(t, err, "not currently supported",
		"a relational manager that refused to initialize did not fail the initialize callback")
}

// The share reconciler (ADR-081) is the one thing this service reads from the broker, and
// nothing else exercises its wiring: a dropped Start leaves shares naming deleted customers
// in place forever, and every other test stays green. It is built on the entity-deleted
// reader, started after the managers and stopped before them.
func TestTheShareReconcilerIsWired(t *testing.T) {
	require.True(t, callsIn(t, "createNatsComponents")["nmgr.NewReader"])
	require.True(t, callsIn(t, "createNatsComponents")["processor.NewShareReconciler"])
	require.True(t, callsIn(t, "afterMicroserviceStarted")["ShareReconciler.Start"])
	require.True(t, callsIn(t, "beforeMicroserviceStopped")["ShareReconciler.Stop"])
	require.True(t, callsIn(t, "afterRdb")["wireShareChecker"])
}
