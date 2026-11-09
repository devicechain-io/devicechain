// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// Result labels for lpwan_ack_reports_total. A fixed enum: never a tenant, device or queue item.
const (
	AckAcknowledged    = "acknowledged"      // the device acknowledged: the command is SUCCESSFUL
	AckTimedOut        = "timed_out"         // the network server gave up waiting: the command is TIMEOUT
	AckTransmitted     = "transmitted"       // a gateway accepted the frame; recorded, the command keeps waiting
	AckTxUntracked     = "tx_untracked"      // a txack for a downlink not awaiting an acknowledgement (every unconfirmed one)
	AckUnmatched       = "unmatched"         // an ack matching no pending downlink: dropped, never guessed
	AckRetried         = "retried"           // the first sight of an unmatched ack, left to redeliver once
	AckMismatch        = "mismatch"          // an ack whose integration or device is not the downlink's: dropped
	AckPoison          = "poison"            // undecodable or malformed
	AckTenantGone      = "tenant_gone"       // the tenant was deleted
	AckStoreFailed     = "store_failed"      // the pending store could not be read or written; left unacked
	AckResponseFail    = "response_failed"   // the answer could not be published; left unacked
	AckDeadline        = "deadline"          // a downlink's wait ran out: the command is answered unanswered
	AckDeadlineFault   = "deadline_fault"    // an overdue downlink could not be answered this pass
	AckContended       = "contended"         // another settler holds the record; this report is left to redeliver
	AckRedelivered     = "redelivered"       // a report for a downlink this replica just settled: its message ack was lost, nothing to do
	AckFailed          = "failed"            // the network server reported it could not send the downlink: the command is NOT_DELIVERED
	AckFailedMaybeSent = "failed_maybe_sent" // a failed report that cannot show the frame was never transmitted: the command is TIMEOUT
)

// AckResults is every ack result label, so a metric can create its children at 0.
var AckResults = []string{
	AckAcknowledged, AckTimedOut, AckTransmitted, AckTxUntracked, AckUnmatched, AckRetried, AckMismatch,
	AckPoison, AckTenantGone, AckStoreFailed, AckResponseFail, AckDeadline, AckDeadlineFault, AckContended, AckRedelivered, AckFailed, AckFailedMaybeSent,
}

// AckMetrics counts ack feed results; a nil AckMetrics counts nothing.
type AckMetrics interface{ CountAck(result string) }

// AckFeedOptions are the feed's tuning.
type AckFeedOptions struct {
	// Workers is the number of concurrent reports. At least 1.
	Workers int
	// TenantDeleted answers the lifecycle question for a tenant; nil admits every tenant.
	TenantDeleted func(string) bool
	// ReadPacer paces retries after a failing read. Required.
	ReadPacer *core.ReadPacer
	// SweepEvery is how often overdue pending downlinks are answered. Defaults to 30 s.
	SweepEvery time.Duration
	// SweepBatch bounds the overdue downlinks answered in one pass. Defaults to 100.
	SweepBatch int
	// Now is the clock (tests).
	Now func() time.Time
	// ResponseAttempts and ResponseBackoff bound the local retry of the answer publish.
	ResponseAttempts int
	ResponseBackoff  time.Duration
	// SettleBudget bounds the whole answer once a settler holds a record's lease. Defaults to a third
	// of SettleLease, and must stay well under it.
	SettleBudget time.Duration
}

// settleBudget is the most time a settler spends answering a command after it took the record's lease.
// It is well under SettleLease, so a settler that is slow (a broker that will not take the publish)
// has stopped before another can take the lease, and two settlers never publish different answers.
const settleBudget = SettleLease / 3

// settleMargin is the slice of the lease a settler leaves unused, so its answer is on the wire before
// the lease can be taken by another.
const settleMargin = SettleLease / 6

// recentSettledTTL and recentSettledMax bound the memory of downlinks this replica has just settled.
const (
	recentSettledTTL = 10 * time.Minute
	recentSettledMax = 4096
)

