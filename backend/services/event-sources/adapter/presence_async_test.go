// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingAsync records what was submitted and completes each call at once with err.
type capturingAsync struct {
	mu   sync.Mutex
	ctxs []context.Context
	msgs []messaging.Message
	err  error
}

func (c *capturingAsync) Submit(ctx context.Context, msgs []messaging.Message, done func(error)) {
	c.mu.Lock()
	c.ctxs = append(c.ctxs, ctx)
	c.msgs = append(c.msgs, msgs...)
	err := c.err
	c.mu.Unlock()
	done(err)
}

func fixedClock() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }

// The asynchronous presence emit puts the SAME message on the wire as the synchronous one:
// payload, key, dedup id and the gate bypass, under the same tenant. Two hand-written copies
// of the marshalling are how a new field goes missing on one path.
func TestEmitPresenceAsyncBuildsTheSynchronousMessage(t *testing.T) {
	ev := PresenceEvent{Connected: true, Reason: "broker-connect", SessionId: 42,
		OccurredAt: time.Date(2026, 10, 15, 11, 0, 0, 0, time.UTC)}

	plain := &fakeWriter{}
	require.NoError(t, NewEmitter(plain, fixedClock, "bp", true).
		EmitPresence(context.Background(), "acme", "mqtt1", "dev-1", ev))

	async := &capturingAsync{}
	emitter := NewEmitter(&fakeWriter{err: errors.New("the sync writer must not be used")}, fixedClock, "bp", true).
		WithAsyncWriter(async)
	var got error = errors.New("done never ran")
	emitter.EmitPresenceAsync(context.Background(), "acme", "mqtt1", "dev-1", ev, func(err error) { got = err })
	require.NoError(t, got)

	require.Len(t, plain.msgs, 1)
	require.Len(t, async.msgs, 1)
	assert.Equal(t, plain.msgs[0], async.msgs[0])
	assert.True(t, async.msgs[0].BypassBackpressure, "a presence transition bypasses the backpressure gate")
	assert.NotEmpty(t, async.msgs[0].DedupID)
	tenant, ok := core.TenantFromContext(async.ctxs[0])
	require.True(t, ok)
	assert.Equal(t, "acme", tenant, "the submit context must carry the tenant that scopes the subject")
}

// The writer's failure is the call's failure, passed to done untouched.
func TestEmitPresenceAsyncReportsTheWritersError(t *testing.T) {
	boom := errors.New("no PubAck")
	emitter := NewEmitter(&fakeWriter{}, fixedClock, "bp", true).WithAsyncWriter(&capturingAsync{err: boom})
	var got error
	emitter.EmitPresenceAsync(context.Background(), "acme", "mqtt1", "dev-1",
		PresenceEvent{Connected: true, SessionId: 1, OccurredAt: fixedClock()}, func(err error) { got = err })
	assert.ErrorIs(t, got, boom)
}

// An emitter with no async writer reports ErrNoAsyncWriter to done: the transition is neither
// written synchronously behind the caller's back nor silently dropped.
func TestEmitPresenceAsyncWithoutAWriterFailsLoudly(t *testing.T) {
	plain := &fakeWriter{}
	var got error
	NewEmitter(plain, fixedClock, "bp", true).EmitPresenceAsync(context.Background(), "acme", "mqtt1", "dev-1",
		PresenceEvent{Connected: true, SessionId: 1, OccurredAt: fixedClock()}, func(err error) { got = err })
	assert.ErrorIs(t, got, ErrNoAsyncWriter)
	assert.Empty(t, plain.msgs, "no silent fall back to the synchronous write")
}

// 🔑 THE DEDUP ID IS A PURE FUNCTION OF CONTENT, so concurrency cannot make two different
// transitions share one. 64 goroutines emit 50 distinct (device, session, state) transitions
// each, all at once: every id is unique, and an identical transition emitted again gets the
// same id (which is what lets the stream collapse a redelivery).
func TestPresenceDedupIdsAreUniqueUnderConcurrency(t *testing.T) {
	async := &capturingAsync{}
	emitter := NewEmitter(&fakeWriter{}, fixedClock, "bp", true).WithAsyncWriter(async)

	const goroutines, per = 64, 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				connected := i%2 == 0
				emitter.EmitPresenceAsync(context.Background(), "acme", "mqtt1", fmt.Sprintf("dev-%d", g),
					PresenceEvent{Connected: connected, SessionId: uint64(1000 + i/2), OccurredAt: fixedClock()}, func(error) {})
			}
		}(g)
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, m := range async.msgs {
		require.False(t, seen[m.DedupID], "two different transitions share dedup id %s", m.DedupID)
		seen[m.DedupID] = true
	}
	require.Len(t, seen, goroutines*per)

	// The same transition again is the SAME id.
	before := len(async.msgs)
	emitter.EmitPresenceAsync(context.Background(), "acme", "mqtt1", "dev-0",
		PresenceEvent{Connected: true, SessionId: 1000, OccurredAt: fixedClock()}, func(error) {})
	assert.True(t, seen[async.msgs[before].DedupID], "an identical transition must reuse its dedup id")
}
