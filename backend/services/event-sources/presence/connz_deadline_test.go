// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// deadlineSpy answers CONNZ normally but records whether the context each page request
// arrived with carried a deadline, and how long it had left.
type deadlineSpy struct {
	hadDeadline []bool
	remaining   []time.Duration
}

func (d *deadlineSpy) Gather(_ context.Context, _ string, _ []byte, _ time.Duration) ([][]byte, error) {
	raw, _ := json.Marshal(map[string]any{
		"server": map[string]any{"id": "A"},
		"statsz": map[string]any{"active_servers": 1},
	})
	return [][]byte{raw}, nil
}

func (d *deadlineSpy) RequestWithContext(ctx context.Context, _ string, _ []byte) (*nats.Msg, error) {
	deadline, ok := ctx.Deadline()
	d.hadDeadline = append(d.hadDeadline, ok)
	if ok {
		d.remaining = append(d.remaining, time.Until(deadline))
	}
	raw, _ := json.Marshal(map[string]any{
		"server": map[string]any{"id": "A"},
		"data":   map[string]any{"total": 0, "num_connections": 0, "offset": 0, "connections": []any{}},
	})
	return &nats.Msg{Data: raw}, nil
}

// TestEveryConnzPageCarriesItsOwnDeadline is the Slice L fix, asserted where it can be
// seen rather than by waiting one out.
//
// 🔴🔴 WITH NO DEADLINE THERE IS NO WAY OUT AT ALL. nats.go's request path selects on
// exactly two things — the reply channel and the context — so a deadline-free context has
// no third exit, and the library's own protections do not cover this failure: no-responders
// is decided at PUBLISH time, so a peer that dies after accepting the request is not caught,
// and the pending request is cleared only on a permanent close or an explicit forced
// reconnect. An ordinary drop-and-auto-reconnect leaves it hanging forever.
//
// 🔑 THE ASSERTION IS THE PRESENCE OF THE DEADLINE, NOT THE ELAPSED TIME. A test that
// passed a short-deadline context and measured how fast the call returned would pass with
// the fix removed — the PARENT context would be doing the work — which is the vacuous
// shape this whole change keeps running into. The parent here deliberately has no deadline,
// so the only thing that can put one on the request is the code under test.
func TestEveryConnzPageCarriesItsOwnDeadline(t *testing.T) {
	spy := &deadlineSpy{}

	// A context with NO deadline, exactly as the service's runCtx is built.
	_, err := FetchInventory(context.Background(), spy, testInstance, 10*time.Millisecond)
	require.NoError(t, err)

	require.NotEmpty(t, spy.hadDeadline, "no page request was made; the fixture proved nothing")
	for i, had := range spy.hadDeadline {
		require.True(t, had,
			"page request %d arrived with no deadline; a peer that dies after accepting it wedges the pass, "+
				"and the canary that would have noticed used to be wedged with it", i)
	}
	for i, left := range spy.remaining {
		require.LessOrEqual(t, left, connzRequestTimeout,
			"page request %d had longer than the per-request bound left on it", i)
		require.Positive(t, left, "page request %d arrived already expired", i)
	}
}

// TestAWedgedPageIsAnErrorRatherThanAWedge drives the behaviour rather than the shape: a
// peer that accepts the request and never answers must end as a failed pass, not a stopped
// one. It shrinks the bound through the parent context so the test does not wait the real
// ten seconds — the shape test above is what proves the per-request deadline exists.
func TestAWedgedPageIsAnErrorRatherThanAWedge(t *testing.T) {
	silent := &silentPeer{}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	inv, err := FetchInventory(ctx, silent, testInstance, 10*time.Millisecond)
	require.NoError(t, err, "one unreachable server does not void the pass")
	require.False(t, inv.Complete,
		"a server whose page never came back cannot be counted as answered; the disconnect direction reads this")
	require.Less(t, time.Since(start), 2*time.Second, "the pass must end, not wedge")
}

// silentPeer answers the server PING but never replies to a CONNZ page.
type silentPeer struct{}

func (s *silentPeer) Gather(_ context.Context, _ string, _ []byte, _ time.Duration) ([][]byte, error) {
	raw, _ := json.Marshal(map[string]any{
		"server": map[string]any{"id": "A"},
		"statsz": map[string]any{"active_servers": 1},
	})
	return [][]byte{raw}, nil
}

func (s *silentPeer) RequestWithContext(ctx context.Context, _ string, _ []byte) (*nats.Msg, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
