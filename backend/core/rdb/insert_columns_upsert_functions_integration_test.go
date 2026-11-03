// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package rdb

import "testing"

func TestColumnarUpsertFragmentFunctionsOnPostgres(t *testing.T) {
	upsertFunctionsRun(t, colUpPG(t))
}

// Two rows of one batch naming the same key fail on Postgres (a row cannot be affected twice
// by one statement), where SQLite applies them in order. The caller deduplicates; S4's
// callers need this test of their own.
func TestColumnarUpsertDuplicateKeysInOneBatchFailOnPostgres(t *testing.T) {
	db := colUpPG(t)
	_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), upRows(1, "dup", "dup"))
	if err == nil {
		t.Fatal("a duplicate key in one batch was accepted on Postgres")
	}
}
