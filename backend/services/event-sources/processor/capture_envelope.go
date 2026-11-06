// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// Inbound envelopes on the capture hop (infrastructure.messaging.envelopes.inbound).
//
// With the setting on, the gateway source groups one tenant's decoded events into the members of
// an envelope in front of its ordered writer (messaging.Former: a tenant whose rate predicts
// company waits up to the linger, a quiet tenant never waits and its events keep the legacy
// shape), and publishes each group through messaging.RoundEngine, the settlement protocol
// device-management's resolved envelopes run too.
//
// # The id, and why a retry cannot double-publish
//
// A group is named by where its members came FROM, never by which members it holds:
//
//	C:<tenant>:<capture seq bucket<<28 | time bucket, b36>:<round b36>
//
// (messaging.RoundKey). The capture sequence and the broker's append time are the same on every
// redelivery of a capture message, so a pod that crashes after the inbound PubAck and before the
// capture ack, or a different pod handed the redelivery, derives the same id; the broker folds
// the republish into the original, the PubAck names it, and the engine reads it back and
// publishes only what it does not already hold. A regrouping across a fetch boundary lands in the
// same bucket and takes the next round. Every event of a tenant goes down this path, a quiet
// tenant's too (as a legacy-shaped message under the group id, never under a per-event id the
// walk could not see).
//
// What does not take it: the message-store source (its ids carry a store epoch) and a message
// with no capture sequence keep the per-event ids they always had.

// captureDedupKind is the id kind letter of a capture-hop group.
const captureDedupKind = "C"

// capturePos is where a captured message came from, carried from the gateway's read loop to the
// submitter in the dedup id token (see positionToken).
type capturePos struct {
	key         messaging.RoundKey
	index       uint32
	redelivered bool
	// ref names the message's entry in the source's beat table (0: none); beat tells the capture
	// broker the message is still being worked on, which a long settlement walk must do or the
	// message's AckWait expires and it is redelivered under the walk.
	ref  uint64
	beat func()
}

// positionToken is the dedup id the gateway hands the decode pipeline in envelope mode: the
// legacy per-event id, then '@', the append-time bucket in base 36, '!' for a redelivery, and '#'
// with the message's beat-table reference. The pipeline passes the string through untouched, and
// only the submitter reads it back (splitPosition), so the position needs no change to the decode
// worker's signature.
func positionToken(legacy string, key messaging.RoundKey, redelivered bool, ref uint64) string {
	tok := legacy + "@" + strconv.FormatUint(key.Epoch, 36)
	if redelivered {
		tok += "!"
	}
	return tok + "#" + strconv.FormatUint(ref, 36)
}

// splitPosition reads positionToken back. A string that is not one (envelope mode off, a
// message with no sequence) is returned as the legacy id with no position.
func splitPosition(tenant, token string) (legacy string, pos *capturePos) {
	at := strings.LastIndexByte(token, '@')
	if at < 0 {
		return token, nil
	}
	legacy, rest := token[:at], token[at+1:]
	var ref uint64
	if hash := strings.LastIndexByte(rest, '#'); hash >= 0 {
		ref, _ = strconv.ParseUint(rest[hash+1:], 36, 64)
		rest = rest[:hash]
	}
	redelivered := strings.HasSuffix(rest, "!")
	rest = strings.TrimSuffix(rest, "!")
	epoch, err := strconv.ParseUint(rest, 36, 64)
	dot := strings.LastIndexByte(legacy, '.')
	if err != nil || dot < 0 {
		return legacy, nil
	}
	seq, err := strconv.ParseUint(legacy[dot+1:], 36, 64)
	if err != nil || seq == 0 {
		return legacy, nil
	}
	return legacy, &capturePos{
		key:         messaging.RoundKey{Tenant: tenant, Bucket: seq / messaging.RoundBucketSize, Epoch: epoch},
		index:       uint32(seq % messaging.RoundBucketSize),
		redelivered: redelivered,
		ref:         ref,
	}
}

// CaptureEnvelopes is the settings the gateway source needs to publish envelopes.
type CaptureEnvelopes struct {
	Rounds  messaging.RoundPublisher
	Former  messaging.FormerConfig
	Metrics *messaging.EnvelopeMetrics
	// TenantGone is the deleted-tenant gate (see messaging.RoundEngineConfig.TenantGone); nil when
	// user-management is unconfigured.
	TenantGone func(tenant string) bool
}

