// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// newGateProcessor builds the inbound processor over the REAL writers main.go builds, on
// nmgr, reading from reader. It is the one place the publish-stage tests name a writer
// constructor, so inbound_pipeline_test.go is the same file on any tree it is run against.
func newGateProcessor(t testing.TB, nmgr *messaging.NatsManager, reader messaging.MessageReader,
	api dmodel.DeviceManagementApi) *InboundEventsProcessor {
	t.Helper()
	resolved, err := nmgr.NewOrderedWriter(streams.ResolvedEvents, messaging.WriterResolution, config.DefaultPublishWindow)
	if err != nil {
		t.Fatalf("resolved writer: %v", err)
	}
	failed, err := nmgr.NewOrderedWriter(streams.FailedEvents, messaging.WriterResolution, config.DefaultPublishWindow)
	if err != nil {
		t.Fatalf("failed writer: %v", err)
	}
	return NewInboundEventsProcessor(nmgr.Microservice, []messaging.MessageReader{reader}, resolved, failed,
		core.NewNoOpLifecycleCallbacks(), api, config.AuthModeOptional,
		time.Duration(config.DefaultMaxEventFutureSkewSeconds)*time.Second,
		NewResolveMetrics(nmgr.Microservice))
}

// newBenchProcessor is newGateProcessor for BenchmarkResolvedPublishThroughput, with the
// inbound readers, the publish window and the resolver pool width as parameters.
func newBenchProcessor(b testing.TB, nmgr *messaging.NatsManager, readers []messaging.MessageReader,
	api dmodel.DeviceManagementApi, window, resolvers int) *InboundEventsProcessor {
	b.Helper()
	resolved, err := nmgr.NewOrderedWriter(streams.ResolvedEvents, messaging.WriterResolution, window)
	if err != nil {
		b.Fatalf("resolved writer: %v", err)
	}
	failed, err := nmgr.NewOrderedWriter(streams.FailedEvents, messaging.WriterResolution, window)
	if err != nil {
		b.Fatalf("failed writer: %v", err)
	}
	iproc := NewInboundEventsProcessor(nmgr.Microservice, readers, resolved, failed,
		core.NewNoOpLifecycleCallbacks(), api, config.AuthModeOptional,
		time.Duration(config.DefaultMaxEventFutureSkewSeconds)*time.Second,
		NewResolveMetrics(nmgr.Microservice))
	iproc.resolverCount = resolvers
	iproc.secretTransit = testSecretTransit(b)
	return iproc
}
