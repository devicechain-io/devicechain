// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴 THIS FILE IS THE ONLY THING THAT COVERS THE BASELINE'S SEEDED DATA, AND THAT IS WHY IT
// EXISTS RATHER THAN BEING FOLDED AWAY WITH THE MIGRATION IT REPLACES.
//
// hack/migration-diff.sh — the harness the whole GA squash rests on — compares
// `pg_dump --schema-only` output, which captures no rows. So a baseline that creates every table
// perfectly and seeds nothing passes with ten green areas. That is not a guess: stubbing out
// seedTenantTiers during this work left verify at exit 0 with every area ok.
//
// The consequences of losing this seed are not cosmetic. A tenant's tier FK is REQUIRED, so an
// instance with no tier rows is one where no tenant can be created at all; and the shed priority
// the tiers carry cannot be recovered from the token, because ADR-065 forbids the behaviour keying
// on a token still meaning what it meant at seed. Both failures surface far from the cause.
//
// It replaces shed_priority_migration_test.go, whose subject (the seed migration appended after
// the first flatten) folded into the baseline at the squash. Running without error was explicitly
// not enough for that test either — it asserted the values were WRITTEN — and the same standard
// applies here.

// seedTierRow reads iam_tenant_tiers back independently of the snapshot types the migration writes
// through, so a test cannot pass by sharing a mistake with the code under test.
type seedTierRow struct {
	ID     uint           `gorm:"primarykey"`
	Token  string         `gorm:"uniqueIndex;not null;size:128"`
	Config map[string]any `gorm:"serializer:json"`
}

func (seedTierRow) TableName() string { return "iam_tenant_tiers" }

func newBaselineDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, NewBaselineSchema().Migrate(db))
	return db
}

// TestBaselineSeedsTierVocabulary is the "a tenant can be created at all" assertion.
func TestBaselineSeedsTierVocabulary(t *testing.T) {
	db := newBaselineDB(t)

	var rows []seedTierRow
	require.NoError(t, db.Find(&rows).Error)

	// A count cross-check, not just "found some": a seed that wrote one tier of three would
	// otherwise satisfy every per-tier assertion below that happened to name that tier.
	require.Len(t, rows, 3, "the baseline must seed exactly the gold/silver/bronze vocabulary")

	byToken := map[string]seedTierRow{}
	for _, r := range rows {
		byToken[r.Token] = r
	}
	for _, token := range []string{"gold", "silver", "bronze"} {
		require.Contains(t, byToken, token)
	}
}

// TestBaselineSeedsShedPriority pins the ADR-063 bands folded in from the seed migration. The
// VALUES matter, not merely their presence: the promise a fresh install sells is that gold is the
// last to shed and bronze the first, which is a claim about the ORDER of these three numbers.
func TestBaselineSeedsShedPriority(t *testing.T) {
	db := newBaselineDB(t)

	priority := func(token string) float64 {
		t.Helper()
		var row seedTierRow
		require.NoError(t, db.Where("token = ?", token).First(&row).Error)
		require.NotNil(t, row.Config, "tier %q carries no config at all", token)
		v, ok := row.Config["shedPriority"]
		require.True(t, ok, "tier %q carries no shedPriority — the shed behaviour cannot recover it "+
			"from the token (ADR-065), so an absent value is an unpriced tier", token)
		f, ok := v.(float64)
		require.True(t, ok, "tier %q shedPriority is %T, not a number", token, v)
		return f
	}

	gold, silver, bronze := priority("gold"), priority("silver"), priority("bronze")
	require.Equal(t, float64(90), gold)
	require.Equal(t, float64(60), silver)
	require.Equal(t, float64(30), bronze)

	// The ordering is the actual product promise; the literals above are one way to satisfy it.
	// Asserting both means a future retune that inverts the bands fails here rather than in a
	// customer's overload.
	require.Greater(t, gold, silver, "gold must shed after silver")
	require.Greater(t, silver, bronze, "silver must shed after bronze")
}

// TestBaselineSeedPreservesTierCeilings guards the fold itself. The shed priority was merged INTO
// config blobs that already carried ADR-023 rate ceilings, and a fold that replaced the blob rather
// than adding a key would silently un-price gold and bronze — leaving them on the platform
// defaults while still described as double and a quarter of them.
func TestBaselineSeedPreservesTierCeilings(t *testing.T) {
	db := newBaselineDB(t)

	var gold, silver, bronze seedTierRow
	require.NoError(t, db.Where("token = ?", "gold").First(&gold).Error)
	require.NoError(t, db.Where("token = ?", "silver").First(&silver).Error)
	require.NoError(t, db.Where("token = ?", "bronze").First(&bronze).Error)

	require.Equal(t, float64(2000), gold.Config["ingestMessagesPerSecond"])
	require.Equal(t, float64(4000), gold.Config["ingestBurst"])
	require.Equal(t, float64(200), gold.Config["outboundMessagesPerSecond"])
	require.Equal(t, float64(400), gold.Config["outboundBurst"])

	require.Equal(t, float64(250), bronze.Config["ingestMessagesPerSecond"])
	require.Equal(t, float64(500), bronze.Config["ingestBurst"])
	require.Equal(t, float64(25), bronze.Config["outboundMessagesPerSecond"])
	require.Equal(t, float64(50), bronze.Config["outboundBurst"])

	// Silver states no rate ceilings ON PURPOSE, so it tracks the platform default and moves with
	// it when an operator raises one. Asserting the absence keeps a well-meaning "fill in the
	// blanks" change from quietly pinning every standard tenant to a number frozen in a migration.
	for _, key := range []string{
		"ingestMessagesPerSecond", "ingestBurst",
		"outboundMessagesPerSecond", "outboundBurst",
	} {
		require.NotContains(t, silver.Config, key,
			"silver must declare no rate ceiling — it is the platform default by design")
	}
}

// TestBaselineSeedIsIdempotent covers the re-runnable requirement every migration here carries:
// migrations run with UseTransaction:false, so a half-applied run replays from the top.
func TestBaselineSeedIsIdempotent(t *testing.T) {
	db := newBaselineDB(t)
	require.NoError(t, NewBaselineSchema().Migrate(db), "a replay must not fail")

	var n int64
	require.NoError(t, db.Model(&seedTierRow{}).Count(&n).Error)
	require.EqualValues(t, 3, n, "a replay must not duplicate the seeded tiers")
}

// TestBaselineSeedDoesNotClobberOperatorEdits pins the seed-if-absent contract. Packaging is the
// operator's to define — what "bronze includes" is a product decision they change live — so a
// replay must not re-assert the shipped values over an edit. This is the property that separates
// this seed from EnsureRole's deliberate clobber, where the code IS the source of truth.
func TestBaselineSeedDoesNotClobberOperatorEdits(t *testing.T) {
	db := newBaselineDB(t)

	var bronze seedTierRow
	require.NoError(t, db.Where("token = ?", "bronze").First(&bronze).Error)
	bronze.Config["shedPriority"] = float64(45)
	bronze.Config["ingestMessagesPerSecond"] = float64(999)
	require.NoError(t, db.Select("Config").Updates(&bronze).Error)

	require.NoError(t, NewBaselineSchema().Migrate(db))

	var after seedTierRow
	require.NoError(t, db.Where("token = ?", "bronze").First(&after).Error)
	require.Equal(t, float64(45), after.Config["shedPriority"], "a replay must not revert an operator's retune")
	require.Equal(t, float64(999), after.Config["ingestMessagesPerSecond"])
}
