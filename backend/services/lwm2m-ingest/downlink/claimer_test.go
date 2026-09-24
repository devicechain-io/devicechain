// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawQuerier answers every query with a fixed JSON data object, decoded into the caller's
// response type exactly as svcclient decodes a real answer, and records what was sent.
type rawQuerier struct {
	data string
	err  error

	gotTenant string
	gotQuery  string
	gotVars   map[string]any
}

func (q *rawQuerier) Query(_ context.Context, _, tenant, query string, vars map[string]any, out any) error {
	q.gotTenant, q.gotQuery, q.gotVars = tenant, query, vars
	if q.err != nil {
		return q.err
	}
	return json.Unmarshal([]byte(q.data), out)
}

// TestClaimDispatchWire pins the live-path claim's wire contract and its three outcomes.
//
// The document text and variable names are what command-delivery's schema accepts, and the
// graphql-go fork refuses an unknown argument sent through a variable, so a drift here fails
// every live command closed. The null/empty split matters in the other direction: an empty
// string read as a win would actuate a command whose outcome nothing could settle.
func TestClaimDispatchWire(t *testing.T) {
	t.Run("won", func(t *testing.T) {
		q := &rawQuerier{data: `{"confirmCommandDispatch":"n2"}`}
		nonce, won, err := NewCommandClaimer(q, "http://cd/graphql").ClaimDispatch(context.Background(), "acme", "c1", "n1")
		require.NoError(t, err)
		assert.True(t, won)
		assert.Equal(t, "n2", nonce, "the caller must receive the ROTATED nonce, which its response has to quote")
		assert.Equal(t, "acme", q.gotTenant)
		assert.Equal(t, map[string]any{"token": "c1", "dispatchNonce": "n1"}, q.gotVars,
			"the envelope's nonce is what the confirmation is predicated on")
		assert.True(t, strings.Contains(q.gotQuery, "confirmCommandDispatch(token: $token, dispatchNonce: $dispatchNonce)"),
			"the document must name command-delivery's field and arguments: %s", q.gotQuery)
	})
	t.Run("null is lost", func(t *testing.T) {
		q := &rawQuerier{data: `{"confirmCommandDispatch":null}`}
		nonce, won, err := NewCommandClaimer(q, "u").ClaimDispatch(context.Background(), "acme", "c1", "n1")
		require.NoError(t, err, "a lost confirmation is an outcome, not an error")
		assert.False(t, won)
		assert.Empty(t, nonce)
	})
	t.Run("empty is an error", func(t *testing.T) {
		q := &rawQuerier{data: `{"confirmCommandDispatch":""}`}
		_, won, err := NewCommandClaimer(q, "u").ClaimDispatch(context.Background(), "acme", "c1", "n1")
		require.Error(t, err)
		assert.False(t, won, "a confirmation with no nonce must never read as a win")
	})
	t.Run("transport error", func(t *testing.T) {
		q := &rawQuerier{err: errors.New("command-delivery unreachable")}
		_, won, err := NewCommandClaimer(q, "u").ClaimDispatch(context.Background(), "acme", "c1", "n1")
		require.Error(t, err)
		assert.False(t, won)
	})
}
