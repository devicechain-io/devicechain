// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var (
	prevMigrateOnce sync.Once
	prevMigrateErr  error
)

// Separate from the purge drill: its rows and triggers must never affect these replays.
func prevReleaseConn(t *testing.T) *gorm.DB {
	t.Helper()
	port := 5432
	if raw := os.Getenv("DC_IT_PGPORT"); raw != "" {
		var err error
		port, err = strconv.Atoi(raw)
		require.NoError(t, err, "DC_IT_PGPORT must be a port number")
	}
	host := "localhost"
	if h := os.Getenv("DC_IT_PGHOST"); h != "" {
		host = h
	}
	// Every area's chain is migrated ONCE per test binary: it is the slowest step here
	// (well over a minute), and every test in this file reads the same migrated schema.
	prevMigrateOnce.Do(func() {
		for _, a := range areas {
			if err := migrateChain(context.Background(), a, host, port, "postgres", "postgres", "dcprevreleasewriters"); err != nil {
				prevMigrateErr = fmt.Errorf("migrating %s: %w", a.name, err)
				return
			}
		}
	})
	require.NoError(t, prevMigrateErr)
	db, err := openDatabase(host, port, "postgres", "postgres", "dcprevreleasewriters")
	require.NoError(t, err)
	t.Cleanup(func() { closeDatabase(db) })
	return db
}

type prevColumn struct {
	typeText                       string
	notNull, hasDefault, generated bool
}
type prevTable struct{ schema, table string }
type prevCatalog map[prevTable]map[string]prevColumn

var prevTableStart = regexp.MustCompile(`^CREATE TABLE ("(?:[^"]|"")+")\.("(?:[^"]|"")+"|[a-z_][a-z_0-9]*) \($`)
var prevCollation = regexp.MustCompile(` COLLATE (?:"(?:[^"]|"")+"|[a-z_][a-z_0-9]*)(?:\.(?:"(?:[^"]|"")+"|[a-z_][a-z_0-9]*))?$`)
var prevColumnLine = regexp.MustCompile(`^\s+("(?:[^"]|"")+"|[a-z_][a-z_0-9]*)\s+(.+)$`)

// This is deliberately an allowlist of pg_dump's type grammar in this release.
// A new/unrecognized spelling must fail, never be silently mistaken for a column.
var prevType = regexp.MustCompile(`^(?:bigint|integer|smallint|boolean|bytea|jsonb|date|text|character varying\([0-9]+\)|numeric(?:\([0-9]+,\s*[0-9]+\))?|timestamp with time zone)(?:\[\])?$`)

