// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// UpdateReportsApi is the slice of the update-management API the device-reports consumer uses. It
// is narrow on purpose: the consumer can apply a report, answer a sync and issue a grant, and
// nothing else.
type UpdateReportsApi interface {
	ApplyReport(ctx context.Context, deviceToken string, rep ota.Report) (ota.Verdict, error)
	CurrentUpdates(ctx context.Context, deviceToken string) ([]model.UpdateAssignmentView, error)
	PublishSyncAnswer(ctx context.Context, deviceToken string, views []model.UpdateAssignmentView)
	GrantDownload(ctx context.Context, deviceToken, assignmentToken string) (*model.Grant, error)
	PublishCancelFor(ctx context.Context, deviceToken, attemptToken, assignmentToken string)
	PublishGrant(ctx context.Context, deviceToken, assignmentToken string, g *model.Grant) error
}

var _ UpdateReportsApi = (*model.Api)(nil)

// Reasons a device message is counted as rejected. A closed vocabulary: they are label values.
const (
	rejectedMalformed      = "malformed"
	rejectedUnattributable = "unattributable"
	rejectedThrottled      = "throttled"
	// rejectedDeviceDisabled: an operator has disabled the device. A session it already had open
	// is not ended by that, so its reports, syncs and download requests are refused per message.
	rejectedDeviceDisabled = "device_disabled"

	maxDeadLetterPayload = 4096

	// SyncCooldown is the shortest gap between two syncs from one device that this service answers.
	// A sync costs a read and up to a few publishes; a device that reconnects in a loop must not
	// turn that into the platform's load.
	SyncCooldown = 5 * time.Second
	// maxSyncGate bounds the throttle's memory: past it, devices not already in the table are
	// treated as throttled until entries expire, which costs a device one retry at its next
	// reconnect and never costs the platform an unbounded map.
	maxSyncGate = 100_000
)

// UpdateReportsMetrics is every Prometheus instrument the update-management device-reports
// consumer exports.
type UpdateReportsMetrics struct {
	metrics  *core.ProcessorMetrics
	reports  *prometheus.CounterVec
	grants   *prometheus.CounterVec
	rejected *prometheus.CounterVec
	expired  *prometheus.CounterVec
	// DesiredFailures counts messages that could not be put on a device's desired channel.
	DesiredFailures prometheus.Counter
}

// reportVerdicts is every verdict label value of the reports counter, so each series exists at zero
// from startup.
var reportVerdicts = []ota.Verdict{
	ota.VerdictApplied, ota.VerdictDuplicate, ota.VerdictStaleAttempt, ota.VerdictDigestMismatch,
	ota.VerdictWrongComponent, ota.VerdictRegression, ota.VerdictUnconfirmed, ota.VerdictNotCancellable,
	ota.VerdictLate, ota.VerdictConflict, ota.VerdictInvalidAttempt, ota.VerdictWrongKind,
	model.VerdictUnknownAssignment,
}

// reportKinds is every kind label value of the reports counter.
var reportKinds = []string{reportKindProgress, reportKindInventory}

const (
	reportKindProgress  = "progress"
	reportKindInventory = "inventory"
)

// reportKind is the counter's kind label for a decoded report's kind.
func reportKind(kind string) string {
	if kind == ota.KindInventory {
		return reportKindInventory
	}
	return reportKindProgress
}

var grantResults = []string{"granted", model.GrantUnknownAssignment, model.GrantSuperseded, model.GrantFinished,
	model.GrantCancelRequested, model.GrantArtifactGone, model.GrantNotConfigured, "error"}

