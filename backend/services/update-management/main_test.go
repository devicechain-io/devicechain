// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The content endpoint is served by this microservice's own mux. Registering it anywhere
// else (http.DefaultServeMux, which no server here serves) would answer 404 at the edge
// while every test of the handler itself stayed green - which is why this asserts the
// route through the function main calls, not through the handler.
func TestTheUploadRouteIsOnTheOwnedMux(t *testing.T) {
	prevMs, prevApi := Microservice, Api
	t.Cleanup(func() { Microservice, Api = prevMs, prevApi })
	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "update-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Api = &model.Api{}

	registerUploadHandler()

	_, pattern := Microservice.Mux().Handler(httptest.NewRequest(http.MethodPut, "/updates/artifacts/fw-1/content", nil))
	require.Equal(t, "PUT /updates/artifacts/{token}/content", pattern)

	// Only PUT is routed; any other method is refused by the mux before the handler.
	rec := httptest.NewRecorder()
	Microservice.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/updates/artifacts/fw-1/content", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
