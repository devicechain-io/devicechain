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
	if err := db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{}, &DeviceProfileActivation{},
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
	holds, refusals, _, err := api.CommandDispatchSchedule(ctx, tokens, "reboot", nil, at)
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
	holds, _, _, err := api.CommandDispatchSchedule(ctx, tokens, "wipe", nil, utcAt(2026, 1, 14, 12, 0))
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
		holds, refusals, _, err := api.CommandDispatchSchedule(ctx, tokens, key, nil, at)
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
	holds, refusals, _, err := api.CommandDispatchSchedule(ctx, tokens, "reboot", &expires, at)
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
	holds, refusals, _, err := api.CommandDispatchSchedule(ctx, tokens, "reboot", nil, utcAt(2026, 1, 14, 12, 0))
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
	holds, refusals, _, err := api.CommandDispatchSchedule(ctx, tokens, "reboot", &expires, at)
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
	holds, refusals, _, err = api.CommandDispatchSchedule(ctx, tokens, "reboot", &expires, at)
	if err != nil || len(refusals) != 0 || len(holds) != 2 {
		t.Fatalf("holds=%d refusals=%d err=%v, want a hold per device", len(holds), len(refusals), err)
	}

	// Expiry exactly at the opening still admits it: the window opens AT that instant.
	expires = utcAt(2026, 1, 18, 1, 0)
	holds, refusals, _, _ = api.CommandDispatchSchedule(ctx, tokens, "reboot", &expires, at)
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
	_, _, _, err := api.CommandDispatchSchedule(ctx, tokens, "reboot", nil, time.Now())
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

// Ungated names exactly the devices the command may reach at any time: an enabled device whose
// type does not gate it. A disabled device and an unknown token are in NO list, because the
// re-stamp pass releases a waiting command's hold only for a device named here, and silence
// about a disabled device must never read as "release it".
func TestUngatedNamesOnlyEnabledDevicesWhoseTypeDoesNotGate(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	tokens := seedGatedType(t, api, ctx, "plant", 3)
	at := utcAt(2026, 1, 14, 12, 0)

	// Control: while the type has its window every device is held and none is ungated.
	holds, _, ungated, err := api.CommandDispatchSchedule(ctx, tokens, "reboot", nil, at)
	if err != nil || len(holds) != 3 || len(ungated) != 0 {
		t.Fatalf("control: holds=%d ungated=%v err=%v", len(holds), ungated, err)
	}
	// A NORMAL command is ungated on every enabled device.
	_, _, ungated, err = api.CommandDispatchSchedule(ctx, tokens, "read", nil, at)
	if err != nil || len(ungated) != 3 {
		t.Fatalf("a NORMAL command: ungated=%v err=%v, want all three", ungated, err)
	}

	if err := api.RDB.DB(ctx).Model(&Device{}).Where("token = ?", tokens[0]).
		Update("disabled_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := api.UpdateDeviceType(ctx, "type-plant", &DeviceTypeUpdateRequest{
		MaintenanceWindowToken: dcgraphql.OptionalString{Set: true, Value: nil},
	}); err != nil {
		t.Fatalf("unassign window: %v", err)
	}
	holds, refusals, ungated, err := api.CommandDispatchSchedule(ctx, append(tokens, "nobody"), "reboot", nil, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(holds) != 0 || len(refusals) != 0 {
		t.Fatalf("an unassigned type still gated: holds=%+v refusals=%+v", holds, refusals)
	}
	if len(ungated) != 2 || ungated[0] != tokens[1] || ungated[1] != tokens[2] {
		t.Fatalf("ungated = %v, want only the two enabled devices %v", ungated, tokens[1:])
	}
}

