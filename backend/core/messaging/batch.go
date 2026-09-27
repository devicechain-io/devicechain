// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import "time"

// CollectBatch is how a batching writer fills its next transaction from the channel its
// read loop hands messages to. It blocks for one message from in, then takes whatever else
// is already waiting — and, with a linger above zero, whatever arrives within it — until
// the batch holds max ADMITTED items.
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
	msg, ok := <-in
	if !ok {
		return nil, false
	}
	limit := max
	if limit < 1 {
		limit = 1
	}
	batch = make([]T, 0, limit)
	if p, ok := admit(msg); ok {
		batch = append(batch, p)
	}
	var lingerC <-chan time.Time
	if linger > 0 {
		timer := time.NewTimer(linger)
		defer timer.Stop()
		lingerC = timer.C
	}
	for len(batch) < limit {
		select {
		case msg, ok := <-in:
			if !ok {
				return batch, false
			}
			if p, ok := admit(msg); ok {
				batch = append(batch, p)
			}
			continue
		default:
		}
		// Nothing is waiting. Without a linger — or once it has run out — write what
		// there is; a batch of one is simply a message written on its own.
		if lingerC == nil {
			return batch, true
		}
		select {
		case msg, ok := <-in:
			if !ok {
				return batch, false
			}
			if p, ok := admit(msg); ok {
				batch = append(batch, p)
			}
		case <-lingerC:
			lingerC = nil
		}
	}
	return batch, true
}
