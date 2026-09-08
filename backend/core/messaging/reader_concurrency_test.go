// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// A durable reader hands out a FETCHED BATCH one message at a time from a plain
// slice, and counts empty fetches in a plain int. Two goroutines in ReadMessage at
// once therefore race both, which can deliver the same message twice or drop a whole
// batch — and both outcomes are silent, because nothing downstream can tell a
// duplicate delivery from a legitimate redelivery.
//
// So the precondition is enforced rather than merely stated: the second caller is
// refused with ErrConcurrentRead while the first is still in flight. Refusing rather
// than serializing is deliberate. A mutex would remove the race and leave the defect
// it is a symptom of — two goroutines splitting one durable's deliveries, each acking
// messages the other never saw — working exactly as before, with nothing to see.
func TestASecondConcurrentReaderIsRefused(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	reader, err := nmgr.NewReader(streams.InboundEvents)
	require.NoError(t, err)

	// Nothing is published, so the first call parks in Fetch for the whole window and
	// the two calls genuinely overlap. Its context is cancelled at the end of the test.
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()

	entered := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		close(entered)
		_, ferr := reader.ReadMessage(firstCtx)
		firstDone <- ferr
	}()
	<-entered
	// Let the first call get past the CAS and into its fetch loop. fetchTimeout is 1s,
	// so it is still inside ReadMessage well past this.
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = reader.ReadMessage(ctx)
	require.ErrorIs(t, err, ErrConcurrentRead,
		"a second goroutine entered ReadMessage while a first was in flight and was not refused, so the "+
			"pending batch and the timeout counter are being raced with no signal to anyone")
	require.NotErrorIs(t, err, io.EOF,
		"the refusal must not read as end-of-stream: every read loop treats io.EOF as shutdown, which "+
			"would turn a caller's bug into a silently stopped consumer")

	cancelFirst()
	require.ErrorIs(t, <-firstDone, io.EOF)
}

// The counterweight. A guard that refused every call — or one that latched on the
// first goroutine to touch the reader — would pass the test above. This pins that a
// single reader still works, and that the flag is cleared on the way out so the SAME
// reader can be read again, from a DIFFERENT goroutine.
//
// The second half is not hypothetical tidiness: event-processing drains these readers
// to head on its startup goroutine (catchUpFactProjections) and then hands the same
// readers to the consumer goroutines it launches after replay. Sequential use across
// goroutines is legal and must stay legal; only overlap is refused.
func TestASingleReaderStillDeliversAndStaysReusableAcrossGoroutines(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	reader, err := nmgr.NewReader(streams.InboundEvents)
	require.NoError(t, err)
	publishN(t, nmgr, streams.InboundEvents, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err, "a lone reader must be unaffected by the concurrency guard")
	require.NotEmpty(t, msg.Value)
	require.NoError(t, msg.Ack())

	// A different goroutine, after the first call returned. This must succeed.
	done := make(chan error, 1)
	go func() {
		second, serr := reader.ReadMessage(ctx)
		if serr == nil {
			serr = second.Ack()
		}
		done <- serr
	}()
	select {
	case serr := <-done:
		require.NoError(t, serr,
			"a sequential read from a different goroutine was refused, so the guard latched instead of "+
				"clearing — that would break the startup-drain-then-consume handoff every fact reader uses")
	case <-time.After(15 * time.Second):
		t.Fatal("a sequential read from a different goroutine never returned")
	}
}

// A hammered reader must never lose or duplicate a message because of the guard. Ten
// goroutines read the same reader; every one that is not refused must return a
// distinct message, and together they must drain exactly what was published.
//
// This is the assertion the -race detector cannot make on its own: a duplicated
// pending pop is a data race, but a DROPPED batch is a lost message that leaves no
// trace at all.
func TestConcurrentReadersNeverDuplicateOrDropAMessage(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	reader, err := nmgr.NewReader(streams.InboundEvents)
	require.NoError(t, err)

	const published = 24
	publishN(t, nmgr, streams.InboundEvents, published)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var mu sync.Mutex
	seen := map[uint64]int{}
	refused := 0

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				msg, rerr := reader.ReadMessage(ctx)
				if rerr != nil {
					mu.Lock()
					if errors.Is(rerr, ErrConcurrentRead) {
						refused++
						mu.Unlock()
						continue
					}
					mu.Unlock()
					return // io.EOF on the deadline once the stream is drained
				}
				_ = msg.Ack()
				mu.Lock()
				seen[msg.StreamSeq]++
				drained := len(seen) == published
				mu.Unlock()
				if drained {
					// Everything published is accounted for. Unwind the peers still
					// parked in Fetch rather than making them wait out the deadline.
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()

	require.Greater(t, refused, 0,
		"no call was ever refused across ten hammering goroutines, so this run never exercised the guard "+
			"and proves nothing about it")
	for seq, n := range seen {
		require.Equal(t, 1, n, "stream sequence %d was handed out %d times: the pending buffer was raced", seq, n)
	}
	require.Len(t, seen, published,
		"messages went missing: a fetched batch was overwritten by a concurrent fetch, which loses every "+
			"message still buffered in it")
}

// A re-bind must not restore the reply-inbox interest that UnbindTerm withdrew.
//
// UnbindTerm exists because the NATS connection is process-wide and buffers across a
// reconnect, so a pull request issued under the old term is served after a successor
// takes over unless the reply inbox is gone. bind() is reachable from the read loop's
// self-heal at the same time, so "unsubscribe, then re-attach a moment later" is
// reachable — and the mutex alone does not stop it, because ordering the two calls
// says nothing about which term they belong to. The unbound flag is what makes the
// withdrawal stick until a successor term explicitly re-opens the reader.
func TestARebindAfterTheTermEndedIsRefused(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	mr, err := nmgr.NewReader(streams.InboundEvents)
	require.NoError(t, err)
	reader := mr.(*natsReader)
	require.NotNil(t, reader.sub.Load(), "NewReader should have bound a subscription")

	require.NoError(t, reader.UnbindTerm())
	require.Nil(t, reader.sub.Load())

	// This is what the read loop's self-heal does when a fetch reports the consumer is
	// gone. It must not re-attach: the term is over.
	err = reader.bind()
	require.ErrorIs(t, err, errReaderUnbound,
		"a bind succeeded after UnbindTerm, so the self-heal can restore a departed term's reply-inbox "+
			"interest — the exact state UnbindTerm exists to remove")
	require.Nil(t, reader.sub.Load(),
		"a subscription was published after the term ended, so the old term's buffered pull requests have "+
			"somewhere to land again")
}

// The counterweight to the test above: a refusal that never lifts would strand every
// successor term with a reader that cannot attach. BindTerm is the one thing that
// re-opens it, and the reader must deliver again afterwards.
func TestBindTermReopensAReaderTheTermEndClosed(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	mr, err := nmgr.NewReader(streams.InboundEvents)
	require.NoError(t, err)
	reader := mr.(*natsReader)

	require.NoError(t, reader.UnbindTerm())

	require.NoError(t, reader.BindTerm(),
		"BindTerm must re-open a reader an earlier term closed, or no successor term can ever consume")
	require.NotNil(t, reader.sub.Load())

	publishN(t, nmgr, streams.InboundEvents, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	msg, err := mr.ReadMessage(ctx)
	require.NoError(t, err, "the re-bound reader must actually consume, not just hold a subscription pointer")
	require.NoError(t, msg.Ack())
}
