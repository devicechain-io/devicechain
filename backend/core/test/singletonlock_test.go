// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSingletonLockGuardPlantedFixtures(t *testing.T) {
	root := t.TempDir()
	plant := func(rel, source string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	plant("backend/services/example/pass.go", `package example
 func pass() { run := func() { db.TryAdvisoryLock(ctx, key, fn); db.TryAdvisoryLockSession(ctx, key, fn) }; run() }`)
	plant("backend/core/rdb/pass.go", `package rdb; func internal() { db.TryAdvisoryLock(ctx, key, fn) }`)
	plant("backend/services/example/pass_test.go", `package example; func test() { db.TryAdvisoryLock(ctx, key, fn) }`)
	plant("backend/services/example/testdata/pass.go", `package example; func fixture() { db.TryAdvisoryLock(ctx, key, fn) }`)
	sites, err := singletonLockSitesUnder(root)
	if err != nil {
		t.Fatal(err)
	}
	subject := singletonLockSite{"backend/services/example/pass.go", "pass"}
	if len(sites) != 2 || sites[0] != subject || sites[1] != subject {
		t.Fatalf("planted calls or exclusions wrong: %v", sites)
	}
	ledger := map[singletonLockSite]string{subject: "fixture seam"}
	if issues := singletonLockLedgerErrors(sites, ledger); len(issues) != 0 {
		t.Fatal(issues)
	}
	if issues := singletonLockLedgerErrors(sites, nil); len(issues) == 0 {
		t.Fatal("missing ledger entry passed")
	}
	ledger[singletonLockSite{"stale.go", "gone"}] = "planted stale entry"
	if issues := singletonLockLedgerErrors(sites, ledger); len(issues) != 1 {
		t.Fatalf("stale entry passed: %v", issues)
	}
}
