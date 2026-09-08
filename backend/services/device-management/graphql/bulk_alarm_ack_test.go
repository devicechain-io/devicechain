// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// alarmAckTestCtx builds a real sqlite-backed device-management Api holding three UNACKNOWLEDGED
// alarms, in a tenant context. Three, not one, so "acknowledged nothing" and "acknowledged the whole
// table" are different-looking results — the same reason deviceIdTestCtx seeds three devices.
func alarmAckTestCtx(t *testing.T) (context.Context, *model.Api) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Alarm{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := core.WithTenant(context.Background(), "acme")
	api := model.NewApi(&rdb.RdbManager{Database: db})
	for _, token := range []string{"alarm-01", "alarm-02", "alarm-03"} {
		alarm := &model.Alarm{
			OriginatorType: "device",
			OriginatorId:   1,
			AlarmKey:       "over-temp",
			MetricKey:      "tempC",
			State:          string(model.AlarmStateActive),
			Severity:       string(model.AlarmSeverityCritical),
			RaisedTime:     time.Now().UTC(),
		}
		alarm.Token = token
		if err := api.RDB.DB(ctx).Create(alarm).Error; err != nil {
			t.Fatalf("seed %s: %v", token, err)
		}
	}
	return context.WithValue(ctx, gqlcore.ContextApiKey, api), api
}

// unackedCount reports how many alarms are still unacknowledged.
func unackedCount(t *testing.T, ctx context.Context, api *model.Api) int64 {
	t.Helper()
	var n int64
	if err := api.RDB.DB(ctx).Model(&model.Alarm{}).Where("acknowledged = ?", false).Count(&n).Error; err != nil {
		t.Fatalf("count unacknowledged: %v", err)
	}
	return n
}

// 🔴 THE FAILURE THIS TEST EXISTS FOR: acknowledgeAlarms(tokens: []) must acknowledge NOTHING.
//
// An empty slice is one gorm formulation away from being no condition at all — the shape that once
// turned devicesById into a full-table read. The blast radius here is worse than a read: it would
// silently acknowledge every alarm in the tenant, destroying exactly the signal an operator relies
// on during a storm, and leaving a plausible-looking audit trail behind it. It is asserted on the
// whole resolver path, not only over the model helper, because a resolver that stops calling the
// guarded helper is a change the helper's own test cannot see.
func TestAcknowledgeAlarmsWithNoTokensAcknowledgesNothing(t *testing.T) {
	ctx, api := alarmAckTestCtx(t)
	ctx = withAuthorities(ctx, auth.AlarmWrite)
	r := &SchemaResolver{}

	result, err := r.AcknowledgeAlarms(ctx, struct{ Tokens []string }{Tokens: []string{}})
	if err != nil {
		t.Fatalf("acknowledgeAlarms([]): %v", err)
	}
	if got := len(result.Acknowledged()); got != 0 {
		t.Fatalf("acknowledgeAlarms([]) reported %d acknowledged, want none", got)
	}
	if got := len(result.Refusals()); got != 0 {
		t.Fatalf("acknowledgeAlarms([]) reported %d refusals, want none — it refused nothing and acted on nothing", got)
	}
	if got := unackedCount(t, ctx, api); got != 3 {
		t.Fatalf("%d alarms left unacknowledged, want 3 — the empty token list was read as 'all'", got)
	}
}

