// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	emodel "github.com/devicechain-io/dc-event-management/model"
	"gorm.io/gorm/schema"
)

// 🔴 WHY THIS FILE EXISTS. The seeder's INSERTs are hand-written SQL, so the compiler
// cannot see them drift from the schema. It did drift: `event_id` was added to `events`
// and `event_id`/`payload_id` to `measurement_events` as NOT NULL columns, the seeder
// kept naming the older column set, and both statements began failing with SQLSTATE
// 23502. Nothing caught it, because the only thing that runs these statements is a
// MANUAL drill against a real cluster — so the DR gate sat broken through every green
// CI run between the schema change and someone next running the drill.
//
// The tests below are the cheap half of that problem: they need no database, they run
// in the normal CI matrix, and they compare the statements against the live models. They
// cannot prove the drill works — only a drill does that — but they make THIS failure
// mode, a required column the SQL forgot, impossible to reintroduce quietly.

// requiredColumns returns the table name and the NOT NULL column names GORM derives for
// a model — including promoted fields from embedded structs, which is how tenant_id
// arrives (via rdb.TenantScoped).
//
// 🔑 It asks GORM's own schema parser rather than walking the struct and snake-casing
// field names by hand. That distinction is the whole point of the test: the columns this
// compares against must be the ones the database actually has, and the only authority on
// that mapping is the code that produced it. A hand-rolled converter is a third
// implementation and gets three things wrong that matter here — an initialism (DeviceID)
// snake-cases to `device_i_d`, a `gorm:"column:..."` override is ignored so the test
// demands a column name the table does not have, and `strings.Contains(tag, "not null")`
// is not GORM's tag grammar. Each of those is a FALSE failure, which is the expensive
// kind: it trains the reader to edit the test rather than the SQL.
func requiredColumns(t *testing.T, model any) (string, []string) {
	t.Helper()
	s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parsing %T: %v", model, err)
	}
	var cols []string
	for _, f := range s.Fields {
		if f.NotNull {
			cols = append(cols, f.DBName)
		}
	}
	if len(cols) == 0 {
		t.Fatalf("%T declares no NOT NULL columns, so this test would pass without testing anything",
			model)
	}
	return s.Table, cols
}

// columnList extracts the parenthesised column list from an INSERT statement, so the
// assertion reads the very string that is handed to the database.
var columnList = regexp.MustCompile(`(?s)INSERT INTO\s+"[^"]*"\.(\w+)\s*\(([^)]*)\)`)

func insertedColumns(t *testing.T, statement string) (string, map[string]bool) {
	t.Helper()
	m := columnList.FindStringSubmatch(statement)
	if m == nil {
		t.Fatalf("could not read a column list out of the statement, so this test would "+
			"assert nothing about it:\n%s", statement)
	}
	named := map[string]bool{}
	for _, c := range strings.Split(m[2], ",") {
		named[strings.TrimSpace(c)] = true
	}
	return m[1], named
}

// Every NOT NULL column of `events` and `measurement_events` is named by the INSERT
// that writes it.
//
// This is the assertion that would have failed the day event_id landed.
func TestTheSeedInsertsNameEveryRequiredColumn(t *testing.T) {
	for _, tc := range []struct {
		statement string
		model     any
	}{
		{insertParentEventSQL, emodel.Event{}},
		{insertMeasurementSQL, emodel.MeasurementEvent{}},
	} {
		wantTable, required := requiredColumns(t, tc.model)
		table, named := insertedColumns(t, tc.statement)
		// The statement and the model are paired by hand above, and the table name
		// comes from the MODEL rather than from a third string written here — so a
		// statement repointed at another table fails loudly instead of silently
		// checking one table's columns against another's model.
		if table != wantTable {
			t.Fatalf("statement inserts into %q but is paired with %T, whose table is %q",
				table, tc.model, wantTable)
		}
		for _, col := range required {
			if !named[col] {
				t.Errorf("INSERT INTO %s omits %q, which the model declares NOT NULL. "+
					"The statement fails with SQLSTATE 23502 and the whole drill aborts "+
					"in its seed phase", table, col)
			}
		}
	}
}

// The two identities are stable across runs and distinct across rows.
//
// Those are the only two properties the schema demands of whatever writes them, and
// they are what a hand-rolled digest would be most likely to get wrong — a value
// derived from wall-clock time or a counter satisfies "distinct" and quietly breaks
// "stable", which turns an interrupted seed's re-run into duplicate rows.
func TestAMeasurementIdentityIsStableAndDistinct(t *testing.T) {
	at := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	first, firstPayload, err := measurementIdentity("t", "dev", "temperature", at, 21.5)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	again, againPayload, err := measurementIdentity("t", "dev", "temperature", at, 21.5)
	if err != nil {
		t.Fatalf("derive again: %v", err)
	}
	if string(first) != string(again) || string(firstPayload) != string(againPayload) {
		t.Fatal("the same measurement derived two different identities, so a re-run would duplicate rows")
	}

	// Each field that varies across the seed's own rows must move the digest. The seed
	// varies time and value within a cohort and the device token for the post-restore
	// probe; tenant and name are here because nothing stops a future seed varying them.
	for _, v := range []struct {
		what                 string
		tenant, device, name string
		at                   time.Time
		value                float64
	}{
		{"tenant", "other", "dev", "temperature", at, 21.5},
		{"device", "t", "other", "temperature", at, 21.5},
		{"name", "t", "dev", "humidity", at, 21.5},
		{"time", "t", "dev", "temperature", at.Add(time.Minute), 21.5},
		{"value", "t", "dev", "temperature", at, 21.6},
	} {
		t.Run(v.what, func(t *testing.T) {
			id, payload, err := measurementIdentity(v.tenant, v.device, v.name, v.at, v.value)
			if err != nil {
				t.Fatalf("derive: %v", err)
			}
			if string(id) == string(first) && string(payload) == string(firstPayload) {
				t.Fatalf("a different %s produced the same identity, so two seeded rows "+
					"would collapse into one and the drill's row count would be wrong", v.what)
			}
		})
	}
}
