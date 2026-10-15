// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

// The type-level half of pgx_classic_scan.go's guarantee, answerable without a server:
// the wrappers withhold driver.RowsColumnScanner and NOTHING else. The live half — real
// rows through real connections, and the scanned values compared — is in
// pgx_classic_scan_integration_test.go.

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The interfaces the wrappers exist to keep, named so a lost one is a compile error here
// before it is a test failure below.
var (
	_ driver.Connector = classicRowsConnector{}

	_ driver.Conn                   = (*classicRowsConn)(nil)
	_ driver.ConnBeginTx            = (*classicRowsConn)(nil)
	_ driver.ConnPrepareContext     = (*classicRowsConn)(nil)
	_ driver.QueryerContext         = (*classicRowsConn)(nil)
	_ driver.ExecerContext          = (*classicRowsConn)(nil)
	_ driver.Pinger                 = (*classicRowsConn)(nil)
	_ driver.SessionResetter        = (*classicRowsConn)(nil)
	_ driver.NamedValueChecker      = (*classicRowsConn)(nil)
	_ interface{ Conn() *pgx.Conn } = (*classicRowsConn)(nil)

	_ driver.Stmt             = (*classicRowsStmt)(nil)
	_ driver.StmtExecContext  = (*classicRowsStmt)(nil)
	_ driver.StmtQueryContext = (*classicRowsStmt)(nil)

	_ driver.Rows                           = (*classicRows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*classicRows)(nil)
	_ driver.RowsColumnTypeLength           = (*classicRows)(nil)
	_ driver.RowsColumnTypePrecisionScale   = (*classicRows)(nil)
	_ driver.RowsColumnTypeScanType         = (*classicRows)(nil)
)

// withheld is the ONE thing the rows wrapper drops: driver.RowsColumnScanner's methods.
var withheld = map[string]bool{"NextRow": true, "ScanColumn": true}

// methodSet renders a type's method set as name → signature with the receiver dropped,
// so a wrapper's promoted or overriding method compares equal to pgx's original.
func methodSet(t reflect.Type) map[string]string {
	out := make(map[string]string, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		m := t.Method(i)
		var in, res []string
		for j := 1; j < m.Type.NumIn(); j++ { // 0 is the receiver
			in = append(in, m.Type.In(j).String())
		}
		for j := 0; j < m.Type.NumOut(); j++ {
			res = append(res, m.Type.Out(j).String())
		}
		out[m.Name] = fmt.Sprintf("(%s) (%s)", strings.Join(in, ", "), strings.Join(res, ", "))
	}
	return out
}

func names(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestPgxClassicScanDrift is the drift guard named in pgx_classic_scan.go. It compares
// each wrapper's method set with pgx's, so a method pgx ADDS in a future version — which
// database/sql may discover by interface assertion — must reach the wrapper, and one the
// wrapper overrides must keep pgx's signature. Only NextRow/ScanColumn may differ.
func TestPgxClassicScanDrift(t *testing.T) {
	// The exclusion means something only while pgx actually has the methods; if it stops
	// implementing RowsColumnScanner (or renames it), this file may have done its job —
	// see its EXIT CONDITION. (The interface itself cannot be named here: it is Go 1.27
	// and this module's go line is 1.26, so it is checked by method name throughout.)
	for n := range withheld {
		_, ok := reflect.TypeFor[*stdlib.Rows]().MethodByName(n)
		require.True(t, ok, "pgx's rows no longer have %s: revisit pgx_classic_scan.go", n)
	}

	for _, tc := range []struct {
		name      string
		pgx, ours reflect.Type
		excluded  map[string]bool
	}{
		{"conn", reflect.TypeFor[*stdlib.Conn](), reflect.TypeFor[*classicRowsConn](), nil},
		{"stmt", reflect.TypeFor[*stdlib.Stmt](), reflect.TypeFor[*classicRowsStmt](), nil},
		{"rows", reflect.TypeFor[*stdlib.Rows](), reflect.TypeFor[*classicRows](), withheld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := methodSet(tc.pgx)
			for n := range tc.excluded {
				require.Contains(t, want, n, "an exclusion pgx no longer has is a stale exclusion")
				delete(want, n)
			}
			got := methodSet(tc.ours)
			assert.Equal(t, names(want), names(got),
				"the wrapper's method set must be pgx's minus the exclusions %v, exactly", names2(tc.excluded))
			assert.Equal(t, want, got, "a wrapper method's signature drifted from pgx's")
		})
	}

	// pgx's connector is unexported and reached only through driver.Connector, so the
	// wrapper forwards just that interface. Were pgx to give it io.Closer (database/sql
	// calls Close on a connector that has it when the DB closes), the wrapper would drop
	// it silently; this is where that would show.
	conn := reflect.TypeOf(stdlib.GetConnector(pgx.ConnConfig{}))
	assert.Equal(t, []string{"Connect", "Driver"}, names(methodSet(conn)),
		"pgx's connector gained a method classicRowsConnector does not forward")
}

