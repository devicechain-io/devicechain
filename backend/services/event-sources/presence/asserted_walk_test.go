// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
)

// pagingClient serves a fixed row set as pages, honouring afterId and pageSize the way
// device-state does. ignoreCursor makes it answer the SAME first page forever, which is how
// a server that has stopped making progress is staged; failOnPage makes one page fail
// mid-walk.
type pagingClient struct {
	rows         int
	ignoreCursor bool
	failOnPage   int // 1-based; 0 disables
	calls        int
	pageSizes    []int
}

// patienceLimit is how many pages this fake serves an ignore-the-cursor walk before
// refusing.
//
// 🔑 IT IS WHAT KEEPS A NEGATIVE CONTROL FROM BEING A HANG. Deleting the walk's
// cursor-progress guard makes such a walk loop forever, so the mutation that removes it
// would otherwise be "caught" by the package timeout killing the whole binary — a result
// that names no test and proves nothing about this one. With the fake losing patience, the
// walk ends and the test fails on its call-count assertion, which is a control rather than
// a stopwatch.
const patienceLimit = 12

func (c *pagingClient) Query(_ context.Context, _, _, _ string, vars map[string]any, out any) error {
	c.calls++
	if c.failOnPage == c.calls {
		return errors.New("device-state is unavailable")
	}
	if c.ignoreCursor && c.calls > patienceLimit {
		return errors.New("this fake has served the same page too many times")
	}
	pageSize, _ := vars["pageSize"].(int)
	c.pageSizes = append(c.pageSizes, pageSize)
	after := 0
	if raw, ok := vars["afterId"].(string); ok && raw != "" && !c.ignoreCursor {
		after, _ = strconv.Atoi(raw)
	}
	resp, ok := out.(*struct {
		AssertedDeviceStates []assertedRow `json:"assertedDeviceStates"`
	})
	if !ok {
		return errors.New("unexpected out type")
	}
	for id := after + 1; id <= c.rows && len(resp.AssertedDeviceStates) < pageSize; id++ {
		resp.AssertedDeviceStates = append(resp.AssertedDeviceStates, assertedRow{
			Id:          strconv.Itoa(id),
			DeviceToken: fmt.Sprintf("dev-%04d", id),
			SessionId:   strconv.Itoa(1_700_000_000_000_000_000 + id),
			Active:      true,
		})
	}
	return nil
}

// TestTheProjectionSnapshotIsWalkedInPages is the size fix on the reconciler's side.
//
// 🔴 THE UNPAGED READ DID NOT DEGRADE, IT STOPPED. Past the cross-service read cap the
// client's answer is a failed read, so the tenant was skipped for the ENTIRE pass — every
// pass — on exactly the instances whose fleets are large enough to need reconciliation.
// And this reconciler is the only account of the deaths a graceful broker shutdown never
// announces.
func TestTheProjectionSnapshotIsWalkedInPages(t *testing.T) {
	client := &pagingClient{rows: assertedStatesPageSize*2 + 7}
	r := NewGraphQLProjectionReader(client, "http://device-state/graphql")

	states, err := r.AssertedStates(context.Background(), "acme", "mqtt1")
	if err != nil {
		t.Fatalf("AssertedStates failed: %v", err)
	}
	if len(states) != client.rows {
		t.Fatalf("the walk covered %d of %d devices", len(states), client.rows)
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 pages for %d rows at %d per page, got %d",
			client.rows, assertedStatesPageSize, client.calls)
	}
	for i, size := range client.pageSizes {
		if size != assertedStatesPageSize {
			t.Fatalf("page %d asked for %d rows, want %d", i, size, assertedStatesPageSize)
		}
	}
}

// TestAServerThatIgnoresTheCursorIsCaughtInTwoPages is why the walk checks PROGRESS
// rather than counting pages.
//
// 🔴🔴 A PAGE CEILING WOULD BE A CAP ON FLEET SIZE WEARING A SAFETY HAT. This set is
// cumulative — nothing prunes an asserted row, so it grows for the life of the tenant —
// so any fixed page ceiling is a row count above which the walk fails on EVERY pass,
// forever. That is precisely the "reconciliation is off for the fleets that need it most"
// failure this change exists to remove, reintroduced above the new bound.
//
// 🔑 THE REAL SIGNATURE OF AN IGNORED CURSOR IS A CURSOR THAT DOES NOT MOVE. Ids ascend
// strictly, so a full page always advances it. Asking that question catches the spin in
// two pages regardless of fleet size, and a negative control for it fails by assertion
// rather than by hanging.
func TestAServerThatIgnoresTheCursorIsCaughtInTwoPages(t *testing.T) {
	client := &pagingClient{rows: assertedStatesPageSize * 4, ignoreCursor: true}
	r := NewGraphQLProjectionReader(client, "http://device-state/graphql")

	states, err := r.AssertedStates(context.Background(), "acme", "mqtt1")
	if err == nil {
		t.Fatal("a walk that cannot make progress must fail rather than return a partial snapshot")
	}
	if states != nil {
		t.Fatalf("a failed walk must return no snapshot at all, got %d devices", len(states))
	}
	if client.calls != 2 {
		t.Fatalf("a stuck cursor must be caught on the second page, made %d calls; without the "+
			"progress guard the walk runs on until the fake loses patience at %d", client.calls, patienceLimit)
	}
}

