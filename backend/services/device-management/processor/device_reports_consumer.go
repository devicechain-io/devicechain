// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// ConfigurationReportApi is the slice of the device-management API the device-reports consumer
// uses. It is narrow on purpose: the consumer can sync a device and record a report, and
// nothing else.
type ConfigurationReportApi interface {
	SyncDeviceConfiguration(ctx context.Context, deviceToken string, haveRevision int64) (*model.DeviceConfigurationRevision, error)
	ApplyDeviceConfigurationReport(ctx context.Context, deviceToken string, revision int64, digest string,
		status model.ConfigurationStatus, errs []string) error
}

// Reasons a device message is counted as rejected, beyond the model's report refusals
// (model.ReportRejected*). A closed vocabulary: they are label values.
const (
	reportRejectedMalformed        = "malformed"
	reportRejectedUnsupportedKind  = "unsupported_kind"
	reportRejectedUnattributable   = "unattributable"
	maxDeadLetterReportPayloadByte = 4096
)

// DeviceReportsConsumer is the durable consumer of the device-reports channel: what a device
// says to the platform about its configuration. It handles two kinds,
//
//   - sync: the device is connected and says what it holds; the consumer mints a revision when
//     the device's document has moved, and delivers the current one (Api.SyncDeviceConfiguration);
//   - config.status: the device reports RECEIVED, APPLIED or REJECTED for a revision; the
//     consumer records it when the device has that revision with that digest
//     (Api.ApplyDeviceConfigurationReport).
//
// 🔴 THE DEVICE AND THE TENANT COME FROM THE SUBJECT, NEVER FROM THE BODY. The broker confines
// a device's grant to publishing on its own device-reports subject, so the token in the subject
// is the one authenticated statement of who is reporting; the body has no identity field at all
// (the decoder rejects one). A device cannot report for its neighbour, and a device of one
// tenant cannot reach another's rows: the tenant context is built from the subject and the
// storage layer refuses a query without one.
//
// Anything that cannot be understood is DEAD-LETTERED, not dropped: a body that does not decode
// (malformed, oversized, a field it must not carry, a duplicate key), a kind this version does
// not serve (the update kinds arrive with their own consumer), and a report naming a revision the
// device does not have or a digest that does not match. Reports that are merely old, and
// reports from a device the tenant does not have, are counted and acked: they are ordinary
// at-least-once traffic and need no operator.
type DeviceReportsConsumer struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Api          ConfigurationReportApi

	// DeviceReportsMetrics is embedded by value and built once in the initialize phase, for the
	// reason given on RaiseAlarmMetrics.
	DeviceReportsMetrics

	dead *deadletter.Sink

	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	readPacer  *core.ReadPacer

	lifecycle core.LifecycleManager
}

// DeviceReportsMetrics is every Prometheus instrument the device-reports consumer exports.
type DeviceReportsMetrics struct {
	metrics      *core.ProcessorMetrics
	rejected     *prometheus.CounterVec
	deadLettered prometheus.Counter
}

// deviceReportRejectionReasons are every rejection label value, so each series exists at zero
// from startup and an alert on the rate sees a series rather than none.
var deviceReportRejectionReasons = []string{
	reportRejectedMalformed, reportRejectedUnsupportedKind, reportRejectedUnattributable,
	model.ReportRejectedUnknownDevice, model.ReportRejectedUnknownRevision,
	model.ReportRejectedDigestMismatch, model.ReportRejectedStale,
}

// NewDeviceReportsMetrics builds the consumer's instruments. Call it from the initialize phase
// (see NewRaiseAlarmMetrics): the consumer is rebuilt on every NATS start, a collector must not be.
func NewDeviceReportsMetrics(ms *core.Microservice) DeviceReportsMetrics {
	m := DeviceReportsMetrics{
		metrics: ms.NewProcessorMetrics("device_reports"),
		rejected: ms.NewCounterVec("config_reports_rejected_total",
			"Device messages on the device-reports channel that were refused, by reason: malformed "+
				"(did not decode), unsupported_kind, unattributable (no tenant or device in the "+
				"subject), unknown_device, unknown_revision, digest_mismatch (the device named a "+
				"revision it was not sent), and stale (older than what is recorded).",
			[]string{"reason"}),
		deadLettered: ms.NewCounter("device_reports_dead_lettered_total",
			"Device messages written to the dead-letter stream because they could not be "+
				"understood or named a configuration revision the device does not have."),
	}
	for _, r := range deviceReportRejectionReasons {
		m.rejected.WithLabelValues(r).Add(0)
	}
	return m
}

