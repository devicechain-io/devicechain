// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// fakeQuerier is a stand-in for svcclient.Client: it captures the query arguments and
// returns a canned set of rows.
//
// It keeps the WHOLE criteria map, not just the fields a particular assertion wants. A fake
// that copies out only the fields it knows about cannot see a field the caller stopped
// sending (or started sending under a new name) — the omission looks like a passing test.
type fakeQuerier struct {
	rows []drainRow
	err  error

	gotTenant   string
	gotBaseURL  string
	gotCriteria map[string]any
	gotDevice   string
	gotPageSize int
	gotPageNum  int
}

func (f *fakeQuerier) Query(ctx context.Context, baseURL, tenant, query string, vars map[string]any, out any) error {
	if f.err != nil {
		return f.err
	}
	f.gotTenant = tenant
	f.gotBaseURL = baseURL
	if crit, ok := vars["criteria"].(map[string]any); ok {
		f.gotCriteria = crit
		f.gotDevice, _ = crit["deviceToken"].(string)
		f.gotPageSize, _ = crit["pageSize"].(int)
		f.gotPageNum, _ = crit["pageNumber"].(int)
	}
	resp := out.(*drainResponse)
	resp.Commands.Results = f.rows
	return nil
}

func strp(s string) *string { return &s }

// TestPendingOrdersOldestFirst is the FOTA-ordering guard: the commands query has no
// server-side ordering, so a device's Write /5/0/1 and Execute /5/0/2 could come back in
// any order; the fetcher MUST return them oldest-first (by numeric id) or a firmware
// update runs against a stale package URI (ADR-075 L4a B3 / L4b).
func TestPendingOrdersOldestFirst(t *testing.T) {
	q := &fakeQuerier{rows: []drainRow{
		{Id: "20", Token: "exec", Name: "lwm2m.execute", Payload: strp(`{"path":"/5/0/2"}`), Status: "PARKED"},
		{Id: "10", Token: "write", Name: "lwm2m.write", Payload: strp(`{"path":"/5/0/1","value":"coaps://fw"}`), Status: "PARKED"},
		{Id: "3", Token: "first", Name: "lwm2m.read", Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED"},
	}}
	f := NewCommandFetcher(q, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "tenantA", "dev-1", time.Now())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	wantOrder := []string{"first", "write", "exec"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d commands, want %d", len(got), len(wantOrder))
	}
	for i, w := range wantOrder {
		if got[i].Token != w {
			t.Fatalf("position %d: got token %q, want %q (not oldest-first)", i, got[i].Token, w)
		}
	}
	// Payload passes through as raw JSON bytes for the executor.
	if string(got[1].Payload) != `{"path":"/5/0/1","value":"coaps://fw"}` {
		t.Fatalf("payload not preserved: %q", string(got[1].Payload))
	}
	// The wire contract: per-tenant scope and the device token. The status SET has its own
	// test (TestPendingRequestsHeldAndParkedOnTheWire) since it is the part that must not drift.
	if q.gotTenant != "tenantA" || q.gotDevice != "dev-1" {
		t.Fatalf("query args = tenant %q device %q", q.gotTenant, q.gotDevice)
	}
	if q.gotPageNum != 1 || q.gotPageSize != maxDrainFetch {
		t.Fatalf("pagination = page %d size %d, want 1/%d (a large fetch so the sort sees the oldest)", q.gotPageNum, q.gotPageSize, maxDrainFetch)
	}
}

// TestPendingTruncatesToOldestAfterSort is the >cap FOTA-ordering guard: when a device has MORE than
// maxDrainPerWake held commands, Pending must select the OLDEST maxDrainPerWake (by id), not an
// arbitrary subset — otherwise a firmware Write could be left off this wake's batch while its Execute
// is dispatched. The fetch page is large (maxDrainFetch), so the sort sees all rows; only then is the
// batch capped.
func TestPendingTruncatesToOldestAfterSort(t *testing.T) {
	// Build maxDrainPerWake+5 rows with ids in DESCENDING order (worst case for a naive head-of-page).
	n := maxDrainPerWake + 5
	rows := make([]drainRow, 0, n)
	for id := n; id >= 1; id-- {
		rows = append(rows, drainRow{
			Id: strconv.Itoa(id), Token: "c" + strconv.Itoa(id), Name: "lwm2m.read",
			Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED",
		})
	}
	f := NewCommandFetcher(&fakeQuerier{rows: rows}, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "t", "d", time.Now())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(got) != maxDrainPerWake {
		t.Fatalf("got %d, want the per-wake cap %d", len(got), maxDrainPerWake)
	}
	// The oldest maxDrainPerWake commands are ids 1..maxDrainPerWake, in ascending order.
	for i := 0; i < maxDrainPerWake; i++ {
		want := "c" + strconv.Itoa(i+1)
		if got[i].Token != want {
			t.Fatalf("position %d: got %q, want %q — not the oldest, in order", i, got[i].Token, want)
		}
	}
}

// TestPendingDropsExpired proves a command already past its horizon is not drained — it
// will TIMEOUT within a sweep and must never actuate a device late.
func TestPendingDropsExpired(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour).Format(time.RFC3339)
	future := now.Add(time.Hour).Format(time.RFC3339)
	q := &fakeQuerier{rows: []drainRow{
		{Id: "1", Token: "expired", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED", ExpiresAt: &past},
		{Id: "2", Token: "live", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED", ExpiresAt: &future},
		{Id: "3", Token: "nottl", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED"},
	}}
	f := NewCommandFetcher(q, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "t", "d", now)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (expired dropped)", len(got))
	}
	for _, c := range got {
		if c.Token == "expired" {
			t.Fatalf("expired command was drained — it must ride to TIMEOUT, not actuate late")
		}
	}
}

// TestPendingRequestsHeldAndParkedOnTheWire is the WIRE test for the drain's status set. It
// asserts on the criteria map actually handed to the GraphQL client, against LITERAL strings
// — never against this package's own constants, because a test that compares a constant to
// itself passes no matter what either side is renamed to and proves only that Go assignment
// works.
//
// What it is defending: command-delivery's status vocabulary and this package's copy of it
// are two independent lists (downlink does not import the command-delivery module), and the
// forked graphql-go rejects an input-object field the schema does not define when it arrives
// through a VARIABLE — which is how criteria is sent. So the field NAME must be `statuses`
// and the VALUES must be exactly the two strings command-delivery persists. A rename on
// either side has to fail here, loudly, rather than drift into a drain that quietly returns
// nothing and leaves a sleeping fleet's backlog undelivered forever.
func TestPendingRequestsHeldAndParkedOnTheWire(t *testing.T) {
	q := &fakeQuerier{}
	f := NewCommandFetcher(q, "http://cd/graphql")

	if _, err := f.Pending(context.Background(), "tenantA", "dev-1", time.Now()); err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if q.gotCriteria == nil {
		t.Fatal("no criteria were sent")
	}
	// The OLD single-value field must be gone: sending both would AND the two filters in
	// command-delivery, and `status = 'PARKED' AND status IN ('HELD','PARKED')` silently
	// drops every HELD row — the exact bug this test exists to prevent, dressed as a leftover.
	if _, ok := q.gotCriteria["status"]; ok {
		t.Fatalf("criteria still carries the single-value `status` field: %#v", q.gotCriteria)
	}
	raw, ok := q.gotCriteria["statuses"]
	if !ok {
		t.Fatalf("criteria carries no `statuses` field: %#v", q.gotCriteria)
	}
	got, ok := raw.([]string)
	if !ok {
		t.Fatalf("`statuses` is %T, want []string (the schema declares [String!])", raw)
	}
	// Literal wire values, deliberately spelled out here and NOT read from statusHeld /
	// statusParked / drainStatuses.
	//
	// 🔴 SENT MUST NOT APPEAR. It used to, and dispatching out of it without a claim was the
	// platform's only unclaimed dispatch — a command that went nowhere and one the device
	// genuinely holds were the same status, so the drain could not tell them apart. The first
	// kind is PARKED now. A SENT row here would mean re-dispatching a command the device may
	// already be running.
	want := map[string]bool{"HELD": false, "PARKED": false}
	if len(got) != len(want) {
		t.Fatalf("statuses = %v, want exactly [HELD PARKED]", got)
	}
	for _, s := range got {
		seen, known := want[s]
		if !known {
			t.Fatalf("statuses carries %q, which is not one of HELD/PARKED: %v", s, got)
		}
		if seen {
			t.Fatalf("statuses repeats %q: %v", s, got)
		}
		want[s] = true
	}
	for s, seen := range want {
		if !seen {
			t.Fatalf("statuses is missing %q: %v — a device's %s backlog would never drain", s, got, s)
		}
	}
}

// TestPendingDrainsHeldAndParkedCarryingStatus proves both halves of the backlog come through
// AND that each row's status rides on the DrainCommand. The status is what the dispatcher
// uses to decide whether a claim is required before it actuates, so a fetcher that dropped
// the field would leave every command looking un-claimable — a whole-fleet silent stall that
// no test of the dispatcher alone can see.
func TestPendingDrainsHeldAndParkedCarryingStatus(t *testing.T) {
	q := &fakeQuerier{rows: []drainRow{
		{Id: "1", Token: "held", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "HELD"},
		{Id: "2", Token: "parked", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED"},
	}}
	f := NewCommandFetcher(q, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "t", "d", time.Now())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d commands, want both the HELD and the PARKED row: %+v", len(got), got)
	}
	if got[0].Token != "held" || got[0].Status != "HELD" {
		t.Fatalf("row 0 = %+v, want the HELD command carrying its status", got[0])
	}
	if got[1].Token != "parked" || got[1].Status != "PARKED" {
		t.Fatalf("row 1 = %+v, want the PARKED command carrying its status", got[1])
	}
}

// TestPendingDropsNonDrainableStatus is the defensive guard: even if the server ever returned
// a row outside the drain's status set (contract drift), the fetcher must never hand a
// terminal command to dispatch (re-actuation). SUCCESSFUL is the case that matters — a
// command the device already answered.
func TestPendingDropsNonDrainableStatus(t *testing.T) {
	q := &fakeQuerier{rows: []drainRow{
		{Id: "1", Token: "done", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "SUCCESSFUL"},
		{Id: "2", Token: "live", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED"},
		{Id: "3", Token: "queued", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "QUEUED"},
	}}
	f := NewCommandFetcher(q, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "t", "d", time.Now())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	// QUEUED is excluded on purpose as well: command-delivery's own sweep will publish it to
	// the LIVE path within a tick, so draining it here too would double-dispatch.
	if len(got) != 1 || got[0].Token != "live" {
		t.Fatalf("got %+v, want only the drainable (PARKED) command", got)
	}
}

// TestPendingPropagatesError proves a fetch failure surfaces (the drain skips + retries
// next wake) rather than silently returning an empty queue.
func TestPendingPropagatesError(t *testing.T) {
	q := &fakeQuerier{err: errors.New("command-delivery unreachable")}
	f := NewCommandFetcher(q, "http://cd/graphql")

	_, err := f.Pending(context.Background(), "t", "d", time.Now())
	if err == nil {
		t.Fatalf("expected the query error to propagate")
	}
}

// TestPendingEmptyIsNotAnError proves the common case — a device with nothing pending —
// returns an empty slice, so a wake with no queue is silent, not a logged failure.
func TestPendingEmptyIsNotAnError(t *testing.T) {
	f := NewCommandFetcher(&fakeQuerier{rows: nil}, "http://cd/graphql")
	got, err := f.Pending(context.Background(), "t", "d", time.Now())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d cmds, err %v; want 0/nil", len(got), err)
	}
}