// UseEnvelopes turns on inbound envelopes for this source. Call it before Start, beside
// SetWriter: the writer must report a PubAck (messaging.AckedWriter), or the source refuses to
// start rather than publish envelopes it cannot settle. The message-store source never takes it.
func (es *GatewayJetStreamSource) UseEnvelopes(c CaptureEnvelopes) error {
	if es.fromStore {
		return errors.New("the message-store source publishes per-event ids and does not take inbound envelopes")
	}
	if c.Rounds == nil {
		return errors.New("inbound envelopes need a round publisher")
	}
	if _, err := c.Former.Validated(); err != nil {
		return err
	}
	es.envelopes = &c
	return nil
}

// EnvelopesEnabled reports whether UseEnvelopes turned envelope mode on.
func (es *GatewayJetStreamSource) EnvelopesEnabled() bool { return es.envelopes != nil }

// captureEngine is one start's envelope machinery.
type captureEngine struct {
	former *messaging.Former
	engine *messaging.RoundEngine
	cancel context.CancelFunc
}

// newCaptureEngine builds the per-start machinery over writer, or refuses.
func (es *GatewayJetStreamSource) newCaptureEngine() (*captureEngine, error) {
	aw, ok := es.writer.(messaging.AckedWriter)
	if !ok {
		return nil, fmt.Errorf("infrastructure.messaging.envelopes.inbound is on, but the capture writer (%T) cannot report a PubAck", es.writer)
	}
	former, err := messaging.NewFormer(es.envelopes.Former, "capture", es.envelopes.Metrics)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	engine, err := messaging.NewRoundEngine(messaging.RoundEngineConfig{
		Kind: eventenvelope.KindUnresolved, DedupKind: captureDedupKind,
		Writer: aw, Rounds: es.envelopes.Rounds, Ctx: ctx, Metrics: es.envelopes.Metrics, TenantGone: es.envelopes.TenantGone,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	return &captureEngine{former: former, engine: engine, cancel: cancel}, nil
}

// envelopeSubmitLoop is the writer's ONE submitter in envelope mode: it takes the built messages,
// groups the positioned ones per tenant, and publishes each closed group. The linger's timer is
// a case of this loop's own select, so every publish still comes from this goroutine. It returns
// when publishes is closed and drained (every open group is flushed first), then closes done.
func (es *GatewayJetStreamSource) envelopeSubmitLoop(ce *captureEngine, publishes <-chan pendingPublish, done chan<- struct{}) {
	defer close(done)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	flush := func(batches []messaging.FormedBatch) {
		for _, b := range batches {
			es.publishGroup(ce, b)
		}
	}
	rearm := func() {
		if d, ok := ce.former.Deadline(); ok {
			timer.Reset(max(time.Until(d), 0))
		} else {
			timer.Stop()
		}
	}
	for {
		select {
		case p, ok := <-publishes:
			if !ok {
				flush(ce.former.Drain(time.Now()))
				return
			}
			if p.pos == nil || len(p.msg.Value) > eventenvelope.MaxRawBytes {
				// It cannot be grouped, and it must not overtake an earlier event of its tenant
				// that is still waiting for company in the open group: flush that group first.
				if tenant, ok := core.TenantFromContext(p.ctx); ok {
					flush(ce.former.DrainTenant(time.Now(), tenant))
				}
				es.writer.Publish(p.ctx, p.msg, p.done)
				continue
			}
			flush(ce.former.Add(time.Now(), p.pos.key.Tenant, messaging.FormedMember{Bytes: p.msg.Value, Token: p}))
		case <-timer.C:
			flush(ce.former.Expired(time.Now()))
		}
		rearm()
	}
}

// publishGroup hands a closed group to the engine, by bucket.
func (es *GatewayJetStreamSource) publishGroup(ce *captureEngine, b messaging.FormedBatch) {
	groups := map[messaging.RoundKey][]messaging.RoundMember{}
	var order []messaging.RoundKey
	var ctx context.Context
	for _, m := range b.Members {
		p := m.Token.(pendingPublish)
		if ctx == nil {
			ctx = p.ctx
		}
		if _, seen := groups[p.pos.key]; !seen {
			order = append(order, p.pos.key)
		}
		groups[p.pos.key] = append(groups[p.pos.key], messaging.RoundMember{
			Index:       p.pos.index,
			Bytes:       p.msg.Value,
			Key:         p.msg.Key,
			Correlation: p.msg.CorrelationID(),
			Redelivered: p.pos.redelivered,
			Settle:      p.done,
			Beat:        p.pos.beat,
		})
	}
	for _, k := range order {
		ce.engine.Publish(ctx, k, groups[k])
	}
}
