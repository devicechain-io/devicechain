// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
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
	host, port := startEmbeddedNats(t)

	prevMs, prevCfg, prevNats, prevRdb, prevSvc := Microservice, Configuration, NatsManager, RdbManager, Svc
	prevApi, prevCached, prevKey, prevTransit, prevGates := Api, CachedApi, DeviceSecretKey, SecretTransit, safetyGates
	prevProc, prevRA, prevDR, prevDrain := InboundEventsProcessor, RaiseAlarmConsumer, DeviceReportsConsumer, OwnershipDrain
	prevCallout, prevBackoff := CalloutResponder, MqttStoreBackoff
	t.Cleanup(func() {
		Microservice, Configuration, NatsManager, RdbManager, Svc = prevMs, prevCfg, prevNats, prevRdb, prevSvc
		Api, CachedApi, DeviceSecretKey, SecretTransit, safetyGates = prevApi, prevCached, prevKey, prevTransit, prevGates
		InboundEventsProcessor, RaiseAlarmConsumer, DeviceReportsConsumer, OwnershipDrain = prevProc, prevRA, prevDR, prevDrain
		CalloutResponder, MqttStoreBackoff = prevCallout, prevBackoff
	})

	issuer, err := nkeys.CreateAccount()
	require.NoError(t, err)
	issuerSeed, err := issuer.Seed()
	require.NoError(t, err)
	issuerPublic, err := issuer.PublicKey()
	require.NoError(t, err)
	t.Setenv(envCalloutIssuerSeed, string(issuerSeed))

	Microservice = &core.Microservice{
		InstanceId:     fmt.Sprintf("catalogboot%d", time.Now().UnixNano()),
		FunctionalArea: "device-management",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	nats := &Microservice.InstanceConfiguration.Infrastructure.Nats
	*nats = mscfg.NatsConfiguration{Hostname: host, Port: port, MqttFailClosed: true}
	nats.Auth = mscfg.NatsAuthConfiguration{CalloutIssuerPublic: issuerPublic}
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	safetyGates = governance.NewSafetyGates(Microservice)

	db := putest.NewSQLiteDB(t, append(credentialTables(), &model.PendingOwnershipFact{})...)
	RdbManager = &rdb.RdbManager{Database: db}
	DeviceSecretKey = dmtest.DeviceSecretKey()
	SecretTransit = testTransit(t)

	Svc = service.New(Microservice, service.Spec{
		Nats: &service.NatsSpec{OnCreate: createNatsComponents},
		AfterNats: func(_ context.Context, m *service.Managers) error {
			// The two steps of the production AfterNats that open broker resources.
			var err error
			Api, CachedApi, err = buildApis(m.Nats, RdbManager, DeviceSecretKey, Configuration)
			if err != nil {
				return err
			}
			return initOwnershipDrain()
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

	// The alarm subscription opens its live feed on first request, not at start:
	// graphql/subscriptions_alarms.go:139.
	live, err := NatsManager.SubscribeLive(ctx, "acme", streams.AlarmEvents)
	require.NoError(t, err, "live subscription on %s", streams.AlarmEvents)
	_ = live
}
