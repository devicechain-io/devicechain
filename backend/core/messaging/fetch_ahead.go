// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	nats "github.com/nats-io/nats.go"
)

// A compile-time check that the widest pull the configuration allows is the widest range
// the gap fill reads one request per sequence: the range a lost pull leaves behind is then
// always one the cheap path reads. Either side moving alone fails the build.
const _ = uint(rangeDirectMax-config.MaxFetchBatch) + uint(config.MaxFetchBatch-rangeDirectMax)

// FETCH-AHEAD: ONE REQUEST IN FLIGHT WHILE THE CALLER WORKS THROUGH THE BATCH IN HAND.
//
// A plain reader pulls a batch, hands it out one message at a time, and only then asks for
// the next, so every batch costs a broker round trip on top of the work. With fetch-ahead
// the next pull is issued as soon as a batch arrives and its result is picked up when the
// buffer runs dry, so the round trip overlaps the work instead of following it.
//
// WHAT RUNS OFF THE READ GOROUTINE IS ONE CALL: sub.Fetch, under a context that times it
// out and that a drop can cancel. Its result, messages or error, is left in a channel,
// and EVERYTHING that decides what the result means (a partial batch, a reconnect, the
// liveness probe, a re-bind, the error the loop is handed, the evidence the pacer reads)
// still runs on the read goroutine in ReadMessage, on the same code a synchronous fetch
// uses. There is never more than one request in flight, and its result is consumed before
// another is issued, so messages arrive in the order a synchronous reader would have seen.
//
// WHEN A FUTURE IS STARTED (startAhead): the reader fetches ahead, is not a capacity
// reader, holds its term, its downstream is not refusing, and the last batches took less
// than the hold budget to hand out. Only after a batch that DELIVERED something: after an
// error or an empty fetch the next call fetches synchronously, so error pacing, the
// liveness probe and the idle long-poll behave exactly as they do without it.
//
// THE COST, AND ITS BOUNDS.
//   - Messages in flight: a future adds at most one batch to what the reader holds. With
//     one request in flight, a dropped connection loses the deliveries of at most ONE pull,
//     so at most one batch (a pull of which nothing arrived is an error and the broker has
//     still counted the batch as delivered): a range the live gap fill reads back, and the
//     configuration caps the batch at the width it reads cheaply (config.MaxFetchBatch).
//   - Age: the broker starts a message's acknowledgement window when it delivers it, so a
//     prefetched batch ages while the batch before it is handed out. The hold budget bounds
//     that to about one budget (a fraction of the window, validated against it); a consumer
//     slower than the budget falls back to one pull at a time on its own, and a result that
//     nevertheless sat longer than half the window is dropped instead of handed out.
//   - Shutdown: a future is left to finish by itself. It ends when its subscription is
//     released or its fetch times out, at most fetchTimeout, and its messages are never
//     acknowledged, so the broker redelivers them. Only a term change (BindTerm), a
//     re-bind and ReaderWithReleaseOnPark (which Naks them) act on it sooner.
//
// 🔴 AN EOF DOES NOT DROP THE FUTURE, AND THAT IS DELIBERATE. ReadMessage returns EOF when
// the CALLER's context ends, and a caller may poll with a short deadline and read again
// (event-processing's fact catch-up does). Dropping the future at each EOF would leave its
// batch delivered and unacked for a whole acknowledgement window, and a catch-up that
// asks the broker what is left would then find nothing undelivered and conclude it had
// reached the head. A plain reader keeps its fetch buffer across an EOF for the same reason.

// fetchFuture is the one pull request a reader has in flight.
type fetchFuture struct {
	sub    *nats.Subscription
	gen    uint64
	cancel context.CancelFunc
	// done receives exactly one result and is buffered, so the goroutine ends the moment
	// its Fetch returns whether or not anyone is still waiting for it.
	done chan fetchResult
}

type fetchResult struct {
	msgs []*nats.Msg
	err  error
	at   time.Time
}

// configureFetch resolves the reader's pull shape from the instance configuration. A
// manager assembled as a struct literal has no Microservice, and reads the defaults.
func (r *natsReader) configureFetch() {
	var cfg config.NatsFetchConfiguration
	if r.nmgr != nil && r.nmgr.Microservice != nil {
		cfg = r.nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Fetch
	}
	r.fetchSize = cfg.BatchSize()
	// A capacity reader asks for its free slots and not for more; it never fetches ahead.
	r.ahead = cfg.Ahead && r.slots == 0
	r.aheadBudget = cfg.HoldBudget(r.nmgr.ackWait())
}