// The counterweight: a resolver that acknowledged nothing under every input would satisfy the test
// above while breaking the feature. Naming two of the three alarms must acknowledge exactly those
// two — and leave the third alone.
func TestAcknowledgeAlarmsAcknowledgesExactlyThoseNamed(t *testing.T) {
	ctx, api := alarmAckTestCtx(t)
	ctx = withAuthorities(ctx, auth.AlarmWrite)
	r := &SchemaResolver{}

	result, err := r.AcknowledgeAlarms(ctx, struct{ Tokens []string }{Tokens: []string{"alarm-01", "alarm-03"}})
	if err != nil {
		t.Fatalf("acknowledgeAlarms: %v", err)
	}
	acked := result.Acknowledged()
	if len(acked) != 2 {
		t.Fatalf("acknowledged %d alarms, want 2", len(acked))
	}
	// First-named order, so the answer is a deterministic function of the request.
	if acked[0].Token() != "alarm-01" || acked[1].Token() != "alarm-03" {
		t.Fatalf("acknowledged out of first-named order: %q, %q", acked[0].Token(), acked[1].Token())
	}
	for _, a := range acked {
		if !a.Acknowledged() {
			t.Fatalf("%s came back in `acknowledged` but is not acknowledged", a.Token())
		}
	}
	if got := unackedCount(t, ctx, api); got != 1 {
		t.Fatalf("%d alarms left unacknowledged, want 1 (alarm-02 untouched)", got)
	}
}

// Partial by design: a token naming no alarm is refused BY NAME, and every other named alarm is
// still acknowledged. Failing the whole request would leave an operator with a stale selection no
// way to make progress except to bisect the list by hand.
func TestAcknowledgeAlarmsIsPartialOnAnUnknownToken(t *testing.T) {
	ctx, api := alarmAckTestCtx(t)
	ctx = withAuthorities(ctx, auth.AlarmWrite)
	r := &SchemaResolver{}

	result, err := r.AcknowledgeAlarms(ctx, struct{ Tokens []string }{
		Tokens: []string{"alarm-01", "gone", "alarm-02"},
	})
	if err != nil {
		t.Fatalf("a vanished alarm must not fail the request: %v", err)
	}
	if got := len(result.Acknowledged()); got != 2 {
		t.Fatalf("acknowledged %d alarms, want the 2 that exist", got)
	}
	refusals := result.Refusals()
	if len(refusals) != 1 {
		t.Fatalf("got %d refusals, want 1", len(refusals))
	}
	if refusals[0].Token() != "gone" {
		t.Fatalf("refusal names %q, want the token that was not found", refusals[0].Token())
	}
	if refusals[0].Code() != string(model.AlarmAckNotFound) {
		t.Fatalf("refusal code = %q, want %q", refusals[0].Code(), model.AlarmAckNotFound)
	}
	if got := unackedCount(t, ctx, api); got != 1 {
		t.Fatalf("%d alarms left unacknowledged, want 1", got)
	}
}

// An over-large request is an ERROR, not a refusal list — it is a fault in how the caller chunked
// its work, not a verdict about any alarm. Asserted at the bound, and just under it, so the check
// cannot degenerate into "always refuse".
func TestAcknowledgeAlarmsIsBounded(t *testing.T) {
	ctx, _ := alarmAckTestCtx(t)
	ctx = withAuthorities(ctx, auth.AlarmWrite)
	r := &SchemaResolver{}

	tooMany := make([]string, model.MaxBulkAcknowledgeAlarms+1)
	for i := range tooMany {
		tooMany[i] = "alarm-" + strconv.Itoa(i)
	}
	if _, err := r.AcknowledgeAlarms(ctx, struct{ Tokens []string }{Tokens: tooMany}); err == nil {
		t.Fatalf("a request of %d tokens must be refused; the limit is %d",
			len(tooMany), model.MaxBulkAcknowledgeAlarms)
	}

	atLimit := tooMany[:model.MaxBulkAcknowledgeAlarms]
	if _, err := r.AcknowledgeAlarms(ctx, struct{ Tokens []string }{Tokens: atLimit}); err != nil {
		t.Fatalf("a request exactly AT the limit must be accepted: %v", err)
	}
}

// Duplicate tokens are collapsed: naming one alarm three times acknowledges it once and reports it
// once, so a client that double-added a row to its selection does not read a repeated entry as
// three separate alarms.
func TestAcknowledgeAlarmsCollapsesDuplicates(t *testing.T) {
	ctx, _ := alarmAckTestCtx(t)
	ctx = withAuthorities(ctx, auth.AlarmWrite)
	r := &SchemaResolver{}

	result, err := r.AcknowledgeAlarms(ctx, struct{ Tokens []string }{
		Tokens: []string{"alarm-01", "alarm-01", "alarm-01"},
	})
	if err != nil {
		t.Fatalf("acknowledgeAlarms: %v", err)
	}
	if got := len(result.Acknowledged()); got != 1 {
		t.Fatalf("acknowledged reported %d entries for one repeated token, want 1", got)
	}
}

