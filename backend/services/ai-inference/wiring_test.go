// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/devicechain-io/dc-ai-inference/graphql"
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
	require.True(t, init["registerAdminHandler"], "the admin plane is no longer registered")
	// The auth gate is started inside the AfterRdb hook, a closure in the initialize
	// function, so it is read from there. Without it the readiness gate never opens.
	require.True(t, init["Microservice.StartInstanceAuthGate"],
		"the auth gate is no longer started before the GraphQL manager is built")
	require.True(t, execTimeoutIsSetOnTheSpec(t),
		"the GraphQL Spec no longer carries graphQLExecTimeout(), so the data plane silently "+
			"reverts to the platform execution bound and ends calls that are waiting on a provider")
}

// execTimeoutIsSetOnTheSpec reports whether main.go writes `ExecTimeout: graphQLExecTimeout()`
// into a composite literal. The raised deadline is applied by core/service between building
// the manager and initializing it, so the only thing this service controls is that the Spec
// carries it.
func execTimeoutIsSetOnTheSpec(t *testing.T) bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	require.NoError(t, err)
	set := false
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "ExecTimeout" {
			if c, ok := kv.Value.(*ast.CallExpr); ok {
				if f, ok := c.Fun.(*ast.Ident); ok && f.Name == "graphQLExecTimeout" {
					set = true
				}
			}
		}
		return true
	})
	return set
}

// TestTheGraphQLServerIsStoppedBeforeTheDatabase drives beforeMicroserviceStopped over the
// real core/service sequence. The relational manager is built but never initialized (that
// needs a live Postgres), so its stop is refused: that refusal is the end marker proving
// the stopper reached the database AFTER the GraphQL manager, whose stop is recorded.
func TestTheGraphQLServerIsStoppedBeforeTheDatabase(t *testing.T) {
	prevMs, prevSvc := Microservice, Svc
	t.Cleanup(func() { Microservice, Svc = prevMs, prevSvc })
	Microservice = &core.Microservice{
		InstanceId:     "ai-inference-shutdown",
		FunctionalArea: "ai-inference-shutdown",
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
		InstanceId:                   "ai-inference-initfail",
		FunctionalArea:               "ai-inference-initfail",
		Readiness:                    core.NewReadinessGate(),
		MicroserviceConfigurationRaw: []byte(`{}`),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())

	err := afterMicroserviceInitialized(context.Background())

	require.ErrorContains(t, err, "not currently supported",
		"a relational manager that refused to initialize did not fail the initialize callback")
}