// A command already waiting for a window keeps waiting when the profile version its type is on
// now marks it NORMAL: asked with alreadyHeld, the answer follows the type's window alone. A
// command the profile no longer defines is UNDEFINED in both modes, never ungated, and only a
// type with no window makes an already-held command ungated.
func TestAlreadyHeldFollowsTheWindowAndNamesUndefinedCommands(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	tokens := seedGatedType(t, api, ctx, "plant", 2)
	at := utcAt(2026, 1, 14, 12, 0)

	// Enqueue mode: NORMAL is ungated, an undefined key is undefined.
	enq, err := api.DispatchScheduleFor(ctx, tokens, "read", nil, at, false)
	if err != nil || len(enq.Holds) != 0 || len(enq.Ungated) != 2 || len(enq.Undefined) != 0 {
		t.Fatalf("enqueue NORMAL: %+v err=%v, want both ungated", enq, err)
	}
	// Held mode: the same NORMAL command stays on the type's window.
	held, err := api.DispatchScheduleFor(ctx, tokens, "read", nil, at, true)
	if err != nil || len(held.Holds) != 2 || len(held.Ungated) != 0 || len(held.Undefined) != 0 {
		t.Fatalf("held NORMAL: %+v err=%v, want both held", held, err)
	}
	if held.Holds[0].WindowToken != "mw-plant" {
		t.Fatalf("held NORMAL window = %q, want mw-plant", held.Holds[0].WindowToken)
	}
	for _, mode := range []bool{false, true} {
		gone, err := api.DispatchScheduleFor(ctx, tokens, "gone", nil, at, mode)
		if err != nil || len(gone.Holds) != 0 || len(gone.Ungated) != 0 || len(gone.Undefined) != 2 {
			t.Fatalf("alreadyHeld=%v undefined key: %+v err=%v, want both undefined", mode, gone, err)
		}
	}

	// Unassigning the type's window: only then is an already-held command ungated, and an
	// undefined one is still undefined.
	if _, err := api.UpdateDeviceType(ctx, "type-plant", &DeviceTypeUpdateRequest{
		MaintenanceWindowToken: dcgraphql.OptionalString{Set: true, Value: nil},
	}); err != nil {
		t.Fatalf("unassign window: %v", err)
	}
	held, err = api.DispatchScheduleFor(ctx, tokens, "read", nil, at, true)
	if err != nil || len(held.Holds) != 0 || len(held.Ungated) != 2 {
		t.Fatalf("held NORMAL, no window: %+v err=%v, want both ungated", held, err)
	}
	gone, err := api.DispatchScheduleFor(ctx, tokens, "gone", nil, at, true)
	if err != nil || len(gone.Ungated) != 0 || len(gone.Undefined) != 2 {
		t.Fatalf("undefined, no window: %+v err=%v, want both undefined", gone, err)
	}
}

// 🔴 "UNDEFINED" FAILS COMMANDS, SO ONLY A DEFINITIVE ANSWER MAY PRODUCE IT. A profile whose
// active-version pointer names a version row that is missing, or a type naming a profile that
// cannot be found, is an invariant breach: the schedule must ERROR (the re-stamp pass then leaves
// every row as stamped), never name the devices undefined. A type with no profile at all is the
// definitive case, and is undefined.
func TestAnUnreadableVocabularyIsAnErrorNeverUndefined(t *testing.T) {
	at := utcAt(2026, 1, 14, 12, 0)
	for _, held := range []bool{false, true} {
		api := newWindowTestApi(t)
		ctx := core.WithTenant(context.Background(), "A")
		tokens := seedGatedType(t, api, ctx, "plant", 2)
		types, err := api.DeviceTypesByToken(ctx, []string{"type-plant"})
		if err != nil || len(types) != 1 || types[0].ProfileId == nil {
			t.Fatalf("resolve type: %v", err)
		}
		profileId := *types[0].ProfileId

		// The active version's row vanishes.
		if err := api.RDB.DB(ctx).Where("device_profile_id = ?", profileId).
			Delete(&DeviceProfileVersion{}).Error; err != nil {
			t.Fatalf("delete versions: %v", err)
		}
		if got, err := api.DispatchScheduleFor(ctx, tokens, "reboot", nil, at, held); err == nil {
			t.Fatalf("alreadyHeld=%v missing version row: %+v, want an error", held, got)
		}

		// The profile itself vanishes.
		if err := api.RDB.DB(ctx).Unscoped().Where("id = ?", profileId).Delete(&DeviceProfile{}).Error; err != nil {
			t.Fatalf("delete profile: %v", err)
		}
		if got, err := api.DispatchScheduleFor(ctx, tokens, "reboot", nil, at, held); err == nil {
			t.Fatalf("alreadyHeld=%v missing profile: %+v, want an error", held, got)
		}

		// The type has no profile: definitive, so undefined.
		if err := api.RDB.DB(ctx).Model(&DeviceType{}).Where("id = ?", types[0].ID).
			Update("profile_id", nil).Error; err != nil {
			t.Fatalf("detach profile: %v", err)
		}
		got, err := api.DispatchScheduleFor(ctx, tokens, "reboot", nil, at, held)
		if err != nil || len(got.Undefined) != 2 || len(got.Holds) != 0 || len(got.Ungated) != 0 {
			t.Fatalf("alreadyHeld=%v no profile: %+v err=%v, want both undefined", held, got, err)
		}
	}
}

