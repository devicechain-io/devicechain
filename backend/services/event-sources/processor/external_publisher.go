// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/rs/zerolog/log"
)

// EventPublisher is where a source's decode workers hand a decoded event on, and the barrier
// the source drains with before it lets go of what it was reading.
type EventPublisher interface {
	// Publish hands a decoded event on and reports the outcome to done exactly once (see
	// DecodedFunc). It may return before the event is stored.
	Publish(source, tenant string, event *model.UnresolvedEvent, payload interface{},
		captureSeq uint64, done func(error))
	// Settle blocks until every Publish that returned before it was called has had its done
	// run, or ctx ends (and then returns ctx's error). It publishes nothing and the
	// publisher stays usable: it is how an owned source proves, before it releases its
	// lease, that nothing of this term is still on its way to the stream.
	Settle(ctx context.Context) error
}

// InlinePublisher adapts a synchronous publish callback to an EventPublisher. It has
// nothing in flight once Publish returns, so its Settle has nothing to wait for.
func InlinePublisher(decoded func(string, string, *model.UnresolvedEvent, interface{}, uint64) error) EventPublisher {
	return inlinePublisher{Inline(decoded)}
}

type inlinePublisher struct{ DecodedFunc }

func (p inlinePublisher) Publish(source, tenant string, event *model.UnresolvedEvent, payload interface{},
	captureSeq uint64, done func(error)) {
	p.DecodedFunc(source, tenant, event, payload, captureSeq, done)
}

func (inlinePublisher) Settle(context.Context) error { return nil }

// PublishHooks are what an AsyncEventPublisher reports through. Both are required, and both
// are called from the writer's goroutine, so they must be quick.
type PublishHooks struct {
	// Backpressured counts one event refused because the ingest pipeline was applying
	// backpressure at the moment of the publish (the gate can close between a source's
	// admit check and the publish).
	Backpressured func(source string)
	// Failed counts one event that was NOT stored because its publish failed: the stream
	// did not acknowledge it, or the writer was closed under it. There is no retry on this
	// transport, so each one is an event lost, and err says why.
	Failed func(source string, err error)
}

var errNoPublishHooks = errors.New("an async event publisher needs both hooks: backpressured and failed")

// errNoWriter is what a Publish made while no writer is installed reports: before the
// service has built its broker components, or after the publisher was closed.
var errNoWriter = errors.New("the external MQTT source has no inbound-events writer: " +
	"it is not started yet, or has been closed")

// AsyncEventPublisher publishes an external MQTT source's decoded events through ONE
// messaging.AsyncWriter, so the source's decode workers do not each wait a PubAck apiece.
//
// 🔑 IT OUTLIVES THE SOURCE'S TERMS. An owned source builds a fresh MqttEventSource for every
// lease term, and a writer holds a reply subscription until the connection closes, so a
// writer built per term would leak one per handover. The publisher is built once per source
// by main, gets its writer once the broker components exist (SetWriter), is handed to every
// term, and is closed once, when the service stops. A term drains it with Settle.
//
// THE CONTRACT IT KEEPS is the sync publish's, plus a longer loss bound: an event is
// acknowledged to the external broker when it is queued for decode, never on the publish
// (see MqttEventSource.onMessage), and a failed publish is not retried (no dedup id exists
// to make a retry safe). What a pod that dies loses is therefore the decode queue, one
// message per decode worker, and now up to the writer's window of published messages
// awaiting their PubAck.
type AsyncEventPublisher struct {
	source string
	build  InboundMessageFunc
	hooks  PublishHooks

	mu     sync.RWMutex
	writer messaging.AsyncWriter
}

// NewAsyncEventPublisher builds a publisher for source. build makes the inbound-events
// message of a decoded event (see InboundMessageFunc).
func NewAsyncEventPublisher(source string, build InboundMessageFunc, hooks PublishHooks) (*AsyncEventPublisher, error) {
	if build == nil {
		return nil, errors.New("an async event publisher needs a message builder")
	}
	if hooks.Backpressured == nil || hooks.Failed == nil {
		return nil, errNoPublishHooks
	}
	return &AsyncEventPublisher{source: source, build: build, hooks: hooks}, nil
}

// NewExternalMqttWriter builds the AsyncWriter an external source publishes inbound-events
// through: window publishes in flight, refusing at a closed backpressure gate as the
// synchronous writer it replaces did. It lives here so that the writer main wires is the
// one a test in this package observes.
func NewExternalMqttWriter(nmgr *messaging.NatsManager, window int) (messaging.AsyncWriter, error) {
	return nmgr.NewAsyncWriter(streams.InboundEvents, window, true)
}

// SetWriter installs the writer. A writer already installed is drained and closed, so a
// service that builds its broker components again does not leak the earlier one.
func (p *AsyncEventPublisher) SetWriter(w messaging.AsyncWriter) {
	p.mu.Lock()
	old := p.writer
	p.writer = w
	p.mu.Unlock()
	closeWriter(old)
}

// Close drains and closes the writer. Call it once the last term has stopped (a Publish
// after it is reported failed, never dropped silently). It is safe to call twice.
func (p *AsyncEventPublisher) Close() { p.SetWriter(nil) }

func closeWriter(w messaging.AsyncWriter) {
	if w == nil {
		return
	}
	w.Draining()
	w.Close()
}

func (p *AsyncEventPublisher) current() messaging.AsyncWriter {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.writer
}

// Publish builds the event's message and submits it. It returns once the writer has room
// for it, which is before the PubAck: a full window holds the calling worker, which is the
// same backpressure the synchronous publish applied, one window later.
func (p *AsyncEventPublisher) Publish(source, tenant string, event *model.UnresolvedEvent, payload interface{},
	captureSeq uint64, done func(error)) {
	ctx, msg, ok := p.build(source, tenant, event, payload, captureSeq)
	if !ok {
		// A terminal, deliberate drop (see InboundMessageFunc): nothing to publish.
		done(nil)
		return
	}
	w := p.current()
	if w == nil {
		p.fail(source, tenant, errNoWriter)
		done(errNoWriter)
		return
	}
	w.Submit(ctx, []messaging.Message{msg}, func(err error) {
		if err != nil {
			if errors.Is(err, messaging.ErrStreamBackpressure) {
				p.hooks.Backpressured(source)
			} else {
				p.fail(source, tenant, err)
			}
		}
		done(err)
	})
}

// fail surfaces one lost event: counted and logged, so a publish that fails is never a
// silent drop. The writer logs the failure too, without the source and tenant.
func (p *AsyncEventPublisher) fail(source, tenant string, err error) {
	p.hooks.Failed(source, err)
	log.Error().Err(err).Str("source", source).Str("tenant", tenant).
		Msg("An event from an external MQTT source was not published and is lost: this transport does not retry.")
}

// Settle waits for every Publish made before it to have settled. With no writer installed
// nothing can be in flight, so it returns nil.
func (p *AsyncEventPublisher) Settle(ctx context.Context) error {
	w := p.current()
	if w == nil {
		return nil
	}
	return w.Settle(ctx)
}
