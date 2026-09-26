// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The detection reconcile compares what event-processing holds with what device-management
// stores, so every value in that comparison has to survive the database it is stored in. SQLite
// keeps bytes and nanoseconds verbatim; Postgres does neither — a jsonb snapshot is re-rendered on
// the way out and a timestamptz keeps microseconds — and each of those turns a comparison that is
// green on the unit tests into one that "repairs" every rule after every publish. These run the
// real migration chain on a real server.
//
// Run them the same way as the other integration tests in this package:
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'OnPostgres' -v
package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/datatypes"
)

// unsortedSpacedRule is a rule definition whose bytes jsonb will NOT hand back: keys out of
// order, whitespace, and a number spelled in a form numeric re-spells.
const unsortedSpacedRule = `{ "when": {"value": 3.50, "op":"gt", "metric":"temp"},  "type":"threshold", "name":"hot" }`

// newPostgresReconcileApi runs the migration chain on the integration server and returns an Api
// plus a tenant context unique to this run, so repeated runs never collide on a token.
func newPostgresReconcileApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := NewApi(newPostgresRdbManager(t))
	tenant := fmt.Sprintf("rc%d", time.Now().UnixNano())
	return api, core.WithTenant(context.Background(), tenant)
}

// seedPostgresProfileRule creates a profile carrying one enabled rule with the given definition.
func seedPostgresProfileRule(t *testing.T, api *Api, ctx context.Context, definition string) {
	t.Helper()
	p := &DeviceProfile{}
	p.Token = "prof"
	if err := api.RDB.DB(ctx).Create(p).Error; err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	dr := &DetectionRule{DeviceProfileId: p.ID, Definition: datatypes.JSON(definition), Enabled: true}
	dr.Token = "hot"
	if err := api.RDB.DB(ctx).Create(dr).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}
}

// A version's publish and a rollback to it announce the SAME rule bodies.
//
// The detection engine replaces a rule whose body changed and drops its running state — its
// duration holds, windows and absence timers. A publish used to announce the bytes it had just
// marshalled while a rollback announced the bytes Postgres renders back out of the jsonb snapshot,
// so every rollback on Postgres looked like an edit of every rule of the version it restored.
func TestPublishAndRollbackAnnounceTheSameRuleBodiesOnPostgres(t *testing.T) {
	api, ctx := newPostgresReconcileApi(t)
	facts := &capturePublishedRules{}
	api.DetectionRulesPublishedPublisher = facts
	seedPostgresProfileRule(t, api, ctx, unsortedSpacedRule)

	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "it"); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "it"); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	if _, err := api.RollbackDeviceProfile(ctx, "prof", 1); err != nil {
		t.Fatalf("rollback to v1: %v", err)
	}
	if len(facts.events) != 3 {
		t.Fatalf("want 3 facts (publish, publish, rollback), got %d", len(facts.events))
	}
	published, rolledBack := facts.events[0], facts.events[2]
	if len(published.Rules) != 1 || len(rolledBack.Rules) != 1 {
		t.Fatalf("each fact carries the one enabled rule: publish %d, rollback %d", len(published.Rules), len(rolledBack.Rules))
	}
	if got, want := rolledBack.Rules[0].Definition, published.Rules[0].Definition; got != want {
		t.Fatalf("the rollback announced different bytes for the same rule of the same version:\n publish:  %s\n rollback: %s", want, got)
	}
}

// The reconcile door answers the same bodies and the same activation instant the facts carried,
// on the database that re-renders both.
func TestTheReconcileDoorAnswersWhatTheFactsCarriedOnPostgres(t *testing.T) {
	api, ctx := newPostgresReconcileApi(t)
	facts := &capturePublishedRules{}
	api.DetectionRulesPublishedPublisher = facts
	seedPostgresProfileRule(t, api, ctx, unsortedSpacedRule)
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "it"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	page, err := api.ActiveProfileRules(ctx, 0, MaxActiveProfileRulesPageSize)
	if err != nil {
		t.Fatalf("door: %v", err)
	}
	if len(page.Entries) != 1 || len(facts.events) != 1 {
		t.Fatalf("want one profile and one fact, got %d and %d", len(page.Entries), len(facts.events))
	}
	door, fact := page.Entries[0], facts.events[0]
	if !door.ActiveSince.Equal(fact.PublishedAt) {
		t.Fatalf("activation instant: door %s, fact %s — the value a fact carries must be the value that reads back",
			door.ActiveSince, fact.PublishedAt)
	}
	if got, want := door.Rules[0].Definition, fact.Rules[0].Definition; got != want {
		t.Fatalf("rule body: door %s, fact %s", got, want)
	}
	// And the one comparison the engine makes is true between the authored text and what
	// Postgres hands back, which is NOT byte-equal — the negative control for the one above.
	if door.Rules[0].Definition == unsortedSpacedRule {
		t.Fatalf("jsonb returned the authored bytes verbatim; this fixture no longer exercises re-rendering")
	}
	if !SameRuleDefinition(unsortedSpacedRule, door.Rules[0].Definition) {
		t.Fatalf("the authored rule and its jsonb rendering compare as different rules:\n %s\n %s",
			unsortedSpacedRule, door.Rules[0].Definition)
	}
}

// The appended migration keeps an older replica writing: during a rolling upgrade a pod built
// before the columns existed inserts a device and moves a profile's pointer WITHOUT naming them,
// and both must succeed — and both must read back through the one resolution rule.
func TestTheReconcileColumnsAcceptAnOlderReplicasWritesOnPostgres(t *testing.T) {
	api, ctx := newPostgresReconcileApi(t)
	db := api.RDB.DB(ctx)
	tenant, _ := core.TenantFromContext(ctx)

	var nullable []string
	if err := api.RDB.Database.Raw(`SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'device-management' AND is_nullable = 'YES'
		AND ((table_name = 'devices' AND column_name = 'expected_since')
		  OR (table_name = 'device_profiles' AND column_name = 'active_since'))`).Scan(&nullable).Error; err != nil {
		t.Fatalf("read the columns: %v", err)
	}
	if len(nullable) != 2 {
		t.Fatalf("both reconcile columns must exist and be nullable, found nullable: %v", nullable)
	}

	dt := &DeviceType{}
	dt.Token = "old-type"
	if err := db.Create(dt).Error; err != nil {
		t.Fatalf("seed type: %v", err)
	}
	// Exactly the INSERT an older build issues: no expected_since.
	if err := db.Exec(`INSERT INTO "device-management".devices (created_at, updated_at, tenant_id, token, device_type_id)
		VALUES (now(), now(), ?, 'old-device', ?)`, tenant, dt.ID).Error; err != nil {
		t.Fatalf("an older replica's device insert failed: %v", err)
	}
	page, err := api.DeviceRosterPage(ctx, 0, MaxRosterPageSize)
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	var created time.Time
	if err := db.Raw(`SELECT created_at FROM "device-management".devices WHERE token = 'old-device'`).Scan(&created).Error; err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if len(page.Entries) != 1 || !page.Entries[0].ExpectedSince.Equal(created) {
		t.Fatalf("a device with no stored membership instant reads back as created_at %s, got %+v", created, page.Entries)
	}
}
