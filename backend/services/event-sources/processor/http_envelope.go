// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// Inbound envelopes on HTTP /events (infrastructure.messaging.envelopes.inbound): a GROUP COMMIT.
//
// A request's event joins its tenant's group (messaging.GroupCommit over the same predictive
// Former the capture hop uses). The handler returns 202 only after the PubAck of the envelope
// that holds its event, so what the client was told is exactly as durable as before. A quiet
// tenant's group is its own event, flushed on the request's own goroutine with no wait: its
// latency, its message shape and its (absent) dedup id are today's. A busy tenant's request waits
// at most the linger for company and shares one PubAck with it.
//
// # Dedup
//
// HTTP has no source sequence, so there is nothing a redelivery could be derived from, and no
// redelivery either: a failed request is answered 503 and the CLIENT retries, which is a new event
// as it always was (event-management's content-derived event id folds it later). The envelope
// does carry an id,
//
//	H:<tenant>:<random 64 bits, b36>:0
//
// but only so that the writer's own in-process retry of the SAME envelope (a publish whose
// outcome was unknown) folds into the first; no other publish can share it. Because the whole
// envelope is one publish, every member's answer is the envelope's: all 202, or all refused. A
// refusal is certainly-not-stored (backpressure, a Retry-After) or unknown (a bare 503) for every
// member alike, exactly as the single-event answer was.

// httpDedupKind is the id kind letter of an HTTP group.
const httpDedupKind = "H"

type httpPending struct {
	ctx    context.Context
	msg    messaging.Message
	source string
	done   chan error
}

// HTTPEnvelopes is the group commit of the HTTP ingest path.
type HTTPEnvelopes struct {
	gc      *messaging.GroupCommit
	publish func(ctx context.Context, source string, msg messaging.Message) error
	// refused is told, once per REQUEST and with that request's own source, that its publish was
	// refused under backpressure (nothing was stored). It is never called for any other failure.
	refused func(source string)
}

// NewHTTPEnvelopes builds the group commit. publish writes one message to inbound-events and
// returns only after its PubAck; it does NOT count a backpressure refusal (refused does, once per
// request), because one refused envelope refuses every request that joined it.
func NewHTTPEnvelopes(cfg messaging.FormerConfig, metrics *messaging.EnvelopeMetrics,
	publish func(ctx context.Context, source string, msg messaging.Message) error,
	refused func(source string)) (*HTTPEnvelopes, error) {
	former, err := messaging.NewFormer(cfg, "http", metrics)
	if err != nil {
		return nil, err
	}
	if refused == nil {
		refused = func(string) {}
	}
	h := &HTTPEnvelopes{publish: publish, refused: refused}
	h.gc = messaging.NewGroupCommit(former, h.flush)
	return h, nil
}

// Stop flushes every open group and refuses further requests.
func (h *HTTPEnvelopes) Stop() { h.gc.Stop() }

// Publish joins msg (built for tenant by inboundEventMessage) to its tenant's group and returns
// the group's outcome: nil only after the PubAck of the message that holds the event.
func (h *HTTPEnvelopes) Publish(ctx context.Context, source, tenant string, msg messaging.Message) error {
	p := &httpPending{ctx: ctx, msg: msg, source: source, done: make(chan error, 1)}
	if len(msg.Value) > eventenvelope.MaxRawBytes {
		// Larger than an envelope can hold: published alone, as it always was.
		return h.single(p)
	}
	if err := h.gc.Add(tenant, messaging.FormedMember{Bytes: msg.Value, Token: p}); err != nil {
		return err
	}
	return <-p.done
}

// flush publishes one closed group and answers each of its requests.
func (h *HTTPEnvelopes) flush(b messaging.FormedBatch) {
	pend := make([]*httpPending, len(b.Members))
	for i, m := range b.Members {
		pend[i] = m.Token.(*httpPending)
	}
	answer := func(err error) {
		for _, p := range pend {
			if errors.Is(err, messaging.ErrStreamBackpressure) {
				h.refused(p.source)
			}
			p.done <- err
		}
	}
	if len(pend) == 1 {
		// A group of one is the legacy message, byte for byte what the path always published.
		pend[0].done <- h.single(pend[0])
		return
	}
	events := make([]eventenvelope.Event, len(pend))
	for i, p := range pend {
		events[i] = eventenvelope.Event{Index: uint32(i), Bytes: p.msg.Value}
	}
	body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindUnresolved, Tenant: b.Tenant}, events)
	if err != nil {
		// The encoder refuses what it cannot hold (a token that is not UTF-8): publish the events
		// singly, each answered by its own publish, as they were before envelopes.
		log.Debug().Err(err).Str("tenant", b.Tenant).Msg("Could not envelope an HTTP group; publishing its events singly")
		for _, p := range pend {
			p.done <- h.single(p)
		}
		return
	}
	msg := messaging.Message{
		Value:   body,
		DedupID: messaging.EnvelopeDedupID(httpDedupKind, b.Tenant, randomSource(), 0),
		Headers: map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue},
	}
	if cid := pend[0].msg.CorrelationID(); cid != "" {
		msg = msg.WithCorrelationID(cid)
	}
	answer(h.publish(pend[0].ctx, pend[0].source, msg))
}

// single publishes one request's event alone and counts its refusal.
func (h *HTTPEnvelopes) single(p *httpPending) error {
	err := h.publish(p.ctx, p.source, p.msg)
	if errors.Is(err, messaging.ErrStreamBackpressure) {
		h.refused(p.source)
	}
	return err
}

// randomSource is the 64 random bits that make an HTTP group's id its own.
func randomSource() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("event-sources: no randomness for an envelope id: " + err.Error())
	}
	return binary.BigEndian.Uint64(b[:])
}
