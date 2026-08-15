// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// TestExpiryPredicatesPartitionEveryRow is the test that keeps expiredWhere and
// liveWhere from drifting apart again.
//
// 🔴 THEY WERE ALREADY APART ONCE, BY ONE INSTANT, AND THAT IS THE WHOLE REASON THIS
// EXISTS. The expiry sweep asked `expires_at < now`; the LwM2M wake drain dropped a row
// on `expires_at <= now`. A command sitting exactly on its horizon therefore belonged to
// NEITHER set — the drain would not deliver it because it read as expired, and the sweep
// would not expire it because it read as live. It was undeliverable and unexpirable at
// the same time, for one instant, which is a bug nobody reproduces and nobody explains.
//
// 🔑 THE ASSERTION IS THE PARTITION, NOT EITHER PREDICATE'S TRUTH TABLE. Testing them
// one at a time is what let them diverge: each was individually defensible, and only the
// pair was wrong. So every case here runs BOTH predicates against the SAME row at the
// SAME instant and demands exactly one match — a test that fails on an overlap (a row in
// both sets, so the drain actuates a command the sweep is expiring) and on a gap (a row
// in neither, the original bug) alike.
//
// The two cases that matter most are the ones the naive predicates got wrong: the
// BOUNDARY, where expires_at == now, and NULL, where SQL's three-valued logic drops a row
// from both sides unless both predicates name it explicitly.
func TestExpiryPredicatesPartitionEveryRow(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	// Truncated to the second so the fixture's horizon and the instant it is compared
	// against are bit-identical after a round trip through the driver. The boundary case
	// is about the COMPARISON OPERATOR, and a sub-microsecond serialization difference
	// would decide it instead — passing or failing for a reason that has nothing to do
	// with the code under test.
	now := time.Now().UTC().Truncate(time.Second)

	cases := []struct {
		name        string
		expiresAt   sql.NullTime
		wantExpired bool
		why         string
	}{
		{
			name:        "an hour past its horizon",
			expiresAt:   sql.NullTime{Time: now.Add(-time.Hour), Valid: true},
			wantExpired: true,
			why:         "a command well past its TTL is the sweep's to expire and the drain must not deliver it",
		},
		{
			name:      "exactly on its horizon",
			expiresAt: sql.NullTime{Time: now, Valid: true},
			// Live, and this direction is the design: the boundary belongs to `live`, so
			// a command actuates one instant early rather than a horizon being enforced
			// one instant late. The sweep is the writer that makes expiry fact, and a
			// drain stricter than the sweep declines to deliver rows nothing will expire.
			wantExpired: false,
			why: "expires_at == now must be LIVE — this is the exact instant the two old " +
				"definitions disagreed on, and it is what put a row in neither set",
		},
		{
			name:        "one second short of its horizon",
			expiresAt:   sql.NullTime{Time: now.Add(time.Second), Valid: true},
			wantExpired: false,
			why:         "a command inside its TTL is deliverable",
		},
		{
			name:        "an hour short of its horizon",
			expiresAt:   sql.NullTime{Time: now.Add(time.Hour), Valid: true},
			wantExpired: false,
			why:         "a command inside its TTL is deliverable",
		},
		{
			name:      "no horizon at all",
			expiresAt: sql.NullTime{},
			// NULL is reachable in production, not merely a schema artifact: a zero
			// DefaultCommandTTL stamps nothing at create.
			wantExpired: false,
			why: "a NULL horizon means NO horizon — never expired, always live. Both predicates " +
				"must name NULL, because `expires_at < ?` against NULL is neither true nor false " +
				"and would drop the row from BOTH sets",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &Command{
				DeviceToken: "dev-1",
				Name:        "reboot",
				Status:      CommandHeld.String(),
				ExpiresAt:   tc.expiresAt,
			}
			cmd.Token = fmt.Sprintf("partition-%02d", i)
			if err := api.RDB.DB(ctx).Create(cmd).Error; err != nil {
				t.Fatalf("seeding the row failed: %v", err)
			}

			expiredSQL, expiredArgs := expiredWhere(now)
			liveSQL, liveArgs := liveWhere(now)

			// Both counts are taken against THIS row alone (by primary key), so the
			// answer is a property of the predicates and not of what other cases left
			// behind in the table.
			var expiredCount, liveCount int64
			if err := api.RDB.DB(ctx).Model(&Command{}).
				Where("id = ?", cmd.ID).Where(expiredSQL, expiredArgs...).
				Count(&expiredCount).Error; err != nil {
				t.Fatalf("counting under expiredWhere failed: %v", err)
			}
			if err := api.RDB.DB(ctx).Model(&Command{}).
				Where("id = ?", cmd.ID).Where(liveSQL, liveArgs...).
				Count(&liveCount).Error; err != nil {
				t.Fatalf("counting under liveWhere failed: %v", err)
			}

			if expiredCount+liveCount != 1 {
				t.Fatalf("expiredWhere matched %d and liveWhere matched %d — the two must be exact "+
					"complements, so EXACTLY ONE matches every row. %d/%d means %s: %s",
					expiredCount, liveCount, expiredCount, liveCount,
					map[bool]string{true: "the sets OVERLAP (the drain would deliver a command the sweep is expiring)",
						false: "there is a GAP (a row that is neither deliverable nor expirable — the original bug)"}[expiredCount+liveCount > 1],
					tc.why)
			}

			gotExpired := expiredCount == 1
			if gotExpired != tc.wantExpired {
				t.Fatalf("the row landed on the %s side, want %s: %s",
					sideName(gotExpired), sideName(tc.wantExpired), tc.why)
			}
		})
	}
}

