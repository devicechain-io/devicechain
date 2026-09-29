// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// A module that depends on core has go mod tidy resolve the imports of the tests of every
// core package it builds, and those tests' own dependencies. So one import in a messaging
// test reaches the go.sum of every module that uses messaging: when a test here read the
// batch cap from core/rdb, the database stack's checksums (gorm, gormigrate, the PostgreSQL
// driver) landed in modules that do not use core/rdb at all.
//
// This lists what messaging's test binaries link — its tests included, through the Go
// loader itself — and fails if any of it is core/rdb or the database stack. Its scope is
// core/messaging only: another core package's tests can still leak the same way, and CI's
// tidy check is what notices that.
const coreModule = "github.com/devicechain-io/dc-microservice"

// databaseLayer are the external imports core/rdb makes directly. The other modules the
// leak added (jinzhu/*, go-sql-driver/mysql, golang.org/x/text, ...) are THEIR dependencies,
// and are deliberately not listed: another core package may use one legitimately.
var databaseLayer = []string{"gorm.io/", "github.com/go-gormigrate/", "github.com/jackc/"}

func inDatabaseLayer(p string) bool {
	if p == coreModule+"/rdb" || strings.HasPrefix(p, coreModule+"/rdb/") {
		return true
	}
	for _, prefix := range databaseLayer {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// databaseLayerIn is the verdict: nil when deps reach none of core/rdb and the database
// stack, and otherwise an error naming every package of it they reach. The controls below
// run this same function over listings known to be dirty, so neither a classifier that
// answers "clean" for everything nor a threshold that lets a few packages through can pass.
func databaseLayerIn(deps []string) ([]string, error) {
	var reached []string
	for _, d := range deps {
		if inDatabaseLayer(d) {
			reached = append(reached, d)
		}
	}
	if len(reached) > 0 {
		return reached, fmt.Errorf("%d package(s) of the database layer: %v", len(reached), reached)
	}
	return nil, nil
}

// testDeps lists every package the test binaries of the package in dir link.
func testDeps(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-test", "-f", "{{.ImportPath}}", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps -test in %s: %v\n%s", dir, err, out)
	}
	return strings.Fields(string(out))
}

func TestMessagingTestsDoNotReachTheDatabaseLayer(t *testing.T) {
	// Positive controls, first. The verdict over a listing of exactly one database-layer
	// package among others finds that one package, for each kind the classifier knows; and it
	// does not take a package for core/rdb because its path starts with the same letters.
	t.Run("the verdict finds a single database-layer package", func(t *testing.T) {
		for _, bad := range []string{
			coreModule + "/rdb", coreModule + "/rdb/tenant", "gorm.io/gorm",
			"github.com/go-gormigrate/gormigrate/v2", "github.com/jackc/pgx/v5/pgconn",
		} {
			listing := []string{"fmt", coreModule + "/messaging", bad, "github.com/nats-io/nats.go"}
			got, err := databaseLayerIn(listing)
			if err == nil || !slices.Equal(got, []string{bad}) {
				t.Errorf("databaseLayerIn(%v) = %v, %v; want [%s] and an error", listing, got, err, bad)
			}
		}
		clean := []string{"fmt", coreModule + "/rdbx", coreModule + "/writerbatch", "gorm.io", "github.com/nats-io/nats.go"}
		if got, err := databaseLayerIn(clean); err != nil || len(got) != 0 {
			t.Errorf("databaseLayerIn(%v) = %v, %v; want none and no error", clean, got, err)
		}
	})

	// And the listing reaches through core packages: core/service imports core/rdb, and
	// core/rdb imports gorm, so the verdict over core/service's listing names both.
	t.Run("the verdict follows core packages to the database layer", func(t *testing.T) {
		reached, err := databaseLayerIn(testDeps(t, "../service"))
		if err == nil {
			t.Fatal("the verdict over core/service's test binaries is clean; core/service imports core/rdb")
		}
		for _, want := range []string{coreModule + "/rdb", "gorm.io/gorm"} {
			if !slices.Contains(reached, want) {
				t.Fatalf("the database layer core/service's test binaries reach does not include %s: %v", want, reached)
			}
		}
	})

	t.Run("messaging, with its tests, reaches none of it", func(t *testing.T) {
		deps := testDeps(t, ".")
		// Anchors: packages only messaging's _test.go files import. If they are missing,
		// the listing does not read test imports, and the absences below mean nothing.
		for _, anchor := range []string{coreModule + "/test", "github.com/nats-io/nats-server/v2/server"} {
			if !slices.Contains(deps, anchor) {
				t.Fatalf("the listing does not contain %s, which only messaging's tests import; "+
					"it is not the test binaries' listing", anchor)
			}
		}
		if _, err := databaseLayerIn(deps); err != nil {
			t.Fatalf("messaging's test binaries link %v", err)
		}
	})
}