// startAhead issues the next pull before the first message of a freshly fetched batch is
// handed out, when every condition for doing so holds (see above).
func (r *natsReader) startAhead() {
	if !r.ahead || r.capacity != nil || r.future != nil {
		return
	}
	if r.held != nil && !r.held() {
		return
	}
	if r.drainEWMA >= r.aheadBudget {
		return
	}
	// A hop that forwards into a stream applying backpressure would fetch messages it can
	// only fail to publish: the same gate ReadMessage parks on before a synchronous fetch.
	if r.downstream != "" && r.nmgr.Backpressure(r.downstream) != nil {
		return
	}
	sub := r.sub.Load()
	if sub == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &fetchFuture{sub: sub, gen: r.aheadGen.Load(), cancel: cancel, done: make(chan fetchResult, 1)}
	r.future = f
	r.aheadStarts.Add(1)
	batch := r.batchSize()
	go func() {
		fctx, fcancel := context.WithTimeout(ctx, fetchTimeout)
		defer fcancel()
		msgs, err := sub.Fetch(batch, nats.Context(fctx))
		// A synchronous fetch reports its own deadline as ErrTimeout; a context deadline
		// reports DeadlineExceeded. Say the same thing, so one piece of code reads both.
		// A cancellation (a drop) stays what it is: nobody is waiting for that result.
		if err != nil && errors.Is(err, context.DeadlineExceeded) {
			err = nats.ErrTimeout
		}
		f.done <- fetchResult{msgs: msgs, err: err, at: time.Now()}
	}()
}

// takeAhead returns the in-flight request's result in place of a synchronous fetch, and
// ok=false when the caller should fetch synchronously instead: there is no future, or
// the one there is stale (a re-bind or a term change replaced the subscription it
// pulled on) or its messages have waited too long to hand out. A result that is not used
// is dropped unacknowledged, which the broker redelivers.
//
// It waits for the result without watching the caller's context, exactly as a
// synchronous Fetch does not: both end within fetchTimeout, and the loop checks the
// context before it fetches again.
func (r *natsReader) takeAhead(sub *nats.Subscription) (msgs []*nats.Msg, err error, ok bool) {
	f := r.future
	if f == nil {
		return nil, nil, false
	}
	r.future = nil
	res := <-f.done
	f.cancel()
	if f.sub != sub || f.gen != r.aheadGen.Load() {
		return nil, nil, false
	}
	if len(res.msgs) > 0 && time.Since(res.at) > r.nmgr.ackWait()/2 {
		return nil, nil, false
	}
	return res.msgs, res.err, true
}

// dropAhead abandons the request in flight: it is cancelled, waited for (the cancellation
// ends the fetch at once, and the wait is bounded regardless), and whatever it had
// delivered is left unacknowledged for the broker to redeliver, or Nak'd when release is
// set so the redelivery is immediate.
func (r *natsReader) dropAhead(release bool) {
	f := r.future
	if f == nil {
		return
	}
	r.future = nil
	f.cancel()
	select {
	case res := <-f.done:
		if release {
			for _, nm := range res.msgs {
				_ = nm.Nak()
			}
		}
	case <-time.After(fetchTimeout + time.Second):
		// The goroutine ends by itself when its fetch does; its result has nowhere to go.
	}
}

// noteDrained folds the time the last batch took to hand out into the moving average that
// gates fetching ahead. It runs when the buffer is found empty, so the interval runs from
// the batch's first hand-out to the call that asks for more: the work done on its last
// message counts, which is what a slow consumer is made of.
func (r *natsReader) noteDrained() {
	if r.drainStart.IsZero() {
		return
	}
	d := time.Since(r.drainStart)
	r.drainStart = time.Time{}
	if r.drainEWMA == 0 {
		r.drainEWMA = d
		return
	}
	r.drainEWMA = (r.drainEWMA + d) / 2
}

// batchSize is how many messages one pull asks for. A reader assembled without NewReader
// (the max-delivery recorder builds its own) never ran configureFetch and has no setting:
// it pulls the default batch, one request at a time.
func (r *natsReader) batchSize() int {
	if r.fetchSize <= 0 {
		return fetchBatch
	}
	return r.fetchSize
}
