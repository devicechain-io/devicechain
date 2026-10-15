// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// pgx_classic_scan.go against a real PostgreSQL server: the wrapped pool must scan every
// destination shape this codebase uses into exactly what the dialector's own pool scans
// it into, its connections and rows must really be the wrappers (on the owned AND the
// guest open path), and the benchmark at the bottom is the number the file exists for.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 -run PgxClassic ./rdb/ -v
//	DC_IT_PGPORT=$PORT go test -tags integration -run '^$' -bench PgxScan -benchmem ./rdb/
package rdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
)

const pgxClassicDatabase = "rdbpgxclassic"

func pgxClassicDSN(t testing.TB, extra string) string {
	t.Helper()
	host, port := itServer(t)
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), host, port, "postgres", "postgres",
		pgxClassicDatabase, ""))
	dsn := postgresKeywordDSN("postgres", "postgres", host, int32(port), pgxClassicDatabase, "disable", nil)
	if extra != "" {
		dsn += " " + extra
	}
	return dsn
}

// openPgxPair opens the same database twice: through the dialector's own pool (what
// every service ran before) and through newPostgresDialector.
func openPgxPair(t testing.TB, dsn string, cfg func() *gorm.Config) (native, classic *gorm.DB) {
	t.Helper()
	var err error
	native, err = gorm.Open(postgres.New(postgres.Config{DSN: dsn}), cfg())
	require.NoError(t, err)
	d, err := newPostgresDialector(postgres.Config{DSN: dsn})
	require.NoError(t, err)
	classic, err = gorm.Open(d, cfg())
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, db := range []*gorm.DB{native, classic} {
			if s, err := db.DB(); err == nil {
				_ = s.Close()
			}
		}
	})
	return native, classic
}

func quietGorm() *gorm.Config { return &gorm.Config{Logger: logger.Discard} }

// upperScanner is a destination that is a sql.Scanner and nothing else database/sql knows.
type upperScanner string

func (u *upperScanner) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*u = "<NULL>"
	case string:
		*u = upperScanner(strings.ToUpper(v))
	case []byte:
		*u = upperScanner(strings.ToUpper(string(v)))
	default:
		return fmt.Errorf("upperScanner: %T", src)
	}
	return nil
}

const pgxClassicTable = `
DROP TABLE IF EXISTS pgx_classic_parity;
CREATE TABLE pgx_classic_parity (
	id     bigint PRIMARY KEY,
	i      integer,
	i64    bigint,
	small  smallint,
	f      real,
	d      double precision,
	num    numeric(12,4),
	b      boolean,
	s      text,
	vc     varchar(20),
	ch     char(5),
	by     bytea,
	ts     timestamptz,
	tsn    timestamp,
	dt     date,
	u      uuid,
	js     jsonb,
	jn     json,
	arr    text[],
	iv     interval
);
INSERT INTO pgx_classic_parity VALUES
	(1, 42, 9007199254740993, -7, 1.5, 3.141592653589793, 12345678.1234, true, 'héllo', 'vc', 'ab',
	 '\xdeadbeef', '2026-10-14 12:34:56.789012+02', '2026-10-14 12:34:56.789012', '2026-10-14',
	 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', '{"k":[1,2,{"z":null}]}', '{"j": "v"}', '{a,"b c",NULL}',
	 '1 day 02:03:04'),
	(2, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
	 NULL, NULL, NULL, NULL),
	(3, 0, 0, 0, 0, 0, 0, false, '', '', '', '\x', '0001-01-01 00:00:00+00', '0001-01-01 00:00:00',
	 '0001-01-01', '00000000-0000-0000-0000-000000000000', 'null', '[]', '{}', '0');
`

// The destination shapes: plain values (populated rows only — a NULL into a plain value
// is an error, compared separately), pointers, sql.Null*, and named Scanner types.
type parityValues struct {
	ID    int64
	I     int
	I64   int64
	Small int16
	F     float32
	D     float64
	Num   string
	B     bool
	S     string
	Vc    string
	Ch    string
	By    []byte
	Ts    time.Time
	Tsn   time.Time
	Dt    time.Time
	U     uuid.UUID
	Js    datatypes.JSON
	Jn    datatypes.JSON
	Arr   string
	Iv    string
}

