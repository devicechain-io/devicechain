// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-microservice/core"
)

// This service's one route of its own must be on the microservice's OWN mux.
//
// 🔴 A ROUTE LEFT ON http.DefaultServeMux IS NOT AN ERROR ANYWHERE. Every server in
// this repository now serves an explicit handler, so the registration still compiles
// and still runs — onto a mux no listener consults. /admin/graphql would answer 404,
// and the operator-facing admin plane would simply not exist, with nothing in the logs
// to say why.
//
// The route is resolved rather than invoked: ServeMux.Handler performs the match and
// returns the pattern, so this asks the question under test — is the path served, and
// by this mux — without standing up a database, a secret store or an inference client.
func TestAdminGraphqlIsOnTheOwnedMux(t *testing.T) {
	prevMs := Microservice
	t.Cleanup(func() { Microservice = prevMs })

	Microservice = &core.Microservice{
		InstanceId:     "test",
		FunctionalArea: "ai-inference",
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())

	registerAdminHandler(map[gqlcore.ContextKey]interface{}{}, nil)

	_, pattern := Microservice.Mux().Handler(httptest.NewRequest(http.MethodGet, "/admin/graphql", nil))
	require.Equal(t, "/admin/graphql", pattern,
		"/admin/graphql is not served by this microservice's mux; the admin plane would 404")
}
