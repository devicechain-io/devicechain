// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// PlantFence writes this database's erasure fence for a tenant — one row in every
// functional-area schema the plan covers — and returns the schemas it fenced.
//
// 🔑 IT IS A SEPARATE, EARLIER TRANSACTION THAN THE SWEEP, AND THAT ORDER IS THE WHOLE
// MECHANISM. A fence planted inside the sweep's transaction becomes visible to other
// sessions only when that transaction commits, which is the same instant the deletion
// finishes — so it would stop nothing the sweep had not already deleted. Planting first
// and committing means every writer that starts after this point is refused, and the
// sweep that follows is chasing a set that has stopped growing. This is
// DeviceAttributeStore's plant → act → re-verify discipline, at tenant scope: Residue is
// the re-verify.
//
// The fence is idempotent, and 🔴 A PASS THAT FINDS ITS OWN ROW RE-OPENS IT rather than
// leaving it alone. That is not belt and braces. Lifting stamps completed_at immediately
// before the token is released, so anything that fails between the two — a crash, a
// completion that errors, one store lifting while another does not — leaves a fence DOWN
// on a purge that is still running. A plant that did nothing would then leave that area
// unfenced for every remaining pass, which is precisely the window the settle clock exists
// to watch. A pass only plants while the tenant row still says purging, and completion is
// the act that removes that row, so there is no pass in which re-opening is wrong.
//
// Passes repeat because a writer already inside its transaction when the fence landed
// still commits, so a first sweep can leave rows a later one collects — which is why
// completion requires the settle window rather than a single clean scan.
//
// 🔴 IT TAKES THE SAME PRECONDITION THE SWEEP DOES, AND FOR A SHARPER REASON. A sweep
// misdirected at a live tenant deletes rows, which is catastrophic and obvious. A fence
// misdirected at a live tenant deletes nothing and BRICKS THE TENANT: every write it
// makes from that moment is refused, with no row missing to make anyone look here. The
// advisory lock guarding a pass is a session lease, not a fence, so the misdirection is
// reachable — the precondition runs inside this transaction, which is the only place it
// cannot be raced.
func PlantFence(ctx context.Context, db *gorm.DB, plan *Plan, tenant string, epoch time.Time,
	now time.Time, pre Precondition) ([]string, error) {
	if strings.TrimSpace(tenant) == "" {
		return nil, fmt.Errorf("refusing to plant an erasure fence for an empty tenant token")
	}
	schemas, err := fenceSchemas(plan)
	if err != nil {
		return nil, err
	}
	planted := epoch.UTC().Truncate(time.Microsecond)
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if pre != nil {
			if perr := pre(tx); perr != nil {
				return perr
			}
		}
		for _, schema := range schemas {
			stmt := fmt.Sprintf(
				`INSERT INTO %q.%s (token, epoch, planted_at, completed_at) VALUES (?, ?, ?, NULL) `+
					`ON CONFLICT (token, epoch) DO UPDATE SET completed_at = NULL`, schema, rdb.FenceTable)
			if e := tx.Exec(stmt, tenant, planted, now.UTC()).Error; e != nil {
				return fmt.Errorf("planting the erasure fence in %q: %w", schema, e)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return schemas, nil
}

// LiftFence stamps every standing fence row for a tenant as completed, in every
// functional-area schema the plan covers.
//
// 🔴 THIS IS THE ACT THAT LETS A SUCCESSOR TENANT WRITE, so it is safe only because of
// what has to be true before it is called: the purge has been clean for the settle
// window AND older than the token hold, which defaults to the full lifetime of a broker
// JWT. No session minted before the deletion can still authenticate by then, so there is
// no pre-deletion writer left for the fence to stop. Shortening the token hold without
// re-deriving this is how a released token starts inheriting writes again.
//
// It is called BEFORE the token is released, not after, and the ordering is chosen for
// how it fails. Crashing between the two leaves the fence down while the token is still
// held, which is the state above — safe. The other order would leave the token released
// while the fence still stood, and every write by the successor at that token would be
// refused, permanently, by a row nothing would ever come back to clear.
//
// It matches on the token alone rather than on (token, epoch). At most one purge of a
// token can be in flight — the tenant row is both the work list and the token
// reservation — so "the standing fence for this token" is unambiguous, and matching a
// timestamp that has been through Postgres's microsecond truncation is a way to match
// nothing at all.
//
// It carries the same precondition the plant and the sweep do, for the scenario the
// coordinator documents against itself: the advisory lock guarding a pass is a session
// lease, so a pass that lost it can still be running while a peer completes the purge,
// an operator recreates the tenant at the reclaimed token and deletes it again. Lifting
// on the token alone would then take down the SUCCESSOR's fresh fence. The plant on the
// next pass repairs it — that is what makes this low rather than severe — but a guard
// that runs inside the transaction does not need the repair.
func LiftFence(ctx context.Context, db *gorm.DB, plan *Plan, tenant string, at time.Time,
	pre Precondition) error {
	if strings.TrimSpace(tenant) == "" {
		return fmt.Errorf("refusing to lift an erasure fence for an empty tenant token")
	}
	schemas, err := fenceSchemas(plan)
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if pre != nil {
			if perr := pre(tx); perr != nil {
				return perr
			}
		}
		for _, schema := range schemas {
			stmt := fmt.Sprintf(
				`UPDATE %q.%s SET completed_at = ? WHERE token = ? AND completed_at IS NULL`,
				schema, rdb.FenceTable)
			if e := tx.Exec(stmt, at.UTC(), tenant).Error; e != nil {
				return fmt.Errorf("lifting the erasure fence in %q: %w", schema, e)
			}
		}
		return nil
	})
}