type parityPointers struct {
	ID    *int64
	I     *int
	I64   *int64
	Small *int16
	F     *float32
	D     *float64
	Num   *string
	B     *bool
	S     *string
	Vc    *string
	Ch    *string
	By    []byte
	Ts    *time.Time
	Tsn   *time.Time
	Dt    *time.Time
	U     *uuid.UUID
	Js    datatypes.JSON
	Jn    *datatypes.JSON
	Arr   *string
	Iv    *string
}

type parityNulls struct {
	ID    sql.NullInt64
	I     sql.NullInt32
	I64   sql.NullInt64
	Small sql.NullInt16
	F     sql.NullFloat64
	D     sql.NullFloat64
	Num   sql.NullString
	B     sql.NullBool
	S     sql.NullString
	Vc    sql.Null[string]
	Ch    upperScanner
	By    sql.Null[[]byte]
	Ts    sql.NullTime
	Tsn   sql.NullTime
	Dt    sql.NullTime
	U     uuid.NullUUID
	Js    sql.Null[datatypes.JSON]
	Jn    sql.NullString
	Arr   upperScanner
	Iv    sql.NullString
}

// selectParity reads every row but skipID; 0 skips nothing, since the ids are 1-3.
func selectParity(db *gorm.DB, skipID int) *gorm.DB {
	return db.Table("pgx_classic_parity").Where("id <> ?", skipID).Order("id")
}