// NewUpdateReportsMetrics builds the consumer's instruments. Call it from the initialize phase: the
// consumer is rebuilt on every NATS start, a collector must not be.
func NewUpdateReportsMetrics(ms *core.Microservice) UpdateReportsMetrics {
	m := UpdateReportsMetrics{
		metrics: ms.NewProcessorMetrics("update_reports"),
		reports: ms.NewCounterVec("update_reports_total",
			"Update progress and inventory reports from devices, by the report kind (progress or inventory) and the verdict the update state machine gave: "+
				"APPLIED, or why it was not (DUPLICATE, STALE_ATTEMPT, DIGEST_MISMATCH, REGRESSION, UNCONFIRMED, LATE, "+
				"CONFLICT, UNKNOWN_ASSIGNMENT, ...). A rising non-applied rate from one fleet is a device-side bug. "+
				"Inventory reports routinely answer UNKNOWN_ASSIGNMENT or UNCONFIRMED when a device reconnects with nothing to do, "+
				"so judge a fleet on kind=progress.",
			[]string{"verdict", "kind"}),
		grants: ms.NewCounterVec("update_download_grants_total",
			"Download capabilities requested by devices, by outcome: granted, or the reason the assignment could not be "+
				"downloaded (superseded, finished, cancel_requested, artifact_unavailable, ...). not_configured means "+
				"updates.downloadBaseUrl is unset and no device can download.",
			[]string{"result"}),
		rejected: ms.NewCounterVec("update_reports_rejected_total",
			"Device messages on the device-reports channel that update-management refused before applying: malformed "+
				"(did not decode), unattributable (no tenant or device in the subject), throttled (a sync inside "+
				"the per-device cooldown) and device_disabled (an operator has disabled the device).",
			[]string{"reason"}),
		expired: ms.NewCounterVec("update_attempts_expired_total",
			"Update attempts the deadline sweep moved because the device went quiet: timed_out before installation "+
				"was reported started, unknown during installation or reboot.",
			[]string{"state"}),
		DesiredFailures: ms.NewCounter("update_desired_publish_failures_total",
			"Update assignments, download grants and cancels that could not be put on a device's desired channel. "+
				"The device picks the state up at its next sync."),
	}
	for _, v := range reportVerdicts {
		for _, k := range reportKinds {
			m.reports.WithLabelValues(string(v), k).Add(0)
		}
	}
	for _, r := range grantResults {
		m.grants.WithLabelValues(r).Add(0)
	}
	for _, r := range []string{rejectedMalformed, rejectedUnattributable, rejectedThrottled, rejectedDeviceDisabled} {
		m.rejected.WithLabelValues(r).Add(0)
	}
	for _, s := range []ota.State{ota.StateTimedOut, ota.StateUnknown} {
		m.expired.WithLabelValues(string(s)).Add(0)
	}
	return m
}

// ReportsConsumer is the durable consumer of the device-reports channel for the update family: what
// a device says about an update it was assigned. It handles
//
//   - update.progress and update.inventory: folded into the device's attempt by the core state
//     machine (Api.ApplyReport);
//   - download.request: answered with a capability URL for the device's CURRENT assignment, or with
//     a cancel when the assignment is not current;
//   - sync: answered with the device's live assignments, so a device that was offline when an
//     assignment was made learns of it on connect.
//
// device-management reads the same channel on its own durable for sync and config.status, so the
// two consumers see every message and each acknowledges the kinds that are not its own without
// saying anything about them. A body that does not decode is device-management's to dead-letter (it
// sees it too), so it is only counted here: lettering it twice would be two records of one fault.
//
// A device an operator has DISABLED is cut off here per message, not per connect: its session may
// stay open, so each report, sync answer and download request is refused (model.ErrDeviceDisabled),
// counted and discarded.
//
// 🔴 THE DEVICE AND THE TENANT COME FROM THE SUBJECT, NEVER FROM THE BODY. The broker confines a
// device's grant to publishing on its own device-reports subject, so the token in the subject is the
// one authenticated statement of who is reporting; the body has no identity field at all (the decoder
// rejects one). A device cannot report for its neighbour, and a device of one tenant cannot reach
// another's rows: the tenant context is built from the subject and the storage layer refuses a query
// without one.
type ReportsConsumer struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Api          UpdateReportsApi

	// UpdateReportsMetrics is embedded by value and built once in the initialize phase.
	UpdateReportsMetrics

	dead *deadletter.Sink

	// Hold keeps a report unacked, outside its delivery budget, while the relational store cannot
	// take writes (core/storehold). A device report is the device's only copy of what it did (an
	// update's progress, a download request), so a database outage past the delivery budget used
	// to letter and ack it. Re-running is safe: ApplyReport is the append-only attempt reducer
	// and answers VerdictDuplicate for a report it has already folded in, a grant is re-issued
	// from the same assignment, and a sync is a read. Nil holds nothing.
	Hold *storehold.Holder
	gate syncGate

	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	readPacer  *core.ReadPacer

	lifecycle core.LifecycleManager
}

