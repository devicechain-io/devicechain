// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// THE WIRING OF THE INTEGRATION ROUTE, through the code that does it. The route's own tests build
// the route by hand; nothing else runs buildIntegrationRoute, the seal it hands the route, or the
// publish that keeps the route's source and marks.

func integrationEvent() *model.UnresolvedEvent {
	return &model.UnresolvedEvent{
		Device: "sensor-001", EventType: eventtype.Measurement, Source: "integration:cs",
		Integration: "cs", SealedIntegrationToken: []byte("sealed"),
	}
}

func TestOnlyTheIntegrationRoutesPublishKeepsItsMarksAndSource(t *testing.T) {
	sealingFixture(t)

	// The device-facing path stamps its own source and removes any integration authority an event
	// arrived with, whatever put it there.
	_, msg, ok := inboundEventMessage("http1", "acme", integrationEvent(), &model.UnresolvedMeasurementsPayload{}, "")
	require.True(t, ok)
	decoded, err := esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.NoError(t, err)
	require.Equal(t, "http1", decoded.Source)
	require.Empty(t, decoded.Integration, "the device-facing path published an integration's authority")
	require.Nil(t, decoded.SealedIntegrationToken)

	// The route's publish keeps what the route stamped.
	_, msg, ok = integrationEventMessage("http1", "acme", integrationEvent(), &model.UnresolvedMeasurementsPayload{}, "acme:cs:1")
	require.True(t, ok)
	require.Equal(t, "acme:cs:1", msg.DedupID)
	decoded, err = esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.NoError(t, err)
	require.Equal(t, "integration:cs", decoded.Source)
	require.Equal(t, "cs", decoded.Integration)
	require.Equal(t, []byte("sealed"), decoded.SealedIntegrationToken)
}

func integrationInfra() mscfg.InfrastructureConfiguration {
	var infra mscfg.InfrastructureConfiguration
	infra.ServiceAuth.Secret = "service-secret"
	infra.UserManagement = mscfg.UserManagementConfiguration{Hostname: "um", Port: 8080, InternalPort: 8081}
	infra.DeviceManagement = mscfg.DeviceManagementConfiguration{Hostname: "dm", Port: 8080}
	return infra
}

func buildHttpSourceFor(t *testing.T, infra mscfg.InfrastructureConfiguration) (*processor.HttpEventSource, *prometheus.Registry) {
	t.Helper()
	savedConfig, savedSources, savedMs := Configuration, EventSources, Microservice
	savedTransit, savedUndecoded := SecretTransit, integrationUndecoded
	t.Cleanup(func() {
		Configuration, EventSources, Microservice = savedConfig, savedSources, savedMs
		SecretTransit, integrationUndecoded = savedTransit, savedUndecoded
	})
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.InstanceConfiguration.Infrastructure = infra
	registry := prometheus.NewRegistry()
	Microservice.UseMetricsRegistry(registry)
	initializeMetrics()
	integrationUndecoded = nil
	var err error
	SecretTransit, err = credential.DeriveSecretTransit(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	Configuration = &config.EventSourcesConfiguration{
		EventSources: []config.EventSource{{
			Id: "http1", Type: config.SourceTypeHttp, Configuration: map[string]string{"port": "8081"},
			Decoder: config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}},
	}
	buildTestRateLimiters(t)
	require.NoError(t, buildEventSources())
	source := EventSources[0].(*processor.HttpEventSource)
	source.Port = 0
	ctx := context.Background()
	require.NoError(t, source.Initialize(ctx))
	require.NoError(t, source.Start(ctx))
	t.Cleanup(func() { _ = source.Stop(context.Background()) })
	return source, registry
}

func postIntegration(t *testing.T, source *processor.HttpEventSource, bearer string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+source.Addr()+"/inst-1/acme/integrations/cs?event=up", strings.NewReader("{}"))
	require.NoError(t, err)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, b.String()
}

// With device-management reachable, the HTTP source built from configuration serves the route:
// a request with no bearer token is a 401, which only the route's pre-check answers.
func TestTheBuiltHttpSourceServesTheIntegrationRoute(t *testing.T) {
	source, _ := buildHttpSourceFor(t, integrationInfra())
	code, body := postIntegration(t, source, "")
	require.Equal(t, http.StatusUnauthorized, code, body)
	require.NotNil(t, integrationUndecoded, "the undecoded recorder the service runs is the route's")
}

// Without a way to reach device-management the route is present and says so (503), rather than
// answering 404 as though the URL were wrong or authenticating against nothing.
func TestWithoutDeviceManagementTheRouteAnswers503(t *testing.T) {
	infra := integrationInfra()
	infra.DeviceManagement = mscfg.DeviceManagementConfiguration{}
	source, _ := buildHttpSourceFor(t, infra)
	code, body := postIntegration(t, source, "token")
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, body, "not available")
}

// The seal the route is given opens for this integration and this tenant, and for no other.
func TestTheSealTheRouteIsGivenIsBoundToTheTenantAndTheIntegration(t *testing.T) {
	_, _ = buildHttpSourceFor(t, integrationInfra())
	route := buildIntegrationRoute()
	require.NotNil(t, route)
	sealed, err := route.Seal("acme", "cs", "bearer-token")
	require.NoError(t, err)
	require.NotContains(t, string(sealed), "bearer-token")

	got, err := SecretTransit.OpenToken("acme", credential.IntegrationSealKind("cs"), sealed)
	require.NoError(t, err)
	require.Equal(t, "bearer-token", got)
	_, err = SecretTransit.OpenToken("acme", credential.IntegrationSealKind("other"), sealed)
	require.Error(t, err, "a token sealed for one integration opened as another's")
	_, err = SecretTransit.OpenToken("globex", credential.IntegrationSealKind("cs"), sealed)
	require.Error(t, err)
	_, err = SecretTransit.OpenToken("acme", "ACCESS_TOKEN", sealed)
	require.Error(t, err, "an integration token opened as a device's access token")
}

// Every series the route reports on is exported from the start, so the first refusal after a
// restart is an increase and not a series appearing at 1.
func TestTheIntegrationSeriesAreExportedAtZero(t *testing.T) {
	_, registry := buildHttpSourceFor(t, integrationInfra())
	families, err := registry.Gather()
	require.NoError(t, err)
	series := map[string]int{}
	for _, f := range families {
		series[f.GetName()] = len(f.GetMetric())
	}
	require.Equal(t, len(processor.IntegrationTypes)*len(processor.IntegrationRequestResults),
		series["devicechain_eventsources_integration_requests_total"])
	require.Equal(t, len(processor.IntegrationUplinkResults), series["devicechain_eventsources_integration_uplinks_total"])
	require.Equal(t, 7, series["devicechain_eventsources_integration_flatten_skipped_total"])
}
