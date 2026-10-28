// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/processor"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
)

// TestStartsUnderItsDeclaredCatalog starts this service's real broker wiring with Deployed
// set, the one condition under which core/messaging holds the area to what core/streams
// and core/kv declare for it. Every other test builds a Microservice literal with
// Deployed=false, so a stream, reader mode, consumer or bucket this service uses but the
// catalog does not declare would pass CI and crash-loop the pod.
//
// It drives main.go as production does: core/service with the real createNatsComponents and
// the real AfterNats (buildApis, which opens every entity-cache KV bucket, and the
// ownership drain), then the real afterMicroserviceStarted, with the MQTT auth callout and
// the fail-closed MQTT back-off ON so the callout responder, the device credential-attempt
// bucket (processor.NewDeviceCredentialChecker) and the store back-off sampler are all
// started. Svc supplies the real max-delivery recorder. The database is an in-memory sqlite
// standing in for Postgres. The one lazy path left, the alarm subscription, is opened
// directly below.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	instance := fmt.Sprintf("catalogboot%d", time.Now().UnixNano())
	// The broker holds the area's login to exactly the permissions natsauth generates for it, so
	// this is also the area's permission test: the real wiring runs against it and the broker's
	// log of refusals is asserted empty at the end.
	broker := permtest.Start(t, instance)

	prevMs, prevCfg, prevNats, prevRdb, prevSvc := Microservice, Configuration, NatsManager, RdbManager, Svc
	prevApi, prevCached, prevKey, prevTransit, prevGates := Api, CachedApi, DeviceSecretKey, SecretTransit, safetyGates
	prevProc, prevRA, prevDR, prevDrain := InboundEventsProcessor, RaiseAlarmConsumer, DeviceReportsConsumer, OwnershipDrain
	prevCallout, prevBackoff, prevKickDrain := CalloutResponder, MqttStoreBackoff, SessionKickDrain
	t.Cleanup(func() {
		Microservice, Configuration, NatsManager, RdbManager, Svc = prevMs, prevCfg, prevNats, prevRdb, prevSvc
		Api, CachedApi, DeviceSecretKey, SecretTransit, safetyGates = prevApi, prevCached, prevKey, prevTransit, prevGates
		InboundEventsProcessor, RaiseAlarmConsumer, DeviceReportsConsumer, OwnershipDrain = prevProc, prevRA, prevDR, prevDrain
		CalloutResponder, MqttStoreBackoff, SessionKickDrain = prevCallout, prevBackoff, prevKickDrain
	})

	issuer, err := nkeys.CreateAccount()
	require.NoError(t, err)
	issuerSeed, err := issuer.Seed()
	require.NoError(t, err)
	issuerPublic, err := issuer.PublicKey()
	require.NoError(t, err)
	t.Setenv(envCalloutIssuerSeed, string(issuerSeed))

	Microservice = &core.Microservice{
		InstanceId:     instance,
		FunctionalArea: "device-management",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	reg := prometheus.NewRegistry()
	Microservice.UseMetricsRegistry(reg)
	nats := &Microservice.InstanceConfiguration.Infrastructure.Nats
	*nats = broker.NatsConfiguration("device-management")
	nats.MqttFailClosed = true
	nats.Auth.CalloutIssuerPublic = issuerPublic
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	safetyGates = governance.NewSafetyGates(Microservice)

	db := putest.NewSQLiteDB(t, append(credentialTables(), &model.PendingOwnershipFact{}, &model.PendingSessionKick{})...)
	RdbManager = &rdb.RdbManager{Database: db}
	DeviceSecretKey = dmtest.DeviceSecretKey()
	SecretTransit = testTransit(t)

	Svc = service.New(Microservice, service.Spec{
		Nats: &service.NatsSpec{OnCreate: createNatsComponents},
		AfterNats: func(_ context.Context, m *service.Managers) error {
			// The two steps of the production AfterNats that open broker resources.
			var err error
			Api, CachedApi, err = buildApis(m.Nats, RdbManager, DeviceSecretKey, EnrollmentKeys, Configuration)
			if err != nil {
				return err
			}
			if err = initOwnershipDrain(); err != nil {
				return err
			}
			if err = initSessionKickDrain(); err != nil {
				return err
			}
			// The third: the self-registration endpoint is mounted on the real mux here, so
			// the route is proved present after the whole start path, not only in isolation.
			return wireProvisioningEndpoint()
		},
	})
	buildMetrics()

	ctx := context.Background()
	require.NoError(t, Svc.Initialize(ctx), "initialize under the catalog: entity-cache KV buckets")
	NatsManager = Svc.Nats
	t.Cleanup(func() {
		_ = beforeMicroserviceStopped(ctx)
		if c := NatsManager.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NoError(t, afterMicroserviceStarted(ctx),
		"start under the catalog: readers, writers, callout responder, credential-attempt bucket, store back-off")

	require.NotNil(t, CalloutResponder, "the callout responder (and its credential-attempt bucket) was not started")
	require.NotNil(t, MqttStoreBackoff, "the MQTT store back-off was not started")

	// The session-kick drain is started by the real start hook, not merely built: its first pass
	// is immediate, so a drain that was never Start()ed leaves its pass counter at zero. The
	// observer is wired to the gauges the alert reads.
	require.NotNil(t, Api.SessionKickObserver, "the session-kick gauges are not wired to the drain")
	for _, series := range []string{"session_kicks_pending", "session_kick_oldest_pending_age_seconds", "session_kick_drain_passes_total"} {
		require.NotEmpty(t, registeredSeries(t, reg, series), "%s is not registered", series)
	}
	// Wait on the condition, not wall time: the series is born at zero with the task's metrics, so
	// "registered" holds from the start and only a pass that really ran lifts it. Bound: one drain
	// interval (the first pass is immediate).
	require.Eventually(t, func() bool {
		families, err := reg.Gather()
		require.NoError(t, err)
		for _, f := range families {
			if f.GetName() == "devicechain_devicemanagement_session_kick_drain_passes_total" {
				for _, m := range f.GetMetric() {
					if m.GetCounter().GetValue() > 0 {
						return true
					}
				}
			}
		}
		return false
	}, sessionKickDrainInterval, 50*time.Millisecond, "the session-kick drain never made a pass: main did not start it")

	// The callout runs with its in-flight cap, its lookup bound and its load metrics attached,
	// and the three series exist on the service's registry. Without the Configure call in the
	// start hook the metrics are never attached.
	maxInFlight, lookup, stop, metrics := CalloutResponder.Limits()
	require.Equal(t, processor.DefaultCalloutMaxInFlight, maxInFlight, "the callout's in-flight cap is not in force")
	require.Equal(t, processor.DefaultCalloutLookupTimeout, lookup)
	require.Equal(t, processor.DefaultCalloutStopWait, stop)
	require.True(t, metrics, "the callout's load metrics were never attached to the responder")
	for _, series := range []string{"callout_in_flight", "callout_refused_busy_total", "callout_plaintext_requests_total",
		"callout_x509_total", "callout_jit_in_flight"} {
		require.NotEmpty(t, registeredSeries(t, reg, series), "%s is not registered", series)
	}
	// The certificate connect counter exists at zero for EVERY result, so an alert on a rate
	// sees a zero series on a healthy instance rather than none. The results are stated here
	// as literals, independently of the processor's list.
	families, err := reg.Gather()
	require.NoError(t, err)
	seen := map[string]float64{}
	for _, f := range families {
		if f.GetName() != "devicechain_devicemanagement_callout_x509_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" {
					seen[l.GetValue()] = m.GetCounter().GetValue()
				}
			}
		}
	}
	for _, result := range []string{"ok", "no_cert", "chain_invalid", "anchor_disabled", "revoked", "client_id_mismatch",
		"unknown_pin", "unsealed", "cert_malformed", "x509_cert_ignored", "jit_created", "jit_adopted", "jit_renewed", "jit_replayed",
		"jit_rate_limited", "jit_busy"} {
		v, ok := seen[result]
		require.True(t, ok, "callout_x509_total{result=%q} does not exist at startup", result)
		require.Zero(t, v, "callout_x509_total{result=%q} is not zero on an idle service", result)
	}

	// The alarm subscription opens its live feed on first request, not at start:
	// graphql/subscriptions_alarms.go:139.
	live, err := NatsManager.SubscribeLive(ctx, "acme", streams.AlarmEvents)
	require.NoError(t, err, "live subscription on %s", streams.AlarmEvents)
	_ = live
	broker.RequireNoViolations(t)

	// The self-registration route survived the whole start path. This test never derives the
	// enrollment keys, so the Api has no provisioning Checker and the endpoint answers 503:
	// mounted and unavailable, which is what distinguishes it from the 404 of a missing route.
	srv := httptest.NewServer(Microservice.Mux())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/provisioning/acme/register", "application/json", strings.NewReader(
		`{"provisionKey":"k","provisionSecret":"s","externalId":"SN-1","credentialSecret":"`+strings.Repeat("a", 43)+`"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "the registration route is not mounted by the start path")
}
