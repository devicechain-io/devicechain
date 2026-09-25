// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The credential lookup's refusals, on the real engine. The lookup fetches the owning
// device on a LEFT JOIN whose ON clause carries the device's soft-delete predicate, and
// whose WHERE carries the tenant predicate for the credential's table alone. SQLite and
// PostgreSQL get the same clause tree from gorm and differ only in quoting, but whether
// the predicates land where they must — and stay unambiguous against two tables sharing
// tenant_id and deleted_at — is a property of the statement the real server accepts, so it
// is checked here and not argued.
//
// Tagged `integration` so it stays out of the default `go test ./...`. Run it against a
// throwaway server, as the other integration tests in this package are run:
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run CredentialLookup -v
package model

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

func TestCredentialLookupJoinOnPostgres(t *testing.T) {
	mgr := newPostgresRdbManager(t)
	api := NewApi(mgr)
	stmts := countQueries(t, api)

	// Every fixture starts from empty tables: the instance id is fixed, so a server that
	// has run this before still holds its rows, and a token would collide on the rerun.
	fresh := func(t *testing.T) credFixture {
		t.Helper()
		sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
		for _, table := range []string{"device_credentials", "devices", "device_types"} {
			if err := sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "device-management".%q CASCADE`, table)).Error; err != nil {
				t.Fatalf("truncate %s: %v", table, err)
			}
		}
		return seedCredentialFixture(t, api, stmts)
	}

	t.Run("a presented credential is one statement", func(t *testing.T) {
		f := fresh(t)
		d, err := f.capi.AuthenticateDevice(f.ctx, basic("cred-1", "s3cret"), time.Now())
		if err != nil || d == nil || d.Token != "dev" {
			t.Fatalf("got (%v, %v), want device dev", d, err)
		}
		requireOneJoinedStatement(t, f, "basic authentication on postgres")
		sql := f.stmts.taken()[0]
		// The shape the refusals below rest on: the device's soft-delete predicate in the
		// JOIN, and the tenant predicate qualified to the credential's table.
		for _, want := range []string{`LEFT JOIN "device-management"."devices" "Device" ON`, `"Device"."deleted_at" IS NULL`,
			`"device_credentials"."tenant_id" =`} {
			if !strings.Contains(sql, want) {
				t.Errorf("the statement lacks %s: %s", want, sql)
			}
		}
	})

	runCredentialLookupControls(t, fresh)
}
