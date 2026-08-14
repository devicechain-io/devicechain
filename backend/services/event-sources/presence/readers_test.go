// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubGraphQL answers one canned response and records what it was asked.
type stubGraphQL struct {
	query     string
	vars      map[string]any
	responder func() (any, error)
}

func (s *stubGraphQL) Query(_ context.Context, _, _ string, query string, vars map[string]any, out any) error {
	s.query, s.vars = query, vars
	data, err := s.responder()
	if err != nil {
		return err
	}
	if out == nil || data == nil {
		return nil
	}
	b, _ := json.Marshal(data)
	return json.Unmarshal(b, out)
}

// 🔴 THIS TESTS THE WIRE SEAM, WHICH EVERY OTHER TEST IN THIS PACKAGE FAKES PAST. The
// reconcile tests drive reconcileTenant against an in-memory projection, so the query
// text and the response decoding are covered by nothing: ask device-state the wrong
// question — activeOnly:true, or forget to select `active` — and every one of them still
// passes while the repair path silently loses the rows it exists to fix.
//
// Two claims, and the first is the one that regresses invisibly:
//
//   - the read asks for BOTH halves (activeOnly:false). Asking activeOnly:true returns
//     only the online devices, so a device wedged offline is absent from the map, its
//     stored session is unknown, and the repair falls back to the broker's regressed id —
//     the exact defect this seam exists to close.
//   - `active` is selected and decoded. Without it every row decodes as Active:false, so
//     direction 2 stops emitting deaths entirely and direction 1 re-repairs the whole
//     connected fleet on every pass.
func TestTheProjectionReadAsksForBothHalvesAndDecodesActive(t *testing.T) {
	gql := &stubGraphQL{responder: func() (any, error) {
		return map[string]any{"assertedDeviceStates": []map[string]any{
			{"deviceToken": "sensor-001", "sessionId": "1786552664076882575", "active": true},
			{"deviceToken": "sensor-002", "sessionId": "1786552664076882000", "active": false},
		}}, nil
	}}
	r := NewGraphQLProjectionReader(gql, "http://device-state/graphql")

	states, err := r.AssertedStates(context.Background(), "acme", "mqtt1")
	require.NoError(t, err)

	assert.Contains(t, gql.query, "activeOnly: false",
		"the read must ask for the offline rows too; without their stored sessionId a repair for a "+
			"device with a regressed broker session is rejected on every pass")
	// 🔴 ANCHORED ON THE FIELD THAT PRECEDES IT IN THE SELECTION SET. `Contains(query,
	// "active")` is satisfied by the word `activeOnly` in the argument list, so it passes
	// with the field deleted — a vacuous assertion for the exact regression this test
	// names. Anchoring on the neighbour rather than on the closing brace keeps it from
	// breaking every time the query is reformatted, which is what it did when paging
	// arguments pushed the selection set onto its own line.
	assert.Contains(t, gql.query, "sessionId active",
		"the read must select `active` in the selection set, or every row decodes as offline "+
			"and direction 2 stops emitting deaths entirely")
	assert.Equal(t, "mqtt1", gql.vars["source"],
		"the read must be source-scoped, or one pass declares every other transport's devices dead")

	require.Len(t, states, 2)
	online := states[DeviceKey("acme", "sensor-001")]
	assert.True(t, online.Active)
	assert.Equal(t, uint64(1786552664076882575), online.SessionId)
	assert.Equal(t, "acme", online.Tenant)

	offline := states[DeviceKey("acme", "sensor-002")]
	assert.False(t, offline.Active, "an asserted-offline row decoded as online; direction 2 would re-kill it")
	assert.Equal(t, uint64(1786552664076882000), offline.SessionId,
		"the stored session is what a repair defers to when the broker's has regressed")
}

// A row whose sessionId cannot be read is dropped rather than defaulted to zero. Zero
// tells both directions a lie: a synthetic death carrying it is refused by presence
// ordering against any real stored session, and a connect repair reads the row as having
// no session to defer to — so the device is "repaired" and counted while nothing changes.
func TestAnUnreadableSessionIsDroppedRatherThanZeroed(t *testing.T) {
	gql := &stubGraphQL{responder: func() (any, error) {
		return map[string]any{"assertedDeviceStates": []map[string]any{
			{"deviceToken": "sensor-001", "sessionId": "notanumber", "active": false},
			{"deviceToken": "", "sessionId": "12", "active": true},
			{"deviceToken": "sensor-003", "sessionId": "12", "active": true},
		}}, nil
	}}
	r := NewGraphQLProjectionReader(gql, "http://device-state/graphql")

	states, err := r.AssertedStates(context.Background(), "acme", "mqtt1")
	require.NoError(t, err)
	require.Len(t, states, 1, "the unparseable-session and empty-token rows must both be dropped")
	_, kept := states[DeviceKey("acme", "sensor-003")]
	assert.True(t, kept)
}

// A read failure surfaces. Reconcile skips the tenant for the pass rather than diffing
// against an empty projection — which would read as "the platform believes nothing is
// online" and, on a complete inventory, is indistinguishable from a fleet that just
// connected.
func TestAProjectionReadErrorSurfaces(t *testing.T) {
	gql := &stubGraphQL{responder: func() (any, error) { return nil, errors.New("device-state unreachable") }}
	r := NewGraphQLProjectionReader(gql, "http://device-state/graphql")
	_, err := r.AssertedStates(context.Background(), "acme", "mqtt1")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "unreachable"))
}
