// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/glebarez/sqlite"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// captureGormLogger is the production logger at its production gorm level, writing to a
// buffer through a zerolog logger at lvl. It never touches the global log.Logger (see
// core/test/logsink.go for why a test must not).
func captureGormLogger(lvl zerolog.Level) (*gormLogger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	zl := zerolog.New(buf).Level(lvl)
	l := newGormLogger()
	l.out = &zl
	return l, buf
}

// logLines parses the buffer as one JSON object per line, failing on anything that is not
// JSON: the old logger's coloured text is exactly what must never appear.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &m), "a log line that is not JSON: %q", raw)
		out = append(out, m)
	}
	return out
}

// uniqueThing gives the tests a constraint a write can violate.
type uniqueThing struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"uniqueIndex"`
}

// openLoggedFenceDB is newFenceDB on the given logger: scoping, fence and audit registered
// in production's order. The buffer is emptied once setup is done.
func openLoggedFenceDB(t *testing.T, l *gormLogger, buf *bytes.Buffer) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: l})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqldb, err := db.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	require.NoError(t, RegisterTenantScoping(db))
	require.NoError(t, RegisterTenantFence(db))
	require.NoError(t, RegisterAuditJournal(db))
	require.NoError(t, db.AutoMigrate(&PurgedTenant{}, &AuditEvent{}, &widget{}, &uniqueThing{}))
	buf.Reset()
	return db
}

var callerInThisFile = regexp.MustCompile(`/gorm_logger_test\.go:\d+$`)

// The ruling's test: an ordinary fenced write — a create and an update for a tenant with
// no fence standing, while another tenant IS fenced — and a lookup that finds nothing log
// nothing at all at info. Then, on the same logger, a statement that really fails must
// log exactly one structured error line; without that, an empty buffer could be a sink
// that was never attached.
func TestAFencedWriteLogsNothingAtInfo(t *testing.T) {
	l, buf := captureGormLogger(zerolog.InfoLevel)
	db := openLoggedFenceDB(t, l, buf)
	plant(t, db, "acme")
	buf.Reset()

	beta := core.WithTenant(context.Background(), "beta")
	require.NoError(t, db.WithContext(beta).Create(&widget{Name: "admitted"}).Error)
	require.NoError(t, db.WithContext(beta).Model(&widget{}).
		Where("name = ?", "admitted").Update("name", "renamed").Error)
	var w widget
	err := db.WithContext(beta).First(&w, "name = ?", "absent").Error
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "premise: the lookup must miss")
	require.Empty(t, buf.String(), "a fenced write and a lookup miss logged something")

	require.Error(t, db.Exec("INSERT INTO no_such_table VALUES (?)", "x").Error)
	lines := logLines(t, buf)
	require.Len(t, lines, 1, "a failed statement must log exactly one line")
	line := lines[0]
	require.Equal(t, "error", line["level"])
	require.Equal(t, "database statement failed", line["message"])
	require.Contains(t, line["sql"], "no_such_table")
	require.Contains(t, line["error"], "no such table")
	require.NotNil(t, line["elapsed_ms"])
	require.Regexp(t, callerInThisFile, line["caller"], "caller must name the code that issued the statement")
}

// The sql field carries placeholders, never the values bound to them. The control shows the
// probe value really reaches a logger: gorm's own logger, configured as its default is,
// prints it.
func TestTheLoggerNeverWritesABoundValue(t *testing.T) {
	const probe = "sekrit-7f3a"
	l, buf := captureGormLogger(zerolog.InfoLevel)
	db := openLoggedFenceDB(t, l, buf)

	beta := db.WithContext(core.WithTenant(context.Background(), "beta"))
	require.NoError(t, beta.Create(&uniqueThing{Name: probe}).Error)
	buf.Reset()
	err := beta.Create(&uniqueThing{Name: probe}).Error
	require.ErrorContains(t, err, "UNIQUE", "premise: a duplicate must fail on the constraint")
	require.Error(t, db.Exec("INSERT INTO no_such_table VALUES (?)", probe).Error)

	lines := logLines(t, buf)
	require.Len(t, lines, 2, "two failed statements, two lines")
	for _, line := range lines {
		require.Equal(t, "error", line["level"])
		sql, _ := line["sql"].(string)
		require.Contains(t, sql, "?", "the statement must show its placeholder")
		require.NotContains(t, sql, probe, "a bound value reached the sql field")
	}

	var control bytes.Buffer
	gormDefault := logger.New(log.New(&control, "", 0), logger.Config{LogLevel: logger.Warn})
	require.Error(t, db.Session(&gorm.Session{Logger: gormDefault}).
		Exec("INSERT INTO no_such_table VALUES (?)", probe).Error)
	require.Contains(t, control.String(), probe, "control: gorm's own logger must show the value")

	// What the claim does NOT cover, pinned so the documentation cannot overstate it: a
	// database's own error text can quote the value it rejected, and it is logged verbatim.
	buf.Reset()
	l.Trace(context.Background(), time.Now(), func() (string, int64) { return "SELECT ?", 0 },
		fmt.Errorf(`invalid input syntax for type uuid: %q`, probe))
	lines = logLines(t, buf)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0]["error"], probe)
}

// sqlDebug (db.Debug()) logs every statement at info, still without values, and a lookup
// miss there carries no error field. The same session under a warn zerolog level logs
// nothing: the configured level now governs sqlDebug. And Debug() must not have turned
// the connection itself to Info.
func TestSqlDebugStillLogsEveryStatementWithoutValues(t *testing.T) {
	const name = "debug-widget-9c1e"
	l, buf := captureGormLogger(zerolog.InfoLevel)
	root := openLoggedFenceDB(t, l, buf)
	debug := root.Debug()
	beta := core.WithTenant(context.Background(), "beta")

	require.NoError(t, debug.WithContext(beta).Create(&widget{Name: name}).Error)
	var w widget
	require.ErrorIs(t, debug.WithContext(beta).First(&w, "name = ?", "absent").Error, gorm.ErrRecordNotFound)

	lines := logLines(t, buf)
	require.GreaterOrEqual(t, len(lines), 3, "fence read, insert and lookup at least")
	fence, miss := 0, 0
	for _, line := range lines {
		require.Equal(t, "info", line["level"])
		require.Equal(t, "database statement", line["message"])
		require.NotContains(t, fmt.Sprint(line), name, "a bound value reached a sqlDebug line")
		require.NotContains(t, line, "error", "a sqlDebug line reported an error: %v", line)
		sql, _ := line["sql"].(string)
		if strings.Contains(sql, FenceTable) {
			fence++
			// The statement's call site, not this logger and not gorm: the one field that
			// says where a line came from.
			require.Regexp(t, `/rdb/tenant_fence\.go:\d+$`, line["caller"])
		}
		if strings.Contains(sql, "name = ?") {
			miss++
		}
	}
	require.Equal(t, 1, fence, "exactly one fence read")
	require.Equal(t, 1, miss, "exactly one lookup miss")

	buf.Reset()
	require.NoError(t, root.WithContext(beta).Create(&widget{Name: "quiet"}).Error)
	require.Empty(t, buf.String(), "Debug() changed the connection's own logger")

	lw, bufw := captureGormLogger(zerolog.WarnLevel)
	dbw := openLoggedFenceDB(t, lw, bufw).Debug()
	require.NoError(t, dbw.WithContext(beta).Create(&widget{Name: name}).Error)
	require.Empty(t, bufw.String(), "sqlDebug lines must obey a warn log level")
}

func traceLines(t *testing.T, l *gormLogger, buf *bytes.Buffer, begin time.Time, rows int64, err error) []map[string]any {
	t.Helper()
	buf.Reset()
	l.Trace(context.Background(), begin, func() (string, int64) { return "SELECT 1", rows }, err)
	return logLines(t, buf)
}

func TestASlowStatementWarns(t *testing.T) {
	l, buf := captureGormLogger(zerolog.TraceLevel)
	l.slow = time.Nanosecond
	secondAgo := time.Now().Add(-time.Second)

	for _, err := range []error{nil, gorm.ErrRecordNotFound} {
		lines := traceLines(t, l, buf, secondAgo, 3, err)
		require.Len(t, lines, 1, "err=%v", err)
		require.Equal(t, "warn", lines[0]["level"])
		require.Equal(t, "slow database statement", lines[0]["message"])
		require.GreaterOrEqual(t, lines[0]["elapsed_ms"], float64(1000))
		require.InDelta(t, 0.000001, lines[0]["threshold_ms"], 1e-9)
		require.Equal(t, float64(3), lines[0]["rows"])
		require.NotContains(t, lines[0], "error", "a slow miss is slow, not failed")
	}

	l.slow = time.Hour
	require.Empty(t, traceLines(t, l, buf, secondAgo, 3, nil), "control: under the threshold")
}

func TestTraceTable(t *testing.T) {
	boom := errors.New("boom")
	slow := time.Now().Add(-time.Second)
	cases := []struct {
		name   string
		level  logger.LogLevel
		begin  time.Time
		err    error
		want   string // "" = no line; else the zerolog level of the one line
		hasErr bool
	}{
		{"miss at warn", logger.Warn, time.Now(), gorm.ErrRecordNotFound, "", false},
		{"wrapped miss at warn", logger.Warn, time.Now(), fmt.Errorf("x: %w", gorm.ErrRecordNotFound), "", false},
		{"failure at warn", logger.Warn, time.Now(), boom, "error", true},
		{"failure at error", logger.Error, time.Now(), boom, "error", true},
		{"failure when silent", logger.Silent, time.Now(), boom, "", false},
		{"slow at error", logger.Error, slow, nil, "", false},
		{"slow miss at warn", logger.Warn, slow, gorm.ErrRecordNotFound, "warn", false},
		{"miss at info", logger.Info, time.Now(), gorm.ErrRecordNotFound, "info", false},
		{"success at info", logger.Info, time.Now(), nil, "info", false},
		{"success at warn", logger.Warn, time.Now(), nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, buf := captureGormLogger(zerolog.TraceLevel)
			l.level = c.level
			lines := traceLines(t, l, buf, c.begin, 1, c.err)
			if c.want == "" {
				require.Empty(t, lines)
				return
			}
			require.Len(t, lines, 1)
			require.Equal(t, c.want, lines[0]["level"])
			_, has := lines[0]["error"]
			require.Equal(t, c.hasErr, has, "error field")
			require.Equal(t, "SELECT 1", lines[0]["sql"])
		})
	}

	l, buf := captureGormLogger(zerolog.TraceLevel)
	l.level = logger.Info
	lines := traceLines(t, l, buf, time.Now(), -1, nil)
	require.Len(t, lines, 1)
	require.NotContains(t, lines[0], "rows", "rows -1 means unknown and must be omitted")
	lines = traceLines(t, l, buf, time.Now(), 0, nil)
	require.Equal(t, float64(0), lines[0]["rows"], "zero rows is a count, not unknown")
}

// gorm's own messages (a failed open, a duplicated callback) go through the same logger,
// under the same gorm gate.
func TestGormsOwnMessagesAreStructured(t *testing.T) {
	l, buf := captureGormLogger(zerolog.TraceLevel)
	l.Error(context.Background(), "failed to initialize database, got error %v", boom7)
	l.Warn(context.Background(), "duplicated callback `%s`", "x")
	l.Info(context.Background(), "replacing callback `%s`", "y") // below the Warn gate
	lines := logLines(t, buf)
	require.Len(t, lines, 2)
	require.Equal(t, "error", lines[0]["level"])
	require.Equal(t, "failed to initialize database, got error boom7", lines[0]["message"])
	require.Regexp(t, callerInThisFile, lines[0]["caller"])
	require.Equal(t, "warn", lines[1]["level"])
	require.Equal(t, "duplicated callback `x`", lines[1]["message"])
}

var boom7 = errors.New("boom7")

// The caller test: both connections core opens hand gorm this logger, reading the global
// logger at each call (out == nil), and keep the rest of their configuration.
func TestOwnedAndGuestConnectionsLogThroughTheServiceLogger(t *testing.T) {
	owned := ownedGormConfig("event-management")
	ol, ok := owned.Logger.(*gormLogger)
	require.True(t, ok, "owned connection logger is %T", owned.Logger)
	require.Nil(t, ol.out)
	require.Equal(t, logger.Warn, ol.level)
	require.Equal(t, slowStatementThreshold, ol.slow)
	ns, ok := owned.NamingStrategy.(schema.NamingStrategy)
	require.True(t, ok, "owned naming strategy is %T", owned.NamingStrategy)
	require.Equal(t, "event-management.", ns.TablePrefix)
	require.False(t, ns.SingularTable)

	guest := guestGormConfig()
	gl, ok := guest.Logger.(*gormLogger)
	require.True(t, ok, "guest connection logger is %T", guest.Logger)
	require.Nil(t, gl.out)
	require.Equal(t, logger.Warn, gl.level)
	require.True(t, guest.DisableAutomaticPing)
	require.Nil(t, guest.NamingStrategy)
}