// TestPgxClassicScanParity scans every shape through gorm on both pools and requires the
// results identical — and, so "identical" cannot be two pools agreeing on nothing,
// requires the populated row to have arrived populated.
func TestPgxClassicScanParity(t *testing.T) {
	native, classic := openPgxPair(t, pgxClassicDSN(t, ""), quietGorm)
	require.NoError(t, native.Exec(pgxClassicTable).Error)

	scanBoth := func(t *testing.T, skipID int, newDest func() any) (any, any) {
		t.Helper()
		n, c := newDest(), newDest()
		require.NoError(t, selectParity(native, skipID).Scan(n).Error, "native")
		require.NoError(t, selectParity(classic, skipID).Scan(c).Error, "classic")
		assert.Equal(t, n, c, "the wrapped pool scanned something different from the dialector's own")
		return n, c
	}

	t.Run("values", func(t *testing.T) {
		n, _ := scanBoth(t, 2, func() any { return &[]parityValues{} })
		rows := *n.(*[]parityValues)
		require.Len(t, rows, 2)
		r := rows[0]
		assert.Equal(t, 42, r.I)
		assert.Equal(t, int64(9007199254740993), r.I64)
		assert.Equal(t, "12345678.1234", r.Num)
		assert.Equal(t, "héllo", r.S)
		assert.Equal(t, "ab   ", r.Ch)
		assert.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, r.By)
		assert.Equal(t, 789012000, r.Ts.Nanosecond())
		assert.Equal(t, uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"), r.U)
		assert.JSONEq(t, `{"k":[1,2,{"z":null}]}`, string(r.Js))
		assert.Equal(t, `{a,"b c",NULL}`, r.Arr)
		assert.Equal(t, "1 day 02:03:04", r.Iv)
	})
	t.Run("pointers", func(t *testing.T) {
		n, _ := scanBoth(t, 0, func() any { return &[]parityPointers{} })
		rows := *n.(*[]parityPointers)
		require.Len(t, rows, 3)
		require.NotNil(t, rows[0].Ts)
		assert.Nil(t, rows[1].I)
		assert.Nil(t, rows[1].Ts)
		assert.Nil(t, rows[1].By)
		assert.Nil(t, rows[1].Js)
		require.NotNil(t, rows[2].By)
		assert.Empty(t, rows[2].By, "an empty bytea is not NULL")
	})
	t.Run("nulls and scanners", func(t *testing.T) {
		n, _ := scanBoth(t, 0, func() any { return &[]parityNulls{} })
		rows := *n.(*[]parityNulls)
		require.Len(t, rows, 3)
		assert.True(t, rows[0].Ts.Valid)
		assert.Equal(t, upperScanner("AB   "), rows[0].Ch)
		assert.False(t, rows[1].I.Valid)
		assert.False(t, rows[1].U.Valid)
		// gorm hands a NULL to a Scanner field as the zero value without calling Scan;
		// the direct database/sql subtest below is where the Scanner sees the NULL.
		assert.Equal(t, upperScanner(""), rows[1].Ch)
	})
	t.Run("maps", func(t *testing.T) {
		// gorm's map scan sizes each cell from ColumnTypes().ScanType, so this is where a
		// lost RowsColumnTypeScanType would show.
		n, _ := scanBoth(t, 0, func() any { return &[]map[string]any{} })
		require.Len(t, *n.(*[]map[string]any), 3)
	})
	t.Run("single columns", func(t *testing.T) {
		var nid, cid []int64
		require.NoError(t, selectParity(native, 0).Pluck("id", &nid).Error)
		require.NoError(t, selectParity(classic, 0).Pluck("id", &cid).Error)
		assert.Equal(t, []int64{1, 2, 3}, nid)
		assert.Equal(t, nid, cid)
		for _, col := range []string{"i", "ts", "tsn", "dt", "u", "js", "by", "num", "arr", "iv"} {
			n, c := []any{}, []any{}
			require.NoError(t, selectParity(native, 0).Pluck(col, &n).Error)
			require.NoError(t, selectParity(classic, 0).Pluck(col, &c).Error)
			assert.Equal(t, n, c, "pluck %s into []any", col)
		}
	})
	t.Run("database/sql directly", func(t *testing.T) {
		// Below gorm: rows.Scan into each struct's field addresses, which is the call
		// database/sql routes through ScanColumn on one pool and Next on the other.
		scanDirect := func(db *gorm.DB, skipID int, newRow func() any) []any {
			sqldb, err := db.DB()
			require.NoError(t, err)
			rows, err := sqldb.Query("SELECT * FROM pgx_classic_parity WHERE id <> $1 ORDER BY id", skipID)
			require.NoError(t, err)
			defer rows.Close()
			var out []any
			for rows.Next() {
				row := newRow()
				v := reflect.ValueOf(row).Elem()
				addrs := make([]any, v.NumField())
				for i := range addrs {
					addrs[i] = v.Field(i).Addr().Interface()
				}
				require.NoError(t, rows.Scan(addrs...))
				out = append(out, row)
			}
			require.NoError(t, rows.Err())
			return out
		}
		for _, tc := range []struct {
			name   string
			skipID int
			newRow func() any
		}{
			{"values", 2, func() any { return &parityValues{} }},
			{"pointers", 0, func() any { return &parityPointers{} }},
			{"nulls", 0, func() any { return &parityNulls{} }},
		} {
			n := scanDirect(native, tc.skipID, tc.newRow)
			assert.Equal(t, n, scanDirect(classic, tc.skipID, tc.newRow), tc.name)
			if tc.name == "nulls" {
				require.Len(t, n, 3)
				assert.Equal(t, upperScanner("<NULL>"), n[1].(*parityNulls).Ch, "the Scanner saw the NULL")
			}
		}

		// And the errors: a NULL into a plain value, a string into an int, an overflow.
		for _, q := range []string{"SELECT NULL::int", "SELECT 'x'::text", "SELECT 99999::int"} {
			scanErr := func(db *gorm.DB) error {
				sqldb, err := db.DB()
				require.NoError(t, err)
				var dest int8
				return sqldb.QueryRow(q).Scan(&dest)
			}
			nerr, cerr := scanErr(native), scanErr(classic)
			require.Error(t, nerr, q)
			require.Error(t, cerr, q)
			assert.Equal(t, nerr.Error(), cerr.Error(), q)
		}
	})
	t.Run("column types", func(t *testing.T) {
		describe := func(db *gorm.DB) []string {
			rows, err := selectParity(db, 0).Rows()
			require.NoError(t, err)
			defer rows.Close()
			cts, err := rows.ColumnTypes()
			require.NoError(t, err)
			var out []string
			for _, ct := range cts {
				l, lok := ct.Length()
				p, s, dok := ct.DecimalSize()
				nl, nok := ct.Nullable()
				out = append(out, fmt.Sprintf("%s %s len=%d/%v dec=%d,%d/%v null=%v/%v scan=%v",
					ct.Name(), ct.DatabaseTypeName(), l, lok, p, s, dok, nl, nok, ct.ScanType()))
			}
			return out
		}
		n := describe(native)
		assert.Equal(t, n, describe(classic))
		assert.Contains(t, n, "num NUMERIC len=0/false dec=12,4/true null=false/false scan=float64")
	})
	t.Run("prepared statements", func(t *testing.T) {
		pn, pc := openPgxPair(t, pgxClassicDSN(t, ""), func() *gorm.Config {
			return &gorm.Config{Logger: logger.Discard, PrepareStmt: true}
		})
		var n, c []parityNulls
		require.NoError(t, selectParity(pn, 0).Scan(&n).Error)
		require.NoError(t, selectParity(pc, 0).Scan(&c).Error)
		assert.Equal(t, n, c)
		require.Len(t, n, 3)
	})
}

