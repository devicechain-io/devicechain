// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/maintwindow"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const sundayNightSlots = `[{"day":"SUNDAY","start":"02:00","durationMinutes":240}]`

func newWindowTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
		&CommandDefinition{}, &MetricDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{},
		&MaintenanceWindow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

func utcAt(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

// seedGatedType wires a device type carrying `reboot` (DISRUPTIVE), `read` (NORMAL) and
// `wipe` (DESTRUCTIVE), assigns it a Berlin Sunday-night window, and returns the device tokens.
func seedGatedType(t *testing.T, api *Api, ctx context.Context, family string, n int) []string {
	t.Helper()
	tokens := seedDevicesSharingAType(t, api, ctx, family, n, []*CommandDefinition{
		{CommandKey: "reboot", Impact: ImpactDisruptive},
		{CommandKey: "read", Impact: ImpactNormal},
		{CommandKey: "wipe", Impact: ImpactDestructive},
		{CommandKey: "legacy"}, // published before the field existed: no impact at all
	})
	if _, err := api.CreateMaintenanceWindow(ctx, &MaintenanceWindowCreateRequest{
		Token: "mw-" + family, Timezone: "Europe/Berlin", Slots: sundayNightSlots,
	}); err != nil {
		t.Fatalf("create window: %v", err)
	}
	types, err := api.DeviceTypesByToken(ctx, []string{"type-" + family})
	if err != nil || len(types) != 1 {
		t.Fatalf("resolve type: %v", err)
	}
	if _, err := api.UpdateDeviceType(ctx, "type-"+family, &DeviceTypeUpdateRequest{
		MaintenanceWindowToken: dcgraphql.OptionalString{Set: true, Value: strp("mw-" + family)},
	}); err != nil {
		t.Fatalf("assign window: %v", err)
	}
	return tokens
}

func TestADisruptiveCommandIsHeldUntilTheNextOpening(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	tokens := seedGatedType(t, api, ctx, "plant", 3)

	// Wednesday 2026-01-14 12:00 UTC; the window opens Sunday 02:00 CET = 01:00 UTC.
	at := utcAt(2026, 1, 14, 12, 0)
	holds, refusals, err := api.CommandDispatchSchedule(ctx, tokens, "reboot", nil, at)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if len(refusals) != 0 {
		t.Fatalf("unexpected refusals: %+v", refusals)
	}
	if len(holds) != 3 {
		t.Fatalf("got %d holds, want one per device", len(holds))
	}
	want, closes := utcAt(2026, 1, 18, 1, 0), utcAt(2026, 1, 18, 5, 0)
	for _, h := range holds {
		if !h.DispatchAfter.Equal(want) || !h.DispatchUntil.Equal(closes) || h.WindowToken != "mw-plant" {
			t.Fatalf("hold = %+v, want [%v, %v) under mw-plant", h, want, closes)
		}
	}
}

func TestADestructiveCommandIsHeldToo(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	tokens := seedGatedType(t, api, ctx, "plant", 1)
	holds, _, err := api.CommandDispatchSchedule(ctx, tokens, "wipe", nil, utcAt(2026, 1, 14, 12, 0))
	if err != nil || len(holds) != 1 {
		t.Fatalf("holds = %v, err = %v; a DESTRUCTIVE command must wait", holds, err)
	}
}

func TestANormalCommandIsNeverHeld(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	tokens := seedGatedType(t, api, ctx, "plant", 2)
	at := utcAt(2026, 1, 14, 12, 0)
	// NORMAL, an undeclared impact (a definition from before the field), a key the
	// vocabulary does not know, all dispatch at once: only DISRUPTIVE/DESTRUCTIVE wait.
	for _, key := range []string{"read", "legacy", "no-such-command"} {
		holds, refusals, err := api.CommandDispatchSchedule(ctx, tokens, key, nil, at)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if len(holds) != 0 || len(refusals) != 0 {
			t.Fatalf("%s was gated: holds=%+v refusals=%+v", key, holds, refusals)
		}
	}
}

// A command enqueued while its window is open may go at once, but ONLY until the window
// closes: it is still bound to that opening, so a device that is offline now and returns
// after the close is not sent a disruptive command between windows. Answering "no hold"
// here would leave the command with no window at all.
func TestACommandInsideTheWindowIsBoundToTheCurrentOpening(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	tokens := seedGatedType(t, api, ctx, "plant", 1)
	at := utcAt(2026, 1, 18, 3, 0) // Sunday 04:00 Berlin, inside 02:00-06:00
	// An expiry before the close does not refuse it: the window is open now.
	expires := utcAt(2026, 1, 18, 4, 0)
	holds, refusals, err := api.CommandDispatchSchedule(ctx, tokens, "reboot", &expires, at)
	if err != nil || len(holds) != 1 || len(refusals) != 0 {
		t.Fatalf("inside the window: holds=%v refusals=%v err=%v", holds, refusals, err)
	}
	h := holds[0]
	if h.DispatchAfter.After(at) {
		t.Fatalf("dispatchAfter = %v; an open window must admit the command now (%v)", h.DispatchAfter, at)
	}
	if !h.DispatchUntil.Equal(utcAt(2026, 1, 18, 5, 0)) || h.WindowToken != "mw-plant" {
		t.Fatalf("hold = %+v, want it bound to the opening that closes at 05:00 UTC under mw-plant", h)
	}
}

func TestATypeWithoutAWindowIsNeverHeld(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	tokens := seedDevicesSharingAType(t, api, ctx, "bare", 1,
		[]*CommandDefinition{{CommandKey: "reboot", Impact: ImpactDisruptive}})
	holds, refusals, err := api.CommandDispatchSchedule(ctx, tokens, "reboot", nil, utcAt(2026, 1, 14, 12, 0))
	if err != nil || len(holds) != 0 || len(refusals) != 0 {
		t.Fatalf("no window assigned: holds=%v refusals=%v err=%v", holds, refusals, err)
	}
}

func TestRefusedWhenTheNextOpeningIsAfterExpiry(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	tokens := seedGatedType(t, api, ctx, "plant", 2)
	at := utcAt(2026, 1, 14, 12, 0)

	// Expires Saturday: the Sunday opening is too late.
	expires := utcAt(2026, 1, 17, 0, 0)
	holds, refusals, err := api.CommandDispatchSchedule(ctx, tokens, "reboot", &expires, at)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if len(holds) != 0 || len(refusals) != 2 {
		t.Fatalf("holds=%d refusals=%d, want 0 and 2", len(holds), len(refusals))
	}
	if refusals[0].Code != RejectWindowAfterExpiry {
		t.Fatalf("code = %q, want %q", refusals[0].Code, RejectWindowAfterExpiry)
	}

	// Expires Monday: the same opening now fits, so the command is held rather than refused.
	expires = utcAt(2026, 1, 19, 0, 0)
	holds, refusals, err = api.CommandDispatchSchedule(ctx, tokens, "reboot", &expires, at)
	if err != nil || len(refusals) != 0 || len(holds) != 2 {
		t.Fatalf("holds=%d refusals=%d err=%v, want a hold per device", len(holds), len(refusals), err)
	}

	// Expiry exactly at the opening still admits it: the window opens AT that instant.
	expires = utcAt(2026, 1, 18, 1, 0)
	holds, refusals, _ = api.CommandDispatchSchedule(ctx, tokens, "reboot", &expires, at)
	if len(refusals) != 0 || len(holds) != 2 {
		t.Fatalf("expiry at the opening: holds=%d refusals=%d", len(holds), len(refusals))
	}
}

func TestEditingAWindowChangesTheNextOpening(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	seedGatedType(t, api, ctx, "plant", 1)
	at := utcAt(2026, 1, 14, 12, 0)

	before, err := api.MaintenanceWindowOpening(ctx, "mw-plant", at)
	if err != nil || !before.Found || before.OpenNow {
		t.Fatalf("before = %+v, err = %v", before, err)
	}
	if _, err := api.UpdateMaintenanceWindow(ctx, "mw-plant", &MaintenanceWindowUpdateRequest{
		Slots: dcgraphql.OptionalString{Set: true, Value: strp(`[{"day":"FRIDAY","start":"20:00","durationMinutes":60}]`)},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, err := api.MaintenanceWindowOpening(ctx, "mw-plant", at)
	if err != nil {
		t.Fatal(err)
	}
	if want := utcAt(2026, 1, 16, 19, 0); !after.DispatchAfter.Equal(want) { // Friday 20:00 CET
		t.Fatalf("after the edit = %v, want %v", after.DispatchAfter, want)
	}
	if !after.DispatchAfter.Before(before.DispatchAfter) {
		t.Fatal("the edit should have pulled the opening earlier")
	}
}

func TestAMissingWindowIsReportedNotFoundNotAnError(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	opening, err := api.MaintenanceWindowOpening(ctx, "gone", time.Now())
	if err != nil || opening.Found {
		t.Fatalf("opening = %+v, err = %v", opening, err)
	}
}

func TestWindowsAreTenantScoped(t *testing.T) {
	api := newWindowTestApi(t)
	a := core.WithTenant(context.Background(), "A")
	b := core.WithTenant(context.Background(), "B")
	seedGatedType(t, api, a, "plant", 1)
	opening, err := api.MaintenanceWindowOpening(b, "mw-plant", time.Now())
	if err != nil || opening.Found {
		t.Fatalf("tenant B resolved tenant A's window: %+v, err = %v", opening, err)
	}
}

func TestAWindowThatNoLongerValidatesFailsTheCheckClosed(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	tokens := seedGatedType(t, api, ctx, "plant", 1)
	// A hand-edited row: the zone is garbage. The gate must error, never treat it as open.
	if err := api.RDB.DB(ctx).Model(&MaintenanceWindow{}).Where("token = ?", "mw-plant").
		Update("timezone", "Mars/Olympus").Error; err != nil {
		t.Fatal(err)
	}
	_, _, err := api.CommandDispatchSchedule(ctx, tokens, "reboot", nil, time.Now())
	if err == nil {
		t.Fatal("an unevaluable window was read as no hold")
	}
}

func TestWindowValidationAtTheApi(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	bad := map[string]MaintenanceWindowCreateRequest{
		"unknown zone":  {Token: "w1", Timezone: "Nowhere/Land", Slots: sundayNightSlots},
		"no slots":      {Token: "w2", Timezone: "UTC", Slots: `[]`},
		"unknown key":   {Token: "w3", Timezone: "UTC", Slots: `[{"day":"SUNDAY","start":"02:00","durationMinutes":60,"cron":"* * * * *"}]`},
		"bad day":       {Token: "w4", Timezone: "UTC", Slots: `[{"day":"Sun","start":"02:00","durationMinutes":60}]`},
		"not an array":  {Token: "w5", Timezone: "UTC", Slots: `{"day":"SUNDAY"}`},
		"trailing junk": {Token: "w6", Timezone: "UTC", Slots: sundayNightSlots + `[]`},
	}
	for name, req := range bad {
		req := req
		t.Run(name, func(t *testing.T) {
			_, err := api.CreateMaintenanceWindow(ctx, &req)
			if err == nil || !errors.Is(err, maintwindow.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestADeviceTypeCannotBeAssignedAnUnknownWindow(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	seedDevicesSharingAType(t, api, ctx, "x", 0, nil)
	_, err := api.UpdateDeviceType(ctx, "type-x", &DeviceTypeUpdateRequest{
		MaintenanceWindowToken: dcgraphql.OptionalString{Set: true, Value: strp("nope")},
	})
	if err == nil {
		t.Fatal("assigned a window that does not exist")
	}
}

func TestAWindowInUseCannotBeDeleted(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	seedGatedType(t, api, ctx, "plant", 1)
	if _, err := api.DeleteMaintenanceWindow(ctx, "mw-plant"); !errors.Is(err, ErrEntityInUse) {
		t.Fatalf("err = %v, want ErrEntityInUse", err)
	}
	// Unassigned, it can go.
	if _, err := api.UpdateDeviceType(ctx, "type-plant", &DeviceTypeUpdateRequest{
		MaintenanceWindowToken: dcgraphql.OptionalString{Set: true, Value: nil},
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := api.DeleteMaintenanceWindow(ctx, "mw-plant"); err != nil || !ok {
		t.Fatalf("delete = %v, %v", ok, err)
	}
}

func TestACommandDefinitionRefusesAnUnknownImpact(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	seedDevicesSharingAType(t, api, ctx, "x", 0, nil)
	_, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
		Token: "cd-bad", DeviceProfileToken: "prof-x", CommandKey: "reboot", Impact: strp("EXPLOSIVE"),
	})
	if err == nil {
		t.Fatal("an impact nothing recognises was stored; the gate would read it as NORMAL")
	}
	created, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
		Token: "cd-ok", DeviceProfileToken: "prof-x", CommandKey: "reboot",
	})
	if err != nil || created.Impact != ImpactNormal {
		t.Fatalf("default impact = %q, err = %v, want NORMAL", created.Impact, err)
	}
}

// A version published before the field existed has no Impact key in its frozen snapshot. It
// must decode to NORMAL, never to a gated impact.
func TestAnOldSnapshotDecodesAsNormal(t *testing.T) {
	raw := []byte(`{"Commands":[{"CommandKey":"reboot","Token":"c1"}]}`)
	var snap ProfileSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if got := snap.Commands[0]; got.EffectiveImpact() != ImpactNormal || got.WindowGated() {
		t.Fatalf("an unstamped definition reads as %q, gated=%v", got.EffectiveImpact(), got.WindowGated())
	}
}
