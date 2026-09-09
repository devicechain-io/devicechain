// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphqlws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// terminalWait bounds how long a test waits for a subscription to end.
//
// 🔴 EVERY WAIT HERE IS BOUNDED, AND THAT IS NOT TIDINESS. The failure these tests
// exist to catch is a frame being ACCEPTED, which means the subscription does not end
// — so `for range sub.C()` written the obvious way does not fail against unfixed
// code, it HANGS, and a hang is the one outcome a CI run reports as neither pass nor
// fail. That is how it behaved when the control was run: the package ran past the
// two-minute mark with nothing to show. Bounded, the same defect is a named failure
// in ten seconds.
const terminalWait = 10 * time.Second

// awaitTerminal ranges the subscription to its end and returns how many values
// arrived, failing the test if it never ends.
//
// The ranging happens in drainCount's goroutine, which asserts NOTHING: a
// require/assert there would unwind only that goroutine, and the test would report a
// pass having checked nothing. Everything below is decided here, on the test
// goroutine.
func awaitTerminal(t *testing.T, sub *Subscription, why string) int {
	t.Helper()
	select {
	case n := <-drainCount(sub):
		return n
	case <-time.After(terminalWait):
		t.Fatalf("the subscription never ended: %s", why)
		return 0
	}
}

// oversizedServer answers the handshake, then sends `pre` ordinary frames followed by
// one frame of blobBytes, and holds the socket open afterwards — so a client that
// ends the subscription can only have decided to.
func oversizedServer(t *testing.T, pre int, blobBytes int) string {
	t.Helper()
	blob := strings.Repeat("A", blobBytes)
	return rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		var sub wsMessage
		if err := conn.ReadJSON(&sub); err != nil {
			return
		}
		for i := 0; i < pre; i++ {
			small, _ := json.Marshal(map[string]any{"data": map[string]int{"counter": i + 1}})
			_ = conn.WriteJSON(wsMessage{ID: sub.ID, Type: msgNext, Payload: small})
		}
		big, _ := json.Marshal(map[string]any{"data": map[string]string{"blob": blob}})
		_ = conn.WriteJSON(wsMessage{ID: sub.ID, Type: msgNext, Payload: big})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
}

// A `next` frame past the read limit ends the subscription with a terminal error
// rather than being buffered.
//
// 🔴 gorilla APPLIES NO LIMIT OF ITS OWN — it gates the check on `readLimit > 0` and
// nothing sets one — so before WithReadLimit a client here buffered whatever the far
// end sent, at whatever size the far end chose. The delivery contract is what makes
// the assertion sharp: the subscription must END, and Err must say so, because a
// consumer that could not tell "the frame was refused" from "the stream finished"
// would read a refusal as a clean completion.
func TestOversizedFrameTerminatesTheSubscription(t *testing.T) {
	const limit = 4096
	url := oversizedServer(t, 1, limit*4)

	c, err := Dial(dialCtx(t), url, nil, WithReadLimit(limit))
	require.NoError(t, err)
	defer c.Close()

	sub, err := c.Subscribe(context.Background(), "subscription { counter(to: 100) }", nil)
	require.NoError(t, err)

	n := awaitTerminal(t, sub, "the oversized frame was accepted and buffered instead of ending it")
	assert.Equal(t, 1, n, "the frame under the ceiling is delivered; the one over it is not")
	require.Error(t, sub.Err(), "the subscription ended without saying why")
	var closeErr *websocket.CloseError
	if errors.As(sub.Err(), &closeErr) {
		assert.Equal(t, websocket.CloseMessageTooBig, closeErr.Code,
			"the client answers an oversized frame with 1009 (message too big)")
	}
}

// A frame comfortably under the ceiling still arrives — the counterweight, without
// which a limit set absurdly low would satisfy the test above and break every real
// stream.
func TestFramesUnderTheReadLimitStillArrive(t *testing.T) {
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		var sub wsMessage
		if err := conn.ReadJSON(&sub); err != nil {
			return
		}
		payload, _ := json.Marshal(map[string]any{"data": map[string]string{"blob": strings.Repeat("A", 8192)}})
		_ = conn.WriteJSON(wsMessage{ID: sub.ID, Type: msgNext, Payload: payload})
		_ = conn.WriteJSON(wsMessage{ID: sub.ID, Type: msgComplete})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	c, err := Dial(dialCtx(t), url, nil, WithReadLimit(1<<20))
	require.NoError(t, err)
	defer c.Close()

	sub, err := c.Subscribe(context.Background(), "subscription { counter(to: 1) }", nil)
	require.NoError(t, err)

	n := awaitTerminal(t, sub, "an 8 KiB frame under a 1 MiB ceiling was neither delivered nor refused")
	assert.Equal(t, 1, n)
	assert.NoError(t, sub.Err(), "a clean server complete")
}

// 🔴 A NON-POSITIVE LIMIT IS UNLIMITED TO gorilla, SO IT MUST NOT SURVIVE THE OPTION,
// and a caller that never passes the option must not be unlimited either.
//
// This is asserted over the WIRE, at a frame past the DEFAULT ceiling, because that
// is the only place the two candidate behaviours differ: a clamp that quietly handed
// 0 to SetReadLimit, and a Dial that never set one at all, both look correct at every
// smaller size. Checking that the clamp assigns the default by re-running the clamp
// would be a control built out of the thing it is controlling.
func TestAnUnsetOrZeroReadLimitStillStopsAtTheDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{"no option at all", nil},
		{"an explicit zero, which gorilla would read as unlimited", []Option{WithReadLimit(0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := oversizedServer(t, 0, int(defaultReadLimit)+(1<<10))

			c, err := Dial(dialCtx(t), url, nil, tc.opts...)
			require.NoError(t, err)
			defer c.Close()

			sub, err := c.Subscribe(context.Background(), "subscription { counter(to: 1) }", nil)
			require.NoError(t, err)

			n := awaitTerminal(t, sub, "a frame past the default ceiling was accepted; this connection has no read limit at all")
			assert.Zero(t, n, "a frame past the default ceiling must not be delivered")
			require.Error(t, sub.Err(), "the subscription ended without saying why")
		})
	}
}
