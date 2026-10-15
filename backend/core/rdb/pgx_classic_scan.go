// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

// Every Postgres pool this package opens goes through newPostgresDialector, which hands
// gorm a *sql.DB whose rows do NOT advertise database/sql/driver.RowsColumnScanner.
//
// Why: Go 1.27 added driver.RowsColumnScanner, and pgx v5.11.0 implements it on
// *stdlib.Rows (stdlib/sql_go1.27.go, build tag go1.27). With both present, database/sql
// scans through (*stdlib.Rows).ScanColumn for EVERY column of EVERY row, and ScanColumn
// calls isSQLScanDestination(reflect.Type) each time — three or more uncached
// reflect.Type.Implements calls (sql.Scanner, pgtype.ArraySetter, pgtype.BytesScanner,
// and again per pointer level). On the GKE round-9 profile that was ~18% of
// device-state's CPU. Withholding the interface puts database/sql back on the classic
// driver.Rows.Next(dest []driver.Value) path, which is what pgx v5.10 and Go 1.26 ran.
//
// What is withheld is EXACTLY NextRow/ScanColumn. Everything else *stdlib.Conn,
// *stdlib.Stmt and *stdlib.Rows implement is kept by struct embedding, because
// database/sql decides behaviour by optional-interface assertions and a lost one changes
// it silently (no QueryerContext ⇒ prepare-per-query; no SessionResetter ⇒ no reset on
// reuse; no ColumnType* ⇒ gorm's column types go blank). TestPgxClassicScanDrift
// compares the method sets reflectively and fails if any is lost — or if a future pgx
// moves the conn/rows/stmt off the concrete types embedded here.
//
// EXIT CONDITION: delete this file — and call postgres.New(postgres.Config{DSN: dsn})
// directly again — once pgx caches isSQLScanDestination per reflect.Type (upstream:
// github.com/jackc/pgx/v5, stdlib/sql_go1.27.go, func isSQLScanDestination, called from
// (*Rows).ScanColumn). Re-run BenchmarkPgxScan to confirm before deleting.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// newPostgresDialector is postgres.New(cfg) with the pool built here rather than by the
// dialector. For a cfg that names neither Conn nor DriverName it replicates, step for
// step, gorm.io/driver/postgres v1.6.3's Dialector.Initialize: parse the DSN with pgx,
// honour PreferSimpleProtocol, apply the DSN's time zone as a runtime parameter and a
// timestamp scan location, append cfg.OptionOpenDB — then opens the pool through
// classicRowsConnector instead of stdlib.OpenDB.
//
// Like the dialector it replaces, it does not dial: the first connection is made by
// gorm.Open's ping, or by the caller's. If gorm.Open fails it closes this pool itself
// (it closes whatever db.DB() returns).
func newPostgresDialector(cfg postgres.Config) (gorm.Dialector, error) {
	if cfg.Conn != nil || cfg.DriverName != "" {
		return postgres.New(cfg), nil
	}
	config, err := pgx.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, err
	}
	if cfg.PreferSimpleProtocol {
		config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	}
	opts := append([]stdlib.OptionOpenDB(nil), cfg.OptionOpenDB...)
	if result := timeZoneMatcher.FindStringSubmatch(cfg.DSN); len(result) > 2 {
		config.RuntimeParams["timezone"] = result[2]
		opts = append(opts, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
			loc, tzErr := time.LoadLocation(result[2])
			if tzErr != nil {
				return tzErr
			}
			conn.TypeMap().RegisterType(&pgtype.Type{
				Name:  "timestamp",
				OID:   pgtype.TimestampOID,
				Codec: &pgtype.TimestampCodec{ScanLocation: loc},
			})
			return nil
		}))
	}
	cfg.Conn = sql.OpenDB(classicRowsConnector{stdlib.GetConnector(*config, opts...)})
	cfg.OptionOpenDB = nil // consumed above; Initialize ignores it once Conn is set
	return postgres.New(cfg), nil
}

// timeZoneMatcher is gorm.io/driver/postgres's own pattern (unexported there), copied
// verbatim so a DSN's time zone is honoured exactly as the dialector honoured it.
var timeZoneMatcher = regexp.MustCompile("(time_zone|TimeZone|timezone)=(.*?)($|&| )")

// Aliases so the embedded fields are not named Conn/Stmt/Rows: a field named Conn would
// shadow, and so drop, (*stdlib.Conn).Conn() *pgx.Conn.
type (
	pgxStdConn = stdlib.Conn
	pgxStdStmt = stdlib.Stmt
	pgxStdRows = stdlib.Rows
)

// classicRowsConnector wraps pgx's connector so every connection it makes hands back
// classicRows. pgx's connector implements Connect and Driver only (no io.Closer); the
// drift test holds that.
type classicRowsConnector struct{ driver.Connector }

func (c classicRowsConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	sc, ok := conn.(*stdlib.Conn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("rdb: pgx connector returned %T, not *stdlib.Conn; see pgx_classic_scan.go", conn)
	}
	return &classicRowsConn{sc}, nil
}

// classicRowsConn is *stdlib.Conn with the two row-returning entry points re-wrapped.
// Every other method — and so every optional driver interface — is promoted unchanged.
type classicRowsConn struct{ *pgxStdConn }

func (c *classicRowsConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return wrapClassicRows(c.pgxStdConn.QueryContext(ctx, query, args))
}

func (c *classicRowsConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *classicRowsConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	s, err := c.pgxStdConn.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	ss, ok := s.(*stdlib.Stmt)
	if !ok {
		_ = s.Close()
		return nil, fmt.Errorf("rdb: pgx prepared %T, not *stdlib.Stmt; see pgx_classic_scan.go", s)
	}
	return &classicRowsStmt{ss}, nil
}

// classicRowsStmt is *stdlib.Stmt whose QueryContext returns classicRows. (Its Query is
// pgx's "deprecated and not implemented" error, promoted as is.)
type classicRowsStmt struct{ *pgxStdStmt }

func (s *classicRowsStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return wrapClassicRows(s.pgxStdStmt.QueryContext(ctx, args))
}

// classicRows is *stdlib.Rows minus driver.RowsColumnScanner. The two fields below sit at
// depth 0, so they shadow the depth-1 methods of the same names: x.NextRow selects the
// field, the methods are not promoted, and the type's method set has neither. Everything
// else *stdlib.Rows implements (Columns, Close, Next, the ColumnType* family, and
// anything a future pgx adds) is promoted.
type classicRows struct {
	*pgxStdRows

	NextRow    struct{} // withholds (*stdlib.Rows).NextRow
	ScanColumn struct{} // withholds (*stdlib.Rows).ScanColumn
}

func wrapClassicRows(r driver.Rows, err error) (driver.Rows, error) {
	if err != nil {
		return nil, err
	}
	sr, ok := r.(*stdlib.Rows)
	if !ok {
		_ = r.Close()
		return nil, fmt.Errorf("rdb: pgx returned rows of %T, not *stdlib.Rows; see pgx_classic_scan.go", r)
	}
	return &classicRows{pgxStdRows: sr}, nil
}
