// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/stretchr/testify/require"
)

func TestRoundSourceIncarnationsHaveSeparateIdsAndCaches(t *testing.T) {
	b := newFakeBroker()
	e, err := NewRoundEngine(RoundEngineConfig{Kind: eventenvelope.KindUnresolved, DedupKind: "W", Writer: fakeWriter{b}, Rounds: fakeRoundPub{b}})
	require.NoError(t, err)
	var settled int
	for _, epoch := range []uint64{123, 124} {
		k := RoundKey{Tenant: "acme", SourceEpoch: epoch}
		e.Publish(context.Background(), k, []RoundMember{{Index: 1, Bytes: []byte{10, 1, 'd'}, Redelivered: true, Settle: func(err error) { require.NoError(t, err); settled++ }}})
		e.Wait()
	}
	require.Equal(t, 2, settled)
	require.Len(t, b.bySeq, 2, "a new incarnation must not hit the previous incarnation's round or known-member cache")
	for _, msg := range b.bySeq {
		require.Contains(t, msg.DedupID, "W:acme:")
	}
}
