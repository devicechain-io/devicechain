// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

type opsTestResolver struct{}

func (*opsTestResolver) Ok() bool { return true }

func opsTestManager(t *testing.T, area string) (*GraphQLManager, *core.Microservice) {
	t.Helper()
	ms := &core.Microservice{FunctionalArea: area}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	gate := core.NewReadinessGate()
	gate.MarkReadyWithoutAuthSurface()
	gql := NewGraphQLManager(ms, core.NewNoOpLifecycleCallbacks(), MustParseSchema(`type Query { ok: Boolean! }`, &opsTestResolver{}), nil, gate)
	if err := gql.ExecuteInitialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return gql, ms
}

func opsPublicStatus(t *testing.T, ms *core.Microservice, method, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	ms.Mux().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code
}

func TestGraphQLOpsListenerLifecycle(t *testing.T) {
	gql, _ := opsTestManager(t, "ops-lifecycle")
	gql.Port = ephemeralPort
	ctx := context.Background()
	if err := gql.ExecuteStart(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gql.ExecuteStop(ctx) })
	for url, want := range map[string]int{
		"http://" + gql.opsServer.Addr() + "/healthz": http.StatusOK,
		"http://" + gql.Server.Addr() + "/graphql":    http.StatusBadRequest,
	} {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s = %d, want %d", url, resp.StatusCode, want)
		}
	}
	addr := gql.opsServer.Addr()
	if err := gql.ExecuteStop(ctx); err != nil {
		t.Fatal(err)
	}
	if resp, err := http.Get("http://" + addr + "/healthz"); err == nil {
		resp.Body.Close()
		t.Fatal("operations listener still answers after stop")
	}
}

func TestGraphQLPublicMuxExcludesOperationalEndpoints(t *testing.T) {
	_, ms := opsTestManager(t, "device-management")
	for _, path := range []string{"/metrics", "/healthz", "/readyz", "/%6detrics", "//metrics"} {
		rec := httptest.NewRecorder()
		ms.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound && (rec.Code < 300 || rec.Code >= 400) {
			t.Errorf("public %s = %d, want 404 or redirect", path, rec.Code)
		}
		rec = httptest.NewRecorder()
		ms.OpsMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		want := http.StatusOK
		if path == "//metrics" {
			if rec.Code < 300 || rec.Code >= 400 {
				t.Errorf("ops %s = %d, want a canonical-path redirect", path, rec.Code)
			}
			continue
		}
		if rec.Code != want {
			t.Errorf("ops %s = %d, want %d", path, rec.Code, want)
		}
	}
	if got := opsPublicStatus(t, ms, http.MethodGet, "/graphql"); got != http.StatusBadRequest {
		t.Errorf("public GraphQL handler = %d, want 400", got)
	}
}

func TestGraphQLOpsClosesWhenAPIStartFails(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, rawPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	gql, _ := opsTestManager(t, "ops-bind-failure")
	gql.Port = int32(port)
	gql.opsServerEphemeral = true
	ctx := context.Background()
	t.Cleanup(func() { _ = gql.ExecuteStop(ctx) })
	if err := gql.ExecuteStart(ctx); err == nil {
		t.Fatal("API bind collision was not reported")
	}
	if gql.opsServer == nil || gql.opsServer.Addr() == "" {
		t.Fatal("ops listener did not start before the API listener")
	}
	if resp, err := http.Get("http://" + gql.opsServer.Addr() + "/healthz"); err == nil {
		resp.Body.Close()
		t.Fatal("ops listener leaked after API startup failed")
	}
}
