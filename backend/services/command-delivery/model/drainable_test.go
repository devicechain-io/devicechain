// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These tests are the ONLY thing pinning the wake drain's ordering guarantee, and they
// carry that weight because the client-side sort they replace is being deleted. The
// LwM2M drain used to over-fetch 1000 rows, sort them by id in memory and truncate to
// 32; with the ordering moved into the query, a server that stopped ordering would look
// identical to one that never had to.
//
// 🔴 EVERY FIXTURE HERE IS ANTI-SORTED ON PURPOSE, and getting that right took THREE
// attempts — each of which produced a fixture that looked anti-sorted and was not. This
// is the "a deleted sort key stayed green because an index supplied the order for free"
// failure, and it is worth writing down exactly how it hides, because the first two
// versions of this file were broken instruments that passed:
//
//  1. Seeding through CreateCommand in the expected order. sqlite returns rows in rowid
//     order for a table scan, auto-assigned ids ARE the rowid, so the answer came back
//     sorted whether or not the query said ORDER BY.
//  2. Seeding with EXPLICIT ids written in descending order. Insertion order is not
//     storage order: id is an INTEGER PRIMARY KEY, hence the rowid, so sqlite physically
//     stores the rows sorted by it and hands them back ascending regardless.
//  3. Explicit ids plus a token derived from the id (`drain-00010`). This is the subtle
//     one. The harness creates the per-tenant partial unique index on (tenant_id, token),
//     the planner picks it to satisfy the tenant predicate, and rows therefore arrive in
//     TOKEN order — which, with the token derived from the id, is id order again. The
//     index supplied the ordering for free and the mutation stayed green.
//
// The fix is the token, and it is why seedDrainable computes one instead of taking it:
// the token is anti-correlated with the id (antiTokenBase - id), so every ordered source
// sqlite could substitute — rowid order, token-index order, insertion order — disagrees
// with the answer these tests demand. Deleting `Order("id ASC")` now turns them red.
//
// 🔑 THAT DEFENCE IS ABOUT THE HARNESS, NOT THE PRODUCT. On Postgres the drain reads its
// own partial index (idx_commands_drainable, keyed …, id) and the order is real. The
// fixture exists so that a test on sqlite is measuring the query rather than the driver's
// storage layout.

// antiTokenBase makes a seeded row's token sort OPPOSITE to its id: a higher id gets a
// lexically smaller token. Any order sqlite might substitute for the one under test is
// therefore visibly wrong, instead of accidentally right.
const antiTokenBase = 99999

// antiToken is the token seeded for a given id. Tests that assert on tokens compute it
// through this, so the anti-correlation cannot drift away from the fixture.
func antiToken(id uint) string {
	return fmt.Sprintf("drain-%05d", antiTokenBase-id)
}

// seedDrainable writes one command with an explicit id, so a test can insert rows in an
// order that differs from the order it expects them back in. It bypasses CreateCommand
// deliberately: the point is to control the primary key, which the enqueue path
// (correctly) will not let a caller do.
func seedDrainable(t *testing.T, api *Api, ctx context.Context, id uint, deviceToken string,
	status CommandStatus, expiresAt sql.NullTime) {
	t.Helper()
	cmd := &Command{
		DeviceToken: deviceToken,
		Name:        "reboot",
		Status:      status.String(),
		ExpiresAt:   expiresAt,
	}
	cmd.ID = id
	cmd.Token = antiToken(id)
	if err := api.RDB.DB(ctx).Create(cmd).Error; err != nil {
		t.Fatalf("seeding command %d: %v", id, err)
	}
}

// tokensOf renders a drain result as the token list, in the order returned.
func tokensOf(found []*Command) []string {
	out := make([]string, 0, len(found))
	for _, c := range found {
		out = append(out, c.Token)
	}
	return out
}

// idsOf renders a drain result as the id list, in the order returned.
func idsOf(found []*Command) []uint {
	out := make([]uint, 0, len(found))
	for _, c := range found {
		out = append(out, c.ID)
	}
	return out
}

