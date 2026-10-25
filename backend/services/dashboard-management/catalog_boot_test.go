// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/service"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// startEmbeddedNats runs an in-process NATS server and returns its host and port.
func startEmbeddedNats(t *testing.T) (string, uint32) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // ephemeral
		JetStream: true,
		StoreDir:  dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)

	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return u.Hostname(), uint32(port)
}

// 🔴 THE REAL WIRING UNDER THE REAL CATALOG. Every other test here builds a Microservice with
// Deployed=false, which the NATS manager does not hold to core/streams and core/kv. This one is
// Deployed, as core.NewMicroservice makes it in production, so a stream, reader mode or durable
// this service opens that its area does not declare fails here rather than crash-looping a pod.
//
// The database and GraphQL halves open nothing on the broker and are left out; the NATS half
// is the service's own createNatsComponents and afterMicroserviceStarted.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	instance := fmt.Sprintf("catalog-boot-%d", time.Now().UnixNano())
	// The broker holds the area's login to exactly the permissions natsauth generates for it, so this
	// is also the area's permission test: the real wiring runs against it and the broker's log of
	// refusals is asserted empty at the end.
	broker := permtest.Start(t, instance)

	savedMs, savedCfg, savedApi, savedSvc, savedRec := Microservice, Configuration, Api, Svc, ShareReconciler
	t.Cleanup(func() {
		Microservice, Configuration, Api, Svc, ShareReconciler = savedMs, savedCfg, savedApi, savedSvc, savedRec
	})

	Microservice = &core.Microservice{
		InstanceId:     instance,
		FunctionalArea: "dashboard-management",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = broker.NatsConfiguration("dashboard-management")
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	ShareReconciler = nil

	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	Api = model.NewApi(&rdb.RdbManager{Database: db})

	// The real dead-letter recorder core/service installs is kept: it is catalog-checked too.
	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{OnCreate: createNatsComponents}})
	ctx := context.Background()
	require.NoError(t, Svc.Initialize(ctx), "initialize under the catalog")
	nm := Svc.Nats
	t.Cleanup(func() {
		// A refused start leaves no reconciler to stop; do not let a nil deref mask the refusal.
		if ShareReconciler != nil {
			if err := beforeMicroserviceStopped(context.Background()); err != nil {
				t.Logf("stopping the service: %v", err)
			}
		}
		if c := nm.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NoError(t, afterMicroserviceStarted(ctx), "start under the catalog (entity-deleted reader, share reconciler; main.go:163)")
	Microservice.Readiness.MarkReadyWithoutAuthSurface()

	require.NotNil(t, ShareReconciler, "share reconciler")
	// The entity-deleted reader in createNatsComponents (main.go:163) is the service's only
	// NewWriter/NewReader/SubscribeLive/KeyValueStore call site: nothing is opened lazily.
	broker.RequireNoViolations(t)
}
