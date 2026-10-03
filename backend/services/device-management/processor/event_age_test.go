// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// tooOldMetric is the counter an age refusal increments, as the registry exports it.
const tooOldMetric = "devicechain_devicemanagement_resolve_event_time_too_old_total"

// withRegistry rebuilds the suite's processor on a microservice that HAS a registry, so a
// test can read what the real wiring counted. The shared test microservice has none, and
// core builds its instruments unregistered then, which would make every count unreadable.
func (suite *InboundEventsProcessorTestSuite) withRegistry() *prometheus.Registry {
	ms := &core.Microservice{InstanceId: "devicechain", FunctionalArea: "device-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	suite.IP = NewInboundEventsProcessor(ms, suite.Inbound,
		msgtest.InlineOrderedWriter{W: suite.Resolved}, msgtest.InlineOrderedWriter{W: suite.Failed},
		core.NewNoOpLifecycleCallbacks(), suite.API, config.AuthModeOptional,
		time.Duration(config.DefaultMaxEventFutureSkewSeconds)*time.Second, NewResolveMetrics(ms))
	require.NoError(suite.T(), suite.IP.Initialize(context.Background()))
	return reg
}

// counterValue reads one counter from the registry; a counter the registry does not hold
// reads as -1, so "never registered" cannot pass for "registered and zero".
func counterValue(suite *InboundEventsProcessorTestSuite, reg *prometheus.Registry, name string) float64 {
	families, err := reg.Gather()
	require.NoError(suite.T(), err)
	for _, f := range families {
		if f.GetName() == name && len(f.GetMetric()) == 1 {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	return -1
}

// agedLocationMessage is a location event dated age before its receipt, on its first
// delivery.
func agedLocationMessage(suite *InboundEventsProcessorTestSuite, processed time.Time, age time.Duration) messaging.Message {
	loc := buildLocationsEvent()
	loc.ProcessedTime = processed
	loc.OccurredTime = processed.Add(-age)
	encoded, err := esproto.MarshalUnresolvedEvent(loc)
	require.NoError(suite.T(), err)
	return messaging.Message{Subject: testTenantSubject, Key: []byte(loc.Device), Value: encoded, NumDelivered: 1}
}

// TestTheAgeRefusalIsCountedOnceAndReadsNoDevice drives ResolveEvent itself on both sides of
// the floor. Past it: refused as Invalid, an ErrTooOld, counted exactly once, and the device
// is never read. At it: the refusal does not fire, the device IS read (and is unknown here,
// so the outcome is DeviceNotFound), and nothing is counted. Both sides go through the same
// resolver, so the zero on the second is a measurement, not an untouched counter.
func TestTheAgeRefusalIsCountedOnceAndReadsNoDevice(t *testing.T) {
	processed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		age        time.Duration
		wantReason dmproto.FailureReason
		wantCount  float64
		readsAny   bool
	}{
		{"one second past the limit", eventtime.MaxAge + time.Second, dmproto.FailureReason_Invalid, 1, false},
		{"exactly at the limit", eventtime.MaxAge, dmproto.FailureReason_DeviceNotFound, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := new(dmtest.MockApi)
			api.On("DevicesByToken", mock.Anything, mock.Anything).Return([]*dmodel.Device{}, nil)
			counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_event_time_too_old_total"})
			rez := NewEventResolver(1, api, config.AuthModeOptional, EventTimePolicy{TooOld: counter},
				nil, nil, nil, nil, nil, nil)

			event := buildLocationsEvent()
			event.ProcessedTime = processed
			event.OccurredTime = processed.Add(-tc.age)
			_, reason, err := rez.ResolveEvent(context.Background(), event)
			require.Error(t, err)
			assert.Equal(t, uint(tc.wantReason), reason)
			assert.Equal(t, tc.wantCount == 1, errors.Is(err, eventtime.ErrTooOld), "%v", err)
			assert.Equal(t, tc.wantCount, testutil.ToFloat64(counter))
			if tc.readsAny {
				api.AssertCalled(t, "DevicesByToken", mock.Anything, mock.Anything)
			} else {
				api.AssertNotCalled(t, "DevicesByToken", mock.Anything, mock.Anything)
			}
		})
	}
}

// TestAnEventDatedBeforeTheAgeLimitIsDeadLetteredOnItsFirstDelivery is the choke point every
// producer passes, the gateway transports included: an event dated more than 366 days before
// the platform received it is dead-lettered as Invalid on its FIRST delivery, without the
// device being read, and counted once.
//
// The refusal is deterministic: it compares two times the event carries immutably, so a
// redelivery answers the same way. Left to the ordinary retry path it would sit in flight
// for every redelivery the stream allows, minutes per event, for nothing.
func (suite *InboundEventsProcessorTestSuite) TestAnEventDatedBeforeTheAgeLimitIsDeadLetteredOnItsFirstDelivery() {
	reg := suite.withRegistry()
	processed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(agedLocationMessage(suite, processed, 400*24*time.Hour), nil)
	suite.API.Mock.On("DevicesByToken", mock.Anything, mock.Anything).Return([]*dmodel.Device{}, nil)

	readAndHandleOne(suite.IP, context.Background())
	item := awaitFailed(suite)
	assert.Equal(suite.T(), uint(dmproto.FailureReason_Invalid), item.event.Reason)
	assert.True(suite.T(), strings.Contains(item.event.Error, "366 days"), "the reason names the limit: %q", item.event.Error)
	suite.API.AssertNotCalled(suite.T(), "DevicesByToken", mock.Anything, mock.Anything)
	assert.Equal(suite.T(), 1.0, counterValue(suite, reg, tooOldMetric), "the refusal is counted once")
}

// The counterweight: terminal routing is keyed on the AGE refusal alone. A fresh event whose
// device is not registered yet is still left for redelivery on its first try (the device
// may appear), the device IS looked up, and nothing is counted as too old.
func (suite *InboundEventsProcessorTestSuite) TestAFreshUnresolvableEventIsStillRetried() {
	reg := suite.withRegistry()
	processed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(agedLocationMessage(suite, processed, time.Hour), nil)
	suite.API.Mock.On("DevicesByToken", mock.Anything, mock.Anything).Return([]*dmodel.Device{}, nil)

	readAndHandleOne(suite.IP, context.Background())
	select {
	case item := <-suite.IP.failed:
		suite.T().Fatalf("a fresh event was dead-lettered on its first delivery: %+v", item.event)
	case <-time.After(2 * time.Second):
	}
	suite.API.AssertCalled(suite.T(), "DevicesByToken", mock.Anything, mock.Anything)
	assert.Equal(suite.T(), 0.0, counterValue(suite, reg, tooOldMetric), "a fresh event is not counted as too old")
}