// TestDrainableCommandsOrdersOnIdAlone asserts the SHAPE of the generated SQL, which is
// the one property of this query no behavioural test can reach.
//
// 🔴 THE DEFECT IT GUARDS AGAINST RETURNS THE CORRECT ROWS IN THE CORRECT ORDER. Building
// this on rdb.ListOf would append Command.DefaultOrder AFTER the closure's order, making
// the SQL `ORDER BY id ASC, commands.created_at DESC, commands.token ASC`. Every
// assertion in this file would still pass — id is unique, so the trailing keys never
// break a tie — while Postgres, unable to prove the first key is already total, plans a
// Sort node and reads the whole backlog to return 32 rows. The partial index
// (idx_commands_drainable) would be dead and nothing would say so. ListOf would also add
// an unconditional COUNT: a second round trip per device wake for a total no caller reads.
//
// So the assertion is on the text: exactly one ORDER BY, and it names id and nothing else.
func TestDrainableCommandsOrdersOnIdAlone(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	seedDrainable(t, api, ctx, 10, "dev-1", CommandHeld, sql.NullTime{})

	// A session logger captures what the real call sends, so this cannot drift from the
	// implementation the way a hand-rebuilt query would.
	var statements []string
	rec := api.RDB.DB(ctx).Session(&gorm.Session{
		Logger: sqlRecorder{captured: &statements},
	})
	recorded := &Api{RDB: &rdb.RdbManager{Database: rec}}
	if _, err := recorded.DrainableCommands(ctx, "dev-1", 32); err != nil {
		t.Fatalf("DrainableCommands failed: %v", err)
	}
	if len(statements) != 1 {
		t.Fatalf("the drain issued %d statements, want exactly 1: %v. A second one is the "+
			"unconditional COUNT rdb.ListOf runs before its data query — a wasted round trip on "+
			"every device wake, for a total this caller never reads", len(statements), statements)
	}

	got := statements[0]
	if n := strings.Count(strings.ToUpper(got), "ORDER BY"); n != 1 {
		t.Fatalf("the drain has %d ORDER BY clauses, want 1: %s", n, got)
	}
	orderBy := got[strings.Index(strings.ToUpper(got), "ORDER BY"):]
	if strings.Contains(orderBy, "created_at") || strings.Contains(orderBy, "token") {
		t.Fatalf("the drain's ORDER BY carries more than id (%q). That is the signature of "+
			"rdb.ListOf appending Command.DefaultOrder after the query's own order: the ROWS and "+
			"their ORDER stay correct, so every other test here still passes, while Postgres can "+
			"no longer prove one key suffices, plans a Sort, and reads the whole backlog to "+
			"return %d rows — idx_commands_drainable dead with nothing reporting it",
			orderBy, DefaultDrainLimit)
	}
	if !strings.Contains(orderBy, "id") {
		t.Fatalf("the drain's ORDER BY does not name id: %q", orderBy)
	}
}

// sqlRecorder is a gorm logger that records only the SQL of each statement executed.
type sqlRecorder struct {
	captured *[]string
}

func (l sqlRecorder) LogMode(logger.LogLevel) logger.Interface { return l }
func (l sqlRecorder) Info(context.Context, string, ...any)     {}
func (l sqlRecorder) Warn(context.Context, string, ...any)     {}
func (l sqlRecorder) Error(context.Context, string, ...any)    {}
func (l sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	*l.captured = append(*l.captured, sql)
}

// TestDrainableCommandsReturnsOldestFirst.
//
// 🔴 OLDEST-FIRST IS A DELIVERY GUARANTEE. A firmware update is a sequence of commands
// whose order IS its meaning, and the backlog is where that order survives an outage.
// Newest-first would hand a waking device an image backwards; a bounded newest-first read
// would hand it the TAIL and silently drop the beginning, with every command reported
// delivered.
//
// The fixture is inserted in DESCENDING id order — the exact inverse of the expected
// answer — because the harness returns insertion order for an unordered query. Seeded the
// other way round, this test would pass against a query with no ORDER BY.
func TestDrainableCommandsReturnsOldestFirst(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	for _, id := range []uint{50, 20, 40, 10, 30} {
		seedDrainable(t, api, ctx, id, "dev-1", CommandHeld, sql.NullTime{})
	}

	found, err := api.DrainableCommands(ctx, "dev-1", 10)
	if err != nil {
		t.Fatalf("DrainableCommands failed: %v", err)
	}
	want := []uint{10, 20, 30, 40, 50}
	got := idsOf(found)
	if len(got) != len(want) {
		t.Fatalf("drained %v, want all of %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("drained %v, want %v — the backlog must come back in ENQUEUE order, or a "+
				"multi-part firmware update is dispatched out of sequence", got, want)
		}
	}
}

