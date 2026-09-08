// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 🔴 THIS FILE EXISTS BECAUSE A DUPLICATE MIGRATION ID REACHED THE MIGRATION-DIFF
// HARNESS WITH EVERY GO TEST GREEN.
//
// A migration appended with an ID another one already uses makes gormigrate refuse the
// WHOLE chain — "Duplicated migration ID" — at service startup. Every existing instance
// crash-loops on upgrade and every fresh install fails to come up. It is about as loud
// a failure as exists, and it is caught about as late as possible: nothing short of
// standing up a real Postgres saw it.
//
// The reason the unit tests could not is structural rather than an oversight.
// newMigratedDB walks `Migrations` calling `m.Migrate(db)` directly — it never
// constructs a gormigrate instance, so the ID is never read, let alone checked. That is
// the right shape for what those tests are for (they are about what the DDL BUILDS),
// but it means the whole ID dimension is invisible to them.
//
// So the IDs get their own assertions here. They are pure data checks, they need no
// database, and they run in the same `go test ./...` a maintainer runs before pushing.
//
// 🔴 The same gap exists in every OTHER area's schema package — each has its own
// `Migrations` slice and its own newMigratedDB-shaped helper. Closing it there wants a
// shared helper rather than nine copies of this file, which is a separate change.

// TestEveryMigrationIDIsUnique is the one that would have caught it. gormigrate keys
// its bookkeeping table by ID, so two migrations claiming one ID is not a naming
// nuisance — it is a chain that cannot run at all.
func TestEveryMigrationIDIsUnique(t *testing.T) {
	seen := map[string]int{}
	for i, m := range Migrations {
		if first, dup := seen[m.ID]; dup {
			t.Errorf("migrations[%d] reuses the ID %q already taken by migrations[%d] — "+
				"gormigrate refuses the whole chain, so no instance would start", i, m.ID, first)
			continue
		}
		seen[m.ID] = i
	}
	require.Len(t, seen, len(Migrations))
}

// The IDs are timestamps, and the chain runs in SLICE order rather than ID order — so
// an ID that goes backwards is not itself a failure. It is a reliable sign of one:
// every way an ID lands out of order (a copy-paste that kept the source's timestamp, a
// migration inserted mid-slice rather than appended, a rebase that reordered two) is
// also a way a chain silently stops matching the order it is read in.
//
// Asserted as STRICTLY increasing, which duplicate-detection above already implies and
// which is stated separately anyway: the two would be checked in different places if
// the ID scheme ever changed, and a reader should not have to derive one from the other.
func TestMigrationIDsAreStrictlyIncreasing(t *testing.T) {
	for i := 1; i < len(Migrations); i++ {
		require.Lessf(t, Migrations[i-1].ID, Migrations[i].ID,
			"migrations[%d] (%s) does not follow migrations[%d] (%s); migrations run in SLICE order, "+
				"so an ID that goes backwards means the slice and the timestamps disagree about the chain",
			i, Migrations[i].ID, i-1, Migrations[i-1].ID)
	}
}

// The control. Both tests above are loops, and a loop over an empty slice passes
// vacuously — so a `Migrations` that had been emptied, or a test file that had drifted
// away from the variable it means to check, would report green.
func TestTheChainIsNotEmpty(t *testing.T) {
	require.NotEmpty(t, Migrations)
	require.Equal(t, NewBaselineSchema().ID, Migrations[0].ID,
		"the baseline must stay first; everything else is appended below it")
}
