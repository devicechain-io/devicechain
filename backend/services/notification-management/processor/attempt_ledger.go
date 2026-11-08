// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
)

// attemptStore is the one persistence call the ledger makes. An interface so a test can
// plant a store that fails, blocks or records.
type attemptStore interface {
	RecordNotificationAttempt(ctx context.Context, attempt *model.NotificationAttempt) error
}

// The outcomes devicechain_notificationmanagement_ledger_writes_total is labelled with.
const (
	ledgerWritten = "written"
	// ledgerFailed — the store refused or timed out; the row is lost.
	ledgerFailed = "failed"
	// ledgerDropped — too many writes already in flight; the row was never attempted.
	ledgerDropped = "dropped"
	// ledgerPurged — the tenant has been deleted and the erasure fence refused the row,
	// which is the right answer, not a fault.
	ledgerPurged = "purged"
)

const (
	// ledgerWriteTimeout bounds one ledger INSERT. It runs off the delivery path, so it
	// costs no dispatch budget; it only has to stop a hung store from holding a writer.
	ledgerWriteTimeout = 5 * time.Second
	// ledgerWriters is how many ledger INSERTs can be in flight at once, and so how many
	// database connections the ledger can hold.
	//
	// 🔴 IT IS SMALL BECAUSE THE POOL IS SHARED WITH DELIVERY. The ledger writes through the
	// same connection pool the dispatcher reads channel secrets, policies and state from
	// (20 connections by default). An earlier draft started a goroutine per row with an
	// in-flight cap of 64: behind a slow database those writes could hold every connection,
	// and the NEXT alarm's secret lookup then waited for one inside its dispatch budget —
	// the ledger delaying a page through the back door. Two writers take at most two
	// connections, whatever the database is doing.
	ledgerWriters = 2
	// ledgerQueueDepth is how many rows can wait for a writer. Past it a row is dropped and
	// counted rather than blocking the sender, because a queue in front of a store that has
	// stopped answering only grows.
	ledgerQueueDepth = 1024
	// ledgerWaitCeiling bounds Wait whatever its context says.
	ledgerWaitCeiling = 5 * time.Second
	// maxLedgerErrorMessage caps the stored provider message, matching the column.
	maxLedgerErrorMessage = 512
)

// AttemptLedger writes the delivery ledger: one row per attempt, off the delivery path.
//
// 🔴 IT NEVER BLOCKS OR DELAYS A PAGE, AND THAT IS THE DESIGN, NOT A SHORTCUT. Every
// delivery in this service runs under a whole-dispatch budget sized to finish inside the
// consumer's AckWait (see dispatchBudget). A synchronous INSERT per attempt would spend
// that budget on bookkeeping, and a slow database would turn into duplicate pages — the
// exact failure the budget was introduced to stop. So record() puts the row on a bounded
// queue and returns at once; a full queue drops the row and counts it. A fixed, small set
// of writers drains the queue, so the ledger never holds more than ledgerWriters of the
// connections delivery also needs. The cost is honesty about what the ledger is: a row
// proves an attempt happened, a missing row proves nothing. The counter is how an operator
// tells the two apart.
//
// A nil *AttemptLedger records nothing, so a notifier built without one (every
// struct-literal fixture in this package) behaves exactly as before.
type AttemptLedger struct {
	store  attemptStore
	queue  chan ledgerJob
	writes *prometheus.CounterVec
	// pending counts rows queued or being written, for Wait; outstanding is the same count
	// as a number Wait can report.
	pending     sync.WaitGroup
	outstanding atomic.Int64
	// abandoned is set when Wait gives up: a writer then discards what is still queued.
	abandoned atomic.Bool
}

// ledgerJob is one queued row and the context (tenant) it is written under.
type ledgerJob struct {
	ctx context.Context
	row *model.NotificationAttempt
}