// TestDrainableCommandsBoundedByLimitTakesTheHead is the half of the ordering guarantee
// the test above cannot reach: when the backlog is larger than the drain, the rows that
// come back must be the OLDEST ones, not just some ordered subset.
//
// A query ordered id DESC with a LIMIT returns a perfectly sorted answer made of the
// wrong rows — the newest 3 of 6 — and the device runs the end of a sequence whose
// beginning it never received.
func TestDrainableCommandsBoundedByLimitTakesTheHead(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	for _, id := range []uint{60, 30, 50, 10, 40, 20} {
		seedDrainable(t, api, ctx, id, "dev-1", CommandHeld, sql.NullTime{})
	}

	found, err := api.DrainableCommands(ctx, "dev-1", 3)
	if err != nil {
		t.Fatalf("DrainableCommands failed: %v", err)
	}
	want := []uint{10, 20, 30}
	got := idsOf(found)
	if len(got) != 3 {
		t.Fatalf("drained %d rows, want the 3 asked for: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("drained %v, want the OLDEST three %v — a bounded newest-first read is "+
				"perfectly sorted and still hands the device the tail of the sequence", got, want)
		}
	}
}

// TestDrainableCommandsExcludesExpiredAndKeepsAnAbsentHorizon.
//
// The NULL row is the load-bearing case in BOTH directions. ExpiresAt is nullable and a
// zero DefaultCommandTTL stamps nothing, so a NULL horizon is a real production state
// meaning "no horizon" — it must be drained. A predicate written as a bare
// `expires_at >= ?` looks right, passes the expired-row assertion, and silently drops
// every NULL-horizon command from the drain forever, because SQL compares NULL to
// neither true nor false.
func TestDrainableCommandsExcludesExpiredAndKeepsAnAbsentHorizon(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	// 🔴 LOCAL TIME, NOT .UTC(), AND IT IS NOT COSMETIC — IT IS THE DIFFERENCE BETWEEN
	// THIS TEST MEASURING THE FILTER AND MEASURING THE DRIVER. The sqlite harness stores
	// a time.Time as TEXT carrying the bound value's own offset ("… 17:24:18+00:00" for a
	// UTC value, "… 13:24:18-04:00" for the same instant in local time) and compares it
	// LEXICALLY. DrainableCommands binds time.Now(), which is local — so a fixture written
	// in UTC is compared string-first against a differently-offset now, and every expired
	// row comes back "live". Same instant, opposite answer.
	//
	// It is a property of THIS harness only: production is Postgres, where timestamptz
	// comparison is instant-based and the offset is not in the stored value at all. But it
	// means a fixture and the code under test must agree on zone here, or the assertion
	// below reports on the driver's collation rather than on the predicate.
	now := time.Now()

	// Inserted with the survivors' ids interleaved among the expired ones, so an
	// implementation that dropped the expiry filter would return them in positions the
	// assertion notices.
	seedDrainable(t, api, ctx, 10, "dev-1", CommandHeld, sql.NullTime{Time: now.Add(-time.Hour), Valid: true})
	seedDrainable(t, api, ctx, 20, "dev-1", CommandHeld, sql.NullTime{})
	seedDrainable(t, api, ctx, 30, "dev-1", CommandParked, sql.NullTime{Time: now.Add(-time.Minute), Valid: true})
	seedDrainable(t, api, ctx, 40, "dev-1", CommandHeld, sql.NullTime{Time: now.Add(time.Hour), Valid: true})

	found, err := api.DrainableCommands(ctx, "dev-1", 10)
	if err != nil {
		t.Fatalf("DrainableCommands failed: %v", err)
	}
	got := idsOf(found)
	if len(got) != 2 || got[0] != 20 || got[1] != 40 {
		t.Fatalf("drained %v, want exactly [20 40]: 20 has a NULL horizon (no horizon = always "+
			"live, and dropping it would silently un-drain every command on an instance with no "+
			"default TTL) and 40 is inside its TTL; 10 and 30 are past theirs and must not actuate "+
			"the device", got)
	}
}