// AckFeed settles confirmed downlinks from the network server's reports. A report reaches it on the
// lpwan-downlink-events stream, written by event-sources only after the sender's bearer token proved
// the integration and the report's device resolved under it.
//
// The mapping onto the command's delivery state, from network evidence only:
//
//   - ack, acknowledged=true: the device acknowledged the frame. SUCCESSFUL / SUCCEEDED.
//   - ack, acknowledged=false: ChirpStack's own timeout of the confirmation (an AckEvent is sent
//     "when a confirmation on a confirmed downlink has been received -or- when the downlink timed
//     out"). The device may have heard the frame and its acknowledgement been lost, so this is
//     TIMEOUT / UNKNOWN, never FAILED / NOT_DELIVERED, which would offer a retry as safe.
//   - txack: a gateway accepted the frame. Evidence it left, not that the device heard it; it is
//     recorded on the pending record and the command keeps waiting.
//   - nothing by the record's deadline: TIMEOUT / UNKNOWN, answered by the sweep.
//
// A report is matched ONLY by the network server's queue item id, inside its own tenant, and only
// when its integration and DevEUI are the ones the downlink was sent through and to. Anything else
// is counted and dropped, with a warning for an ack: never matched by guess.
type AckFeed struct {
	reader    messaging.MessageReader
	responses cmddownlink.ResponseWriter
	pending   PendingStore
	metrics   AckMetrics
	opts      AckFeedOptions

	ctx      context.Context
	cancel   context.CancelFunc
	messages chan messaging.Message
	readerWG sync.WaitGroup
	workerWG sync.WaitGroup
	sweepWG  sync.WaitGroup
	stopOnce sync.Once

	// settled remembers the downlinks this replica answered a moment ago, so a report redelivered
	// because its message ack was lost is recognised and not counted as an unmatched ack. It is a
	// courtesy of this replica only: another replica may receive the redelivery and count it
	// unmatched, which is why the documentation tells operators an occasional unmatched is not a fault.
	settledMu sync.Mutex
	settled   map[string]time.Time
}

