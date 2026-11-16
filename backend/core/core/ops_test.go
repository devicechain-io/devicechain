// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestPublicMuxExcludesOperationalEndpoints(t *testing.T) {
	ms := &Microservice{FunctionalArea: "ops-isolation"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	gate := NewReadinessGate()
	gate.MarkReadyWithoutAuthSurface()
	ms.RegisterProbes(gate)
	ms.Mux().HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	public := httptest.NewServer(ms.Mux())
	defer public.Close()
	ops := httptest.NewServer(ms.OpsMux())
	defer ops.Close()
	for _, path := range []string{"/metrics", "/healthz", "/readyz", "/%6detrics", "//metrics"} {
		t.Run(path, func(t *testing.T) {
			// Follow ServeMux's canonical-path redirects as a real scraper does.
			resp, err := http.Get(public.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("public %s = %d, want 404", path, resp.StatusCode)
			}
			resp, err = http.Get(ops.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("ops %s = %d, want 200", path, resp.StatusCode)
			}
			if strings.Contains(path, "metrics") || path == "/%6detrics" {
				var body strings.Builder
				_, _ = io.Copy(&body, resp.Body)
				if !strings.Contains(body.String(), "# HELP") {
					t.Error("ops metrics missing exposition")
				}
			}
		})
	}
	rec := httptest.NewRecorder()
	ms.Mux().ServeHTTP(rec, httptest.NewRequest("GET", "/graphql", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatal("public application handler stopped answering")
	}
	if ms.OpsMux() != ms.OpsMux() || ms.OpsMux() == ms.Mux() {
		t.Fatal("ops mux must be stable and separate")
	}
	server := ms.NewOpsServer()
	if server.server.Addr != ":8083" || server.server.Handler != ms.OpsMux() {
		t.Fatal("ops server port or handler differs from chart contract")
	}
	_ = server.Shutdown(context.Background())
}