// TestDrainableCommandsReturnsOnlyTheWaitingBacklog pins the status set to HELD ∪ PARKED.
//
// QUEUED is the sharp exclusion: it has not been through the presence gate, so the sweep
// still owns it and will dispatch it over whichever transport the gate picks. Draining it
// here would put the same row in front of two dispatchers on two transports, with only
// the claim between the device and a double actuation.
//
// SENT is the ordinary one — it is at a device already. The terminal states are covered
// too, since a drained CANCELLED or SUCCESSFUL command re-fires an operation that was
// called off or already finished.
func TestDrainableCommandsReturnsOnlyTheWaitingBacklog(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	// Seeded so the two rows that MUST come back are the last two inserted and the
	// highest ids: an implementation that ignored the status filter would return the
	// excluded rows first, and one that ignored the ordering would return them in
	// insertion order.
	excluded := []CommandStatus{CommandQueued, CommandSent, CommandSuccessful, CommandFailed,
		CommandTimeout, CommandExpired, CommandCancelled}
	for i, status := range excluded {
		seedDrainable(t, api, ctx, uint(10+i), "dev-1", status, sql.NullTime{})
	}
	seedDrainable(t, api, ctx, 90, "dev-1", CommandParked, sql.NullTime{})
	seedDrainable(t, api, ctx, 95, "dev-1", CommandHeld, sql.NullTime{})

	found, err := api.DrainableCommands(ctx, "dev-1", 100)
	if err != nil {
		t.Fatalf("DrainableCommands failed: %v", err)
	}
	got := idsOf(found)
	if len(got) != 2 || got[0] != 90 || got[1] != 95 {
		t.Fatalf("drained %v, want exactly the HELD and PARKED rows [90 95]. A QUEUED row here "+
			"would be dispatched by BOTH the drain and the sweep; a terminal one would re-fire a "+
			"command that was already finished or called off", got)
	}
}

// TestDrainableCommandsOnlyReadsTheNamedDevice: device_token is a filter, and a drain that
// ignored it would hand one device every other device's pending actuations.
func TestDrainableCommandsOnlyReadsTheNamedDevice(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	seedDrainable(t, api, ctx, 10, "dev-other", CommandHeld, sql.NullTime{})
	seedDrainable(t, api, ctx, 20, "dev-1", CommandHeld, sql.NullTime{})
	seedDrainable(t, api, ctx, 30, "dev-other", CommandParked, sql.NullTime{})

	found, err := api.DrainableCommands(ctx, "dev-1", 10)
	if err != nil {
		t.Fatalf("DrainableCommands failed: %v", err)
	}
	if got := idsOf(found); len(got) != 1 || got[0] != 20 {
		t.Fatalf("drained %v, want only dev-1's row [20]", got)
	}
}

