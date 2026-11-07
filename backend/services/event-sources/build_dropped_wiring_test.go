// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// The HTTP source answers 202 whenever its decoded hook returns nil. An event dropped while its
// message was built (no key to seal a presented secret, a marshal failure) used to return nil.
//
// GUARD: kills an onEventDecoded that returns nil for ok=false, and a build drop that is not
// counted under its own cause.
func TestHTTPEventDroppedAtConstructionIsReportedAndCounted(t *testing.T) {
	count := func(cause string) float64 {
		return testutil.ToFloat64(BuildDroppedCounter.WithLabelValues("http", cause))
	}

	t.Run("a presented secret that cannot be sealed", func(t *testing.T) {
		sealingFixture(t)
		SecretTransit = nil
		before := count("no_transit_key")
		err := onEventDecoded("http", "acme", presentingEvent("pw-long-enough-to-matter"),
			&model.UnresolvedMeasurementsPayload{}, "")
		require.ErrorIs(t, err, processor.ErrEventNotBuilt)
		require.Equal(t, before+1, count("no_transit_key"))
	})

	t.Run("a presented access token that cannot be sealed", func(t *testing.T) {
		sealingFixture(t)
		SecretTransit = nil
		before := count("no_transit_key")
		err := onEventDecoded("http", "acme", presentingToken("tok-0123456789"),
			&model.UnresolvedMeasurementsPayload{}, "")
		require.ErrorIs(t, err, processor.ErrEventNotBuilt)
		require.Equal(t, before+1, count("no_transit_key"))
	})

	t.Run("an event that cannot be marshalled", func(t *testing.T) {
		sealingFixture(t)
		ev := &model.UnresolvedEvent{Device: "bad\xffutf8", EventType: eventtype.Measurement}
		before := count("marshal_failed")
		err := onEventDecoded("http", "acme", ev, &model.UnresolvedMeasurementsPayload{}, "")
		require.ErrorIs(t, err, processor.ErrEventNotBuilt)
		require.Equal(t, before+1, count("marshal_failed"))
	})

	t.Run("an event with no tenant", func(t *testing.T) {
		sealingFixture(t)
		before := count("no_tenant")
		err := onEventDecoded("http", "", &model.UnresolvedEvent{Device: "d", EventType: eventtype.Measurement},
			&model.UnresolvedMeasurementsPayload{}, "")
		require.ErrorIs(t, err, processor.ErrEventNotBuilt)
		require.Equal(t, before+1, count("no_tenant"))
	})
}

// Every cause is exported at 0 from the start, so the first drop after a restart is an increase
// from 0 and not a series born at 1. The labels are spelled out: they are the alert's vocabulary.
func TestBuildDroppedCausesAreSeededAtZero(t *testing.T) {
	sealingFixture(t)
	seedRefusalCounters([]string{"http"})
	require.Equal(t, 4, testutil.CollectAndCount(BuildDroppedCounter), "one series per cause, exported before any drop")
	for _, cause := range []string{"no_tenant", "no_transit_key", "seal_failed", "marshal_failed"} {
		require.Zero(t, testutil.ToFloat64(BuildDroppedCounter.WithLabelValues("http", cause)), cause)
	}
}