// fenceSchemas returns the functional-area schemas in a plan, each of which must carry a
// fence table.
//
// 🔑 THE AREA SET IS DERIVED FROM THE PLAN, NOT LISTED. An area is a schema holding its
// own gormigrate bookkeeping table — the same derivation migrationTableExemption uses to
// recognise that table — because that is the one object every area has and nothing else
// does. A written list would be a fourth place the area set lives, and it would be the
// copy nobody updates when an area is added: the new area would simply never be fenced,
// with every gate green.
//
// 🔴 A FUNCTIONAL AREA WITH NO FENCE TABLE IS AN ERROR, NOT A SKIP. rdb creates the table
// in every schema it manages, so its absence means either an area running an older core
// or a schema this code has misidentified. Both are answers the purge must not guess at:
// skipping would ack "swept and fenced" for a schema with no fence, which is the exact
// silence ADR-077 decision 6 exists to make impossible.
func fenceSchemas(plan *Plan) ([]string, error) {
	if plan == nil {
		return nil, fmt.Errorf("refusing to touch an erasure fence with no classification plan")
	}
	areas := []string{}
	hasFence := map[string]bool{}
	for _, e := range plan.Entries {
		if e.Table.Name == strings.ReplaceAll(e.Table.Schema, "-", "_")+"_migrations" {
			areas = append(areas, e.Table.Schema)
		}
		if e.Table.Name == rdb.FenceTable {
			hasFence[e.Table.Schema] = true
		}
	}
	if len(areas) == 0 {
		return nil, fmt.Errorf("refusing to touch an erasure fence: the plan names no functional " +
			"area, so nothing would be fenced and the purge would still claim it had been")
	}
	missing := []string{}
	for _, a := range areas {
		if !hasFence[a] {
			missing = append(missing, a)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("refusing to touch an erasure fence: schema(s) %s carry no %s table, "+
			"so writes for a purged tenant would keep being accepted there while the purge reported "+
			"the area swept and fenced", strings.Join(missing, ", "), rdb.FenceTable)
	}
	return areas, nil
}