// NewAckFeed builds the feed. Every collaborator except metrics is required.
func NewAckFeed(reader messaging.MessageReader, responses cmddownlink.ResponseWriter, pending PendingStore,
	metrics AckMetrics, opts AckFeedOptions) *AckFeed {
	if reader == nil || responses == nil || pending == nil || opts.ReadPacer == nil {
		panic("lpwan: NewAckFeed needs a reader, response writer, pending store and read pacer")
	}
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	if opts.SweepEvery <= 0 {
		opts.SweepEvery = 30 * time.Second
	}
	if opts.SweepBatch < 1 {
		opts.SweepBatch = 100
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ResponseAttempts < 1 {
		opts.ResponseAttempts = 3
	}
	if opts.ResponseBackoff <= 0 {
		opts.ResponseBackoff = 200 * time.Millisecond
	}
	if opts.SettleBudget <= 0 || opts.SettleBudget > SettleLease/2 {
		opts.SettleBudget = settleBudget
	}
	return &AckFeed{reader: reader, responses: responses, pending: pending, metrics: metrics, opts: opts,
		ctx: context.Background(), settled: map[string]time.Time{}}
}

func (f *AckFeed) count(result string) {
	if f.metrics != nil {
		f.metrics.CountAck(result)
	}
}

// Start launches the workers, the read loop and the deadline sweep.
func (f *AckFeed) Start() {
	f.ctx, f.cancel = context.WithCancel(context.Background())
	f.messages = make(chan messaging.Message, f.opts.Workers)
	for i := 0; i < f.opts.Workers; i++ {
		f.workerWG.Add(1)
		go func() {
			defer f.workerWG.Done()
			for msg := range f.messages {
				messaging.Process(msg, func(m messaging.Message) { f.handle(f.ctx, m) })
			}
		}()
	}
	f.readerWG.Add(1)
	go func() {
		defer f.readerWG.Done()
		messaging.RunConsumer(f.ctx, f.reader, f.opts.ReadPacer, func(msg messaging.Message) bool {
			select {
			case f.messages <- msg:
				return true
			case <-f.ctx.Done():
				return false
			}
		})
	}()
	f.sweepWG.Add(1)
	go func() {
		defer f.sweepWG.Done()
		t := time.NewTicker(f.opts.SweepEvery)
		defer t.Stop()
		for {
			select {
			case <-f.ctx.Done():
				return
			case <-t.C:
				f.Sweep(f.ctx)
			}
		}
	}()
}

// Stop ends the loops and lets in-flight reports finish. It is idempotent.
func (f *AckFeed) Stop() {
	f.stopOnce.Do(func() {
		if f.cancel != nil {
			f.cancel()
		}
		f.readerWG.Wait()
		f.sweepWG.Wait()
		if f.messages != nil {
			close(f.messages)
		}
		f.workerWG.Wait()
	})
}

// handle settles one report. A report that cannot be settled NOW (a store or publish fault) is left
// unacked and redelivers; every other disposition acks.
func (f *AckFeed) handle(ctx context.Context, msg messaging.Message) {
	_, tenant, ok := messaging.TenantContextFromSubject(context.Background(), msg.Subject)
	if !ok || core.ValidateToken(tenant) != nil {
		log.Warn().Str("subject", clipLog(msg.Subject)).Msg("Dropping a downlink report with no valid tenant in its subject.")
		f.count(AckPoison)
		ack(msg)
		return
	}
	var rep cmddownlink.DownlinkReport
	if err := json.Unmarshal(msg.Value, &rep); err != nil || rep.QueueItemID == "" || rep.Integration == "" ||
		(rep.DevEUI == "" && rep.DeviceRef == "") || (rep.Kind != cmddownlink.DownlinkReportAck &&
		rep.Kind != cmddownlink.DownlinkReportTxAck && rep.Kind != cmddownlink.DownlinkReportFailed) {
		log.Warn().Str("tenant", tenant).Msg("Dropping an undecodable downlink report.")
		f.count(AckPoison)
		ack(msg)
		return
	}
	if f.opts.TenantDeleted != nil && f.opts.TenantDeleted(tenant) {
		f.count(AckTenantGone)
		ack(msg)
		return
	}
	p, err := f.pending.Lookup(ctx, tenant, rep.QueueItemID)
	switch {
	case errors.Is(err, ErrNoPending):
		f.unmatched(msg, tenant, rep)
		return
	case err != nil:
		if ctx.Err() == nil {
			f.count(AckStoreFailed)
			log.Warn().Err(err).Str("tenant", tenant).Msg("Could not read a pending LoRaWAN downlink; leaving the report unacked to retry.")
		}
		return
	}
	if p.Integration != rep.Integration || !sameDevice(p, rep) {
		// The queue item id matched a downlink sent through another integration or to another device.
		// The id is unguessable, so this is a misconfigured sender or an attack; either way it
		// settles nothing, and the real downlink keeps waiting for its own report.
		f.count(AckMismatch)
		log.Warn().Str("tenant", tenant).Str("kind", rep.Kind).
			Msg("Dropping a downlink report whose integration or device is not the one the downlink was sent through.")
		ack(msg)
		return
	}
	if rep.Kind == cmddownlink.DownlinkReportTxAck {
		if err := f.pending.MarkTransmitted(ctx, tenant, p.ID, f.opts.Now()); err != nil {
			if ctx.Err() == nil {
				f.count(AckStoreFailed)
				log.Warn().Err(err).Str("tenant", tenant).Msg("Could not record a downlink's transmission; leaving the report unacked to retry.")
			}
			return
		}
		f.count(AckTransmitted)
		ack(msg)
		return
	}
	// Exactly one settler answers the command: a report and the deadline sweep both claim the
	// record's lease first, so a late acknowledgement cannot be answered beside a timeout. A report
	// that loses is left to redeliver; by then the record is gone and it is dropped as unmatched.
	token, ok := f.claim(ctx, p)
	if !ok {
		return
	}
	if rep.Kind == cmddownlink.DownlinkReportFailed {
		// The row was read before the lease was taken, and a transmission report for the same downlink
		// can have been recorded since by another worker. Decide on the row as it is now; a row that
		// cannot be read again is treated as one that may have been transmitted.
		fresh, err := f.pending.Lookup(ctx, tenant, rep.QueueItemID)
		if err != nil {
			p = &Pending{ID: p.ID, Tenant: p.Tenant, Integration: p.Integration, DeviceToken: p.DeviceToken,
				DevEUI: p.DevEUI, DeviceRef: p.DeviceRef, CommandToken: p.CommandToken, DispatchNonce: p.DispatchNonce,
				QueueItemID: p.QueueItemID, DeadlineAt: p.DeadlineAt, TransmittedAt: &token}
		} else {
			p = fresh
		}
	}
	env := cmddownlink.ResponseEnvelope{CommandToken: p.CommandToken, DispatchNonce: p.DispatchNonce}
	result := AckAcknowledged
	switch {
	case rep.Kind == cmddownlink.DownlinkReportFailed && neverTransmitted(p, rep):
		// A network server drop of a downlink it had not transmitted, named by the server itself and
		// not preceded by a retry or a transmission report: what NOT_DELIVERED (safe to send again)
		// means.
		result = AckFailed
		env.Reach = cmddownlink.ReachUnsent
		env.Error = strPtr("the network server reported it dropped the downlink before transmitting it")
		env.Payload = reasonPayload(ReasonFailed)
	case rep.Kind == cmddownlink.DownlinkReportFailed:
		// 🔴 A FAILED REPORT IS NOT ALWAYS A FRAME THAT NEVER LEFT. The Application Server emits one when
		// it gives up retrying a confirmed downlink that WAS transmitted (retries exhausted, or the
		// re-push after a nack failed), and the network server emits one for a gateway that reports it
		// could not transmit. Whatever cannot be positively shown to be a pre-transmission drop is
		// answered as unanswered: the device may have acted, so sending again may act twice.
		result = AckFailedMaybeSent
		env.Reach = cmddownlink.ReachUnanswered
		env.Error = strPtr("the network server reported the downlink failed, and it cannot be shown that it was never transmitted; the device may have received it")
		env.Payload = reasonPayload(ReasonFailedMaybeSent)
	case rep.Acknowledged:
		env.Success = true
		body, _ := json.Marshal(map[string]any{"stage": "acknowledged", "queueItemId": p.QueueItemID, "fCntDown": rep.FCntDown})
		env.Payload = strPtr(string(body))
	default:
		result = AckTimedOut
		env.Reach = cmddownlink.ReachUnanswered
		env.Error = strPtr("the network server reported the confirmed downlink was not acknowledged by the device in time; the device may have received it")
		env.Payload = reasonPayload(ReasonNotAcknowledged)
	}
	if f.respond(ctx, p, env, token) {
		f.count(result)
		ack(msg)
	}
}

// sameDevice reports whether a report names the device the downlink was sent to. A downlink stored
// with a DeviceRef (The Things Stack) is matched on it alone; one without (ChirpStack) on its DevEUI.
//
// A downlink with a DeviceRef (The Things Stack) also needs the report's DeviceToken to equal the
// downlink's: DeviceRef is text the network server sent, while DeviceToken is what the platform's own
// device lookup answered under the proven integration. A report without one cannot settle it.
func sameDevice(p *Pending, rep cmddownlink.DownlinkReport) bool {
	if p.DeviceRef != "" {
		return p.DeviceRef == rep.DeviceRef && rep.DeviceToken != "" && rep.DeviceToken == p.DeviceToken
	}
	return rep.DeviceRef == "" && strings.EqualFold(p.DevEUI, rep.DevEUI) &&
		(rep.DeviceToken == "" || rep.DeviceToken == p.DeviceToken)
}

// preTransmissionErrors are the network server's own errors that name a drop which cannot follow a
// transmission: a confirmed downlink queued for a multicast device is refused by a static property of
// the device, however many times the item is looked at (pkg/networkserver/downlink.go).
//
// 🔴 THE LIST IS DELIBERATELY ONE NAME. Every other network-server drop can be reached on a LATER pass
// over an item that an earlier pass already tried to send. If a gateway-server scheduling call fails
// ambiguously (it may have been accepted), the network server puts the item back at the head of the
// queue with no retry counted and no transmission reported, and a later pass can then drop it as
// unknown_session (after a rejoin), application_downlink_too_long (after a data-rate drop),
// downlink_expired, or on a scheduling error. From the report alone those cannot be told apart from a
// drop of an item that was never tried, so each is TIMEOUT. A "safe to send again" is only said where
// the cause is static.
var preTransmissionErrors = map[string]bool{
	"confirmed_multicast_downlink": true,
}

// neverTransmitted reports whether a failed report positively shows the downlink was never
// transmitted: no retry preceded it (a retry means an earlier transmission), no transmission was
// reported for it, and the error is one of the network server's own pre-transmission drops. Anything
// less is "cannot show".
func neverTransmitted(p *Pending, rep cmddownlink.DownlinkReport) bool {
	return rep.Attempt == 0 && p.TransmittedAt == nil &&
		rep.ErrorNamespace == "pkg/networkserver" && preTransmissionErrors[rep.ErrorName]
}

// claim takes the record's settle lease, counting why when it cannot.
func (f *AckFeed) claim(ctx context.Context, p *Pending) (time.Time, bool) {
	token, won, err := f.pending.Claim(ctx, p.Tenant, p.ID, f.opts.Now(), SettleLease)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			f.count(AckStoreFailed)
			log.Warn().Err(err).Str("tenant", p.Tenant).Msg("Could not claim a pending LoRaWAN downlink; leaving it to retry.")
		}
		return time.Time{}, false
	case !won:
		f.count(AckContended)
		return time.Time{}, false
	}
	return token, true
}