// TestPgxClassicScanTimeZoneDSN is the one branch of the dialector's Initialize no DSN
// this codebase builds takes: a time zone in the DSN becomes a runtime parameter and the
// scan location of timestamp. It is replicated, so it is compared.
func TestPgxClassicScanTimeZoneDSN(t *testing.T) {
	native, classic := openPgxPair(t, pgxClassicDSN(t, "timezone=America/New_York"), quietGorm)
	const q = `SELECT current_setting('TimeZone') AS zone, '2026-10-14 12:00:00'::timestamp AS tsn,
		'2026-10-14 12:00:00+00'::timestamptz AS ts`
	type row struct {
		Zone string
		Tsn  time.Time
		Ts   time.Time
	}
	var n, c row
	require.NoError(t, native.Raw(q).Scan(&n).Error)
	require.NoError(t, classic.Raw(q).Scan(&c).Error)
	assert.Equal(t, "America/New_York", n.Zone)
	assert.Equal(t, "America/New_York", n.Tsn.Location().String(), "the control: the dialector applied the zone")
	assert.Equal(t, n.Zone, c.Zone)
	assert.Equal(t, n.Tsn.Location().String(), c.Tsn.Location().String())
	assert.True(t, n.Tsn.Equal(c.Tsn) && n.Ts.Equal(c.Ts), "native %+v classic %+v", n, c)
}

// driverRowsOf runs q on one of db's driver connections, directly and through a prepared
// statement, and returns the driver-level rows' types.
func driverRowsOf(t *testing.T, db *gorm.DB) (connT, directRows, stmtRows reflect.Type) {
	t.Helper()
	sqldb, err := db.DB()
	require.NoError(t, err)
	ctx := context.Background()
	conn, err := sqldb.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.Raw(func(dc any) error {
		connT = reflect.TypeOf(dc)
		r, err := dc.(driver.QueryerContext).QueryContext(ctx, "SELECT 1", nil)
		if err != nil {
			return err
		}
		directRows = reflect.TypeOf(r)
		_ = r.Close()
		s, err := dc.(driver.ConnPrepareContext).PrepareContext(ctx, "SELECT 1")
		if err != nil {
			return err
		}
		defer s.Close()
		r, err = s.(driver.StmtQueryContext).QueryContext(ctx, nil)
		if err != nil {
			return err
		}
		stmtRows = reflect.TypeOf(r)
		return r.Close()
	}))
	return connT, directRows, stmtRows
}

type columnScanner interface {
	NextRow() error
}

