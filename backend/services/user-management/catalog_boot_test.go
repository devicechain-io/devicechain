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

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-user-management/deadletters"
	"github.com/devicechain-io/dc-user-management/identity"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func startCatalogBootNats(t *testing.T) (string, uint32) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
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

// 🔑 THE START, UNDER THE CATALOG. Every other test here builds a Microservice with
// Deployed=false, so none runs this service's real broker wiring while the catalog is
// enforced. This one does, over a Deployed microservice.
//
// afterMicroserviceInitialized cannot be driven as a whole: it opens PostgreSQL, builds the
// secret store and the identity manager before it reaches the broker. So this reproduces,
// against the deployed manager and in production's order (Initialize, then the buckets, then
// Start), every call that function and afterMicroserviceStarted make on the broker; the line
// each one mirrors is named. The OAuth issuer URL is set so the optional buckets (oauth codes,
// SSO state, SSO codes) are opened too.
//
// Not covered, by design: the tenant purge (purge/broker.go, purge/keyvalue.go, purge/detect.go)
// reaches the broker through the RAW connection (NatsManager.Conn), which the catalog does not
// see, so it opens no stream or bucket through the manager.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	instance := fmt.Sprintf("catalogboot%d", time.Now().UnixNano())
	// The broker holds the area's login to exactly the permissions natsauth generates for it, and
	// has the identity account the sign-in buckets live in, so this is also the area's permission
	// test: the real wiring runs against it and the broker's log of refusals is asserted empty at
	// the end.
	broker := permtest.Start(t, instance)

	prevMs, prevCfg, prevNats := Microservice, Configuration, NatsManager
	prevStore, prevMetrics, prevConsumer := DeadLetterStore, DeadLetterMetrics, DeadLetterConsumer
	t.Cleanup(func() {
		Microservice, Configuration, NatsManager = prevMs, prevCfg, prevNats
		DeadLetterStore, DeadLetterMetrics, DeadLetterConsumer = prevStore, prevMetrics, prevConsumer
	})

	Microservice = &core.Microservice{
		InstanceId:     instance,
		FunctionalArea: "user-management",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = broker.NatsConfiguration("user-management")
	Microservice.InstanceConfiguration.Infrastructure.Nats.Auth.IdentityAccount = true
	Microservice.MicroserviceConfigurationRaw = []byte(`{"auth":{"issuerUrl":"https://devicechain.example.com/user-management"}}`)
	require.NoError(t, parseConfiguration())
	require.True(t, Configuration.OAuthEnabled(), "the optional oauth/sso buckets would not be opened")

	// main.go: the store and the metrics the dead-letter consumer is built from.
	DeadLetterStore = deadletters.NewStore(&rdb.RdbManager{})
	DeadLetterMetrics = deadletters.NewMetrics(Microservice)

	// main.go ~L160: the oncreate callback, which runs in Start: the dead-letters reader and
	// the consumer over it.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(nmgr *messaging.NatsManager) error {
			reader, err := nmgr.NewReader(streams.DeadLetters)
			if err != nil {
				return err
			}
			DeadLetterConsumer = deadletters.NewConsumer(Microservice, reader, DeadLetterStore,
				core.NewNoOpLifecycleCallbacks(), DeadLetterMetrics)
			return DeadLetterConsumer.Initialize(context.Background())
		})
	// main.go: the real recorder, as the service builds it by hand.
	NatsManager.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(deadletter.NewProducer(Microservice)))
	mgr := NatsManager
	t.Cleanup(func() {
		if DeadLetterConsumer != nil {
			_ = DeadLetterConsumer.Stop(context.Background())
		}
		_ = mgr.Stop(context.Background())
		if c := mgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	NatsManager.UseIdentityLogin(permtest.Password(natsauth.IdentityUser))
	ctx := context.Background()
	require.NoError(t, NatsManager.Initialize(ctx), "initializing under the catalog")

	// main.go L187: refresh-token bucket.
	refreshTTL := time.Duration(Configuration.Auth.RefreshTokenTtlSeconds) * time.Second
	_, err := NatsManager.KeyValueStore(identity.RefreshBucket, identity.RefreshBucket, refreshTTL)
	require.NoError(t, err, "bucket "+identity.RefreshBucket)
	// main.go L195: OAuth authorization codes (OAuthEnabled).
	_, err = NatsManager.KeyValueStore(identity.AuthCodeBucket, identity.AuthCodeBucket, identity.AuthCodeTTL)
	require.NoError(t, err, "bucket "+identity.AuthCodeBucket)
	// main.go L206 and L210: SSO state and SSO codes (OAuthEnabled).
	_, err = NatsManager.KeyValueStore(kv.BucketSsoState, kv.BucketSsoState, identity.SsoStateTTL)
	require.NoError(t, err, "bucket "+kv.BucketSsoState)
	_, err = NatsManager.KeyValueStore(kv.BucketSsoCodes, kv.BucketSsoCodes, identity.SsoCodeTTL)
	require.NoError(t, err, "bucket "+kv.BucketSsoCodes)
	// main.go L217: the distributed lock.
	_, err = NatsManager.NewDistributedLock(5 * time.Second)
	require.NoError(t, err, "the distributed lock bucket")
	// main.go L227: the credential attempt store.
	attemptsKV, err := NatsManager.CredentialAttemptStore()
	require.NoError(t, err, "the credential attempt store")
	_, err = credential.NewChecker(attemptsKV, identity.CredentialPolicies)
	require.NoError(t, err)

	// afterMicroserviceStarted: NatsManager.Start runs the oncreate callback (the reader and the
	// consumer's durable), then DeadLetterConsumer.Start.
	require.NoError(t, NatsManager.Start(ctx), "starting under the catalog: dead-letters reader, max-delivery recorder")
	require.NotNil(t, DeadLetterConsumer, "the oncreate callback built no dead-letter consumer")
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	require.NoError(t, DeadLetterConsumer.Start(ctx), "starting the dead-letter consumer")
	broker.RequireNoViolations(t)
}
