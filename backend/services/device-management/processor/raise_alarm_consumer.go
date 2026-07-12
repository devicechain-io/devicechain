// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// RaiseAlarmConsumer is the discrete NATS consumer for REACT raise-alarm requests (ADR-051 slice 5c
// / ADR-054): event-processing's REACT dispatcher emits a raise-alarm request when a detection rule's
// raiseAlarm action fires, and this consumer raises/escalates the alarm through device-management's
// existing engine (Api.RaiseAlarm → raiseOrEscalateAlarm), so a rule-driven alarm is indistinguishable
// downstream from a measurement-driven one — same Alarm object, ack/clear, graph rollup, and
// alarm-events→notification flow (ADR-041/017).
//
// It is a separate consumer from the measurement-driven alarm evaluator by design (raise-alarm must
// not share a failure fate with measurement evaluation), and it needs no dead-letter path: a request
// that repeatedly fails to apply is dropped after the redelivery cap. Raise-alarm volume is low (one
// per firing rule with a raiseAlarm action), so it processes inline in the read loop rather than
// behind a worker pool. An at-least-once redelivery is safe: Api.RaiseAlarm is an upsert keyed on
// (device, alarmKey), so an exact-duplicate is idempotent and the engine's cross-cycle occurred-time
// guards stop a stale raise from reactivating a cleared alarm. (It does NOT guard within-cycle
// reordering — see RaiseAlarmRequest.OccurredTime — the same latest-wins gap the evaluator has.)
//
// SLICE-6 CO-EXISTENCE: this consumer and the measurement evaluator write the SAME (device, alarmKey)
// row; while both run, the evaluator's auto-clear/tier-rederivation can clear or clobber a
// REACT-raised alarm. That is why the DISPATCH side is gated default-off until slice 6 retires the
// measurement-driven evaluator (per tenant, atomically) — the two paths must not both raise.
type RaiseAlarmConsumer struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Api          model.DeviceManagementApi

	metrics *core.ProcessorMetrics

	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup

	lifecycle core.LifecycleManager
}

// NewRaiseAlarmConsumer creates the raise-alarm consumer over its dedicated reader.
func NewRaiseAlarmConsumer(ms *core.Microservice, reader messaging.MessageReader,
	callbacks core.LifecycleCallbacks, api model.DeviceManagementApi) *RaiseAlarmConsumer {
	rc := &RaiseAlarmConsumer{
		Microservice: ms,
		Reader:       reader,
		Api:          api,
		metrics:      ms.NewProcessorMetrics("raise-alarm"),
	}
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "raise-alarm-proc")
	rc.lifecycle = core.NewLifecycleManager(name, rc, callbacks)
	return rc
}

// Initialize component.
func (rc *RaiseAlarmConsumer) Initialize(ctx context.Context) error {
	return rc.lifecycle.Initialize(ctx)
}

// ExecuteInitialize builds the cancelable read context.
func (rc *RaiseAlarmConsumer) ExecuteInitialize(ctx context.Context) error {
	rc.procCtx, rc.procCancel = context.WithCancel(ctx)
	return nil
}

// Start component.
func (rc *RaiseAlarmConsumer) Start(ctx context.Context) error {
	return rc.lifecycle.Start(ctx)
}

// ExecuteStart launches the read loop.
func (rc *RaiseAlarmConsumer) ExecuteStart(ctx context.Context) error {
	rc.readerWG.Add(1)
	go func() {
		defer rc.readerWG.Done()
		for {
			if eof := rc.readMessage(rc.procCtx); eof {
				return
			}
		}
	}()
	return nil
}

// readMessage reads and handles one raise-alarm request. It returns true when the stream is
// exhausted or shutting down.
func (rc *RaiseAlarmConsumer) readMessage(ctx context.Context) bool {
	msg, err := rc.Reader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on raise-alarm stream")
			return true
		}
		rc.Reader.HandleResponse(err)
		return false
	}
	rc.handle(ctx, msg)
	return ctx.Err() != nil
}

