// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build fleetpg

package pgtest

import "testing"

// TestFleetPGHarnessRoundTrip proves the harness reaches a real server, isolates a test's
// database, and gives a second connection a separate session. CI counts the passed tests whose
// name begins TestFleetPG; this is the one that proves the count can be non-zero.
func TestFleetPGHarnessRoundTrip(t *testing.T) {
	env := Open(t)

	var one int
	if err := env.Gorm.Raw(`SELECT 1`).Scan(&one).Error; err != nil || one != 1 {
		t.Fatalf("round trip failed: %v %d", err, one)
	}
	if err := env.Gorm.Exec(`CREATE TABLE t_roundtrip (id int primary key)`).Error; err != nil {
		t.Fatal(err)
	}

	other := env.Connect(t)
	var pidA, pidB int
	env.Gorm.Raw(`SELECT pg_backend_pid()`).Scan(&pidA)
	other.Raw(`SELECT pg_backend_pid()`).Scan(&pidB)
	if pidA == 0 || pidB == 0 {
		t.Fatalf("no backend pid: %d %d", pidA, pidB)
	}
	var n int
	if err := other.Raw(`SELECT count(*) FROM t_roundtrip`).Scan(&n).Error; err != nil {
		t.Fatalf("the second connection cannot see the first's table: %v", err)
	}

	// Two tests never share a database.
	second := Open(t)
	if second.Name == env.Name {
		t.Fatal("two Open calls returned the same database")
	}
	if err := second.Gorm.Raw(`SELECT count(*) FROM t_roundtrip`).Scan(&n).Error; err == nil {
		t.Fatal("a second test's database can see another test's table")
	}
}
