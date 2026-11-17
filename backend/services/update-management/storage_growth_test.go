// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// The assignment history is declared append-only, so its growth is reported. The table name
// is written out, not derived from the list: comparing the list with itself passes for any
// list, including an empty one. A model renamed, or dropped from the list, fails here rather
// than silently leaving the persistence dashboard without the table.
func TestAssignmentHistoryIsReportedAsAppendOnly(t *testing.T) {
	var tables []string
	for _, m := range appendOnlyModels {
		parsed, err := schema.Parse(m, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %T: %v", m, err)
		}
		tables = append(tables, parsed.Table)
	}
	want := []string{"update_assignments"}
	if len(tables) != len(want) || tables[0] != want[0] {
		t.Fatalf("append-only tables are %v, want %v", tables, want)
	}
}
