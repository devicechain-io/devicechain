// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newEnqueueTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	// PublishDeviceProfile snapshots every definition list and reconciles rule scope
	// refs, so all of those tables must exist even though this gate only reads commands.
	if err := db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
		&CommandDefinition{}, &MetricDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// seedDeviceWithCommands wires device -> type -> profile and, when commands is
// non-nil, PUBLISHES a profile version carrying them. Publishing (rather than just
// creating draft rows) is the point: the enqueue gate reads the active published
// snapshot, so a test that only seeded drafts would validate against a vocabulary
// the device never actually receives.
func seedDeviceWithCommands(t *testing.T, api *Api, ctx context.Context, deviceToken string, commands []*CommandDefinition) {
	t.Helper()

	profile := &DeviceProfile{}
	profile.Token = "prof-" + deviceToken
	if err := api.RDB.DB(ctx).Create(profile).Error; err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	dtype := &DeviceType{}
	dtype.Token = "type-" + deviceToken
	dtype.ProfileId = &profile.ID
	if err := api.RDB.DB(ctx).Create(dtype).Error; err != nil {
		t.Fatalf("seed device type: %v", err)
	}

	device := &Device{DeviceTypeId: dtype.ID}
	device.Token = deviceToken
	if err := api.RDB.DB(ctx).Create(device).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}

	if commands == nil {
		return
	}
	for _, cd := range commands {
		cd.DeviceProfileId = profile.ID
		if cd.Token == "" {
			cd.Token = "cd-" + cd.CommandKey + "-" + deviceToken
		}
		if err := api.RDB.DB(ctx).Create(cd).Error; err != nil {
			t.Fatalf("seed command definition: %v", err)
		}
	}
	if _, err := api.PublishDeviceProfile(ctx, profile.Token, nil, nil, "test"); err != nil {
		t.Fatalf("publish profile: %v", err)
	}
}