// TestPgxClassicScanWrapsEveryOpenPath asserts by type what the live pool hands
// database/sql — with the dialector's own pool as the control, so the assertion is known
// to be able to fail — and then that BOTH production open paths (initializePostgres and
// Guest.Connect) use the wrapper.
func TestPgxClassicScanWrapsEveryOpenPath(t *testing.T) {
	native, classic := openPgxPair(t, pgxClassicDSN(t, ""), quietGorm)
	scanner := reflect.TypeFor[columnScanner]()

	conn, direct, stmt := driverRowsOf(t, native)
	assert.True(t, direct.Implements(scanner) && stmt.Implements(scanner),
		"the control: the dialector's own pool advertises RowsColumnScanner (%v, %v, %v)", conn, direct, stmt)

	conn, direct, stmt = driverRowsOf(t, classic)
	assert.Equal(t, reflect.TypeFor[*classicRowsConn](), conn)
	assert.Equal(t, reflect.TypeFor[*classicRows](), direct, "rows from QueryContext")
	assert.Equal(t, reflect.TypeFor[*classicRows](), stmt, "rows from a prepared statement")

	t.Run("owned", func(t *testing.T) {
		ctx := context.Background()
		host, port := itServer(t)
		const instance = "rdbpgxclassicowned"
		require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
		mgr := &RdbManager{
			Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "pgx-classic"},
			Migrations: []*gormigrate.Migration{{
				ID:      "20261014000000",
				Migrate: func(tx *gorm.DB) error { return tx.Exec("SELECT 1").Error },
			}},
			InstanceConfig: config.DatastoreConfiguration{
				Type: "postgres",
				Configuration: map[string]interface{}{
					"hostname": host, "port": port, "username": "postgres", "password": "postgres",
					"sslMode": "disable",
				},
			},
		}
		require.NoError(t, mgr.ExecuteInitialize(ctx))
		t.Cleanup(func() {
			if s, err := mgr.Database.DB(); err == nil {
				_ = s.Close()
			}
		})
		conn, direct, _ := driverRowsOf(t, mgr.Database)
		assert.Equal(t, reflect.TypeFor[*classicRowsConn](), conn)
		assert.Equal(t, reflect.TypeFor[*classicRows](), direct)
	})
	t.Run("guest", func(t *testing.T) {
		g := liveGuest(t, "postgres")
		require.NoError(t, g.Connect(context.Background()))
		conn, direct, _ := driverRowsOf(t, g.DB(context.Background()))
		assert.Equal(t, reflect.TypeFor[*classicRowsConn](), conn)
		assert.Equal(t, reflect.TypeFor[*classicRows](), direct)
	})
}

// benchRow is ten columns of the kinds device-state's hot reads scan.
type benchRow struct {
	ID       int64
	TenantID string
	Token    string
	Seq      int32
	Value    float64
	Active   bool
	Updated  time.Time
	DeviceID uuid.UUID
	Meta     datatypes.JSON
	Note     *string
}

// BenchmarkPgxScan scans 10,000 rows × 10 columns through gorm on each pool.
func BenchmarkPgxScan(b *testing.B) {
	native, classic := openPgxPair(b, pgxClassicDSN(b, ""), quietGorm)
	require.NoError(b, native.Exec(`
		DROP TABLE IF EXISTS pgx_classic_bench;
		CREATE TABLE pgx_classic_bench AS
		SELECT g::bigint AS id, 'tenant-' || (g % 7) AS tenant_id, 'tok-' || g AS token,
			(g % 1000)::int AS seq, g * 1.5::float8 AS value, g % 2 = 0 AS active,
			now() - g * interval '1 second' AS updated, gen_random_uuid() AS device_id,
			jsonb_build_object('g', g) AS meta, CASE WHEN g % 3 = 0 THEN NULL ELSE 'n' || g END AS note
		FROM generate_series(1, 10000) g;`).Error)
	for _, tc := range []struct {
		name string
		db   *gorm.DB
	}{{"native", native}, {"classic", classic}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var rows []benchRow
				if err := tc.db.Table("pgx_classic_bench").Find(&rows).Error; err != nil {
					b.Fatal(err)
				}
				if len(rows) != 10000 {
					b.Fatalf("scanned %d rows", len(rows))
				}
			}
		})
	}
}
