// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// An instance upgraded onto a configured design rate has its hot streams on the flat 1 GiB /
// 5M-message ceilings an earlier build gave them. Services only VERIFY their streams (and
// streamDrift ignores the ceilings), so the move is the broker administrator's: its
// pre-upgrade hook Job, or `dcctl broker reconcile`, must move the ceilings ON THE EXISTING STREAM (UpdateStream, not a delete and recreate), so the
// stream keeps what it holds. This is the path that makes the outage runway real on an
// instance that already exists, and a negative control rides along: a stream the runway does
// not size (a control-plane one) keeps its ceilings.
func TestAnExistingHotStreamGetsItsRunwayCeilingsInPlace(t *testing.T) {
	nmgr := startDedupManager(t, "runway-upgrade")
	n := &nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats

	// The configuration of the earlier build: no design rate, so only the tier applies.
	n.StreamDesignRate, n.StreamRunwayMinutes = 0, 0
	legacyBounds := map[string]streamBounds{}
	for _, suffix := range []string{streams.ResolvedEvents, streams.DeviceRoster} {
		name := StreamName(nmgr.Microservice.InstanceId, suffix)
		legacy := &nats.StreamConfig{
			Name:      name,
			Subjects:  StreamSubjects(nmgr.Microservice.InstanceId, suffix),
			Storage:   nats.FileStorage,
			Retention: nats.LimitsPolicy,
			Discard:   nats.DiscardOld,
			MaxAge:    streamMaxAge,
			Replicas:  nmgr.effectiveStreamReplicas(),
		}
		legacyBounds[suffix] = nmgr.streamBounds(suffix)
		applyStreamBounds(legacy, legacyBounds[suffix])
		_, err := nmgr.js.AddStream(legacy)
		require.NoError(t, err)
		_, err = nmgr.js.Publish(tenantSubjectFor(t, nmgr, suffix), []byte("held"))
		require.NoError(t, err)
	}
	require.Equal(t, config.DefaultStreamMaxBytes, legacyBounds[streams.ResolvedEvents].maxBytes)
	require.Equal(t, config.DefaultStreamMaxMsgs, legacyBounds[streams.ResolvedEvents].maxMsgs)

	// The upgrade: the target rate and a 15-minute runway.
	n.StreamDesignRate, n.StreamRunwayMinutes = 20_000, 15
	wantBytes := int64(20_000) * 15 * 60 * streams.RunwayBytesPerMessageFor(streams.ResolvedEvents)
	wantMsgs := int64(20_000) * 15 * 60
	require.Greater(t, wantBytes, config.DefaultStreamMaxBytes, "the scenario must need more than the flat ceiling")

	for _, suffix := range []string{streams.ResolvedEvents, streams.DeviceRoster} {
		name := StreamName(nmgr.Microservice.InstanceId, suffix)
		before, err := nmgr.js.StreamInfo(name)
		require.NoError(t, err)

		_, err = nmgr.NewWriter(suffix) // ensureStream, as the broker administrator's upgrade hook runs it
		require.NoError(t, err)

		after, err := nmgr.js.StreamInfo(name)
		require.NoError(t, err)
		require.Equal(t, before.Created, after.Created, "%s must be updated in place, not recreated", suffix)
		require.Equal(t, uint64(1), after.State.Msgs, "%s must keep what it held", suffix)
		if suffix == streams.ResolvedEvents {
			require.Equal(t, wantBytes, after.Config.MaxBytes, "the runway ceiling must reach the existing stream")
			require.Equal(t, wantMsgs, after.Config.MaxMsgs, "the message ceiling must follow the runway")
		} else {
			require.Equal(t, legacyBounds[suffix].maxBytes, after.Config.MaxBytes, "a stream not sized by time keeps its ceiling")
			require.Equal(t, legacyBounds[suffix].maxMsgs, after.Config.MaxMsgs)
		}
	}
}