// Impacts carries the classification the window gate decided on, per device, for every device
// whose type DEFINES the command. A transport adapter that gates on impact reads it, so it must
// hold on every path out of the verdict: gated, not gated, and a type with no window at all.
// Devices the schedule decides nothing about (disabled, unknown, command undefined) have no
// entry, and the absence is the signal: it must never read as NORMAL.
func TestImpactsNameTheClassificationForEveryDeviceThatDefinesTheCommand(t *testing.T) {
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	tokens := seedGatedType(t, api, ctx, "plant", 3)
	at := utcAt(2026, 1, 14, 12, 0)

	byToken := func(s *DispatchSchedule) map[string]string {
		out := map[string]string{}
		for _, i := range s.Impacts {
			out[i.DeviceToken] = i.Impact
		}
		return out
	}

	// Gated path: the disruptive command is held, and its impact is reported.
	reboot, err := api.DispatchScheduleFor(ctx, tokens, "reboot", nil, at, false)
	if err != nil || len(reboot.Holds) != 3 {
		t.Fatalf("control: %+v err=%v", reboot, err)
	}
	for _, token := range tokens {
		if got := byToken(reboot)[token]; got != ImpactDisruptive {
			t.Fatalf("reboot impact for %s = %q, want DISRUPTIVE", token, got)
		}
	}
	wipe, err := api.DispatchScheduleFor(ctx, tokens, "wipe", nil, at, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens {
		if got := byToken(wipe)[token]; got != ImpactDestructive {
			t.Fatalf("wipe impact for %s = %q, want DESTRUCTIVE: the field must follow the definition", token, got)
		}
	}
	// Not-gated path: a NORMAL command is ungated and still carries its (NORMAL) impact. The
	// two must differ, or the field is a constant.
	read, err := api.DispatchScheduleFor(ctx, tokens, "read", nil, at, false)
	if err != nil || len(read.Ungated) != 3 {
		t.Fatalf("control: %+v err=%v", read, err)
	}
	for _, token := range tokens {
		if got := byToken(read)[token]; got != ImpactNormal {
			t.Fatalf("read impact for %s = %q, want NORMAL", token, got)
		}
	}
	// An undefined command has NO entry: silence is not NORMAL.
	gone, err := api.DispatchScheduleFor(ctx, tokens, "gone", nil, at, false)
	if err != nil || len(gone.Undefined) != 3 || len(gone.Impacts) != 0 {
		t.Fatalf("undefined command: %+v err=%v, want no impacts", gone, err)
	}
	// A disabled device and an unknown token are decided nothing about.
	if err := api.RDB.DB(ctx).Model(&Device{}).Where("token = ?", tokens[0]).
		Update("disabled_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	some, err := api.DispatchScheduleFor(ctx, append(tokens, "nobody"), "reboot", nil, at, false)
	if err != nil {
		t.Fatal(err)
	}
	got := byToken(some)
	if _, ok := got[tokens[0]]; ok {
		t.Fatalf("a disabled device has an impact entry: %v", got)
	}
	if _, ok := got["nobody"]; ok || len(got) != 2 {
		t.Fatalf("impacts = %v, want exactly the two enabled devices", got)
	}
	// No-window path: the type carries no window, the command is ungated, and its impact is
	// still reported as DISRUPTIVE rather than defaulting.
	if _, err := api.UpdateDeviceType(ctx, "type-plant", &DeviceTypeUpdateRequest{
		MaintenanceWindowToken: dcgraphql.OptionalString{Set: true, Value: nil},
	}); err != nil {
		t.Fatalf("unassign window: %v", err)
	}
	free, err := api.DispatchScheduleFor(ctx, tokens[1:], "reboot", nil, at, false)
	if err != nil || len(free.Ungated) != 2 {
		t.Fatalf("no window: %+v err=%v", free, err)
	}
	for _, token := range tokens[1:] {
		if got := byToken(free)[token]; got != ImpactDisruptive {
			t.Fatalf("no-window reboot impact for %s = %q, want its declared class, not a default", token, got)
		}
	}
}
