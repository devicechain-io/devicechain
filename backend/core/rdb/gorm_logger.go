// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/utils"
)

// slowStatementThreshold is gorm's own default (logger.Default's), kept so that moving a
// connection onto gormLogger changes the FORMAT of what an operator sees and nothing else.
// Deliberately not configurable.
const slowStatementThreshold = 200 * time.Millisecond

// gormLogger is the one logger every connection core opens hands to gorm (owned and guest
// alike — see ownedGormConfig and guestGormConfig). It exists because gorm's fallback,
// logger.Default, writes coloured multi-line text straight to os.Stdout: outside the
// service's JSON log, without its instance/area fields, deaf to the configured log
// level, and with every bound value interpolated into the statement it prints.
//
// Its rules, in the order Trace applies them:
//   - a statement that failed is an error line — unless the failure is "no rows"
//     (gorm.ErrRecordNotFound), which is an answer, not a failure, and is never an error
//     line at any level;
//   - a statement slower than the threshold is a warn line, a no-rows one included (a
//     slow lookup that finds nothing is exactly the shape a missing index takes);
//   - with sqlDebug on (gorm's Info level, set by db.Debug()) every statement is an info
//     line, and those lines are then filtered by the configured zerolog level like any
//     other info line.
//
// Bound values never reach the sql field: ParamsFilter drops them for every statement, at
// every level. That is a claim about the sql field only. A database's own error text can
// quote the value it rejected ("invalid input syntax for type uuid: …"), and that text is
// the error field, verbatim.
type gormLogger struct {
	// level is gorm's own gate: logger.Warn for a connection as opened, logger.Info for a
	// session made by db.Debug() (sqlDebug). LogMode copies; it never mutates this one.
	level logger.LogLevel
	slow  time.Duration
	// out is where lines go. nil — the only value production ever uses — means the global
	// log.Logger AS IT IS AT EACH CALL, never a copy taken when the connection opened:
	// core.NewMicroservice reassigns the global to add the instance/area/tenant fields,
	// and a copy taken before that would log without them. Tests set it so they never
	// write the global.
	out *zerolog.Logger
}

var (
	_ logger.Interface  = (*gormLogger)(nil)
	_ gorm.ParamsFilter = (*gormLogger)(nil)
)

func newGormLogger() *gormLogger {
	return &gormLogger{level: logger.Warn, slow: slowStatementThreshold}
}

func (l *gormLogger) sink() *zerolog.Logger {
	if l.out != nil {
		return l.out
	}
	return &log.Logger
}

// LogMode returns a copy at the given level. It must not change the receiver: db.Debug()
// calls it on the connection's own logger, and mutating it would turn every other session
// on that connection to Info.
func (l *gormLogger) LogMode(lvl logger.LogLevel) logger.Interface {
	c := *l
	c.level = lvl
	return &c
}

// Info, Warn and Error carry gorm's own messages (a failed open, a duplicated callback).
// utils.FileWithLineNum is called directly in each: it skips a fixed number of frames, so
// calling it from a shared helper would name this file instead of the code that called gorm.

func (l *gormLogger) Info(_ context.Context, msg string, args ...any) {
	if l.level >= logger.Info {
		l.sink().Info().Str("caller", utils.FileWithLineNum()).Msg(fmt.Sprintf(msg, args...))
	}
}

func (l *gormLogger) Warn(_ context.Context, msg string, args ...any) {
	if l.level >= logger.Warn {
		l.sink().Warn().Str("caller", utils.FileWithLineNum()).Msg(fmt.Sprintf(msg, args...))
	}
}

func (l *gormLogger) Error(_ context.Context, msg string, args ...any) {
	if l.level >= logger.Error {
		l.sink().Error().Str("caller", utils.FileWithLineNum()).Msg(fmt.Sprintf(msg, args...))
	}
}

// Trace logs one executed statement, by the rules on gormLogger.
//
// "No rows" is recognised with errors.Is, which has one known blind spot: gorm joins a
// second error onto a first as "%v; %w", so a real failure followed by ErrRecordNotFound
// reads as not-found. gorm's own logger shares it, and gorm's query callbacks do not run
// once a statement already carries an error, so the shape does not arise from gorm itself.
func (l *gormLogger) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= logger.Silent {
		return
	}
	elapsed := time.Since(begin)
	notFound := err != nil && errors.Is(err, gorm.ErrRecordNotFound)

	var e *zerolog.Event
	var msg string
	switch {
	case err != nil && !notFound && l.level >= logger.Error:
		e, msg = l.sink().Error().Err(err), "database statement failed"
	case l.slow > 0 && elapsed > l.slow && l.level >= logger.Warn:
		e, msg = l.sink().Warn().Float64("threshold_ms", float64(l.slow)/float64(time.Millisecond)),
			"slow database statement"
	case l.level >= logger.Info:
		e, msg = l.sink().Info(), "database statement"
	default:
		return
	}
	if e == nil {
		// The zerolog level drops this line; skip rendering the statement at all.
		return
	}
	sql, rows := fc()
	e = e.Str("sql", sql)
	if rows != -1 {
		e = e.Int64("rows", rows)
	}
	// Computed explicitly, so zerolog.DurationFieldUnit cannot change what the field means.
	e.Float64("elapsed_ms", float64(elapsed)/float64(time.Millisecond)).
		Str("caller", utils.FileWithLineNum()).
		Msg(msg)
}

// ParamsFilter implements gorm.ParamsFilter: gorm renders a logged statement from what this
// returns, and with no values it renders the statement's placeholders. Unconditional — at
// every level, sqlDebug included — because a switch to bring the values back would be a
// setting whose only effect is to write credentials and tenant tokens into the log.
func (l *gormLogger) ParamsFilter(_ context.Context, sql string, _ ...any) (string, []any) {
	return sql, nil
}
