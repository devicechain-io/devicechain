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
//
// The second return is how the refusal looked FROM THE FAR END: the error that ends
// this server's own read loop, which is the only place a test can see whether the
// client said why it was leaving or simply dropped the socket. Buffered, so the
// script never blocks on a test that does not read it.
func oversizedServer(t *testing.T, pre int, blobBytes int) (string, <-chan error) {
	t.Helper()
	blob := strings.Repeat("A", blobBytes)
	peerEnd := make(chan error, 1)
	url := rawServer(t, func(conn *websocket.Conn) {
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
				select {
				case peerEnd <- err:
				default:
				}
				return
			}
		}
	})
	return url, peerEnd
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
	url, peerEnd := oversizedServer(t, 1, limit*4)

	c, err := Dial(dialCtx(t), url, nil, WithReadLimit(limit))
	require.NoError(t, err)
	defer c.Close()

	sub, err := c.Subscribe(context.Background(), "subscription { counter(to: 100) }", nil)
	require.NoError(t, err)

	n := awaitTerminal(t, sub, "the oversized frame was accepted and buffered instead of ending it")
	assert.Equal(t, 1, n, "the frame under the ceiling is delivered; the one over it is not")
	// 🔴 ASSERTED, NOT CONDITIONAL. This read the client's terminal error as a
	// *websocket.CloseError under an `if`, and a gorilla client that refuses a frame
	// for its SIZE reports its own ErrReadLimit, never a close error — the far end
	// sent nothing. So the check inside that `if` had never once executed, and a
	// subscription that ended for any other reason at all would have satisfied it.
	require.ErrorIs(t, sub.Err(), websocket.ErrReadLimit,
		"the subscription ended, but not because the frame was over the ceiling")

	// And the far end was TOLD. A client that refuses a frame and simply drops the
	// socket leaves the server unable to tell a refusal from a crashed consumer, and
	// this is the only vantage point from which the difference is visible. Bounded,
	// because the thing being reported is a close frame that never arrives — and an
	// unbounded receive here would make that outcome a hang rather than a failure.
	select {
	case err := <-peerEnd:
		var closeErr *websocket.CloseError
		if !errors.As(err, &closeErr) {
			t.Fatalf("the far end's connection ended with %v, want a close frame — the client refused the "+
				"frame without telling the peer why", err)
		}
		assert.Equal(t, websocket.CloseMessageTooBig, closeErr.Code,
			"the client answers an oversized frame with 1009 (message too big)")
	case <-time.After(terminalWait):
		t.Fatal("the far end never saw the connection end: the client refused the frame and then left the " +
			"socket open, so the server has no way to know the stream is over")
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
			url, _ := oversizedServer(t, 0, int(defaultReadLimit)+(1<<10))

			c, err := Dial(dialCtx(t), url, nil, tc.opts...)
			require.NoError(t, err)
			defer c.Close()

			sub, err := c.Subscribe(context.Background(), "subscription { counter(to: 1) }", nil)
			require.NoError(t, err)

			n := awaitTerminal(t, sub, "a frame past the default ceiling was accepted; this connection has no read limit at all")
			assert.Zero(t, n, "a frame past the default ceiling must not be delivered")
			require.ErrorIs(t, sub.Err(), websocket.ErrReadLimit,
				"the subscription ended, but not because the frame was over the default ceiling")
		})
	}
}
