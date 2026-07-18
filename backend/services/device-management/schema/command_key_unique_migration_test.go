// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// cmdDefRow mirrors the columns the migration touches, so the test can seed rows
// directly without dragging in the live model.
type cmdDefRow struct {
	ID              uint `gorm:"primarykey"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       gorm.DeletedAt `gorm:"index"`
	TenantId        string
	Token           string
	CommandKey      string
	DeviceProfileId uint
}

func (cmdDefRow) TableName() string { return "command_definitions" }

func newCommandKeyDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&cmdDefRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// seed inserts a row bypassing any application-level guard — the point is to
// reproduce the state a database written BEFORE the uniqueness check existed can
// legitimately be in.
func seed(t *testing.T, db *gorm.DB, tenant string, profile uint, key, token string) uint {
	t.Helper()
	row := &cmdDefRow{TenantId: tenant, DeviceProfileId: profile, CommandKey: key, Token: token}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed %s/%d/%s: %v", tenant, profile, key, err)
	}
	return row.ID
}

// The migration has to survive a database that ALREADY holds duplicates. The
// migration-diff harness cannot prove this: it runs the chain against an empty
// database, so the de-duplication is a no-op there and only the CREATE INDEX is
// exercised. On real data a leftover duplicate would abort the whole migration and
// block the upgrade, so it is tested directly here.
func TestCommandKeyUniqueMigration_DeduplicatesExistingRows(t *testing.T) {
	db := newCommandKeyDB(t)

	// Two duplicates of the same (tenant, profile, key), plus rows that merely LOOK
	// similar and must survive untouched: same key on another profile, same key in
	// another tenant, and a different key on the same profile.
	keep := seed(t, db, "A", 1, "drive", "cd-1")
	shadowed := seed(t, db, "A", 1, "drive", "cd-2")
	otherProfile := seed(t, db, "A", 2, "drive", "cd-3")
	otherTenant := seed(t, db, "B", 1, "drive", "cd-4")
	otherKey := seed(t, db, "A", 1, "reboot", "cd-5")

	if err := NewCommandKeyUniqueSchema().Migrate(db); err != nil {
		t.Fatalf("migration failed against a database holding duplicates: %v", err)
	}

	live := func(id uint) bool {
		var n int64
		db.Model(&cmdDefRow{}).Where("id = ?", id).Count(&n)
		return n == 1 // gorm's default scope excludes soft-deleted rows
	}

	// The LOWEST id survives: that is the row the enqueue gate was already resolving
	// (it takes the first match in id order), so de-duplication retires the shadowed
	// copies rather than changing which definition is in force.
	if !live(keep) {
		t.Error("the oldest duplicate must survive — it is the definition already in force")
	}
	if live(shadowed) {
		t.Error("the shadowed duplicate must be soft-deleted")
	}
	for name, id := range map[string]uint{
		"same key on another profile": otherProfile,
		"same key in another tenant":  otherTenant,
		"another key on same profile": otherKey,
	} {
		if !live(id) {
			t.Errorf("de-duplication was too broad: %s must survive", name)
		}
	}
}

// The invariant the index exists to make unrepresentable, plus the tombstone
// behaviour that a plain (non-partial) unique index would get wrong.
func TestCommandKeyUniqueMigration_IndexSemantics(t *testing.T) {
	db := newCommandKeyDB(t)
	if err := NewCommandKeyUniqueSchema().Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	seed(t, db, "A", 1, "drive", "cd-1")

	t.Run("a duplicate key on the same profile is refused", func(t *testing.T) {
		err := db.Create(&cmdDefRow{TenantId: "A", DeviceProfileId: 1, CommandKey: "drive", Token: "cd-dup"}).Error
		if err == nil {
			t.Fatal("the storage layer accepted a second definition of the same command key")
		}
	})

	t.Run("the same key on a different profile is allowed", func(t *testing.T) {
		if err := db.Create(&cmdDefRow{TenantId: "A", DeviceProfileId: 2, CommandKey: "drive", Token: "cd-p2"}).Error; err != nil {
			t.Fatalf("profiles must not share a uniqueness slot: %v", err)
		}
	})

	t.Run("the same key in a different tenant is allowed", func(t *testing.T) {
		if err := db.Create(&cmdDefRow{TenantId: "B", DeviceProfileId: 1, CommandKey: "drive", Token: "cd-t2"}).Error; err != nil {
			t.Fatalf("tenants must never collide: %v", err)
		}
	})

	// The whole reason the index is PARTIAL. A plain unique index counts tombstones,
	// so a deleted definition would hold its key forever and recreating it would fail
	// — the bug already parked on DeviceClaim, ProvisioningProfile and MetricDefinition.
	t.Run("soft-deleting frees the key for reuse", func(t *testing.T) {
		if err := db.Where("tenant_id = ? AND device_profile_id = ? AND command_key = ?", "A", 1, "drive").
			Delete(&cmdDefRow{}).Error; err != nil {
			t.Fatalf("soft-delete: %v", err)
		}
		if err := db.Create(&cmdDefRow{TenantId: "A", DeviceProfileId: 1, CommandKey: "drive", Token: "cd-reborn"}).Error; err != nil {
			t.Fatalf("a soft-deleted definition must free its key: %v", err)
		}
	})
}
