// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package intentseam is device-management's read-only observation seam for the automation area:
// what the members of a frozen device-group version hold for one configuration key, as one
// digest or as keyset pages of fixed-size rows, and whether two frozen targets share a device.
// It knows nothing about intents. It stores nothing and writes nothing.
//
// Every statement here is hand-written SQL, so the gorm tenant callback, which reaches only a
// typed statement's own table, does not apply. The statements therefore obey these rules
// themselves, and TestSeamSQLNamesItsTenantOnEveryTable checks them on the built text:
//
//   - the outer devices row carries devices.tenant_id = $tenant AND devices.deleted_at IS NULL,
//     written in the statement;
//   - every joined table carries its own tenant_id = $tenant and deleted_at IS NULL;
//   - the lowered selector fragment is always AND-ed inside that scoped outer row;
//   - membership is decided by the outer devices row and the selector alone: every other table is
//     a LEFT JOIN whose predicates sit in its ON clause, so a missing or soft-deleted right-hand
//     row yields NULLs and never removes a device.
//
// And what the seam may cost device-management is enforced here, so a misbehaving caller cannot
// exceed it: each statement runs in its own read-only REPEATABLE READ transaction under SET LOCAL
// statement_timeout, at most MaxConcurrentStatements run at once per process (MaxDigestStatements
// of them digests or intersections), a statement that cannot get a slot within BusyWait is
// refused SEAM_BUSY, and a stored value longer than intent.MaxIntentValueBytes is never read.
package intentseam

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/intent"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"gorm.io/gorm"
)

// The seam's budgets. They are constants of device-management, not configuration: they are what
// the cost gate measures against.
const (
	// DigestTimeout bounds one digest statement.
	DigestTimeout = 20 * time.Second
	// IntersectTimeout bounds one target-intersection statement.
	IntersectTimeout = 20 * time.Second
	// PageTimeout bounds one page statement.
	PageTimeout = 5 * time.Second
	// PreviewTimeout bounds one bulk-change preview.
	PreviewTimeout = 30 * time.Second
	// MaxConcurrentStatements is the most seam statements one process runs at once, so the
	// seam never holds more of its database connections than this.
	MaxConcurrentStatements = 4
	// MaxDigestStatements is how many of those may be digests or intersections, the two
	// statements that evaluate a whole target.
	MaxDigestStatements = 2
	// BusyWait is how long a statement waits for a slot before it is refused SEAM_BUSY.
	BusyWait = 2 * time.Second
	// MaxPageRows bounds one page.
	MaxPageRows = 1000
	// PageByteBudget bounds one page's rows as they serialize, under svcclient's 1 MiB response
	// cap with margin. Rows are fixed-size, so a 1,000-row page is about a quarter of it; the
	// budget ends a page early only if a row ever grows.
	PageByteBudget = 512 << 10
)

// Codes a seam answer or refusal carries. A rejection is a decided answer about the target; an
// error with one of the Seam* codes means no answer was produced and none may be inferred.
const (
	// CodeProfileVersionMissing: a member's profile names an active version that does not exist.
	// That is an invariant breach, reported as loudly as the configuration document reports it,
	// never as a member that declares nothing.
	CodeProfileVersionMissing = "PROFILE_VERSION_MISSING"
	// CodeTargetTooLarge: the target has more members than an intent may observe. Refused,
	// never truncated.
	CodeTargetTooLarge = "TARGET_TOO_LARGE"
	// CodeSeamBusy: no statement slot was free within BusyWait.
	CodeSeamBusy = "SEAM_BUSY"
	// CodeSeamTimeout: the statement exceeded its timeout. No partial answer exists.
	CodeSeamTimeout = "SEAM_TIMEOUT"
)

// Error is a refusal to produce an answer. It rides GraphQL extensions.code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string              { return e.Message }
func (e *Error) Extensions() map[string]any { return map[string]any{"code": e.Code} }
func seamErr(code, format string, a ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// ErrPostgresOnly is returned on any other database: the seam's statements are PostgreSQL.
var ErrPostgresOnly = errors.New("the intent observation seam requires PostgreSQL")

// The statement kinds, as metric labels.
const (
	OpDigest    = "digest"
	OpPage      = "page"
	OpIntersect = "intersect"
	OpPreview   = "preview"
)

var (
	seamStatements = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "devicechain_devicemanagement_intent_seam_statements_total",
		Help: "Intent observation seam statements, by statement and result.",
	}, []string{"op", "result"})
	seamDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "devicechain_devicemanagement_intent_seam_statement_duration_seconds",
		Help:    "Wall time of intent observation seam statements that ran, by statement.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30},
	}, []string{"op"})
)

func init() {
	for _, op := range []string{OpDigest, OpPage, OpIntersect, OpPreview} {
		for _, result := range []string{"ok", "rejected", "timeout", "busy", "error"} {
			seamStatements.WithLabelValues(op, result).Add(0)
		}
		seamDuration.WithLabelValues(op)
	}
}