func settledKey(tenant, key string) string { return tenant + "\x00" + key }

// remember notes that this replica has just settled the downlink.
func (f *AckFeed) remember(tenant, key string) {
	now := f.opts.Now()
	f.settledMu.Lock()
	defer f.settledMu.Unlock()
	if len(f.settled) >= recentSettledMax {
		for k, at := range f.settled {
			if now.Sub(at) > recentSettledTTL {
				delete(f.settled, k)
			}
		}
		if len(f.settled) >= recentSettledMax {
			f.settled = map[string]time.Time{}
		}
	}
	f.settled[settledKey(tenant, key)] = now
}

// recentlySettled reports whether this replica settled the downlink within recentSettledTTL.
func (f *AckFeed) recentlySettled(tenant, key string) bool {
	f.settledMu.Lock()
	defer f.settledMu.Unlock()
	at, ok := f.settled[settledKey(tenant, key)]
	return ok && f.opts.Now().Sub(at) <= recentSettledTTL
}

// respond publishes the answer and removes the record, reporting whether the report is settled.
// Publishing first and deleting after means a crash between them repeats the answer, which
// command-delivery ignores for a command already terminal; the reverse would lose it.
//
// 🔴 THE PUBLISH IS BOUNDED BY settleBudget, WELL UNDER THE LEASE, and the delete holds the lease's token.
// A settler that took longer than the lease would be overtaken by another that answers differently (a
// report against the sweep), and the command would settle on whichever answer arrived first, which
// need not be the true one. Bounded, it has stopped before the lease can be taken; and if it was
// overtaken anyway its delete removes nothing, so the overtaker's record is untouched.
func (f *AckFeed) respond(ctx context.Context, p *Pending, env cmddownlink.ResponseEnvelope, token time.Time) bool {
	// The budget runs from the lease's own expiry, not from when this call began: a worker that was
	// paused between claiming and answering has less of the lease left, and none left means it must
	// not answer at all (another settler may already have).
	budget := min(f.opts.SettleBudget, token.Sub(f.opts.Now())-settleMargin)
	if budget <= 0 {
		f.count(AckContended)
		return false
	}
	pctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := cmddownlink.PublishResponseContext(pctx, f.responses, p.Tenant, p.DeviceToken, env, f.opts.ResponseAttempts, f.opts.ResponseBackoff); err != nil {
		f.count(AckResponseFail)
		log.Warn().Err(err).Str("tenant", p.Tenant).Str("command", p.CommandToken).
			Msg("Could not publish a confirmed LoRaWAN downlink's outcome; it will be retried.")
		return false
	}
	f.remember(p.Tenant, p.QueueItemID)
	if err := f.pending.Delete(ctx, p.Tenant, p.ID, token); err != nil {
		// Answered already; a leftover record is answered again by the sweep, which command-delivery
		// ignores. Not worth holding the report for.
		log.Warn().Err(err).Str("tenant", p.Tenant).Str("command", p.CommandToken).
			Msg("Answered a confirmed LoRaWAN downlink but could not remove its pending record; the sweep will repeat the answer.")
	}
	return true
}