// handle applies one raise-alarm request and acks/naks it. A no-tenant subject, an unparseable
// body, a missing/invalid field, or a device that no longer exists is poison (redelivery cannot
// help) — acked and dropped. A transient store failure is naked for redelivery up to the cap, then
// dropped (a lost REACT alarm re-raises on the next firing of the rule).
func (rc *RaiseAlarmConsumer) handle(ctx context.Context, msg messaging.Message) {
	done := rc.metrics.Start()

	msgctx, _, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Msgf("Raise-alarm dropping message with no parseable tenant in subject %q", msg.Subject)
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	req, err := model.UnmarshalRaiseAlarmRequest(msg.Value)
	if err != nil {
		log.Warn().Err(err).Msg("Raise-alarm dropping unparseable request")
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	// ADR-057 staging (6d-pre-2b): the contributor-set integrator that resolves a rule's contribution
	// on a RESOLVED edge lands in 6d-pre-2c. Until then a resolved request must NOT reach the raise
	// path (which would wrongly raise/escalate); drop it fail-closed. In practice the alarm dispatch
	// gate is off until slice 6 (after 2c), so this is a defensive net for the 2b→2c window, not a
	// live path. A raised edge (or a legacy empty edge) falls through to the raise below.
	if req.Edge == "resolved" {
		log.Debug().Str("device", req.DeviceToken).Str("alarmKey", req.AlarmKey).
			Msg("Raise-alarm dropping a resolved edge (contributor-resolve integrator lands in 6d-pre-2c)")
		msg.Ack()
		done(core.ResultOK)
		return
	}

	// Map the rule's authoring-vocabulary severity (lowercase) to the AlarmSeverity tier (ADR-041).
	// An empty device token/alarm key or an unknown severity is a malformed request (a forged or
	// buggy producer) — poison, dropped.
	severity := strings.ToUpper(req.Severity)
	// A zero occurred time is dropped too: it becomes the alarm's raised time AND the ordering key
	// the engine's cross-cycle guards use, so a zero would stamp a 0001-01-01 alarm and defeat the
	// ordering. A well-formed producer always stamps the detection's event time, so this only guards
	// a forged/buggy producer — symmetric with the roster consumer's zero-time drop.
	if req.DeviceToken == "" || req.AlarmKey == "" || req.OccurredTime.IsZero() || !model.AlarmSeverity(severity).Valid() {
		log.Warn().Str("device", req.DeviceToken).Str("alarmKey", req.AlarmKey).Str("severity", req.Severity).
			Msg("Raise-alarm dropping malformed request (empty device/alarm key, zero time, or unknown severity)")
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	// Resolve the device token to its row id through the interface (the cached accessor). A store
	// error is transient (retry); an EMPTY result is a device that no longer exists (deleted between
	// detection and dispatch) — poison, dropped, since a retry cannot bring it back.
	devices, err := rc.Api.DevicesByToken(msgctx, []string{req.DeviceToken})
	if err != nil {
		rc.retryOrDrop(msg, done, err, "resolve device for raise-alarm")
		return
	}
	if len(devices) == 0 {
		log.Warn().Str("device", req.DeviceToken).Msg("Raise-alarm dropping request for a device that no longer exists")
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	if err := rc.Api.RaiseAlarm(msgctx, devices[0].ID, req.AlarmKey, req.MetricKey, severity, req.Value, req.OccurredTime); err != nil {
		rc.retryOrDrop(msg, done, err, "apply raise-alarm")
		return
	}

	msg.Ack()
	done(core.ResultOK)
}

// retryOrDrop naks a transiently-failed request for redelivery, or acks (drops) it once the
// redelivery cap is reached so one persistently-failing request cannot redeliver forever.
func (rc *RaiseAlarmConsumer) retryOrDrop(msg messaging.Message, done func(string), err error, what string) {
	if msg.NumDelivered >= messaging.MaxDeliver {
		log.Error().Err(err).Str("what", what).Int("attempts", msg.NumDelivered).
			Msg("Raise-alarm failed past redelivery cap; dropping (re-raises on the rule's next firing)")
		msg.Ack()
		done(core.ResultFailed)
		return
	}
	log.Warn().Err(err).Str("what", what).Msg("Raise-alarm failed; will retry on redelivery")
	msg.Nak()
	done(core.ResultRetry)
}

// Stop component.
func (rc *RaiseAlarmConsumer) Stop(ctx context.Context) error {
	return rc.lifecycle.Stop(ctx)
}

// ExecuteStop cancels the read loop and waits for it to exit before the reader is torn down.
func (rc *RaiseAlarmConsumer) ExecuteStop(context.Context) error {
	if rc.procCancel != nil {
		rc.procCancel()
	}
	rc.readerWG.Wait()
	return nil
}

// Terminate component.
func (rc *RaiseAlarmConsumer) Terminate(ctx context.Context) error {
	return rc.lifecycle.Terminate(ctx)
}

// ExecuteTerminate is a no-op; the reader is owned by the NATS manager.
func (rc *RaiseAlarmConsumer) ExecuteTerminate(context.Context) error {
	return nil
}