// Seam runs the observation statements. One Seam is shared by every request of a process, so its
// limiter is the per-process bound.
type Seam struct {
	all, heavy chan struct{}
	busyWait   time.Duration
	timeouts   map[string]time.Duration
	pageBytes  int
	// maxMembers is the most members a digest observes before it refuses TARGET_TOO_LARGE:
	// intent.MaxDevicesPerIntent.
	maxMembers int
	// heldHook, when set, runs while a statement holds its slot, before the statement; tests use
	// it to hold slots open.
	heldHook func(op string)
}

// New returns a Seam with the platform's budgets.
func New() *Seam {
	return &Seam{
		all:      make(chan struct{}, MaxConcurrentStatements),
		heavy:    make(chan struct{}, MaxDigestStatements),
		busyWait: BusyWait,
		timeouts: map[string]time.Duration{
			OpDigest: DigestTimeout, OpPage: PageTimeout, OpIntersect: IntersectTimeout, OpPreview: PreviewTimeout,
		},
		pageBytes:  PageByteBudget,
		maxMembers: intent.MaxDevicesPerIntent,
	}
}

func heavyOp(op string) bool { return op == OpDigest || op == OpIntersect }

// acquire takes a statement slot (and a digest slot for a heavy statement) or refuses SEAM_BUSY
// once busyWait has passed. What it took is released by the returned function.
func (s *Seam) acquire(ctx context.Context, op string) (func(), error) {
	timer := time.NewTimer(s.busyWait)
	defer timer.Stop()
	take := func(ch chan struct{}) error {
		select {
		case ch <- struct{}{}:
			return nil
		case <-timer.C:
			return seamErr(CodeSeamBusy, "the observation seam is at its concurrency limit; retry later")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var held []chan struct{}
	release := func() {
		for _, ch := range held {
			<-ch
		}
	}
	if heavyOp(op) {
		if err := take(s.heavy); err != nil {
			return nil, err
		}
		held = append(held, s.heavy)
	}
	if err := take(s.all); err != nil {
		release()
		return nil, err
	}
	held = append(held, s.all)
	return release, nil
}

// Run executes fn as one seam statement of kind op: inside a slot, in a read-only REPEATABLE READ
// transaction (so every query fn makes reads one snapshot), under SET LOCAL statement_timeout. A
// timeout is a SEAM_TIMEOUT error, never a partial answer. fn reports whether its answer was a
// rejection, for the metric. The bulk-change preview runs through Run as well, so it shares the
// limiter and has its own timeout.
func (s *Seam) Run(ctx context.Context, db *gorm.DB, op string, fn func(tx *gorm.DB) (rejected bool, err error)) error {
	if s == nil {
		return errors.New("the intent observation seam is not wired")
	}
	timeout, ok := s.timeouts[op]
	if !ok {
		return fmt.Errorf("unknown seam statement %q", op)
	}
	release, err := s.acquire(ctx, op)
	if err != nil {
		seamStatements.WithLabelValues(op, resultOf(err, false)).Inc()
		return err
	}
	defer release()
	if s.heldHook != nil {
		s.heldHook(op)
	}
	start := time.Now()
	var rejected bool
	db = db.WithContext(ctx)
	postgres := db.Dialector.Name() == "postgres"
	var opts []*sql.TxOptions
	if postgres {
		opts = append(opts, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if postgres {
			if err := tx.Exec(fmt.Sprintf("SET LOCAL statement_timeout = %d", timeout.Milliseconds())).Error; err != nil {
				return err
			}
			// Plan every execution for its own tenant and selector values. The driver prepares
			// and caches statements per connection, and after five executions PostgreSQL may
			// switch a cached statement to a generic plan that ignores them; for a statement whose
			// right plan depends on which tenant and which selector it is, a generic plan was
			// measured an order of magnitude slower on the cost fixture.
			if err := tx.Exec("SET LOCAL plan_cache_mode = force_custom_plan").Error; err != nil {
				return err
			}
		}
		var ferr error
		rejected, ferr = fn(tx)
		return ferr
	}, opts...)
	seamDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
	if isStatementTimeout(err) {
		err = seamErr(CodeSeamTimeout, "the %s statement exceeded its %s budget; no partial answer is given", op, timeout)
	}
	seamStatements.WithLabelValues(op, resultOf(err, rejected)).Inc()
	return err
}

func isStatementTimeout(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "57014"
}

func resultOf(err error, rejected bool) string {
	var se *Error
	switch {
	case err == nil && rejected:
		return "rejected"
	case err == nil:
		return "ok"
	case errors.As(err, &se) && se.Code == CodeSeamBusy:
		return "busy"
	case errors.As(err, &se) && se.Code == CodeSeamTimeout:
		return "timeout"
	}
	return "error"
}
