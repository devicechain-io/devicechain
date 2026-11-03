// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"sync"
	"time"
)

// Collector serializes COLLECTION across the writers of one pod: the one place a batching
// writer waits for messages is held by one writer at a time.
//
// 🔴 WHY IT EXISTS. Writers that each linger on one shared channel split what arrives ten
// ways, so each batch holds about rate x (linger + commit) / writers and the linger buys
// almost nothing. With a Collector, one writer at a time holds the token while it RECEIVES
// a batch; the rest wait for the token instead of taking its messages, so that batch fills
// at the full rate. The token is held only around the raw receive. Admission (decoding,
// tenant) and the commit both happen after it is released, so neither is serialized and
// writers still decode and commit in parallel.
//
// A nil *Collector is no token at all, which is what a writer assembled by struct literal
// (most tests) has: collection then behaves exactly as CollectBatch does.
type Collector struct {
	mu sync.Mutex
}

// CollectRaw is how a batching writer receives its next batch of raw messages from the
// channel its read loop hands messages to. It holds c's token for the whole receive and
// releases it on every way out, including the channel closing.
//
// It blocks for one message from in, then takes whatever else is already waiting — and,
// with a linger above zero, whatever arrives within it — until the batch holds max messages.
// Messages are counted RAW: none is admitted or decoded here, so a message admission later
// refuses still took its place in the batch, which ends up smaller than its raw count.
// received, when non-nil, runs for each message as it is taken; it is where a writer starts
// the clock on its RED metrics, so the linger and the wait for the token are counted in a
// message's duration exactly as before.
//
// The batch is appended to buf[:0] and so reuses buf's storage when it holds max messages;
// the caller owns buf and must not keep it past its next call. open is false once in is
// closed; the batch returned beside it is still to be written, which lets a writer drain at
// shutdown. max below 1 is treated as 1.
//
// A batch that reached max did so without waiting out the linger. A caller can see this
// from len(batch) == max, and a share of such batches near 100% means a backlog.
func CollectRaw(c *Collector, in <-chan Message, buf []Message, max int, linger time.Duration,
	received func(Message)) (batch []Message, open bool) {
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
	}
	batch = buf[:0]
	open = fillBatch(in, max, linger, func(msg Message) bool {
		if received != nil {
			received(msg)
		}
		batch = append(batch, msg)
		return true
	})
	return batch, open
}

// CollectBatch is how a batching writer fills its next transaction from the channel its
// read loop hands messages to, with no collector: it blocks for one message from in, then
// takes whatever else is already waiting — and, with a linger above zero, whatever arrives
// within it — until the batch holds max ADMITTED items.
//
// admit runs the checks a message must pass before it can be written, and disposes of a
// message it refuses (acks it, records its result); a refused message is then in no batch
// and does not count toward max. open is false once in is closed; the batch returned beside
// it is still to be written, which is what lets a writer drain its channel at shutdown. max
// below 1 is treated as 1.
//
// Without a linger it adds no latency: under light load a writer finds one message and
// writes it alone, and batches grow by themselves once messages arrive faster than single
// transactions keep up with.
func CollectBatch[T any](in <-chan Message, max int, linger time.Duration,
	admit func(Message) (T, bool)) (batch []T, open bool) {
	open = fillBatch(in, max, linger, func(msg Message) bool {
		p, ok := admit(msg)
		if ok {
			if batch == nil {
				batch = make([]T, 0, batchLimit(max))
			}
			batch = append(batch, p)
		}
		return ok
	})
	return batch, open
}

func batchLimit(max int) int {
	if max < 1 {
		return 1
	}
	return max
}

// fillBatch is the ONE definition of the linger rules, shared by CollectRaw and CollectBatch. It
// blocks for a first message, then keeps taking messages until take has counted max of them
// (take reports whether the message counts toward max), or nothing more is waiting and the
// linger has run out. open is false once in is closed.
func fillBatch(in <-chan Message, max int, linger time.Duration, take func(Message) bool) (open bool) {
	msg, ok := <-in
	if !ok {
		return false
	}
	var first time.Time
	if linger > 0 {
		first = time.Now()
	}
	limit := batchLimit(max)
	n := 0
	if take(msg) {
		n++
	}
	// The timer is made only when the writer is about to wait: a batch assembled from a
	// backlog never needs one.
	var timer *time.Timer
	var lingerC <-chan time.Time
	lingerLeft := linger > 0
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for n < limit {
		select {
		case msg, ok := <-in:
			if !ok {
				return false
			}
			if take(msg) {
				n++
			}
			continue
		default:
		}
		// Nothing is waiting. Without a linger — or once it has run out — write what
		// there is; a batch of one is simply a message written on its own.
		if !lingerLeft {
			return true
		}
		if timer == nil {
			// The linger runs from the first message, not from when the writer first had
			// to wait.
			left := linger - time.Since(first)
			if left <= 0 {
				return true
			}
			timer = time.NewTimer(left)
			lingerC = timer.C
		}
		select {
		case msg, ok := <-in:
			if !ok {
				return false
			}
			if take(msg) {
				n++
			}
		case <-lingerC:
			lingerLeft, lingerC = false, nil
		}
	}
	return true
}
