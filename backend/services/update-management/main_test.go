// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-update-management/config"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The content endpoint is served by this microservice's own mux. Registering it anywhere
// else (http.DefaultServeMux, which no server here serves) would answer 404 at the edge
// while every test of the handler itself stayed green - which is why this asserts the
// route through the function main calls, not through the handler.
func TestTheUploadRouteIsOnTheOwnedMux(t *testing.T) {
	prevMs, prevApi, prevCfg := Microservice, Api, Configuration
	t.Cleanup(func() { Microservice, Api, Configuration = prevMs, prevApi, prevCfg })
	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "update-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Api = &model.Api{}
	Configuration = config.NewUpdateManagementConfiguration()

	registerUploadHandler()

	_, pattern := Microservice.Mux().Handler(httptest.NewRequest(http.MethodPut, "/updates/artifacts/fw-1/content", nil))
	require.Equal(t, "PUT /updates/artifacts/{token}/content", pattern)

	// Only PUT is routed; any other method is refused by the mux before the handler.
	rec := httptest.NewRecorder()
	Microservice.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/updates/artifacts/fw-1/content", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// buildApi is what makes the catalogue safe to run: it wires the device-profile check and
// the tenant-lifecycle gate from the instance configuration. A wiring that quietly left
// either nil would still compile and still pass every test of the model, which is why this
// drives the real function with a real-shaped configuration.
func TestBuildApiWiresTheProfileCheckAndTheLifecycleGate(t *testing.T) {
	prevMs, prevCfg, prevRdb := Microservice, Configuration, RdbManager
	t.Cleanup(func() { Microservice, Configuration, RdbManager = prevMs, prevCfg, prevRdb })

	newMs := func(secret string, dmPort uint32) *core.Microservice {
		ms := &core.Microservice{InstanceId: "test", FunctionalArea: "update-management"}
		ms.UseMetricsRegistry(prometheus.NewRegistry())
		infra := &ms.InstanceConfiguration.Infrastructure
		infra.Blob = mscfg.BlobConfiguration{Backend: "filesystem", Directory: t.TempDir()}
		infra.ServiceAuth.Secret = secret
		infra.UserManagement = mscfg.UserManagementConfiguration{Hostname: "um", Port: 8080, InternalPort: 8080}
		infra.DeviceManagement = mscfg.DeviceManagementConfiguration{Hostname: "dm", Port: dmPort}
		return ms
	}
	Configuration = config.NewUpdateManagementConfiguration()
	RdbManager = &rdb.RdbManager{}

	Microservice = newMs("secret", 8080)
	api, err := buildApi(context.Background())
	require.NoError(t, err)
	require.NotNil(t, api.Profiles, "createArtifact would be refused for every request")
	require.NotNil(t, api.Groups, "startUpdateRollout would be refused for every request")
	require.NotNil(t, api.OnRolloutStep, "no rollout step would reach the metrics")
	require.NotNil(t, api.TenantDeleted, "a deleted tenant's token could re-land artifacts")
	require.NotNil(t, api.Blobs)
	require.Equal(t, Configuration.Updates.MaxTenantArtifactBytes, api.Limits.MaxTenantArtifactBytes)

	// Without the service secret the profile check cannot be built, and the API says so by
	// having none (which createArtifact turns into a refusal) rather than by a stub.
	Microservice = newMs("", 8080)
	api, err = buildApi(context.Background())
	require.NoError(t, err)
	require.Nil(t, api.Profiles)
	require.Nil(t, api.Groups)

	Microservice = newMs("secret", 0)
	api, err = buildApi(context.Background())
	require.NoError(t, err)
	require.Nil(t, api.Profiles)
}

// With updates.objectStore: none the service builds NO store even when the instance's blob
// configuration names a usable directory: the volume is not mounted for it, so using the
// directory would fail at upload time. Without the setting the same configuration builds a store.
func TestBuildBlobStoreHonoursObjectStoreNone(t *testing.T) {
	prevMs, prevCfg := Microservice, Configuration
	t.Cleanup(func() { Microservice, Configuration = prevMs, prevCfg })

	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "update-management"}
	Microservice.InstanceConfiguration.Infrastructure.Blob = mscfg.BlobConfiguration{Backend: "filesystem", Directory: t.TempDir()}

	Configuration = config.NewUpdateManagementConfiguration()
	store, err := buildBlobStore(context.Background())
	require.NoError(t, err)
	require.NotNil(t, store, "the default mode uses the instance store")

	Configuration = config.NewUpdateManagementConfiguration()
	Configuration.Updates.ObjectStore = config.ObjectStoreNone
	store, err = buildBlobStore(context.Background())
	require.NoError(t, err)
	require.Nil(t, store, "none must build no store even though a directory is configured")
}
