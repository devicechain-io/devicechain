// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/conflict"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/gorm"
)

// The optimistic-concurrency precondition on UpdateNotificationPolicy.
//
// Every precondition string here is built with dcgraphql.FormatTime — the function that
// serves `updatedAt` — and never with a layout restated in the test, so the tests send
// exactly what a client would have been handed.

const stalePolicyToken = "ops-policy"

// staleTestApi is the unit fixture on ONE connection (so a write injected from inside a
// callback lands in the same :memory: database) and on a clock of its own: strictly
// increasing, one microsecond apart, and always carrying sub-microsecond digits. Two
// writes in one test therefore never share a version, and a test that simulates a
// database keeping microseconds (see storeMicroseconds) is certain to lose digits.
func staleTestApi(t *testing.T) (*Api, *gorm.DB) {
	t.Helper()
	var db *gorm.DB
	api := newTestApiConfigured(t, func(t *testing.T, d *gorm.DB) {
		sqlDB, err := d.DB()
		if err != nil {
			t.Fatalf("reach the pool: %v", err)
		}
		sqlDB.SetMaxOpenConns(1)
		base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
		var tick atomic.Int64
		d.Config.NowFunc = func() time.Time {
			return base.Add(time.Duration(tick.Add(1))*time.Microsecond + 789*time.Nanosecond)
		}
		db = d
	})
	return api, db
}

// seedStalePolicy creates two channels and a policy with two rules on them.
func seedStalePolicy(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	for _, tok := range []string{"smtp-crit", "smtp-any"} {
		if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
			Token: tok, ChannelType: ChannelTypeSMTP, Enabled: true,
		}); err != nil {
			t.Fatalf("seed channel %s: %v", tok, err)
		}
	}
	if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: stalePolicyToken, Name: strPtr("Original"), Enabled: true,
		Rules: []*NotificationRuleCreateRequest{
			{Severity: "CRITICAL", ChannelToken: "smtp-crit", Recipients: strPtr(`["oncall@example.invalid"]`)},
			{Severity: SeverityAny, ChannelToken: "smtp-any"},
		},
	}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
}

// readStalePolicy re-reads the policy from the database: every assertion below is about
// what is STORED.
func readStalePolicy(t *testing.T, api *Api, ctx context.Context) *NotificationPolicy {
	t.Helper()
	found, err := api.NotificationPoliciesByToken(ctx, []string{stalePolicyToken})
	if err != nil || len(found) != 1 {
		t.Fatalf("read the policy: err=%v rows=%d", err, len(found))
	}
	return found[0]
}

func versionOf(p *NotificationPolicy) string { return *dcgraphql.FormatTime(p.UpdatedAt) }

func ruleIds(p *NotificationPolicy) []uint {
	ids := make([]uint, 0, len(p.Rules))
	for _, r := range p.Rules {
		ids = append(ids, r.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func sameIds(a, b []uint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func majorRuleOnly() OptionalNotificationRuleList {
	return OptionalNotificationRuleListOf([]*NotificationRuleCreateRequest{
		{Severity: "MAJOR", ChannelToken: "smtp-crit", Recipients: strPtr(`["dayshift@example.invalid"]`)},
	})
}

// A stale refusal is a lost update, not a taken value, and the console recognises it by
// its sentence.
func TestPolicyErrConflictIsNotAUniquenessConflict(t *testing.T) {
	if conflict.Is(ErrConflict) {
		t.Fatalf("ErrConflict (%v) is classified as a uniqueness conflict", ErrConflict)
	}
	if _, ok := any(ErrConflict).(interface{ Extensions() map[string]any }); ok {
		t.Fatal("ErrConflict carries extensions, so it can carry a code")
	}
	const want = "notification policy was modified by another writer; reload and try again"
	if got := ErrConflict.Error(); got != want {
		t.Fatalf("ErrConflict reads %q, want %q", got, want)
	}
}

// The chaos-drill scenario: two operators edit the same policy. The second to save sends
// the version they read, which the first has since moved on. The second save is refused
// and the first operator's policy — its name AND its rule rows — survives untouched.
func TestStalePolicySaveIsRefusedAndWritesNothing(t *testing.T) {
	api, _ := staleTestApi(t)
	ctx := tenantCtx("A")
	seedStalePolicy(t, api, ctx)
	operatorA := readStalePolicy(t, api, ctx)

	if _, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
		Name: dcgraphql.OptionalStringOf("B"),
	}, nil); err != nil {
		t.Fatalf("operator B's save: %v", err)
	}
	afterB := readStalePolicy(t, api, ctx)

	expected := versionOf(operatorA)
	_, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
		Name:  dcgraphql.OptionalStringOf("A"),
		Rules: majorRuleOnly(),
	}, &expected)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("a save from a stale copy returned %v, want ErrConflict", err)
	}

	after := readStalePolicy(t, api, ctx)
	if after.Name.String != "B" {
		t.Fatalf("stored name is %q, want operator B's %q", after.Name.String, "B")
	}
	if !sameIds(ruleIds(after), ruleIds(afterB)) || len(after.Rules) != 2 {
		t.Fatalf("rule rows moved: %v, want %v", ruleIds(after), ruleIds(afterB))
	}
	if versionOf(after) != versionOf(afterB) {
		t.Fatalf("the refused save moved the version: %s, want %s", versionOf(after), versionOf(afterB))
	}
}

