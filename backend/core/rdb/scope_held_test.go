// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// heldDevice stamps a device's anchors once (an AnchorColumn), as device-state's live view
// of a device does.
type heldDevice struct {
	gorm.Model
	TenantScoped
	DeviceToken  string
	ScopeAnchors Anchors
}

func (heldDevice) ScopedBy() ScopeRule { return AnchorColumn("scope_anchors") }

// heldReading is a row of another table whose visibility is its device's: HeldAnchors.
type heldReading struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	DeviceToken string
	Name        string
}

func (heldReading) ScopedBy() ScopeRule {
	return HeldAnchors(AnchorHolder{Model: &heldDevice{}, Keys: []KeyPair{{Set: "device_token", Row: "device_token"}},
		Column: "scope_anchors"})
}

// misheldReading names a holder column that does not exist.
type misheldReading struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	DeviceToken string
}

func (misheldReading) ScopedBy() ScopeRule {
	return HeldAnchors(AnchorHolder{Model: &heldDevice{}, Keys: []KeyPair{{Set: "device_token", Row: "device_token"}},
		Column: "no_such_column"})
}

// unheldReading names a holder that is not tenant-scoped (gadget), which would let another
// tenant's holder row make this tenant's row visible.
type unheldReading struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	DeviceToken string
}

func (unheldReading) ScopedBy() ScopeRule {
	return HeldAnchors(AnchorHolder{Model: &gadget{}, Keys: []KeyPair{{Set: "name", Row: "device_token"}},
		Column: "name"})
}

// 🔑 THE HELD-ANCHORS PREDICATE: the holder's anchors, overlapped with the scope's, through
// a subquery that joins the holder on the declared key, carries the TENANT ON THE HOLDER,
// and skips a soft-deleted holder — beside the row's own tenant predicate, never instead of
// it. Each anchor is a bound parameter.
func TestAHeldAnchorsReadReadsTheHoldersStamp(t *testing.T) {
	db := newTestDB(t)
	stmt := dry(db, scopedCtx("A", "customer:acme-corp", "group:north")).Find(&[]heldReading{}).Statement
	if stmt.Error != nil {
		t.Fatal(stmt.Error)
	}
	sql := stmt.SQL.String()
	for _, want := range []string{
		"`held_readings`.`tenant_id` = ?",
		"EXISTS (SELECT 1 FROM `held_devices` AS `dc_scope_holder` WHERE `dc_scope_holder`.`tenant_id` = ?",
		"AND `dc_scope_holder`.`device_token` = `held_readings`.`device_token`",
		"AND `dc_scope_holder`.`deleted_at` IS NULL",
		"AND `dc_scope_holder`.`scope_anchors` && ARRAY[?, ?]::text[])",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in: %s", want, sql)
		}
	}
	if !reflect.DeepEqual(stmt.Vars, []any{"A", "A", "customer:acme-corp", "group:north"}) {
		t.Errorf("want the tenant bound on the row AND on the holder, then the anchors: %v", stmt.Vars)
	}

	// 🔴 An anchorless scope reads nothing, never everything.
	stmt = dry(db, scopedCtx("A")).Find(&[]heldReading{}).Statement
	if stmt.Error != nil || !strings.Contains(stmt.SQL.String(), "AND 1 = 0)") || strings.Contains(stmt.SQL.String(), "&&") {
		t.Errorf("an anchorless scope: %v %s", stmt.Error, stmt.SQL.String())
	}
	// Negative control: an unscoped read carries no scope predicate at all.
	stmt = dry(db, unscopedCtx("A")).Find(&[]heldReading{}).Statement
	if strings.Contains(stmt.SQL.String(), "dc_scope_holder") || strings.Contains(stmt.SQL.String(), "1 = 0") {
		t.Errorf("an unscoped read carries a scope predicate: %s", stmt.SQL.String())
	}
	// Count goes through the same callback.
	stmt = dry(db, scopedCtx("A", "customer:acme-corp")).Model(&heldReading{}).Count(new(int64)).Statement
	if !strings.Contains(stmt.SQL.String(), "dc_scope_holder") {
		t.Errorf("a scoped count carries no scope predicate: %s", stmt.SQL.String())
	}
}

// Scoped writes to a HeldAnchors model are refused: nothing a scoped member does writes a
// row whose visibility another row decides.
func TestAHeldAnchorsModelRefusesScopedWrites(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&heldReading{}, &heldDevice{}); err != nil {
		t.Fatal(err)
	}
	ctx := scopedCtx("A", "customer:acme-corp")
	if err := db.WithContext(ctx).Model(&heldReading{}).Where("1 = 1").Update("name", "x").Error; !isScopeRefusal(err) {
		t.Errorf("scoped update: %v", err)
	}
	if err := db.WithContext(ctx).Create(&heldReading{Name: "x"}).Error; !isScopeRefusal(err) {
		t.Errorf("scoped create: %v", err)
	}
	if err := db.WithContext(ctx).Where("1 = 1").Delete(&heldReading{}).Error; !isScopeRefusal(err) {
		t.Errorf("scoped delete: %v", err)
	}
}

// A rule naming a missing holder column, or a holder that is not tenant-scoped, is refused
// rather than read without the clause it cannot build.
func TestAMisdeclaredHeldAnchorsRuleRefuses(t *testing.T) {
	db := newTestDB(t)
	if err := dry(db, scopedCtx("A", "customer:acme-corp")).Find(&[]misheldReading{}).Error; !isScopeRefusal(err) {
		t.Errorf("a holder column that does not exist: %v", err)
	}
	if err := dry(db, scopedCtx("A", "customer:acme-corp")).Find(&[]unheldReading{}).Error; !isScopeRefusal(err) {
		t.Errorf("a holder that is not tenant-scoped: %v", err)
	}
	empty := HeldAnchors(AnchorHolder{Model: &heldDevice{}, Column: "scope_anchors"})
	if _, err := heldAnchorsExpr(dry(db, scopedCtx("A")).Model(&heldReading{}), empty.rule().holder, "A", nil); err == nil {
		t.Errorf("a rule with no join key was accepted")
	}
}
