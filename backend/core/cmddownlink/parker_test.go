// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmddownlink

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParkWire pins the park mutation's document and variables against LITERALS (never the
// package's own constant) and its three outcomes. Parking without the nonce would be a
// re-actuation lever, so the nonce must ride the wire under the schema's name.
func TestParkWire(t *testing.T) {
	t.Run("parked", func(t *testing.T) {
		q := &rawQuerier{data: `{"parkCommand":true}`}
		parked, err := NewParker(q, "http://cd/graphql").Park(context.Background(), "acme", "c1", "n1")
		require.NoError(t, err)
		assert.True(t, parked)
		assert.Equal(t, "acme", q.gotTenant)
		assert.Equal(t, map[string]any{"token": "c1", "dispatchNonce": "n1"}, q.gotVars)
		assert.True(t, strings.Contains(q.gotQuery, "parkCommand(token: $token, dispatchNonce: $dispatchNonce)"), q.gotQuery)
	})
	t.Run("settled is not an error", func(t *testing.T) {
		q := &rawQuerier{data: `{"parkCommand":false}`}
		parked, err := NewParker(q, "u").Park(context.Background(), "acme", "c1", "n1")
		require.NoError(t, err, "a row that moved on under the request is an outcome, not a failure")
		assert.False(t, parked)
	})
	t.Run("transport error", func(t *testing.T) {
		q := &rawQuerier{err: errors.New("command-delivery unreachable")}
		parked, err := NewParker(q, "u").Park(context.Background(), "acme", "c1", "n1")
		require.Error(t, err, "the caller must leave the message unacked, so the error must surface")
		assert.False(t, parked)
	})
}

// TestClaimWire pins the drain-path claim: the nonce comes back, null is LOST, and an empty
// nonce is an error rather than a win.
func TestClaimWire(t *testing.T) {
	t.Run("won", func(t *testing.T) {
		q := &rawQuerier{data: `{"markCommandSent":"n9"}`}
		nonce, won, err := NewClaimer(q, "u").Claim(context.Background(), "acme", "c1")
		require.NoError(t, err)
		assert.True(t, won)
		assert.Equal(t, "n9", nonce)
		assert.Equal(t, map[string]any{"token": "c1"}, q.gotVars)
		assert.True(t, strings.Contains(q.gotQuery, "markCommandSent(token: $token)"), q.gotQuery)
	})
	t.Run("null is lost", func(t *testing.T) {
		q := &rawQuerier{data: `{"markCommandSent":null}`}
		nonce, won, err := NewClaimer(q, "u").Claim(context.Background(), "acme", "c1")
		require.NoError(t, err)
		assert.False(t, won)
		assert.Empty(t, nonce)
	})
	t.Run("empty is an error", func(t *testing.T) {
		q := &rawQuerier{data: `{"markCommandSent":""}`}
		_, won, err := NewClaimer(q, "u").Claim(context.Background(), "acme", "c1")
		require.Error(t, err)
		assert.False(t, won, "a claim with no nonce must never read as a win")
	})
	t.Run("transport error", func(t *testing.T) {
		q := &rawQuerier{err: errors.New("down")}
		_, won, err := NewClaimer(q, "u").Claim(context.Background(), "acme", "c1")
		require.Error(t, err)
		assert.False(t, won)
	})
}