// storeMicroseconds makes the SQLite fixture keep updated_at the way PostgreSQL does, to
// the microsecond, by rewriting the column after every write to a policy. SQLite keeps
// nanoseconds, so without this a response that carried the in-memory value gorm SENT
// would be indistinguishable from one carrying the stored value.
func storeMicroseconds(t *testing.T, db *gorm.DB) {
	t.Helper()
	const name = "test:store_microseconds"
	if err := db.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
		p, ok := tx.Statement.Model.(*NotificationPolicy)
		if !ok || tx.Error != nil || tx.Statement.RowsAffected == 0 {
			return
		}
		if err := tx.Session(&gorm.Session{NewDB: true}).
			Exec("UPDATE notification_policies SET updated_at = ? WHERE id = ?",
				p.UpdatedAt.Truncate(time.Microsecond), p.ID).Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
}

// A fresh save succeeds and hands back the STORED version, which is what the caller sends
// as its next precondition. On a database that keeps microseconds, a response carrying
// the nanoseconds gorm sent makes that next save stale before anyone else has written.
// Both paths reload: with a precondition, and without one.
func TestPolicySaveReturnsTheStoredNotTheInMemoryVersion(t *testing.T) {
	for _, guarded := range []bool{true, false} {
		name := "without a precondition"
		if guarded {
			name = "with a precondition"
		}
		t.Run(name, func(t *testing.T) {
			api, db := staleTestApi(t)
			storeMicroseconds(t, db)
			ctx := tenantCtx("A")
			seedStalePolicy(t, api, ctx)
			before := readStalePolicy(t, api, ctx)

			var expected *string
			if guarded {
				v := versionOf(before)
				expected = &v
			}
			resp, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
				Name:  dcgraphql.OptionalStringOf("Fresh"),
				Rules: majorRuleOnly(),
			}, expected)
			if err != nil {
				t.Fatalf("a save from the current version was refused: %v", err)
			}
			stored := readStalePolicy(t, api, ctx)
			// The negative control: the simulation is live, so the stored version has lost
			// the sub-microsecond digits the fixture's clock always sends. Without this, a
			// response carrying the in-memory value would match the stored one here.
			if stored.UpdatedAt.Nanosecond()%1000 != 0 {
				t.Fatalf("the stored version %v kept its nanoseconds; the test cannot see the defect",
					stored.UpdatedAt)
			}
			if versionOf(resp) == versionOf(before) {
				t.Fatalf("the save did not move the version (%s)", versionOf(resp))
			}
			if versionOf(resp) != versionOf(stored) {
				t.Fatalf("the response carries version %s, but %s is stored", versionOf(resp), versionOf(stored))
			}
			if stored.Name.String != "Fresh" || len(resp.Rules) != 1 || resp.Rules[0].Severity != "MAJOR" ||
				resp.Rules[0].Channel == nil || resp.Rules[0].Channel.Token != "smtp-crit" {
				t.Fatalf("the save did not apply, or the response does not show it: stored %q, rules %+v",
					stored.Name.String, resp.Rules)
			}

			// The console's convention: advance the baseline from the response.
			next := versionOf(resp)
			if _, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
				Name: dcgraphql.OptionalStringOf("Again"),
			}, &next); err != nil {
				t.Fatalf("a save from the version the previous save returned was refused: %v", err)
			}
			if got := readStalePolicy(t, api, ctx).Name.String; got != "Again" {
				t.Fatalf("stored name is %q, want %q", got, "Again")
			}
		})
	}
}