// The same authority as the single mutation, and no other. Without it the bulk path would be a way
// around the gate on acknowledgeAlarm — with more blast radius than the thing it bypassed.
func TestAcknowledgeAlarmsRequiresAlarmWrite(t *testing.T) {
	ctx, api := alarmAckTestCtx(t)
	r := &SchemaResolver{}

	// A caller holding only read authority.
	readOnly := withAuthorities(ctx, auth.AlarmRead)
	if _, err := r.AcknowledgeAlarms(readOnly, struct{ Tokens []string }{Tokens: []string{"alarm-01"}}); err == nil {
		t.Fatal("acknowledgeAlarms must refuse a caller without alarm:write")
	}
	if got := unackedCount(t, ctx, api); got != 3 {
		t.Fatalf("%d alarms left unacknowledged after a refused call, want 3", got)
	}
}

// Acknowledgment is idempotent, and the bulk path inherits that from the single one: re-acknowledging
// an already-acknowledged alarm returns it in `acknowledged` rather than refusing it, and does not
// move the recorded acknowledgment time.
func TestAcknowledgeAlarmsIsIdempotent(t *testing.T) {
	ctx, api := alarmAckTestCtx(t)
	ctx = withAuthorities(ctx, auth.AlarmWrite)
	r := &SchemaResolver{}

	if _, err := r.AcknowledgeAlarms(ctx, struct{ Tokens []string }{Tokens: []string{"alarm-01"}}); err != nil {
		t.Fatalf("first acknowledge: %v", err)
	}
	first, err := api.AlarmsByToken(ctx, []string{"alarm-01"})
	if err != nil || len(first) != 1 {
		t.Fatalf("read back: %v", err)
	}
	firstAckedAt := first[0].AcknowledgedTime

	result, err := r.AcknowledgeAlarms(ctx, struct{ Tokens []string }{Tokens: []string{"alarm-01"}})
	if err != nil {
		t.Fatalf("second acknowledge: %v", err)
	}
	if got := len(result.Acknowledged()); got != 1 {
		t.Fatalf("a repeat acknowledge reported %d acknowledged, want 1", got)
	}
	if got := len(result.Refusals()); got != 0 {
		t.Fatalf("a repeat acknowledge produced %d refusals, want none", got)
	}
	again, err := api.AlarmsByToken(ctx, []string{"alarm-01"})
	if err != nil || len(again) != 1 {
		t.Fatalf("read back: %v", err)
	}
	if again[0].AcknowledgedTime.Time != firstAckedAt.Time {
		t.Fatalf("the acknowledgment time moved on a repeat: %v → %v", firstAckedAt.Time, again[0].AcknowledgedTime.Time)
	}
}

// 🔴 The bulk path must be tenant-scoped like every other alarm write, and the global GORM callback
// is what enforces it — but "the callback saves us" is a claim, not an observation, so it is measured
// here: with no tenant in context the request must FAIL rather than acknowledge across tenants.
func TestAcknowledgeAlarmsFailsClosedWithNoTenant(t *testing.T) {
	ctx, api := alarmAckTestCtx(t)
	tenantless := context.WithValue(withAuthorities(context.Background(), auth.AlarmWrite),
		gqlcore.ContextApiKey, api)
	r := &SchemaResolver{}

	if _, err := r.AcknowledgeAlarms(tenantless, struct{ Tokens []string }{Tokens: []string{"alarm-01"}}); err == nil {
		t.Fatal("acknowledgeAlarms with no tenant in context must be refused")
	}
	if got := unackedCount(t, ctx, api); got != 3 {
		t.Fatalf("%d alarms left unacknowledged after a tenantless call, want 3", got)
	}
}