// TestPgxClassicScanInterfaces states the same thing by interface rather than by method:
// every optional database/sql/driver interface pgx implements, the wrapper implements,
// except RowsColumnScanner, which the rows wrapper must NOT. The list is the driver
// package's optional interfaces for each role; the drift test above covers the ones
// nobody has written yet.
func TestPgxClassicScanInterfaces(t *testing.T) {
	type iface struct {
		name string
		typ  reflect.Type
	}
	of := func(name string, typ reflect.Type) iface { return iface{name, typ} }

	conn := []iface{
		of("Conn", reflect.TypeFor[driver.Conn]()),
		of("ConnBeginTx", reflect.TypeFor[driver.ConnBeginTx]()),
		of("ConnPrepareContext", reflect.TypeFor[driver.ConnPrepareContext]()),
		of("QueryerContext", reflect.TypeFor[driver.QueryerContext]()),
		of("Queryer", reflect.TypeFor[driver.Queryer]()),
		of("ExecerContext", reflect.TypeFor[driver.ExecerContext]()),
		of("Execer", reflect.TypeFor[driver.Execer]()),
		of("Pinger", reflect.TypeFor[driver.Pinger]()),
		of("SessionResetter", reflect.TypeFor[driver.SessionResetter]()),
		of("Validator", reflect.TypeFor[driver.Validator]()),
		of("NamedValueChecker", reflect.TypeFor[driver.NamedValueChecker]()),
		of("Conn() *pgx.Conn", reflect.TypeFor[interface{ Conn() *pgx.Conn }]()),
	}
	stmt := []iface{
		of("Stmt", reflect.TypeFor[driver.Stmt]()),
		of("StmtExecContext", reflect.TypeFor[driver.StmtExecContext]()),
		of("StmtQueryContext", reflect.TypeFor[driver.StmtQueryContext]()),
		of("NamedValueChecker", reflect.TypeFor[driver.NamedValueChecker]()),
		of("ColumnConverter", reflect.TypeFor[driver.ColumnConverter]()),
	}
	rows := []iface{
		of("Rows", reflect.TypeFor[driver.Rows]()),
		of("RowsColumnTypeDatabaseTypeName", reflect.TypeFor[driver.RowsColumnTypeDatabaseTypeName]()),
		of("RowsColumnTypeLength", reflect.TypeFor[driver.RowsColumnTypeLength]()),
		of("RowsColumnTypeNullable", reflect.TypeFor[driver.RowsColumnTypeNullable]()),
		of("RowsColumnTypePrecisionScale", reflect.TypeFor[driver.RowsColumnTypePrecisionScale]()),
		of("RowsColumnTypeScanType", reflect.TypeFor[driver.RowsColumnTypeScanType]()),
		of("RowsNextResultSet", reflect.TypeFor[driver.RowsNextResultSet]()),
	}

	check := func(t *testing.T, list []iface, pgxT, oursT reflect.Type) {
		for _, i := range list {
			assert.Equal(t, pgxT.Implements(i.typ), oursT.Implements(i.typ),
				"%s: pgx implements it = %v, the wrapper = %v", i.name, pgxT.Implements(i.typ), oursT.Implements(i.typ))
		}
	}
	t.Run("conn", func(t *testing.T) {
		check(t, conn, reflect.TypeFor[*stdlib.Conn](), reflect.TypeFor[*classicRowsConn]())
	})
	t.Run("stmt", func(t *testing.T) {
		check(t, stmt, reflect.TypeFor[*stdlib.Stmt](), reflect.TypeFor[*classicRowsStmt]())
	})
	t.Run("rows", func(t *testing.T) {
		check(t, rows, reflect.TypeFor[*stdlib.Rows](), reflect.TypeFor[*classicRows]())
		// driver.RowsColumnScanner is {NextRow() error; ScanColumn(ScanContext, int, any)
		// error}; database/sql takes the per-column path only when BOTH are present, and
		// the wrapper must have NEITHER under any signature.
		for n := range withheld {
			_, has := reflect.TypeFor[*stdlib.Rows]().MethodByName(n)
			assert.True(t, has, "the control: pgx's rows have %s", n)
			_, has = reflect.TypeFor[*classicRows]().MethodByName(n)
			assert.False(t, has, "the wrapper has %s, so database/sql may take the per-column "+
				"reflection path again", n)
		}
	})
}

func names2(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