func prevIdent(s string) string {
	if strings.HasPrefix(s, `"`) {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}
func prevWhitespace(s string) string { return strings.Join(strings.Fields(s), " ") }

func prevReleaseFixture(t *testing.T) prevCatalog {
	t.Helper()
	files, err := filepath.Glob("testdata/prev-release/v1.3.0/*.sql")
	require.NoError(t, err)
	require.Len(t, files, len(areas), "fixture must cover every area")
	result := prevCatalog{}
	seen := map[string]bool{}
	for _, file := range files {
		b, err := os.ReadFile(file)
		require.NoError(t, err)
		var current prevTable
		inTable, found := false, 0
		for i, line := range strings.Split(string(b), "\n") {
			fail := func() { t.Fatalf("%s:%d: unrecognized CREATE TABLE/column line: %q", file, i+1, line) }
			if !inTable {
				if !strings.HasPrefix(line, "CREATE TABLE ") {
					continue
				}
				m := prevTableStart.FindStringSubmatch(line)
				if m == nil {
					fail()
				}
				current = prevTable{prevIdent(m[1]), prevIdent(m[2])}
				require.Equal(t, strings.TrimSuffix(filepath.Base(file), ".sql"), current.schema)
				require.NotContains(t, result, current, "duplicate fixture table")
				result[current] = map[string]prevColumn{}
				seen[current.schema] = true
				inTable = true
				found++
				continue
			}
			// pg_dump may follow ')' with a separate WITH (storage parameters) line.
			if line == ");" || line == ")" {
				require.NotEmpty(t, result[current], "empty fixture table %v", current)
				inTable = false
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(line), "CONSTRAINT ") {
				continue
			}
			m := prevColumnLine.FindStringSubmatch(strings.TrimSuffix(line, ","))
			if m == nil {
				fail()
			}
			// pg_dump prints COLLATE after NOT NULL in the event store. It is
			// column metadata, not part of format_type's type text. Recognize
			// its full identifier grammar; do not discard an arbitrary suffix.
			rest := prevCollation.ReplaceAllString(m[2], "")
			col := prevColumn{}
			if strings.HasSuffix(rest, " NOT NULL") {
				col.notNull = true
				rest = strings.TrimSuffix(rest, " NOT NULL")
			}
			if at := strings.Index(rest, " DEFAULT "); at >= 0 {
				require.NotEmpty(t, rest[at+len(" DEFAULT "):], "%s:%d empty default", file, i+1)
				col.hasDefault = true
				rest = rest[:at]
			}
			col.typeText = prevWhitespace(rest)
			if !prevType.MatchString(col.typeText) {
				fail()
			}
			name := prevIdent(m[1])
			require.NotContains(t, result[current], name)
			result[current][name] = col
		}
		require.False(t, inTable, "%s: unterminated table", file)
		require.GreaterOrEqual(t, found, 1, "%s: no tables parsed", file)
	}
	for _, a := range areas {
		require.True(t, seen[a.name], "missing fixture for %s", a.name)
	}
	require.Contains(t, result[prevTable{"notification-management", "notification_states"}], "cycle_raised_at")
	return result
}

func TestPrevReleaseColumns(t *testing.T) {
	old := prevReleaseFixture(t)
	db := prevReleaseConn(t)
	schemas := map[string]bool{}
	for key := range old {
		schemas[key.schema] = true
	}
	live := prevCatalog{}
	for schema := range schemas {
		rows, err := db.Raw(`SELECT n.nspname, c.relname, a.attname,
   pg_catalog.format_type(a.atttypid, a.atttypmod), a.attnotnull, a.atthasdef,
   (a.attidentity <> '' OR a.attgenerated <> '')
   FROM pg_catalog.pg_attribute a
   JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = ? AND c.relkind IN ('r','p')
   AND a.attnum > 0 AND NOT a.attisdropped`, schema).Rows()
		require.NoError(t, err)
		for rows.Next() {
			var key prevTable
			var name string
			var col prevColumn
			require.NoError(t, rows.Scan(&key.schema, &key.table, &name, &col.typeText, &col.notNull, &col.hasDefault, &col.generated))
			if live[key] == nil {
				live[key] = map[string]prevColumn{}
			}
			live[key][name] = col
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
	var violations []string
	for key, columns := range old {
		now, exists := live[key]
		label := key.schema + "." + key.table
		if !exists {
			violations = append(violations, label+": v1.3.0 table missing")
			continue
		}
		for name, before := range columns {
			after, exists := now[name]
			if !exists {
				violations = append(violations, label+"."+name+": v1.3.0 column missing (drop/rename)")
				continue
			}
			if prevWhitespace(before.typeText) != prevWhitespace(after.typeText) {
				violations = append(violations, fmt.Sprintf("%s.%s: type changed from %q to %q", label, name, before.typeText, after.typeText))
			}
			// A default does not rescue this one: a default fills an OMITTED column, and a
			// v1.3 writer that knows the column binds NULL to it explicitly (absent
			// coordinates, absent measurement metadata), which NOT NULL then refuses.
			if !before.notNull && after.notNull {
				violations = append(violations, label+"."+name+": nullable column became NOT NULL (an old writer may bind NULL explicitly; a default does not apply)")
			}
		}
		for name, col := range now {
			if _, existed := columns[name]; !existed && col.notNull && !col.hasDefault && !col.generated {
				violations = append(violations, label+"."+name+": added NOT NULL column has no default/identity/generated value")
			}
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("v1.3.0 rolling-writer incompatibilities:\n  %s\nA v1.3 pod's INSERT omits columns it does not know; additions must accept omitted values. Old tables, columns and types must survive the overlap.", strings.Join(violations, "\n  "))
	}
}