// The enqueue gate implements ADR-043 decision 3 + 4 exactly. The three-way
// strictness is the whole feature, and getting any leg wrong is either a security
// hole (accepting a mis-keyed actuation) or an outage (rejecting every ad-hoc
// command on an unpublished profile).
func TestValidateCommandEnqueue(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")

	t.Run("nonexistent device is rejected", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		v, err := api.ValidateCommandEnqueue(ctx, "ghost", "reboot", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Allowed {
			t.Fatal("a command to a nonexistent device must be rejected")
		}
		if !strings.Contains(v.Reason, "does not exist") {
			t.Fatalf("unhelpful reason: %q", v.Reason)
		}
	})

	// ADR-043 decision 4: the backward path. A profile that declares no command
	// vocabulary (or was never published) keeps accepting ad-hoc commands.
	t.Run("profile with no published vocabulary allows free-form", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-freeform", nil)
		v, err := api.ValidateCommandEnqueue(ctx, "dev-freeform", "anything", []byte(`{"whatever":123}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !v.Allowed {
			t.Fatalf("an undeclared vocabulary must stay free-form, got: %q", v.Reason)
		}
	})

	t.Run("declared vocabulary rejects an unknown command key", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-strict", []*CommandDefinition{
			defWithSchema(t, "reboot", nil),
		})
		v, err := api.ValidateCommandEnqueue(ctx, "dev-strict", "self-destruct", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Allowed {
			t.Fatal("a command outside the declared vocabulary must be rejected")
		}
		if !strings.Contains(v.Reason, "self-destruct") {
			t.Fatalf("reason should name the offending command: %q", v.Reason)
		}
	})

	t.Run("declared command with empty schema accepts any payload", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-empty", []*CommandDefinition{
			defWithSchema(t, "reboot", nil),
		})
		v, err := api.ValidateCommandEnqueue(ctx, "dev-empty", "reboot", []byte(`{"force":true}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !v.Allowed {
			t.Fatalf("an empty schema must accept any payload, got: %q", v.Reason)
		}
	})

	t.Run("payload is validated against the declared schema", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-typed", []*CommandDefinition{
			defWithSchema(t, "drive", []CommandParameter{
				{Name: "speed", DataType: MetricInt, Required: true, MinValue: f64(0), MaxValue: f64(100)},
				{Name: "gear", DataType: MetricString, Enum: []string{"low", "high"}},
			}),
		})

		cases := []struct {
			name    string
			payload string
			allowed bool
		}{
			{"valid", `{"speed":50,"gear":"low"}`, true},
			{"required missing", `{"gear":"low"}`, false},
			{"out of bounds", `{"speed":500}`, false},
			{"wrong type", `{"speed":"fast"}`, false},
			{"not in enum", `{"speed":50,"gear":"turbo"}`, false},
			{"undeclared key", `{"speed":50,"nitro":true}`, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				v, err := api.ValidateCommandEnqueue(ctx, "dev-typed", "drive", []byte(tc.payload))
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if v.Allowed != tc.allowed {
					t.Fatalf("allowed=%v (want %v) for %s; reason=%q", v.Allowed, tc.allowed, tc.payload, v.Reason)
				}
			})
		}
	})

	// A device whose profile declares a required parameter must not accept a
	// no-argument command: this is the case an "absent payload is always fine"
	// shortcut would wrongly admit.
	t.Run("absent payload still fails a required parameter", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-req", []*CommandDefinition{
			defWithSchema(t, "drive", []CommandParameter{
				{Name: "speed", DataType: MetricInt, Required: true},
			}),
		})
		v, err := api.ValidateCommandEnqueue(ctx, "dev-req", "drive", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Allowed {
			t.Fatal("a missing required parameter must be rejected even with no payload")
		}
	})

	// The gate reads the PUBLISHED snapshot, never the draft. A definition authored
	// but not yet published must not constrain enqueue: the device has not been told
	// about it, so validating against it would reject commands the device accepts
	// (and, symmetrically, accept ones it does not). This is the single easiest thing
	// to get wrong here — CommandDefinitionsByDeviceProfile returns the draft and
	// reads almost identically at the call site.
	t.Run("an unpublished draft definition does not constrain enqueue", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		// Seed device+type+profile with NO published version...
		seedDeviceWithCommands(t, api, ctx, "dev-draft", nil)
		// ...then author a draft definition against that profile, without publishing.
		profile, err := api.deviceProfileByToken(ctx, "prof-dev-draft")
		if err != nil {
			t.Fatalf("load profile: %v", err)
		}
		draft := defWithSchema(t, "drive", []CommandParameter{
			{Name: "speed", DataType: MetricInt, Required: true},
		})
		draft.DeviceProfileId = profile.ID
		draft.Token = "cd-draft-only"
		if err := api.RDB.DB(ctx).Create(draft).Error; err != nil {
			t.Fatalf("seed draft definition: %v", err)
		}

		// The draft declares "drive" with a required parameter. If the gate read the
		// draft, this payload-less call would be REJECTED. Reading the published
		// snapshot (empty), it stays free-form.
		v, err := api.ValidateCommandEnqueue(ctx, "dev-draft", "drive", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !v.Allowed {
			t.Fatalf("an unpublished draft must not constrain enqueue, got: %q", v.Reason)
		}
	})

	// Command-key matching is EXACT, never case-insensitive. A mis-cased key is a
	// mis-keyed actuation, which is precisely what the strict-validation rationale
	// exists to stop — and a case-folding match would silently deliver "REBOOT" as
	// "reboot". (This case was found by an adversarial mutation surviving the suite.)
	t.Run("command key matching is case-sensitive", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-case", []*CommandDefinition{
			defWithSchema(t, "reboot", nil),
		})
		v, err := api.ValidateCommandEnqueue(ctx, "dev-case", "Reboot", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Allowed {
			t.Fatal("a mis-cased command key must not match a declared command")
		}
	})

	// ADR-043 decision 3 names soft-deleted devices explicitly. Nothing else pins
	// that this gate keeps gorm's default soft-delete scope, so a stray Unscoped()
	// upstream would silently resurrect deleted devices as valid command targets.
	t.Run("a soft-deleted device is rejected", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-gone", nil)
		if err := api.RDB.DB(ctx).Where("token = ?", "dev-gone").Delete(&Device{}).Error; err != nil {
			t.Fatalf("soft-delete device: %v", err)
		}
		v, err := api.ValidateCommandEnqueue(ctx, "dev-gone", "reboot", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Allowed {
			t.Fatal("a soft-deleted device must not accept commands")
		}
	})

	// Tenant isolation: the gate must never resolve a device from another tenant,
	// or one tenant could probe another's fleet through enqueue verdicts.
	t.Run("a device in another tenant is not visible", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-tenant-a", nil)
		other := core.WithTenant(context.Background(), "B")
		v, err := api.ValidateCommandEnqueue(other, "dev-tenant-a", "reboot", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Allowed {
			t.Fatal("a device from another tenant must not be resolvable")
		}
	})
}