// NewAttemptLedger builds a ledger over store and starts its writers, which live for the
// life of the process. writes may be nil (no metric).
func NewAttemptLedger(store attemptStore, writes *prometheus.CounterVec) *AttemptLedger {
	l := &AttemptLedger{
		store:  store,
		queue:  make(chan ledgerJob, ledgerQueueDepth),
		writes: writes,
	}
	for i := 0; i < ledgerWriters; i++ {
		go l.writer()
	}
	return l
}

// Wait blocks until every row already handed to the ledger has been written or given up
// on. Shutdown calls it so a clean stop does not lose the last rows; tests call it before
// reading.
//
// It is bounded by ctx and by ledgerWaitCeiling, whichever ends first: behind a hung
// database every queued row would otherwise hold it for its own write timeout in turn.
// What is still queued when it gives up is discarded by the writers rather than written
// later, counted as dropped and logged; it returns how many rows it abandoned.
func (l *AttemptLedger) Wait(ctx context.Context) int {
	if l == nil {
		return 0
	}
	done := make(chan struct{})
	go func() {
		l.pending.Wait()
		close(done)
	}()
	wctx, cancel := context.WithTimeout(ctx, ledgerWaitCeiling)
	defer cancel()
	select {
	case <-done:
		return 0
	case <-wctx.Done():
	}
	l.abandoned.Store(true)
	n := int(l.outstanding.Load())
	// The writers may be stuck behind the store, so the queue is emptied here rather than
	// left for them. A row a writer already holds is not counted: it may still land.
	for drained := false; !drained; {
		select {
		case <-l.queue:
			l.count(ledgerDropped)
			l.done()
		default:
			drained = true
		}
	}
	log.Warn().Int("rows", n).
		Msg("Stopped waiting for the notification delivery ledger; rows still queued are dropped")
	return n
}

func (l *AttemptLedger) count(outcome string) {
	if l.writes != nil {
		l.writes.WithLabelValues(outcome).Inc()
	}
}

// record queues one row for a writer. ctx supplies the tenant; its cancellation and
// deadline do not apply, because the write deliberately outlives the dispatch it
// describes.
func (l *AttemptLedger) record(ctx context.Context, row *model.NotificationAttempt) {
	if l == nil || l.store == nil {
		return
	}
	l.pending.Add(1)
	l.outstanding.Add(1)
	select {
	case l.queue <- ledgerJob{ctx: context.WithoutCancel(ctx), row: row}:
	default:
		l.done()
		l.count(ledgerDropped)
	}
}

// writer drains the queue, one INSERT at a time.
func (l *AttemptLedger) writer() {
	for job := range l.queue {
		l.write(job)
	}
}

// done retires one row from the pending count.
func (l *AttemptLedger) done() {
	l.outstanding.Add(-1)
	l.pending.Done()
}

func (l *AttemptLedger) write(job ledgerJob) {
	defer l.done()
	if l.abandoned.Load() {
		l.count(ledgerDropped)
		return
	}
	cctx, cancel := context.WithTimeout(job.ctx, ledgerWriteTimeout)
	defer cancel()
	err := l.store.RecordNotificationAttempt(cctx, job.row)
	switch {
	case err == nil:
		l.count(ledgerWritten)
	case errors.Is(err, rdb.ErrTenantPurged):
		l.count(ledgerPurged)
	default:
		l.count(ledgerFailed)
		log.Warn().Err(err).Str("alarm", job.row.AlarmToken).Str("channel", job.row.ChannelToken).
			Msg("Could not write the notification delivery ledger row; the delivery itself is unaffected")
	}
}

// attemptContext is what every ledger row for one delivery shares.
type attemptContext struct {
	alarm       string
	trigger     string
	subjectHash string
}

// newAttemptContext describes the deliveries of one rendered notification.
func newAttemptContext(alarm, trigger string, rendered *RenderedNotification) attemptContext {
	ac := attemptContext{alarm: alarm, trigger: trigger}
	if rendered != nil {
		sum := sha256.Sum256([]byte(rendered.Subject))
		ac.subjectHash = hex.EncodeToString(sum[:])
	}
	return ac
}

