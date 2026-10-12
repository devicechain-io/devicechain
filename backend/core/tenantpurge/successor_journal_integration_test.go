// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The audit journal a purge retains, read back through the journal's own read path by a
// later tenant created with the same token, on a real PostgreSQL server.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./tenantpurge/ -v
package tenantpurge_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
)

// journalOf reads one tenant's journal the way the tenant-plane auditEvents query does: in
// that tenant's context, scoped by the callback, with no filter of its own.
func journalOf(t *testing.T, mgr *rdb.RdbManager, ctx context.Context) []rdb.AuditEvent {
	t.Helper()
	res, err := mgr.AuditEvents(ctx, rdb.AuditEventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}})
	if err != nil {
		t.Fatalf("read the journal: %v", err)
	}
	return res.Results
}

func TestALaterTenantAtTheSameTokenReadsNoneOfTheRetainedJournal(t *testing.T) {
	mgr := newMemoArea(t)
	sys := core.WithSystemContext(context.Background())
	tenant := fmt.Sprintf("succ%d", time.Now().UnixNano())
	ctx := core.WithTenant(context.Background(), tenant)

	// The earlier tenant's activity: a mutation through the real callback, and a sign-in.
	if err := mgr.Database.WithContext(ctx).Create(&memoWidget{Name: "earlier"}).Error; err != nil {
		t.Fatalf("seed a mutation: %v", err)
	}
	if err := mgr.RecordAuthEvent(ctx, rdb.AuditOpLogin, "jane@earlier.example", tenant); err != nil {
		t.Fatalf("seed a sign-in: %v", err)
	}
	if n := len(journalOf(t, mgr, ctx)); n < 2 {
		t.Fatalf("the earlier tenant's journal holds %d row(s) before the purge; want at least 2", n)
	}

	// The purge, as the relational store runs it, then the release.
	plan, err := tenantpurge.Classify(sys, mgr.Database)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	epoch := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := tenantpurge.PlantFence(sys, mgr.Database, plan, tenant, epoch, epoch, nil); err != nil {
		t.Fatalf("plant: %v", err)
	}
	for pass := 0; pass < 2; pass++ { // passes repeat; the second must change nothing
		if _, err := tenantpurge.Sweep(sys, mgr.Database, plan, tenant, epoch, nil); err != nil {
			t.Fatalf("sweep pass %d: %v", pass, err)
		}
	}
	residue, err := tenantpurge.Residue(sys, mgr.Database, plan, tenant, epoch)
	if err != nil {
		t.Fatalf("residue: %v", err)
	}
	if residue.Rows != 0 || residue.Retained() != 0 {
		t.Fatalf("the purge left %d row(s) and %d retained row(s) still identifying %q",
			residue.Rows, residue.Retained(), tenant)
	}
	if err := tenantpurge.LiftFence(sys, mgr.Database, plan, tenant, time.Now().UTC(), nil); err != nil {
		t.Fatalf("lift: %v", err)
	}

	// A later tenant at the same token, with activity of its own.
	if err := mgr.Database.WithContext(ctx).Create(&memoWidget{Name: "later"}).Error; err != nil {
		t.Fatalf("the later tenant could not write: %v", err)
	}
	later := journalOf(t, mgr, ctx)
	if len(later) != 1 {
		t.Fatalf("the later tenant's journal holds %d row(s); want only its own 1 — the rest are "+
			"the earlier tenant's", len(later))
	}

	// The retained rows are still there, redacted, under the key the deletion record names.
	key := tenantpurge.RetainedTenant(tenant, epoch)
	retained, err := mgr.AuditEvents(sys, rdb.AuditEventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}, Tenant: &key})
	if err != nil {
		t.Fatalf("read the retained journal: %v", err)
	}
	if len(retained.Results) < 2 {
		t.Fatalf("found %d retained row(s) under %q; want the earlier tenant's 2 or more",
			len(retained.Results), key)
	}
	for _, r := range retained.Results {
		if r.Actor != "" || r.EntityLabel != "" {
			t.Fatalf("a retained row still names someone: actor=%q label=%q", r.Actor, r.EntityLabel)
		}
	}
}