// NewDeviceReportsConsumer creates the consumer over its dedicated reader.
func NewDeviceReportsConsumer(ms *core.Microservice, reader messaging.MessageReader,
	callbacks core.LifecycleCallbacks, api ConfigurationReportApi, dead *deadletter.Sink,
	metrics DeviceReportsMetrics) *DeviceReportsConsumer {
	rc := &DeviceReportsConsumer{
		Microservice:         ms,
		Reader:               reader,
		Api:                  api,
		DeviceReportsMetrics: metrics,
		dead:                 dead,
	}
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "device-reports-proc")
	rc.lifecycle = core.NewLifecycleManager(name, rc, callbacks)
	return rc
}

func (rc *DeviceReportsConsumer) pacer() *core.ReadPacer {
	if rc.readPacer == nil {
		rc.readPacer = core.NewReadPacer(rc.Microservice, "device-reports")
	}
	return rc.readPacer
}

// Initialize component.
func (rc *DeviceReportsConsumer) Initialize(ctx context.Context) error {
	return rc.lifecycle.Initialize(ctx)
}

// ExecuteInitialize builds the cancelable read context.
func (rc *DeviceReportsConsumer) ExecuteInitialize(ctx context.Context) error {
	rc.procCtx, rc.procCancel = context.WithCancel(ctx)
	return nil
}

// Start component.
func (rc *DeviceReportsConsumer) Start(ctx context.Context) error {
	return rc.lifecycle.Start(ctx)
}