// A copy stale by less than a second is still stale.
func TestPolicyPreconditionIsSubSecond(t *testing.T) {
	api, _ := staleTestApi(t)
	ctx := tenantCtx("A")
	seedStalePolicy(t, api, ctx)
	stored := readStalePolicy(t, api, ctx).UpdatedAt
	sent := stored.Add(-time.Microsecond)
	// The negative control: at whole seconds the two are the same string, so a refusal
	// below proves sub-second precision rather than merely "a different value".
	if stored.Format(time.RFC3339) != sent.Format(time.RFC3339) {
		t.Fatalf("the fixture's versions differ at whole seconds (%v, %v); the test proves nothing", stored, sent)
	}
	expected := *dcgraphql.FormatTime(sent)
	_, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Late"),
	}, &expected)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("a precondition a microsecond stale returned %v, want ErrConflict", err)
	}
	if got := readStalePolicy(t, api, ctx).Name.String; got != "Original" {
		t.Fatalf("stored name is %q, want it unchanged", got)
	}
}

// Without a precondition the last write wins, even over a row that moved: the argument is
// optional, and leaving it out is what every caller did before it existed.
func TestOmittedPreconditionIsLastWriteWins(t *testing.T) {
	api, _ := staleTestApi(t)
	ctx := tenantCtx("A")
	seedStalePolicy(t, api, ctx)
	if _, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
		Name: dcgraphql.OptionalStringOf("B"),
	}, nil); err != nil {
		t.Fatalf("the other writer: %v", err)
	}
	if _, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
		Name:  dcgraphql.OptionalStringOf("A"),
		Rules: majorRuleOnly(),
	}, nil); err != nil {
		t.Fatalf("a save with no precondition was refused: %v", err)
	}
	after := readStalePolicy(t, api, ctx)
	if after.Name.String != "A" || len(after.Rules) != 1 || after.Rules[0].Severity != "MAJOR" {
		t.Fatalf("the last write did not win: name %q, rules %+v", after.Name.String, after.Rules)
	}
}

// A malformed request is malformed whoever else is writing. Reporting it as stale would
// send the caller off to reload and retry a request that can never succeed.
func TestAMalformedPolicySaveIsRefusedAsMalformedNotAsStale(t *testing.T) {
	api, _ := staleTestApi(t)
	ctx := tenantCtx("A")
	seedStalePolicy(t, api, ctx)
	expected := "2000-01-01T00:00:00Z" // stale by construction
	_, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
		Rules: OptionalNotificationRuleListOf([]*NotificationRuleCreateRequest{
			{Severity: "critical", ChannelToken: "smtp-crit"},
		}),
	}, &expected)
	if err == nil {
		t.Fatal("a lowercase severity was accepted")
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("a malformed request was refused as stale: %v", err)
	}
	want := validateSeverity("critical")
	if want == nil || err.Error() != want.Error() {
		t.Fatalf("refused with %q, want the severity refusal %v", err, want)
	}
}

// interleave arranges for another writer's UPDATE to land between UpdateNotificationPolicy's
// read of the policy and its guarded write: it runs once, right after the first query on
// notification_policies, which is the update's own load. The early compare then passes —
// the caller's version was current when it was read — and only the guarded write can
// refuse. It reports how many times it fired and what the injected write did.
type interleave struct {
	armed    atomic.Bool
	fired    atomic.Int32
	err      error
	affected int64
}

func injectWriterAfterRead(t *testing.T, db *gorm.DB, token string) *interleave {
	t.Helper()
	in := &interleave{}
	const name = "test:interleave_writer"
	if err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != "notification_policies" || !in.armed.CompareAndSwap(true, false) {
			return
		}
		in.fired.Add(1)
		res := tx.Session(&gorm.Session{NewDB: true}).
			Exec("UPDATE notification_policies SET name = ?, updated_at = ? WHERE token = ?",
				"other writer", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), token)
		in.err, in.affected = res.Error, res.RowsAffected
	}); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	return in
}

// runWithDeadline fails the test rather than hanging it: the fixture has one connection,
// so a statement that held it while another waited would block forever.
func runWithDeadline(t *testing.T, f func() (*NotificationPolicy, error)) (*NotificationPolicy, error) {
	t.Helper()
	type result struct {
		p   *NotificationPolicy
		err error
	}
	done := make(chan result, 1)
	go func() {
		p, err := f()
		done <- result{p, err}
	}()
	select {
	case r := <-done:
		return r.p, r.err
	case <-time.After(30 * time.Second):
		t.Fatal("the update did not return: the one-connection fixture deadlocked")
		return nil, nil
	}
}