// row builds the ledger row for one outcome of delivery d.
func (ac attemptContext) row(d delivery, status string, attempt, maxAttempts int,
	started time.Time, latency time.Duration, measured bool, err error) *model.NotificationAttempt {
	r := &model.NotificationAttempt{
		CreatedAt:    started,
		AlarmToken:   ac.alarm,
		Trigger:      ac.trigger,
		ChannelToken: d.channel.Token,
		ChannelType:  d.channel.ChannelType,
		SubjectHash:  ac.subjectHash,
		Status:       status,
		Attempt:      attempt,
		MaxAttempts:  maxAttempts,
	}
	if d.policy != "" {
		r.PolicyToken = sql.NullString{String: d.policy, Valid: true}
	}
	if recipients, jerr := json.Marshal(nonNilStrings(d.recipients)); jerr == nil {
		r.Recipients = datatypes.JSON(recipients)
	}
	if measured {
		r.LatencyMillis = sql.NullInt64{Int64: latency.Milliseconds(), Valid: true}
	}
	if code := attemptErrorCode(status, err); code != "" {
		r.ErrorCode = sql.NullString{String: code, Valid: true}
	}
	if err != nil {
		r.ErrorMessage = sql.NullString{String: sanitizeProviderMessage(err), Valid: true}
	}
	return r
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// attemptErrorCode classifies a non-SENT outcome into the ledger's closed code set. The
// code is never derived from error TEXT, which belongs to whatever library failed.
func attemptErrorCode(status string, err error) string {
	switch status {
	case model.AttemptStatusSent:
		return ""
	case model.AttemptStatusNotAttempted:
		if err != nil {
			return model.AttemptErrorSecretUnavailable
		}
		return model.AttemptErrorBudgetSpent
	}
	switch {
	case errors.Is(err, errDispatchBudgetSpent):
		return model.AttemptErrorBudgetSpent
	case errors.Is(err, egress.ErrBlocked):
		return model.AttemptErrorEgressBlocked
	case errors.Is(err, httpsink.ErrAuthRefused):
		return model.AttemptErrorCredentialRefused
	case isTimeout(err):
		return model.AttemptErrorTimeout
	default:
		return model.AttemptErrorDelivery
	}
}

// errDispatchBudgetSpent marks an attempt the whole-dispatch budget ended, as opposed to
// the attempt's own timeout. It never leaves the ledger: the delivery path sees the
// adapter's error unchanged.
var errDispatchBudgetSpent = errors.New("the dispatch budget was spent before the attempt finished")

// isTimeout reports a timeout of any shape an adapter returns: the attempt's context
// deadline (the HTTP adapters), or a socket deadline, which SMTP sets on its connection
// and which surfaces as a net.Error wrapping os.ErrDeadlineExceeded rather than as
// context.DeadlineExceeded.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var nerr net.Error
	return errors.As(err, &nerr) && nerr.Timeout()
}

// urlPattern matches anything URL-shaped left in an error's text.
var urlPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"']+`)

// sanitizeProviderMessage is the error text the ledger stores.
//
// 🔴 A DELIVERY URL CAN BE A CREDENTIAL. A Slack or Teams incoming-webhook URL carries its
// secret in the path, and Go's HTTP client puts the full URL into every transport error
// ("Post \"https://hooks.slack.com/services/T/B/XXXX\": dial tcp ..."). The ledger is
// readable by anyone with notification:read, which is a wider audience than the people
// allowed to see a channel's secret — so a raw err.Error() here would publish webhook
// secrets to every reader of the tenant's ledger. The *url.Error is unwrapped to its
// cause, and anything URL-shaped that remains is replaced, before the text is stored.
func sanitizeProviderMessage(err error) string {
	if err == nil {
		return ""
	}
	var uerr *url.Error
	for errors.As(err, &uerr) && uerr.Err != nil {
		err = uerr.Err
	}
	msg := urlPattern.ReplaceAllString(err.Error(), "<url>")
	if len(msg) > maxLedgerErrorMessage {
		cut := maxLedgerErrorMessage
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut]
	}
	return msg
}
