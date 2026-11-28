// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
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
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	dctest "github.com/devicechain-io/dc-microservice/test"
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
	catalogBoot(t, "")
}

func TestCatalogBootCleanupAfterStartupFailure(t *testing.T) {
	for _, stage := range []string{"readers", "consumers", "callout", "initialize"} {
		t.Run(stage, func(t *testing.T) { catalogBoot(t, stage) })
	}
}

func catalogBoot(t *testing.T, failStage string) {
	t.Helper()
	injected := errors.New("injected catalog startup failure: " + failStage)
	instance := fmt.Sprintf("catalogboot%d", time.Now().UnixNano())
	// The broker holds the area's login to exactly the permissions natsauth generates for it, so
	// this is also the area's permission test: the real wiring runs against it and the broker's
	// log of refusals is asserted empty at the end.
	broker := permtest.Start(t, instance)

	prevMs, prevCfg, prevNats, prevRdb, prevSvc := Microservice, Configuration, NatsManager, RdbManager, Svc
	prevApi, prevCached, prevKey, prevTransit, prevGates := Api, CachedApi, DeviceSecretKey, SecretTransit, safetyGates
	prevProc, prevRA, prevDR, prevDrain := InboundEventsProcessor, RaiseAlarmConsumer, DeviceReportsConsumer, OwnershipDrain
	prevEffects := AgentEffectsDrain
	prevDeletion := EntityDeletionDrain
	prevAlarmEvents := AlarmEventDrain
	prevCallout, prevBackoff, prevKickDrain, prevTenants := CalloutResponder, MqttStoreBackoff, SessionKickDrain, ProvisioningTenants
	prevReaper, prevClassifier, prevMarkerHeal := AgentProposalReaper, AlarmPositionClassifier, CreateEnrollMarkerHeal
	prevReportMaintenance := ConfigurationReportMaintenance
	prevOrphanSweep := AttributeOrphanSweep
	prevFleetExpirySweep := FleetExpirySweep
	prevPeers := servicePeers
	prevPin := IssuerPin
	prevKill, prevAccounting, prevAccountant, prevSharding := AgentKillConsumer, DecodeAccounting, DecodeAccountant, stopStreamSharding
	ProvisioningTenants = nil
	// A failed start must not inherit another test's consumers or background tasks.
	NatsManager, Api, CachedApi = nil, nil, nil
	InboundEventsProcessor, RaiseAlarmConsumer, DeviceReportsConsumer, AgentKillConsumer = nil, nil, nil, nil
	AgentEffectsDrain, EntityDeletionDrain, AlarmEventDrain, OwnershipDrain = nil, nil, nil, nil
	SessionKickDrain, AgentProposalReaper, AlarmPositionClassifier, CreateEnrollMarkerHeal = nil, nil, nil, nil
	ConfigurationReportMaintenance, AttributeOrphanSweep, DecodeAccounting = nil, nil, nil
	CalloutResponder, IssuerPin, MqttStoreBackoff, stopStreamSharding = nil, nil, nil, nil
	t.Cleanup(func() {
		servicePeers = prevPeers
		ProvisioningTenants, IssuerPin = prevTenants, prevPin
		Microservice, Configuration, NatsManager, RdbManager, Svc = prevMs, prevCfg, prevNats, prevRdb, prevSvc
		Api, CachedApi, DeviceSecretKey, SecretTransit, safetyGates = prevApi, prevCached, prevKey, prevTransit, prevGates
		InboundEventsProcessor, RaiseAlarmConsumer, DeviceReportsConsumer, OwnershipDrain = prevProc, prevRA, prevDR, prevDrain
		AgentEffectsDrain = prevEffects
		EntityDeletionDrain = prevDeletion
		AlarmEventDrain = prevAlarmEvents
		CalloutResponder, MqttStoreBackoff, SessionKickDrain = prevCallout, prevBackoff, prevKickDrain
		AgentProposalReaper, AlarmPositionClassifier, CreateEnrollMarkerHeal = prevReaper, prevClassifier, prevMarkerHeal
		ConfigurationReportMaintenance = prevReportMaintenance
		AttributeOrphanSweep = prevOrphanSweep
		AgentKillConsumer, DecodeAccounting, DecodeAccountant, stopStreamSharding = prevKill, prevAccounting, prevAccountant, prevSharding
		FleetExpirySweep = prevFleetExpirySweep
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
	// The callout pins certificate connects to the broker's live server ids over the system account,
	// as the generated permissions give the dc_sys login: the pin is wired against them here, and a
	// permission it lacked would show in the violations asserted empty at the end.
	nats.Auth.SysUser, nats.Auth.SysPassword = natsauth.SysUser, permtest.Password(natsauth.SysUser)
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	servicePeers = svcclient.NewPeers(Microservice.InstanceConfiguration.Infrastructure, Microservice.FunctionalArea)
	safetyGates = service.FromManagers(Microservice, service.Managers{}).SafetyGates()

	db := putest.NewSQLiteDB(t, append(credentialTables(), &model.EntityAttribute{}, &model.AttributeGeneration{}, &model.PendingOwnershipFact{}, &model.PendingEntityDeletion{}, &model.PendingAlarmEvent{}, &model.PendingSessionKick{}, &model.PendingRevocationKick{}, &model.PendingRevocationExpansion{}, &model.PendingRevocationExpansionSerial{}, &model.AgentProposal{}, &model.DeviceConfigurationReport{}, &model.FleetOperation{}, &model.FleetOperationLine{}, &model.FleetOperationEvent{})...)
	RdbManager = &rdb.RdbManager{Database: db}
	DeviceSecretKey = dmtest.DeviceSecretKey()
	SecretTransit = testTransit(t)

	Svc = service.New(Microservice, service.Spec{
		Nats: &service.NatsSpec{OnCreate: func(nmgr *messaging.NatsManager) error {
			if failStage == "readers" {
				if err := createInboundReaders(nmgr); err != nil {
					return err
				}
				return injected
			}
			if err := createNatsComponents(nmgr); err != nil {
				return err
			}
			if failStage == "consumers" {
				return injected
			}
			return nil
		}},
		AfterNats: func(_ context.Context, m *service.Managers) error {
			if failStage == "initialize" {
				return injected
			}
			// The two steps of the production AfterNats that open broker resources.
			var err error
			Api, CachedApi, err = buildApis(m.Nats, RdbManager, DeviceSecretKey, EnrollmentKeys, Configuration)
			if err != nil {
				return err
			}
			if err = initAgentEffectsDrain(); err != nil {
				return err
			}
			if err = initEntityDeletionDrain(); err != nil {
				return err
			}
			if err = initAlarmEventDrain(); err != nil {
				return err
			}
			if err = initOwnershipDrain(); err != nil {
				return err
			}
			if err = initSessionKickDrain(); err != nil {
				return err
			}
			if err = initAgentProposalReaper(); err != nil {
				return err
			}
			if err = initAlarmPositionClassifier(); err != nil {
				return err
			}
			if err = initCreateEnrollMarkerHeal(); err != nil {
				return err
			}
			if err = initConfigurationReportMaintenance(); err != nil {
				return err
			}
			if err = initAttributeOrphanSweep(); err != nil {
				return err
			}
			if err = initFleetExpirySweep(); err != nil {
				return err
			}
			if err = initDecodeAccounting(); err != nil {
				return err
			}
			// The third: the self-registration endpoint is mounted on the real mux here, so
			// the route is proved present after the whole start path, not only in isolation.
			return wireProvisioningEndpoint()
		},
	})
	buildMetrics()

	ctx := context.Background()
	cleanup := func() error { return nil }
	t.Cleanup(func() { require.NoError(t, cleanup(), "catalog cleanup after stage %q", failStage) })
	checkCleanup := func() {
		t.Helper()
		conn := Svc.Nats.Conn()
		captured := cleanup
		// The explicit check owns this one cleanup.
		cleanup = func() error { return nil }
		// Teardown must use the captured fixture, not the package global.
		NatsManager = nil
		if failStage == "consumers" {
			// An uninitialized HTTP manager refuses Stop before the NATS manager is
			// reached. The captured connection still has to close on that error.
			svc := Svc
			svc.GraphQL = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(), nil, nil, Microservice.Readiness)
			defer func() {
				svc.GraphQL = nil
				require.NoError(t, svc.Stop(ctx), "finish managers after the deliberately refused stop")
			}()
		}
		var cleanupErr error
		require.NotPanics(t, func() { cleanupErr = captured() }, "cleanup after stage %q", failStage)
		if failStage == "consumers" {
			require.ErrorContains(t, cleanupErr, "service managers")
		} else {
			require.NoError(t, cleanupErr)
		}
		require.True(t, conn.IsClosed(), "the initialized broker connection leaked after %q", failStage)
	}
	t.Log("catalog boot: initializing")
	err = Svc.Initialize(ctx)
	cleanup = captureCatalogBootCleanup(ctx, Svc)
	NatsManager = Svc.Nats
	if failStage == "initialize" {
		require.ErrorIs(t, err, injected)
		checkCleanup()
		return
	}
	require.NoError(t, err, "initialize under the catalog: entity-cache KV buckets")
	dctest.UseFreeTCPPort(t, &service.ProbesPort)
	if failStage == "callout" {
		t.Setenv(envCalloutIssuerSeed, "invalid-seed")
	}
	t.Log("catalog boot: starting managers, consumers and callout")
	err = afterMicroserviceStarted(ctx)
	cleanup = captureCatalogBootCleanup(ctx, Svc)
	if failStage != "" {
		if failStage == "callout" {
			require.ErrorContains(t, err, "seed")
			require.NotNil(t, InboundEventsProcessor)
		} else {
			require.ErrorIs(t, err, injected)
		}
		checkCleanup()
		return
	}
	require.NoError(t, err,
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
	// Registration of devices by certificate on their first connect is ON, with the bounds the
	// configuration says (defaults here), and the per-source bucket is OFF by default. Without the
	// call in the start hook a certificate that pins nothing is refused as unknown_pin forever.
	jitOn, jitSlots, jitPerSource := CalloutResponder.JitEnabled()
	require.True(t, jitOn, "registration on first connect was never turned on: main did not wire it")
	require.Equal(t, 16, jitSlots, "the registration in-flight bound is not the configured default")
	require.False(t, jitPerSource, "the per-source registration bucket must be off by default")
	// The issuer pin is wired and has learned this broker's server id over the system account under
	// the generated dc_sys permissions (the broker's violation log is asserted empty below).
	require.NotNil(t, IssuerPin, "the issuer pin was never wired: certificate connects are unpinned")
	require.Eventually(t, func() bool { return len(IssuerPin.pin.Members()) == 1 },
		10*time.Second, 50*time.Millisecond, "the issuer pin never learned the broker's server id")
	for _, series := range []string{"callout_issuer_pin_refresh_total", "callout_issuer_pin_servers", "callout_issuer_pin_connected"} {
		require.NotEmpty(t, registeredSeries(t, reg, series), "%s is not registered", series)
	}
	for _, series := range []string{"callout_in_flight", "callout_refused_busy_total", "callout_plaintext_requests_total",
		"callout_x509_total", "callout_jit_in_flight", "callout_session_limited_total"} {
		require.NotEmpty(t, registeredSeries(t, reg, series), "%s is not registered", series)
	}
	// The per-device session bounds are attached, over the bucket the broker administrator made.
	maxSessions, burst, perMinute := CalloutResponder.SessionBounds()
	require.Equal(t, []int{16, 20, 60}, []int{maxSessions, burst, perMinute},
		"the callout runs without its per-device session bounds, or not with the defaults")
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
		"unknown_pin", "unsealed", "issuer_unpinned", "issuer_pin_unavailable", "cert_malformed", "x509_cert_ignored", "jit_created", "jit_adopted", "jit_renewed", "jit_replayed",
		"jit_rate_limited", "jit_busy", "jit_ambiguous", "jit_revoked", "jit_provisioning_failed", "jit_invalid_value",
		"jit_device_not_registered", "jit_device_not_earmarked", "jit_device_disabled", "jit_device_type_mismatch",
		"jit_device_token_collision", "jit_already_provisioned", "jit_limit_exceeded", "jit_provisioning_unavailable",
		"jit_provisioning_misconfigured", "jit_credential_type_unsupported"} {
		v, ok := seen[result]
		require.True(t, ok, "callout_x509_total{result=%q} does not exist at startup", result)
		require.Zero(t, v, "callout_x509_total{result=%q} is not zero on an idle service", result)
	}

	// The alarm subscription opens its live feed on first request, not at start:
	// graphql/subscriptions_alarms.go:139.
	liveCtx, cancelLive := context.WithCancel(ctx)
	t.Cleanup(cancelLive)
	live, err := NatsManager.SubscribeLive(liveCtx, "acme", streams.AlarmEvents)
	require.NoError(t, err, "live subscription on %s", streams.AlarmEvents)
	t.Cleanup(func() {
		cancelLive()
		require.Eventually(t, func() bool {
			select {
			case _, open := <-live:
				return !open
			default:
				return false
			}
		}, 5*time.Second, 10*time.Millisecond, "the live subscription did not finish before manager cleanup")
	})
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

type catalogBootStop struct {
	name string
	stop func(context.Context) error
}

func runCatalogBootStops(ctx context.Context, steps []catalogBootStop) error {
	var errs []error
	for _, step := range steps {
		if err := step.stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", step.name, err))
		}
	}
	return errors.Join(errs...)
}

// Capture at each lifecycle boundary, before checking its error: Initialize opens a
// connection, AfterNats initializes tasks, and Start constructs the consumers. A full
// production stop assumes all of them exist. A failed test start cannot assume that,
// nor may its cleanup read globals that another cleanup can restore. Stop through the
// components' lifecycle guards, including initialized components whose Start failed.
func captureCatalogBootCleanup(ctx context.Context, svc *service.Service) func() error {
	var steps []catalogBootStop
	add := func(name string, present bool, stop func(context.Context) error) {
		if present {
			steps = append(steps, catalogBootStop{name, stop})
		}
	}
	callout, pin, backoff, sharding := CalloutResponder, IssuerPin, MqttStoreBackoff, stopStreamSharding
	add("callout", callout != nil, func(context.Context) error { return callout.Stop() })
	add("issuer pin", pin != nil, func(context.Context) error {
		pin.stop()
		return nil
	})
	add("MQTT back-off", backoff != nil, func(context.Context) error {
		backoff.Stop()
		return nil
	})
	add("agent effects", AgentEffectsDrain != nil, AgentEffectsDrain.Stop)
	add("entity deletion", EntityDeletionDrain != nil, EntityDeletionDrain.Stop)
	add("alarm events", AlarmEventDrain != nil, AlarmEventDrain.Stop)
	add("alarm relay", Api != nil, Api.StopAlarmEventRelay)
	add("ownership", OwnershipDrain != nil, OwnershipDrain.Stop)
	add("session kicks", SessionKickDrain != nil, SessionKickDrain.Stop)
	add("proposal reaper", AgentProposalReaper != nil, AgentProposalReaper.Stop)
	add("alarm positions", AlarmPositionClassifier != nil, AlarmPositionClassifier.Stop)
	add("enrollment heal", CreateEnrollMarkerHeal != nil, CreateEnrollMarkerHeal.Stop)
	add("report maintenance", ConfigurationReportMaintenance != nil, ConfigurationReportMaintenance.Stop)
	add("attribute orphans", AttributeOrphanSweep != nil, AttributeOrphanSweep.Stop)
	add("inbound events", InboundEventsProcessor != nil, InboundEventsProcessor.Stop)
	add("decode accounting", DecodeAccounting != nil, DecodeAccounting.Stop)
	add("decode flush", DecodeAccounting != nil && DecodeAccountant != nil, DecodeAccountant.Flush)
	add("raise alarm", RaiseAlarmConsumer != nil, RaiseAlarmConsumer.Stop)
	add("agent kills", AgentKillConsumer != nil, AgentKillConsumer.Stop)
	add("device reports", DeviceReportsConsumer != nil, DeviceReportsConsumer.Stop)
	// Managers stop after their users; placement remains available through that stop.
	add("service managers", true, svc.Stop)
	add("stream placement", sharding != nil, func(context.Context) error {
		sharding()
		return nil
	})
	var closeConn func()
	if svc.Nats != nil {
		if conn := svc.Nats.Conn(); conn != nil {
			closeConn = conn.Close
		}
	}
	return func() error {
		// Even a stop error must not leak the already-open broker connection.
		if closeConn != nil {
			defer closeConn()
		}
		return runCatalogBootStops(ctx, steps)
	}
}

func TestCatalogBootStopsContinueInOrderAndReportErrors(t *testing.T) {
	first, second := errors.New("first stop failed"), errors.New("second stop failed")
	var order []string
	step := func(name string, err error) catalogBootStop {
		return catalogBootStop{name, func(context.Context) error {
			order = append(order, name)
			return err
		}}
	}
	err := runCatalogBootStops(context.Background(), []catalogBootStop{
		step("callout", first), step("consumer", second), step("managers", nil), step("placement", nil),
	})
	require.Equal(t, []string{"callout", "consumer", "managers", "placement"}, order)
	require.ErrorIs(t, err, first)
	require.ErrorIs(t, err, second)
	require.ErrorContains(t, err, "callout: first stop failed")
	require.ErrorContains(t, err, "consumer: second stop failed")
}