// unmatched disposes of a report for no pending downlink. A txack is the ordinary case (every
// unconfirmed downlink produces one) and only counted. An ack for nothing is an anomaly and is
// warned about, but its first sight is left to redeliver once: the network server can report the
// acknowledgement before the adapter has stored the record it queued a moment earlier.
func (f *AckFeed) unmatched(msg messaging.Message, tenant string, rep cmddownlink.DownlinkReport) {
	if rep.Kind == cmddownlink.DownlinkReportTxAck {
		f.count(AckTxUntracked)
		ack(msg)
		return
	}
	if f.recentlySettled(tenant, rep.QueueItemID) {
		// This replica answered this downlink a moment ago and the report was redelivered because the
		// message ack was lost. Nothing is wrong and nothing to settle.
		f.count(AckRedelivered)
		ack(msg)
		return
	}
	if msg.NumDelivered < 2 {
		f.count(AckRetried)
		return
	}
	f.count(AckUnmatched)
	log.Warn().Str("tenant", tenant).Str("integration", clipLog(rep.Integration)).Str("queueItem", clipLog(rep.QueueItemID)).
		Bool("acknowledged", rep.Acknowledged).
		Msg("Dropping a LoRaWAN downlink acknowledgement that matches no pending downlink.")
	ack(msg)
}

