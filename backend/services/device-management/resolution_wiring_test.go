// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// resolution.workers in the service's configuration document reaches the processor it builds.
// Nothing else runs this wiring: a processor test builds the processor itself, so a service
// that stopped passing the setting would run the default while every other test stayed green.
//
// It builds the resolve metrics and a dead-letter producer directly rather than through
// buildMetrics, which reads the producer core/service builds and this test has no service to run.
func TestConfiguredResolutionWorkersReachTheProcessor(t *testing.T) {
	savedMs, savedCfg, savedMetrics, savedDead, savedTransit := Microservice, Configuration, ResolveMetrics, DeadLetters, SecretTransit
	t.Cleanup(func() {
		Microservice, Configuration, ResolveMetrics, DeadLetters, SecretTransit = savedMs, savedCfg, savedMetrics, savedDead, savedTransit
	})

	for _, tc := range []struct {
		doc  string
		want int
	}{
		{`{"resolution":{"workers":3}}`, 3},
		// Counterweight: with nothing set, the processor gets the default, so a wiring that
		// always passed 3 (or anything else fixed) cannot pass both.
		{`{}`, 10},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
			Microservice.UseMetricsRegistry(prometheus.NewRegistry())
			Microservice.MicroserviceConfigurationRaw = []byte(tc.doc)
			require.NoError(t, parseConfiguration())
			ResolveMetrics = processor.NewResolveMetrics(Microservice)
			DeadLetters = deadletter.NewProducer(Microservice)
			SecretTransit = testTransit(t)

			require.Equal(t, tc.want, newInboundEventsProcessor().Resolvers())
		})
	}
}

// resolution.publishWindow in the service's configuration document reaches BOTH writers the
// service builds, and a document that omits it gets the default. A processor test builds its
// own writers, so a service that stopped passing the setting would run the default while every
// other test stayed green.
func TestConfiguredPublishWindowReachesTheWriters(t *testing.T) {
	savedMs, savedCfg, savedResolved, savedFailed := Microservice, Configuration, ResolvedEventsWriter, FailedEventsWriter
	t.Cleanup(func() {
		Microservice, Configuration, ResolvedEventsWriter, FailedEventsWriter = savedMs, savedCfg, savedResolved, savedFailed
	})
	host, port := startEmbeddedNats(t)

	for _, tc := range []struct {
		doc  string
		want int
	}{
		{`{"resolution":{"publishWindow":300}}`, 300},
		// Counterweight: with nothing set the writers get the default, so a wiring that
		// always passed 300 (or any other fixed value) cannot pass both.
		{`{}`, 512},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
			Microservice.UseMetricsRegistry(prometheus.NewRegistry())
			Microservice.MicroserviceConfigurationRaw = []byte(tc.doc)
			require.NoError(t, parseConfiguration())

			nmgr := startNatsManager(t, host, port, fmt.Sprintf("pubwindow%d", time.Now().UnixNano()))
			require.NoError(t, createPublishWriters(nmgr))
			for name, w := range map[string]messaging.OrderedWriter{"resolved": ResolvedEventsWriter, "failed": FailedEventsWriter} {
				require.Equal(t, tc.want, w.(interface{ Window() int }).Window(), "the %s events writer's window", name)
			}
			ResolvedEventsWriter.Close()
			FailedEventsWriter.Close()
		})
	}
}

// The processor's resolvers are handed the CachedApi itself, which is what lets an event's
// cache reads be made at the same time (model.ReadAheadForEvent dispatches on the concrete
// type). A wrapper put around it here, for tracing say, would quietly send every event back
// to reading its caches one after another, with every processor test still green, because
// they build their own resolvers.
func TestTheProcessorReadsItsCachesAtTheSameTime(t *testing.T) {
	savedMs, savedCfg, savedMetrics, savedCached, savedDead, savedTransit :=
		Microservice, Configuration, ResolveMetrics, CachedApi, DeadLetters, SecretTransit
	t.Cleanup(func() {
		Microservice, Configuration, ResolveMetrics, CachedApi, DeadLetters, SecretTransit =
			savedMs, savedCfg, savedMetrics, savedCached, savedDead, savedTransit
	})
	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	ResolveMetrics = processor.NewResolveMetrics(Microservice)
	DeadLetters = deadletter.NewProducer(Microservice)
	SecretTransit = testTransit(t)
	CachedApi = model.NewCachedApi(&model.Api{}, &model.Caches{})

	require.True(t, model.ReadsAheadConcurrently(newInboundEventsProcessor().Api),
		"the inbound processor's api is not the CachedApi itself, so no event reads its caches at the same time")
}

// The inbound processor counts a lost failure record on THIS service's producer: the one
// core/service built, which the max-delivery recorder and the alarm publisher share. A processor
// test builds its own producer, so a main.go that stopped passing this one would leave every
// processor test green while the loss went uncounted in production.
func TestTheInboundProcessorCountsLostRecordsOnTheServiceProducer(t *testing.T) {
	savedMs, savedCfg, savedMetrics, savedDead := Microservice, Configuration, ResolveMetrics, DeadLetters
	t.Cleanup(func() {
		Microservice, Configuration, ResolveMetrics, DeadLetters = savedMs, savedCfg, savedMetrics, savedDead
	})
	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	ResolveMetrics = processor.NewResolveMetrics(Microservice)
	DeadLetters = deadletter.NewProducer(Microservice)
	savedTransit := SecretTransit
	t.Cleanup(func() { SecretTransit = savedTransit })
	SecretTransit = testTransit(t)

	require.NotNil(t, DeadLetters)
	require.Same(t, DeadLetters, newInboundEventsProcessor().DeadLetters())
}

// The key the service derives from the root key is the one the processor's resolvers open
// sealed credential secrets with. A processor built without it refuses at construction
// (WithSecretTransit panics on nil), but one handed some OTHER key would start and refuse
// every MQTT password, so the wiring is checked by identity.
func TestTheProcessorOpensSealedSecretsWithTheServicesKey(t *testing.T) {
	savedMs, savedCfg, savedMetrics, savedDead, savedTransit := Microservice, Configuration, ResolveMetrics, DeadLetters, SecretTransit
	t.Cleanup(func() {
		Microservice, Configuration, ResolveMetrics, DeadLetters, SecretTransit = savedMs, savedCfg, savedMetrics, savedDead, savedTransit
	})
	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	ResolveMetrics = processor.NewResolveMetrics(Microservice)
	DeadLetters = deadletter.NewProducer(Microservice)
	SecretTransit = testTransit(t)

	require.Same(t, SecretTransit, newInboundEventsProcessor().SecretTransit())
}

func testTransit(t *testing.T) *credential.SecretTransit {
	t.Helper()
	tr, err := credential.DeriveSecretTransit(make([]byte, 32))
	require.NoError(t, err)
	return tr
}