// THE RACE. A writer lands after the update read the policy and before it wrote. The
// caller's version was current when read, so the early compare passes; the guarded write
// must match nothing and refuse, leaving the other writer's name and rule rows in place.
func TestAWriterBetweenTheReadAndTheWriteIsNotOverwritten(t *testing.T) {
	api, db := staleTestApi(t)
	ctx := tenantCtx("A")
	seedStalePolicy(t, api, ctx)
	before := readStalePolicy(t, api, ctx)
	expected := versionOf(before)

	in := injectWriterAfterRead(t, db, stalePolicyToken)
	in.armed.Store(true)
	_, err := runWithDeadline(t, func() (*NotificationPolicy, error) {
		return api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
			Name:  dcgraphql.OptionalStringOf("mine"),
			Rules: majorRuleOnly(),
		}, &expected)
	})

	// The negative control: the other writer really wrote, once, between the read and
	// the write. Without it, the assertions below could pass on an update that never
	// reached its guarded write at all.
	if in.fired.Load() != 1 || in.err != nil || in.affected != 1 {
		t.Fatalf("the interleaved writer did not land: fired=%d err=%v rows=%d",
			in.fired.Load(), in.err, in.affected)
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("a save overtaken between its read and its write returned %v, want ErrConflict", err)
	}
	after := readStalePolicy(t, api, ctx)
	if after.Name.String != "other writer" {
		t.Fatalf("stored name is %q, want the other writer's", after.Name.String)
	}
	if !sameIds(ruleIds(after), ruleIds(before)) || len(after.Rules) != 2 {
		t.Fatalf("rule rows moved: %v, want %v", ruleIds(after), ruleIds(before))
	}
}

// The twin, and the counterweight: with no precondition the same interleave is overwritten
// — last write wins, which is what leaving the argument out means.
func TestAWriterBetweenTheReadAndTheWriteLosesWithoutAPrecondition(t *testing.T) {
	api, db := staleTestApi(t)
	ctx := tenantCtx("A")
	seedStalePolicy(t, api, ctx)

	in := injectWriterAfterRead(t, db, stalePolicyToken)
	in.armed.Store(true)
	_, err := runWithDeadline(t, func() (*NotificationPolicy, error) {
		return api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
			Name:  dcgraphql.OptionalStringOf("mine"),
			Rules: majorRuleOnly(),
		}, nil)
	})
	if in.fired.Load() != 1 || in.err != nil || in.affected != 1 {
		t.Fatalf("the interleaved writer did not land: fired=%d err=%v rows=%d",
			in.fired.Load(), in.err, in.affected)
	}
	if err != nil {
		t.Fatalf("a save with no precondition was refused: %v", err)
	}
	after := readStalePolicy(t, api, ctx)
	if after.Name.String != "mine" || len(after.Rules) != 1 {
		t.Fatalf("the last write did not win: name %q, %d rules", after.Name.String, len(after.Rules))
	}
}

// A guarded save writes the policy row and nothing it carries. The loaded policy holds
// its preloaded rules and their channels; a map update on it would otherwise upsert every
// one of them, through every create callback, on every save.
func TestAGuardedPolicySaveCreatesNoRuleOrChannelRows(t *testing.T) {
	api, db := staleTestApi(t)
	ctx := tenantCtx("A")
	seedStalePolicy(t, api, ctx)
	before := readStalePolicy(t, api, ctx)

	var creates atomic.Int32
	const name = "test:count_child_creates"
	if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		switch tx.Statement.Table {
		case "notification_rules", "notification_channels":
			creates.Add(1)
		}
	}); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })

	expected := versionOf(before)
	if _, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	}, &expected); err != nil {
		t.Fatalf("guarded save: %v", err)
	}
	if n := creates.Load(); n != 0 {
		t.Fatalf("a save that named no rules issued %d creates on the rule and channel tables", n)
	}
	after := readStalePolicy(t, api, ctx)
	if after.Name.String != "Renamed" || !sameIds(ruleIds(after), ruleIds(before)) {
		t.Fatalf("name %q, rule ids %v; want Renamed and %v", after.Name.String, ruleIds(after), ruleIds(before))
	}
}