// NewReportsConsumer creates the consumer over its dedicated reader.
func NewReportsConsumer(ms *core.Microservice, reader messaging.MessageReader, callbacks core.LifecycleCallbacks,
	api UpdateReportsApi, dead *deadletter.Sink, metrics UpdateReportsMetrics) *ReportsConsumer {
	rc := &ReportsConsumer{
		Microservice:         ms,
		Reader:               reader,
		Api:                  api,
		UpdateReportsMetrics: metrics,
		dead:                 dead,
		gate:                 syncGate{last: map[string]time.Time{}, cooldown: SyncCooldown, now: time.Now},
	}
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "device-reports-proc")
	rc.lifecycle = core.NewLifecycleManager(name, rc, callbacks)
	return rc
}

func (rc *ReportsConsumer) pacer() *core.ReadPacer {
	if rc.readPacer == nil {
		rc.readPacer = core.NewReadPacer(rc.Microservice, "device-reports")
	}
	return rc.readPacer
}

// Initialize component.
func (rc *ReportsConsumer) Initialize(ctx context.Context) error { return rc.lifecycle.Initialize(ctx) }

// ExecuteInitialize builds the cancelable read context.
func (rc *ReportsConsumer) ExecuteInitialize(ctx context.Context) error {
	rc.procCtx, rc.procCancel = context.WithCancel(ctx)
	return nil
}

// Start component.
func (rc *ReportsConsumer) Start(ctx context.Context) error { return rc.lifecycle.Start(ctx) }

// ExecuteStart launches the read loop.
func (rc *ReportsConsumer) ExecuteStart(ctx context.Context) error {
	rc.readerWG.Add(1)
	go func() {
		defer rc.readerWG.Done()
		messaging.RunConsumer(rc.procCtx, rc.Reader, rc.pacer(), func(msg messaging.Message) bool {
			rc.handle(rc.procCtx, msg)
			return true
		})
	}()
	return nil
}