// TestALargeFleetIsNotCappedByThePageCount is the counterweight, and it is the reason the
// ceiling had to go rather than merely being raised.
//
// A walk that keeps making progress must keep going, however many pages that takes. Any
// fixed ceiling answers "your fleet is too big" with the same error a broken server gets.
func TestALargeFleetIsNotCappedByThePageCount(t *testing.T) {
	// Comfortably past the 200-page ceiling this replaced (200 x 500 = 100,000 rows).
	client := &pagingClient{rows: assertedStatesPageSize*250 + 3}
	r := NewGraphQLProjectionReader(client, "http://device-state/graphql")

	states, err := r.AssertedStates(context.Background(), "acme", "mqtt1")
	if err != nil {
		t.Fatalf("a fleet past the old page ceiling must still be walked, got %v", err)
	}
	if len(states) != client.rows {
		t.Fatalf("the walk covered %d of %d devices", len(states), client.rows)
	}
}

// TestAFailureMidWalkFailsTheWholeRead is the atomicity rule, and here the partial answer
// is worse than none.
//
// 🔴🔴 THIS SNAPSHOT IS ONE SIDE OF A DIFF. A device missing because its page was never
// fetched is indistinguishable from a device the projection holds no row for — so keeping
// the pages already read would make the pass emit connect "repairs" for devices that
// needed none, each stamped with the broker's own session id. The caller already handles a
// failed read correctly by skipping this tenant for the pass.
func TestAFailureMidWalkFailsTheWholeRead(t *testing.T) {
	client := &pagingClient{rows: assertedStatesPageSize * 3, failOnPage: 2}
	r := NewGraphQLProjectionReader(client, "http://device-state/graphql")

	states, err := r.AssertedStates(context.Background(), "acme", "mqtt1")
	if err == nil {
		t.Fatal("a page failing mid-walk must fail the whole read")
	}
	if states != nil {
		t.Fatalf("no partial snapshot may escape, got %d devices", len(states))
	}
}

// TestTheCursorAdvancesPastRowsTheWalkDROPPED is the wedge this walk would otherwise have.
//
// 🔑 THE CURSOR COMES FROM THE LAST ROW READ, NOT THE LAST ROW KEPT. Rows whose session is
// unreadable are dropped from the snapshot on purpose. Taking the cursor from the last
// KEPT row means a page whose final row is dropped asks for the same page again — and if
// it keeps being dropped, forever. The walk would consume its whole ceiling and fail, on a
// tenant whose data is merely slightly odd.
func TestTheCursorAdvancesPastRowsTheWalkDROPPED(t *testing.T) {
	client := &droppedTailClient{}
	r := NewGraphQLProjectionReader(client, "http://device-state/graphql")

	states, err := r.AssertedStates(context.Background(), "acme", "mqtt1")
	if err != nil {
		t.Fatalf("AssertedStates failed: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("expected the walk to advance past the dropped tail row in 2 calls, got %d", client.calls)
	}
	if len(states) != assertedStatesPageSize-1 {
		t.Fatalf("expected %d kept devices, got %d", assertedStatesPageSize-1, len(states))
	}
}

// droppedTailClient serves one full page whose LAST row has an unreadable session (so the
// fold drops it), then an empty page.
type droppedTailClient struct{ calls int }

func (c *droppedTailClient) Query(_ context.Context, _, _, _ string, vars map[string]any, out any) error {
	c.calls++
	resp := out.(*struct {
		AssertedDeviceStates []assertedRow `json:"assertedDeviceStates"`
	})
	if c.calls > 1 {
		// The cursor must have moved past row assertedStatesPageSize; if it did not, the
		// walk is asking for the same page and this fake would keep feeding it.
		if raw, _ := vars["afterId"].(string); raw != strconv.Itoa(assertedStatesPageSize) {
			return fmt.Errorf("the walk resumed at %q, not past the dropped last row", raw)
		}
		return nil
	}
	for id := 1; id <= assertedStatesPageSize; id++ {
		session := strconv.Itoa(1_700_000_000_000_000_000 + id)
		if id == assertedStatesPageSize {
			session = "not-a-session" // dropped by the fold
		}
		resp.AssertedDeviceStates = append(resp.AssertedDeviceStates, assertedRow{
			Id:          strconv.Itoa(id),
			DeviceToken: fmt.Sprintf("dev-%04d", id),
			SessionId:   session,
			Active:      true,
		})
	}
	return nil
}
