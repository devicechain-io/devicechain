// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// seedTierRow is a minimal iam_tenant_tiers row for the migration test — the same
// columns NewTierShedPrioritySeed reads/writes. Declared local to the test for the
// snapshot rule; it must map to the same table the migration pins.
type seedTierRow struct {
	ID     uint           `gorm:"primarykey"`
	Token  string         `gorm:"uniqueIndex;not null;size:128"`
	Config map[string]any `gorm:"serializer:json"`
}

func (seedTierRow) TableName() string { return "iam_tenant_tiers" }

func newSeedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&seedTierRow{}))
	return db
}

func tierConfig(t *testing.T, db *gorm.DB, token string) map[string]any {
	t.Helper()
	var row seedTierRow
	require.NoError(t, db.Where("token = ?", token).First(&row).Error)
	return row.Config
}

// TestTierShedPrioritySeedLandsTheDefaults is the "verify the thing that matters" for
// the gold promise: the seed must actually WRITE shedPriority onto the tiers (running
// without error is not enough — a mis-serialized merge would leave gold un-shed-priced
// and silently un-protected). It also pins that an existing ceiling config survives
// the merge.
func TestTierShedPrioritySeedLandsTheDefaults(t *testing.T) {
	db := newSeedTestDB(t)
	// gold carries a ceiling already — the merge must not drop it.
	require.NoError(t, db.Create(&seedTierRow{Token: "gold",
		Config: map[string]any{"ingestMessagesPerSecond": float64(2000)}}).Error)
	require.NoError(t, db.Create(&seedTierRow{Token: "silver"}).Error) // nil config
	require.NoError(t, db.Create(&seedTierRow{Token: "bronze",
		Config: map[string]any{"ingestMessagesPerSecond": float64(250)}}).Error)

	require.NoError(t, NewTierShedPrioritySeed().Migrate(db))

	gold := tierConfig(t, db, "gold")
	require.EqualValues(t, 90, gold["shedPriority"], "gold must be seeded to the never-shed band")
	require.EqualValues(t, 2000, gold["ingestMessagesPerSecond"], "the existing ceiling must survive the merge")
	require.EqualValues(t, 60, tierConfig(t, db, "silver")["shedPriority"])
	require.EqualValues(t, 30, tierConfig(t, db, "bronze")["shedPriority"])
}

// TestTierShedPrioritySeedIsIdempotent pins that a replay (the UseTransaction:false
// re-run doctrine) is a no-op — it does not change a value that is already present.
func TestTierShedPrioritySeedIsIdempotent(t *testing.T) {
	db := newSeedTestDB(t)
	require.NoError(t, db.Create(&seedTierRow{Token: "gold"}).Error)

	require.NoError(t, NewTierShedPrioritySeed().Migrate(db))
	require.NoError(t, NewTierShedPrioritySeed().Migrate(db)) // replay
	require.EqualValues(t, 90, tierConfig(t, db, "gold")["shedPriority"])
}

// TestTierShedPrioritySeedDoesNotStompOperatorEdits pins that a tier whose
// shedPriority an operator has already set keeps THEIR value — packaging is the
// operator's to define (the same reason seedTenantTiers seeds-if-absent).
func TestTierShedPrioritySeedDoesNotStompOperatorEdits(t *testing.T) {
	db := newSeedTestDB(t)
	require.NoError(t, db.Create(&seedTierRow{Token: "gold",
		Config: map[string]any{"shedPriority": float64(85)}}).Error) // operator-tuned

	require.NoError(t, NewTierShedPrioritySeed().Migrate(db))
	require.EqualValues(t, 85, tierConfig(t, db, "gold")["shedPriority"], "operator's value must survive")
}

// TestTierShedPrioritySeedSkipsAbsentTier pins that a seeded tier an operator has
// deleted is NOT recreated by the seed (it only merges into rows that exist).
func TestTierShedPrioritySeedSkipsAbsentTier(t *testing.T) {
	db := newSeedTestDB(t)
	require.NoError(t, db.Create(&seedTierRow{Token: "gold"}).Error) // only gold exists

	require.NoError(t, NewTierShedPrioritySeed().Migrate(db))

	var count int64
	require.NoError(t, db.Model(&seedTierRow{}).Where("token = ?", "bronze").Count(&count).Error)
	require.Zero(t, count, "an absent seeded tier must not be recreated")
}

// TestTierShedPrioritySeedRollback pins the rollback removes only the value it seeded,
// and leaves an operator-tuned value in place.
func TestTierShedPrioritySeedRollback(t *testing.T) {
	db := newSeedTestDB(t)
	require.NoError(t, db.Create(&seedTierRow{Token: "gold"}).Error)
	require.NoError(t, db.Create(&seedTierRow{Token: "silver",
		Config: map[string]any{"shedPriority": float64(55)}}).Error) // operator-tuned

	m := NewTierShedPrioritySeed()
	require.NoError(t, m.Migrate(db))
	require.NoError(t, m.Rollback(db))

	_, seeded := tierConfig(t, db, "gold")["shedPriority"]
	require.False(t, seeded, "the seeded value must be removed on rollback")
	require.EqualValues(t, 55, tierConfig(t, db, "silver")["shedPriority"], "an operator's value must survive rollback")
}