// TestExpireStaleHonoursTheSharedHorizon pins the SWEEP to the shared predicate, which
// the partition test above deliberately cannot do.
//
// 🔴 THIS EXISTS BECAUSE A NEGATIVE CONTROL FOUND ITS ABSENCE. Reverting ExpireStale to a
// hand-written `expires_at <= ?` — the exact one-instant split the shared predicate was
// introduced to remove — left the whole package GREEN. The partition test proves the two
// predicates are complements; it says nothing about whether the sweep still ASKS them.
// Both halves are needed: one keeps the definitions together, this one keeps the caller
// attached to them.
//
// A command sitting exactly on its horizon must SURVIVE the sweep, because the boundary
// belongs to `live`. If the sweep expired it, the drain (which reads liveWhere) would
// consider it deliverable at the very instant the sweep marked it EXPIRED, and whichever
// ran first would decide — a race whose loser is a real actuation.
func TestExpireStaleHonoursTheSharedHorizon(t *testing.T) {
	api := newTestApi(t)
	// A system context, as ExpireStale's contract requires, so the sweep spans tenants.
	sysctx := core.WithSystemContext(core.WithTenant(context.Background(), "A"))

	// Local time and second precision, for the reason spelled out in the drain tests: the
	// sqlite harness stores a time.Time as offset-bearing TEXT and compares it lexically,
	// so the fixture and the instant handed to the sweep must agree on both.
	now := time.Now().Truncate(time.Second)
	seedDrainable(t, api, sysctx, 10, "dev-1", CommandHeld,
		sql.NullTime{Time: now, Valid: true})
	// The counterweight: a row one second past its horizon under the SAME call. Without
	// it this test is satisfied by a sweep that expires nothing at all.
	seedDrainable(t, api, sysctx, 20, "dev-1", CommandHeld,
		sql.NullTime{Time: now.Add(-time.Second), Valid: true})

	count, byFromStatus, err := api.ExpireStale(sysctx, now)
	if err != nil {
		t.Fatalf("ExpireStale failed: %v", err)
	}
	if count != 1 || byFromStatus[CommandHeld.String()] != 1 {
		t.Fatalf("the sweep expired %d rows (%v), want exactly the one PAST its horizon. "+
			"Expiring both means the sweep is asking `expires_at <= now` again — the hand-written "+
			"predicate that disagreed with the drain by one instant; expiring neither means it "+
			"stopped expiring", count, byFromStatus)
	}
	onTheBoundary := loadOrFail(t, api, sysctx, 10)
	if onTheBoundary.Status != CommandHeld.String() {
		t.Fatalf("the command sitting exactly on its horizon became %s; the boundary belongs to "+
			"LIVE, and a sweep stricter than the drain expires commands the drain is still "+
			"willing to deliver", onTheBoundary.Status)
	}
	pastIt := loadOrFail(t, api, sysctx, 20)
	if pastIt.Status != CommandExpired.String() {
		t.Fatalf("the command past its horizon is %s, want EXPIRED", pastIt.Status)
	}
}

func sideName(expired bool) string {
	if expired {
		return "EXPIRED"
	}
	return "LIVE"
}
