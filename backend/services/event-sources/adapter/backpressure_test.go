// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Presence transitions are admitted while inbound-events refuses readings; readings are not.
// Every producer of a transition through this emitter (the broker presence tap, Sparkplug,
// LwM2M) has no durable retry, so a refused one would be lost, where a refused reading is
// counted and, over HTTP, retried by the client.
func TestOnlyPresenceTransitionsBypassBackpressure(t *testing.T) {
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "sp", true)
	at := time.UnixMilli(1_700_000_000_500).UTC()

	require.NoError(t, e.EmitPresence(context.Background(), "acme", "s", "dev-1",
		PresenceEvent{ExternalId: "g/n", Connected: false, Reason: "ndeath", SessionId: 7, OccurredAt: at}))
	require.NoError(t, e.EmitPresenceDemotion(context.Background(), "acme", "s", "dev-1",
		DemotionEvent{SessionId: 7, OccurredAt: at, Reason: "released"}))
	require.NoError(t, emitErr(e, context.Background(), "acme", "s", "dev-1",
		[]Sample{{Name: "t", Value: 1, Time: at.UnixMilli()}}))

	require.Len(t, w.msgs, 3)
	assert.True(t, w.msgs[0].BypassBackpressure, "a presence transition must be admitted past a closed gate")
	assert.True(t, w.msgs[1].BypassBackpressure, "a demotion must be admitted past a closed gate")
	assert.False(t, w.msgs[2].BypassBackpressure, "a reading must NOT bypass the gate")
}
