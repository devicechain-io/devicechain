// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Reconciler.AssertedActive (device-state read → floor input) ------------

// TestAssertedActiveParsesFloorsSkipsAndScopesBySource pins the reconcile read: it is
// source-scoped (the query carries the caller's source so a sibling source's devices are
// never returned — the cross-disconnect guard); sessionId arrives as a String (a
// UnixNano that overflows a 32-bit Int); the max across the kept set is the epoch-floor
// input; and a row that cannot anchor the reconcile — a null external id or an
// unparseable sessionId — is dropped rather than corrupting the result.
func TestAssertedActiveParsesFloorsSkipsAndScopesBySource(t *testing.T) {
	var gotSource any
	gql := &fakeGraphQL{responder: func(_ string, vars map[string]any) (any, error) {
		gotSource = vars["source"]
		return map[string]any{"assertedActiveDeviceStates": []map[string]any{
			{"externalId": "plant-a/n1", "sessionId": "100"},
			{"externalId": "plant-a/n1/d1", "sessionId": "250"},
			{"externalId": nil, "sessionId": "999"},             // null external id → skipped
			{"externalId": "plant-a/n2", "sessionId": "notnum"}, // unparseable session → skipped
		}}, nil
	}}
	r := NewReconciler(gql, "http://device-state/graphql")

	devices, max, err := r.AssertedActive(context.Background(), "acme", "sparkplug:h1")
	require.NoError(t, err)
	assert.Equal(t, "sparkplug:h1", gotSource, "the read must be scoped to the caller's source (cross-disconnect guard)")
	assert.Equal(t, uint64(250), max, "the floor is the max sessionId among the kept rows")
	require.Len(t, devices, 2, "the null-externalId and bad-session rows are dropped")
	assert.Equal(t, "plant-a/n1", devices[0].ExternalId)
	assert.Equal(t, uint64(100), devices[0].SessionId)
	assert.Equal(t, "plant-a/n1/d1", devices[1].ExternalId)
	assert.Equal(t, uint64(250), devices[1].SessionId)
}

func TestAssertedActivePropagatesReadError(t *testing.T) {
	gql := &fakeGraphQL{responder: func(_ string, _ map[string]any) (any, error) {
		return nil, errors.New("device-state unreachable")
	}}
	r := NewReconciler(gql, "http://device-state/graphql")
	_, _, err := r.AssertedActive(context.Background(), "acme", "sparkplug:h1")
	require.Error(t, err, "a read failure must surface so reconcile aborts rather than guessing")
}
