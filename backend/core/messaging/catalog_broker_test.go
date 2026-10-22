// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

func initializedManager(t *testing.T, area string, deployed bool) *NatsManager {
	t.Helper()
	srv := runTestServer(t)
	if deployed {
		// A deployed manager only verifies; the broker administrator has created what it opens.
		_, err := adminOn(t, srv).ReconcileBroker(context.Background(), streams.PlatformAreas)
		require.NoError(t, err)
	}
	ms := testMicroservice(t, srv, area)
	ms.Deployed = deployed
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	require.NoError(t, nmgr.Initialize(context.Background()))
	t.Cleanup(nmgr.nc.Close)
	return nmgr
}

// Against a real broker: a declared use works, an undeclared one is refused, and the refusal
// is free of side effects - the stream the area was not declared to touch is not created.
func TestDeployedManagerOpensWhatItDeclaresAndNothingElse(t *testing.T) {
	nmgr := initializedManager(t, "event-sources", true)

	w, err := nmgr.NewWriter(streams.InboundEvents)
	require.NoError(t, err, "event-sources is the declared writer of inbound-events")
	require.NotNil(t, w)
	r, err := nmgr.NewReader(streams.DeviceEventsCapture)
	require.NoError(t, err, "event-sources is the declared reader of the capture")
	require.NotNil(t, r)

	_, err = nmgr.NewWriter(streams.DeviceCommands)
	require.Error(t, err, "event-sources does not write device-commands")
	_, err = nmgr.NewReader(streams.InboundEvents)
	require.Error(t, err, "event-sources does not read inbound-events")

	_, err = nmgr.js.StreamInfo(StreamName("test", streams.DeviceCommands))
	require.Error(t, err, "a refused writer must not have created the stream")
}

// The consumer-name argument for leaving consumers with their services rests on two facts
// about what the declaration can produce, and this holds them against a real broker: no stream
// the platform creates sources, mirrors, transforms or re-publishes, and every consumer it
// creates carries a filter.
func TestNoStreamCrossesStreamsAndEveryConsumerIsFiltered(t *testing.T) {
	nmgr := initializedManager(t, "catalog-scan", false)

	for _, s := range streams.All {
		if s.Dormant {
			continue
		}
		name, err := nmgr.ensureStream(s.Suffix)
		require.NoError(t, err, s.Suffix)
		info, err := nmgr.js.StreamInfo(name)
		require.NoError(t, err, s.Suffix)
		cfg := info.Config
		require.Empty(t, cfg.Sources, "%s sources another stream", s.Suffix)
		require.Nil(t, cfg.Mirror, "%s mirrors another stream", s.Suffix)
		require.Nil(t, cfg.RePublish, "%s re-publishes", s.Suffix)
		require.Nil(t, cfg.SubjectTransform, "%s transforms subjects", s.Suffix)

		if s.Shape == streams.ShapeAdvisory {
			continue // read only by the max-delivery recorder, whose filter list is set by it
		}
		reader, err := nmgr.NewReader(s.Suffix)
		require.NoError(t, err, s.Suffix)
		nr := reader.(*natsReader)
		ci, err := nmgr.js.ConsumerInfo(name, nr.durable)
		require.NoError(t, err, s.Suffix)
		require.True(t, ci.Config.FilterSubject != "" || len(ci.Config.FilterSubjects) > 0,
			"the durable for %s has no filter: an unfiltered reader sees what a push consumer stored under its source subject", s.Suffix)
	}
}
