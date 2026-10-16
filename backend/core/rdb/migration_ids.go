// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"errors"
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
)

// gormigrateReservedID is the ID gormigrate keeps for its own InitSchema bookkeeping row.
// It is unexported upstream, so it is restated here; a migration claiming it is refused
// by gormigrate with a ReservedIDError.
const gormigrateReservedID = "SCHEMA_INIT"

// ErrEmptyMigrationChain is returned for a chain with no migrations at all. gormigrate
// refuses one too (ErrNoMigrationDefined), and every area's chain starts with a baseline,
// so an empty chain is never intended.
var ErrEmptyMigrationChain = errors.New("migration chain is empty")

// CheckMigrationIDs refuses a migration chain gormigrate would refuse at startup over its
// IDs: an empty chain, a nil entry, an empty ID, the reserved "SCHEMA_INIT" ID, or two
// migrations sharing one ID.
//
// Why this exists: a duplicate ID makes gormigrate refuse the WHOLE chain, so every
// existing instance crash-loops on upgrade and every fresh install fails to come up. The
// unit tests in each area cannot see it, because their fixtures call each migration's
// Migrate func directly and never construct gormigrate, so the ID is never read. Only the
// migration-diff harness, against a real Postgres, used to catch it. Each area now runs
// this from a plain `go test`, and the runtime runs it before gormigrate.New so the
// startup error names the same check.
//
// Every problem in the chain is reported, not just the first, so a test failure lists them
// all in one run.
func CheckMigrationIDs(migrations []*gormigrate.Migration) error {
	if len(migrations) == 0 {
		return ErrEmptyMigrationChain
	}
	var errs []error
	seen := make(map[string]int, len(migrations))
	for i, m := range migrations {
		switch {
		case m == nil:
			errs = append(errs, fmt.Errorf("migrations[%d] is nil", i))
			continue
		case m.ID == "":
			errs = append(errs, fmt.Errorf("migrations[%d] has an empty ID", i))
			continue
		case m.ID == gormigrateReservedID:
			errs = append(errs, fmt.Errorf("migrations[%d] uses the ID %q, which gormigrate reserves", i, m.ID))
		}
		if first, dup := seen[m.ID]; dup {
			errs = append(errs, fmt.Errorf("migrations[%d] reuses the ID %q already taken by migrations[%d]; "+
				"gormigrate refuses the whole chain, so no instance would start", i, m.ID, first))
			continue
		}
		seen[m.ID] = i
	}
	return errors.Join(errs...)
}
