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
type fakeQuerier struct {
	rows []drainRow
	err  error

	gotTenant   string
	gotBaseURL  string
	gotDevice   string
	gotStatus   string
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
		f.gotDevice, _ = crit["deviceToken"].(string)
		f.gotStatus, _ = crit["status"].(string)
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
		{Id: "20", Token: "exec", Name: "lwm2m.execute", Payload: strp(`{"path":"/5/0/2"}`), Status: "SENT"},
		{Id: "10", Token: "write", Name: "lwm2m.write", Payload: strp(`{"path":"/5/0/1","value":"coaps://fw"}`), Status: "SENT"},
		{Id: "3", Token: "first", Name: "lwm2m.read", Payload: strp(`{"path":"/3/0/0"}`), Status: "SENT"},
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
	// The wire contract: per-tenant scope, the device token, SENT-only, bounded page.
	if q.gotTenant != "tenantA" || q.gotDevice != "dev-1" || q.gotStatus != statusSent {
		t.Fatalf("query args = tenant %q device %q status %q", q.gotTenant, q.gotDevice, q.gotStatus)
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
			Payload: strp(`{"path":"/3/0/0"}`), Status: "SENT",
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
		{Id: "1", Token: "expired", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "SENT", ExpiresAt: &past},
		{Id: "2", Token: "live", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "SENT", ExpiresAt: &future},
		{Id: "3", Token: "nottl", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "SENT"},
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

// TestPendingDropsNonSent is the defensive guard: even if the server ever returned a
// non-SENT row (contract drift), the fetcher must never hand a terminal command to
// dispatch (re-actuation).
func TestPendingDropsNonSent(t *testing.T) {
	q := &fakeQuerier{rows: []drainRow{
		{Id: "1", Token: "done", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "SUCCESSFUL"},
		{Id: "2", Token: "live", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "SENT"},
	}}
	f := NewCommandFetcher(q, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "t", "d", time.Now())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(got) != 1 || got[0].Token != "live" {
		t.Fatalf("got %+v, want only the SENT command", got)
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