// ExecuteStart launches the read loop.
func (rc *DeviceReportsConsumer) ExecuteStart(ctx context.Context) error {
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
func (rc *DeviceReportsConsumer) handle(ctx context.Context, msg messaging.Message) {
	done := rc.metrics.Start()

	msgctx, _, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	token, tokenOK := messaging.ParseDeviceFromScopedSubject(msg.Subject, streams.DeviceReports)
	if !ok || !tokenOK {
		log.Warn().Str("subject", msg.Subject).Msg("Device report dropped: the subject names no tenant and device.")
		rc.rejected.WithLabelValues(reportRejectedUnattributable).Inc()
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	rep, err := ota.DecodeDeviceReport(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("device", token).Int("bytes", len(msg.Value)).Msg("Device report refused: it does not decode.")
		rc.rejected.WithLabelValues(reportRejectedMalformed).Inc()
		rc.deadLetter(msgctx, msg, token, "malformed device report", err)
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	switch {
	case rep.Sync != nil:
		if _, err := rc.Api.SyncDeviceConfiguration(msgctx, token, rep.Sync.Config.HaveRevision); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				rc.unknownDevice(msg, token, done)
				return
			}
			rc.retryOrDrop(msgctx, msg, token, done, err, "sync")
			return
		}
	case rep.ConfigStatus != nil:
		s := rep.ConfigStatus
		err := rc.Api.ApplyDeviceConfigurationReport(msgctx, token, s.Revision, s.Digest,
			model.ConfigurationStatus(s.Status), s.Errors)
		var refusal *model.ConfigurationReportRefusal
		switch {
		case err == nil:
		case errors.As(err, &refusal):
			rc.refused(msgctx, msg, token, refusal, done)
			return
		default:
			rc.retryOrDrop(msgctx, msg, token, done, err, "config.status")
			return
		}
	default:
		// An update.* report: well-formed, and not served by this version. Said out loud, not
		// swallowed - a device reporting an update the platform never assigned is worth seeing.
		log.Warn().Str("device", token).Str("kind", rep.Kind).Msg("Device report refused: this kind is not served yet.")
		rc.rejected.WithLabelValues(reportRejectedUnsupportedKind).Inc()
		rc.deadLetter(msgctx, msg, token, "device report of a kind this version does not serve",
			fmt.Errorf("kind %q", rep.Kind))
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	msg.Ack()
	done(core.ResultOK)
}

func (rc *DeviceReportsConsumer) unknownDevice(msg messaging.Message, token string, done func(string)) {
	log.Warn().Str("device", token).Msg("Device report dropped: the tenant has no such device.")
	rc.rejected.WithLabelValues(model.ReportRejectedUnknownDevice).Inc()
	msg.Ack()
	done(core.ResultInvalid)
}

// refused handles a report the model understood and refused. Old and unknown-device reports are
// ordinary traffic: counted. A report for a revision the device was never sent, or with a
// digest that is not that revision's, is a confused or hostile device: counted and lettered.
func (rc *DeviceReportsConsumer) refused(ctx context.Context, msg messaging.Message, token string,
	refusal *model.ConfigurationReportRefusal, done func(string)) {
	rc.rejected.WithLabelValues(refusal.Reason).Inc()
	switch refusal.Reason {
	case model.ReportRejectedUnknownRevision, model.ReportRejectedDigestMismatch:
		log.Warn().Str("device", token).Str("reason", refusal.Reason).
			Msg("Device configuration report refused: it names a revision the device was not sent.")
		rc.deadLetter(ctx, msg, token, "device configuration report refused", refusal)
	default:
		log.Debug().Str("device", token).Str("reason", refusal.Reason).Msg("Device configuration report refused.")
	}
	msg.Ack()
	done(core.ResultInvalid)
}

// retryOrDrop leaves a transiently failed message unacked for AckWait-paced redelivery, or
// dead-letters and acks it once the redelivery cap is reached.
func (rc *DeviceReportsConsumer) retryOrDrop(ctx context.Context, msg messaging.Message, token string,
	done func(string), err error, what string) {
	if msg.NumDelivered >= messaging.MaxDeliver {
		log.Error().Err(err).Str("what", what).Str("device", token).Int("attempts", msg.NumDelivered).
			Msg("Device report failed past the redelivery cap; dead-lettering.")
		rc.deadLetterReason(ctx, msg, token, deadletter.ReasonExhausted,
			"a device report could not be applied after every delivery attempt", err)
		msg.Ack()
		done(core.ResultFailed)
		return
	}
	log.Warn().Err(err).Str("what", what).Str("device", token).Msg("Device report failed; will retry on redelivery.")
	done(core.ResultRetry)
}

func (rc *DeviceReportsConsumer) deadLetter(ctx context.Context, msg messaging.Message, token, summary string, cause error) {
	rc.deadLetterReason(ctx, msg, token, deadletter.ReasonUnprocessable, summary, cause)
}

// deadLetterReason files a letter about msg. The payload is bounded: a device chooses its bytes.
func (rc *DeviceReportsConsumer) deadLetterReason(ctx context.Context, msg messaging.Message, token string,
	reason deadletter.Reason, summary string, cause error) {
	if rc.dead == nil {
		return
	}
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	payload := msg.Value
	if len(payload) > maxDeadLetterReportPayloadByte {
		payload = payload[:maxDeadLetterReportPayloadByte]
		detail += fmt.Sprintf(" (payload truncated to %d of %d bytes)", maxDeadLetterReportPayloadByte, len(msg.Value))
	}
	if err := rc.dead.WriteFor(ctx, msg, deadletter.Envelope{
		Reason:     reason,
		Summary:    summary,
		Detail:     detail,
		Reference:  token,
		OccurredAt: time.Now().UTC(),
		Payload:    payload,
	}); err != nil {
		log.Error().Err(err).Str("device", token).Msg("LOST device report: it could be neither applied nor dead-lettered.")
		return
	}
	rc.deadLettered.Inc()
}

// Stop component.
func (rc *DeviceReportsConsumer) Stop(ctx context.Context) error { return rc.lifecycle.Stop(ctx) }

// ExecuteStop cancels the read loop and waits for it to exit before the reader is torn down.
func (rc *DeviceReportsConsumer) ExecuteStop(context.Context) error {
	if rc.procCancel != nil {
		rc.procCancel()
	}
	rc.readerWG.Wait()
	return nil
}

// Terminate component.
func (rc *DeviceReportsConsumer) Terminate(ctx context.Context) error {
	return rc.lifecycle.Terminate(ctx)
}

// ExecuteTerminate is a no-op; the reader is owned by the NATS manager.
func (rc *DeviceReportsConsumer) ExecuteTerminate(context.Context) error { return nil }