// Sweep answers every pending downlink whose wait has run out as unanswered (TIMEOUT / UNKNOWN):
// the network server never reported an outcome, so the platform cannot say the device did not act.
// It is also what closes a command whose report was lost or whose record outlived a missed
// deletion. Replicas may sweep together; the repeated answer is ignored by command-delivery.
func (f *AckFeed) Sweep(ctx context.Context) {
	due, err := f.pending.Expired(ctx, f.opts.Now(), f.opts.SweepBatch)
	if err != nil {
		if ctx.Err() == nil {
			f.count(AckDeadlineFault)
			log.Warn().Err(err).Msg("Could not list overdue LoRaWAN downlinks; trying again next pass.")
		}
		return
	}
	for i := range due {
		p := due[i]
		// Before claiming: a deleted tenant's writes are fenced while its purge runs, so a claim
		// would fail as store_failed, once per row, every pass, and the rows would fill the batch
		// ahead of live tenants' overdue downlinks. Nobody is left to answer, so no lease is needed.
		if f.opts.TenantDeleted != nil && f.opts.TenantDeleted(p.Tenant) {
			if err := f.pending.DeleteGone(ctx, p.Tenant, p.ID); err != nil {
				// Not settled: the row stays and is seen again next pass, so say so.
				if ctx.Err() == nil {
					f.count(AckStoreFailed)
					log.Warn().Err(err).Str("tenant", p.Tenant).
						Msg("Could not delete a deleted tenant's pending LoRaWAN downlink; trying again next pass.")
				}
				continue
			}
			f.count(AckTenantGone)
			continue
		}
		token, ok := f.claim(ctx, &p)
		if !ok {
			continue
		}
		text := "the network server reported no outcome for the confirmed downlink in time; it may still be queued or may have been delivered"
		if p.TransmittedAt != nil {
			text = "a gateway transmitted the confirmed downlink but no acknowledgement was reported in time; the device may have received it"
		}
		env := cmddownlink.ResponseEnvelope{CommandToken: p.CommandToken, DispatchNonce: p.DispatchNonce,
			Reach: cmddownlink.ReachUnanswered, Error: strPtr(text), Payload: reasonPayload(ReasonNoAcknowledgement)}
		if f.respond(ctx, &p, env, token) {
			f.count(AckDeadline)
		} else {
			f.count(AckDeadlineFault)
		}
	}
}