// TestDrainableCommandsCannotSeeAnotherTenantsBacklog.
//
// 🔴 THE DEVICE TOKEN IS NOT THE FENCE. Device tokens are unique per TENANT, not per
// instance, so two tenants can legitimately own a device called "dev-1" — and the drain
// filters on the token supplied by a transport that authenticated the connection, not the
// tenancy. What keeps the two apart is the dc:tenant_query scope callback, which is why
// this function builds on api.RDB.DB(ctx) and not on raw SQL. This test is what would fail
// if it ever stopped doing so.
func TestDrainableCommandsCannotSeeAnotherTenantsBacklog(t *testing.T) {
	api := newTestApi(t)
	ctxA := core.WithTenant(context.Background(), "A")
	ctxB := core.WithTenant(context.Background(), "B")

	// Same device token in both tenants, and B's rows are OLDER — so a drain that lost
	// the tenant predicate would return them FIRST, at the head of A's dispatch order.
	seedDrainable(t, api, ctxB, 10, "dev-1", CommandHeld, sql.NullTime{})
	seedDrainable(t, api, ctxB, 20, "dev-1", CommandParked, sql.NullTime{})
	seedDrainable(t, api, ctxA, 30, "dev-1", CommandHeld, sql.NullTime{})

	found, err := api.DrainableCommands(ctxA, "dev-1", 10)
	if err != nil {
		t.Fatalf("DrainableCommands failed: %v", err)
	}
	if got := idsOf(found); len(got) != 1 || got[0] != 30 {
		t.Fatalf("tenant A drained %v, want only its own row [30]. A device token is unique per "+
			"TENANT, so it can never stand in for the tenant fence", got)
	}

	// And the other direction, so the assertion above cannot be satisfied by a drain that
	// is simply broken and returns one arbitrary row.
	fromB, err := api.DrainableCommands(ctxB, "dev-1", 10)
	if err != nil {
		t.Fatalf("DrainableCommands for tenant B failed: %v", err)
	}
	if got := idsOf(fromB); len(got) != 2 || got[0] != 10 || got[1] != 20 {
		t.Fatalf("tenant B drained %v, want its own two rows [10 20]", got)
	}
}

// TestDrainableCommandsDefaultsToThirtyTwo: a caller naming no limit gets DefaultDrainLimit
// rows, and they are the OLDEST ones.
//
// This is the case the client used to get wrong by construction — it asked for 1000, sorted
// in memory and threw away 968. The bound is the server's now, so it has to be asserted
// here or nothing asserts it at all.
func TestDrainableCommandsDefaultsToThirtyTwo(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	// Seeded newest-id-first so insertion order is the exact inverse of the expected head.
	const total = DefaultDrainLimit + 8
	for i := total; i >= 1; i-- {
		seedDrainable(t, api, ctx, uint(i), "dev-1", CommandHeld, sql.NullTime{})
	}

	for _, limit := range []int{0, -1} {
		found, err := api.DrainableCommands(ctx, "dev-1", limit)
		if err != nil {
			t.Fatalf("DrainableCommands(limit=%d) failed: %v", limit, err)
		}
		if len(found) != DefaultDrainLimit {
			t.Fatalf("limit=%d drained %d rows, want the default %d", limit, len(found), DefaultDrainLimit)
		}
		if got := idsOf(found); got[0] != 1 || got[len(got)-1] != DefaultDrainLimit {
			t.Fatalf("limit=%d drained ids %v, want the oldest %d (1..%d)",
				limit, got, DefaultDrainLimit, DefaultDrainLimit)
		}
	}
}

// TestDrainableCommandsClampsToMaxPageSize: a caller cannot ask for an unbounded drain.
//
// The set this bounds is a fleet's whole accumulated backlog after an outage — exactly the
// thing that must not arrive in one response — so the clamp is a memory bound on the
// service, not a courtesy to the caller.
func TestDrainableCommandsClampsToMaxPageSize(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	const total = rdb.MaxPageSize + 5
	for i := total; i >= 1; i-- {
		seedDrainable(t, api, ctx, uint(i), "dev-1", CommandHeld, sql.NullTime{})
	}

	found, err := api.DrainableCommands(ctx, "dev-1", total+10_000)
	if err != nil {
		t.Fatalf("DrainableCommands failed: %v", err)
	}
	if len(found) != rdb.MaxPageSize {
		t.Fatalf("a request for %d drained %d rows, want the clamp at rdb.MaxPageSize (%d)",
			total+10_000, len(found), rdb.MaxPageSize)
	}
	if got := found[0].ID; got != 1 {
		t.Fatalf("the clamped drain starts at id %d, want the oldest row (1) — a clamp that "+
			"truncates the WRONG end drops the beginning of every sequence", got)
	}

	// A limit under the maximum is still honoured exactly, so the clamp is a ceiling and
	// not a fixed size.
	exact, err := api.DrainableCommands(ctx, "dev-1", 7)
	if err != nil {
		t.Fatalf("DrainableCommands(7) failed: %v", err)
	}
	if len(exact) != 7 {
		t.Fatalf("a request for 7 drained %d rows: %v", len(exact), tokensOf(exact))
	}
}