// handle applies one device message and acks it or leaves it unacked for redelivery.
func (rc *ReportsConsumer) handle(ctx context.Context, msg messaging.Message) {
	done := rc.metrics.Start()

	msgctx, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	token, tokenOK := messaging.ParseDeviceFromScopedSubject(msg.Subject, streams.DeviceReports)
	if !ok || !tokenOK {
		log.Warn().Str("subject", msg.Subject).Msg("Device report dropped: the subject names no tenant and device.")
		rc.rejected.WithLabelValues(rejectedUnattributable).Inc()
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	rep, err := ota.DecodeDeviceReport(msg.Value)
	if err != nil {
		// device-management sees the same bytes and dead-letters them.
		log.Debug().Err(err).Str("device", token).Int("bytes", len(msg.Value)).Msg("Device report does not decode.")
		rc.rejected.WithLabelValues(rejectedMalformed).Inc()
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	switch {
	case rep.Update != nil:
		var verdict ota.Verdict
		err := rc.held(msg, func() (e error) {
			verdict, e = rc.Api.ApplyReport(msgctx, token, *rep.Update)
			return e
		})
		if err != nil {
			if rc.settleDisabled(err, msg, token, rep.Kind, done) {
				return
			}
			rc.retryOrDrop(msgctx, msg, token, done, err, rep.Kind)
			return
		}
		rc.reports.WithLabelValues(string(verdict), reportKind(rep.Kind)).Inc()
		if verdict != ota.VerdictApplied && verdict != ota.VerdictDuplicate {
			log.Debug().Str("device", token).Str("kind", rep.Kind).Str("verdict", string(verdict)).
				Msg("Update report not applied.")
		}
	case rep.DownloadRequest != nil:
		if err := rc.held(msg, func() error {
			return rc.grant(msgctx, token, rep.DownloadRequest.AssignmentID)
		}); err != nil {
			if rc.settleDisabled(err, msg, token, rep.Kind, done) {
				return
			}
			rc.retryOrDrop(msgctx, msg, token, done, err, rep.Kind)
			return
		}
	case rep.Sync != nil:
		if !rc.gate.allow(tenant + "/" + token) {
			rc.rejected.WithLabelValues(rejectedThrottled).Inc()
			msg.Ack()
			done(core.ResultInvalid)
			return
		}
		var views []model.UpdateAssignmentView
		err := rc.held(msg, func() (e error) {
			views, e = rc.Api.CurrentUpdates(msgctx, token)
			return e
		})
		if err != nil {
			rc.gate.forget(tenant + "/" + token)
			if rc.settleDisabled(err, msg, token, rep.Kind, done) {
				return
			}
			rc.retryOrDrop(msgctx, msg, token, done, err, rep.Kind)
			return
		}
		rc.Api.PublishSyncAnswer(msgctx, token, views)
	default:
		// config.status: device-management's.
	}

	msg.Ack()
	done(core.ResultOK)
}

// held runs one store operation for msg under the consumer's store hold (core/storehold). A nil
// Hold runs it once. Shutdown ends the hold with messaging.ErrHoldReleased, which Dispose
// answers Retry on every delivery, so retryOrDrop leaves the message unacked.
func (rc *ReportsConsumer) held(msg messaging.Message, op func() error) error {
	ctx := rc.procCtx
	if ctx == nil {
		ctx = context.Background()
	}
	return rc.Hold.Run(ctx, []messaging.Message{msg}, op)
}

// settleDisabled settles a message from a device an operator has disabled, and reports whether it
// was one. It is settled on its first delivery and discarded: no redelivery can answer differently
// while the device stays disabled, and a device is disabled precisely when its traffic is unwanted,
// so a dead letter per message would move the flood into the dead-letter store. It is counted
// (update_reports_rejected_total{reason="device_disabled"}), and the device is told nothing.
func (rc *ReportsConsumer) settleDisabled(err error, msg messaging.Message, token, kind string, done func(string)) bool {
	if !errors.Is(err, model.ErrDeviceDisabled) {
		return false
	}
	log.Debug().Str("device", token).Str("kind", kind).Msg("Discarding a device report from a disabled device.")
	rc.rejected.WithLabelValues(rejectedDeviceDisabled).Inc()
	msg.Ack()
	done(core.ResultDropped)
	return true
}

// grant answers a download.request. A refusal is not an error of the message: it is counted, and
// when the assignment is not one the device should still be working on, the device is told to drop
// it.
func (rc *ReportsConsumer) grant(ctx context.Context, device, assignment string) error {
	g, err := rc.Api.GrantDownload(ctx, device, assignment)
	var refusal *model.GrantRefusal
	switch {
	case err == nil:
	case errors.As(err, &refusal):
		rc.grants.WithLabelValues(refusal.Reason).Inc()
		if refusal.Reason == model.GrantNotConfigured {
			log.Error().Str("device", device).
				Msg("A device asked for a download but updates.downloadBaseUrl is not set; no capability can be issued.")
		}
		if refusal.Stale {
			rc.Api.PublishCancelFor(ctx, device, refusal.AttemptToken, assignment)
		}
		return nil
	case errors.Is(err, model.ErrDeviceDisabled):
		return err
	default:
		rc.grants.WithLabelValues("error").Inc()
		return err
	}
	if err := rc.Api.PublishGrant(ctx, device, assignment, g); err != nil {
		rc.grants.WithLabelValues("error").Inc()
		return err
	}
	rc.grants.WithLabelValues("granted").Inc()
	return nil
}

// retryOrDrop leaves a transiently failed message unacked for AckWait-paced redelivery, or
// dead-letters and acks it once the redelivery cap is reached.
//
// The decision is messaging.Dispose's. A deleted tenant is not transient: its erasure fence
// refuses the write on every delivery, and a dead letter would be a new row about a tenant
// whose data is being erased, so the report is settled on the delivery it arrived on and
// lettered nowhere.
func (rc *ReportsConsumer) retryOrDrop(ctx context.Context, msg messaging.Message, token string, done func(string),
	err error, what string) {
	switch messaging.Dispose(msg, err) {
	case messaging.TenantErased:
		log.Info().Str("what", what).Str("device", token).
			Msg("Dropping a device report for a deleted tenant; this area's data has been erased.")
		msg.Ack()
		done(core.ResultErased)
		return
	case messaging.Exhausted:
		log.Error().Err(err).Str("what", what).Str("device", token).Int("attempts", msg.NumDelivered).
			Msg("Device report failed past the redelivery cap; dead-lettering.")
		rc.deadLetter(ctx, msg, token, "a device report could not be applied after every delivery attempt", err)
		msg.Ack()
		done(core.ResultFailed)
		return
	}
	log.Warn().Err(err).Str("what", what).Str("device", token).Msg("Device report failed; will retry on redelivery.")
	done(core.ResultRetry)
}

// deadLetter files a letter about msg. The payload is bounded: a device chooses its bytes.
func (rc *ReportsConsumer) deadLetter(ctx context.Context, msg messaging.Message, token, summary string, cause error) {
	if rc.dead == nil {
		return
	}
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	payload := msg.Value
	if len(payload) > maxDeadLetterPayload {
		payload = payload[:maxDeadLetterPayload]
		detail += fmt.Sprintf(" (payload truncated to %d of %d bytes)", maxDeadLetterPayload, len(msg.Value))
	}
	if err := rc.dead.WriteFor(ctx, msg, deadletter.Envelope{
		Reason:     deadletter.ReasonExhausted,
		Summary:    summary,
		Detail:     detail,
		Reference:  token,
		OccurredAt: time.Now().UTC(),
		Payload:    payload,
	}); err != nil {
		log.Error().Err(err).Str("device", token).Msg("LOST device report: it could be neither applied nor dead-lettered.")
	}
}

// Stop component.
func (rc *ReportsConsumer) Stop(ctx context.Context) error { return rc.lifecycle.Stop(ctx) }

// ExecuteStop cancels the read loop and waits for it to exit before the reader is torn down.
func (rc *ReportsConsumer) ExecuteStop(context.Context) error {
	if rc.procCancel != nil {
		rc.procCancel()
	}
	rc.readerWG.Wait()
	return nil
}

// Terminate component.
func (rc *ReportsConsumer) Terminate(ctx context.Context) error { return rc.lifecycle.Terminate(ctx) }

// ExecuteTerminate is a no-op; the reader is owned by the NATS manager.
func (rc *ReportsConsumer) ExecuteTerminate(context.Context) error { return nil }

// syncGate is the per-device cooldown on syncs. It is process-local on purpose: it bounds the work
// one replica does, and a device that lands on another replica is simply answered there.
type syncGate struct {
	mu       sync.Mutex
	last     map[string]time.Time
	cooldown time.Duration
	now      func() time.Time
}

func (g *syncGate) allow(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if t, ok := g.last[key]; ok && now.Sub(t) < g.cooldown {
		return false
	}
	if len(g.last) >= maxSyncGate {
		for k, t := range g.last {
			if now.Sub(t) >= g.cooldown {
				delete(g.last, k)
			}
		}
		if len(g.last) >= maxSyncGate {
			return false
		}
	}
	g.last[key] = now
	return true
}

// forget lets a device sync again at once, after an answer that failed and will be retried.
func (g *syncGate) forget(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.last, key)
}
