// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
)

// activePagingClient serves a fixed asserted-active set as pages, honouring afterId and
// pageSize. The sessions ASCEND with the row id, so the largest one — the epoch floor the
// caller derives — is only visible to a walk that reaches the LAST page.
type activePagingClient struct {
	rows       int
	failOnPage int // 1-based; 0 disables
	calls      int
}

func (c *activePagingClient) Query(_ context.Context, _, _, _ string, vars map[string]any, out any) error {
	c.calls++
	if c.failOnPage == c.calls {
		return errors.New("device-state is unavailable")
	}
	pageSize, _ := vars["pageSize"].(int)
	after := 0
	if raw, ok := vars["afterId"].(string); ok && raw != "" {
		after, _ = strconv.Atoi(raw)
	}
	resp, ok := out.(*struct {
		AssertedDeviceStates []struct {
			Id         string  `json:"id"`
			ExternalId *string `json:"externalId"`
			SessionId  string  `json:"sessionId"`
		} `json:"assertedDeviceStates"`
	})
	if !ok {
		return errors.New("unexpected out type")
	}
	for id := after + 1; id <= c.rows && len(resp.AssertedDeviceStates) < pageSize; id++ {
		ext := fmt.Sprintf("group/node-%04d", id)
		resp.AssertedDeviceStates = append(resp.AssertedDeviceStates, struct {
			Id         string  `json:"id"`
			ExternalId *string `json:"externalId"`
			SessionId  string  `json:"sessionId"`
		}{
			Id:         strconv.Itoa(id),
			ExternalId: &ext,
			SessionId:  strconv.FormatUint(uint64(1_700_000_000_000_000_000+id), 10),
		})
	}
	return nil
}

// TestTheEpochFloorComesFromTHEWHOLESET is the reason this walk cannot stop early.
//
// 🔴🔴 THE MAXIMUM SESSION IS A FLOOR, NOT A STATISTIC. The adapter's epoch generator is
// primed from it so every session it mints supersedes whatever the projection already
// holds. Derive that floor from a subset and it can be LOWER than a stored session — after
// which every emission the adapter makes is rejected by presence ordering, for devices this
// pass will never revisit. That is a wedge until the process restarts, produced by a read
// that merely returned less than it should have.
func TestTheEpochFloorComesFromTHEWHOLESET(t *testing.T) {
	client := &activePagingClient{rows: assertedActivePageSize*2 + 3}
	r := NewReconciler(client, "http://device-state/graphql")

	devices, floor, err := r.AssertedActive(context.Background(), "acme", "sparkplug:h1")
	if err != nil {
		t.Fatalf("AssertedActive failed: %v", err)
	}
	if len(devices) != client.rows {
		t.Fatalf("the walk covered %d of %d devices", len(devices), client.rows)
	}
	want := uint64(1_700_000_000_000_000_000 + client.rows)
	if floor != want {
		t.Fatalf("the epoch floor is %d, want %d — it was taken from a page rather than the whole set",
			floor, want)
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 pages, got %d", client.calls)
	}
}

// TestAFailedPageYieldsNoFloorAtAll is the atomicity rule, and here it is stricter than
// "return what we have" precisely because a partial floor is worse than no floor.
//
// A failed read is retried on the next pass and costs nothing permanent. A floor that is
// too low cannot be detected by the code that uses it — every emission simply stops
// applying.
func TestAFailedPageYieldsNoFloorAtAll(t *testing.T) {
	client := &activePagingClient{rows: assertedActivePageSize * 3, failOnPage: 2}
	r := NewReconciler(client, "http://device-state/graphql")

	devices, floor, err := r.AssertedActive(context.Background(), "acme", "sparkplug:h1")
	if err == nil {
		t.Fatal("a page failing mid-walk must fail the whole read")
	}
	if devices != nil {
		t.Fatalf("no partial device list may escape, got %d", len(devices))
	}
	if floor != 0 {
		t.Fatalf("a failed walk must yield no floor, got %d — a floor from a prefix of the set is "+
			"exactly the value that wedges the adapter", floor)
	}
}

// TestTheActiveWalkStopsWhenTheCursorSTOPSMOVING keeps a server that ignores the cursor
// from spinning — in production until the pod restarts, and in a test until the package
// timeout kills the binary. A control that hangs is not a control.
//
// 🔑 IT ASKS WHETHER PROGRESS IS BEING MADE, NOT HOW MANY PAGES HAVE PASSED. A page-count
// ceiling would double as a hard cap on fleet size, above which every pass fails forever —
// the failure this change removes, reintroduced above the bound.
func TestTheActiveWalkStopsWhenTheCursorSTOPSMOVING(t *testing.T) {
	client := &cursorIgnoringClient{}
	r := NewReconciler(client, "http://device-state/graphql")

	_, _, err := r.AssertedActive(context.Background(), "acme", "sparkplug:h1")
	if err == nil {
		t.Fatal("a walk that cannot make progress must fail rather than spin")
	}
	if client.calls != 2 {
		t.Fatalf("a stuck cursor must be caught on the second page, made %d calls; without the "+
			"progress guard the walk runs on until the fake loses patience at %d", client.calls, activePatienceLimit)
	}
}

// TestALargeAssertedFleetIsNotCappedByThePageCount is the counterweight: a walk that keeps
// making progress must keep going, however many pages that takes.
func TestALargeAssertedFleetIsNotCappedByThePageCount(t *testing.T) {
	client := &activePagingClient{rows: assertedActivePageSize*250 + 1}
	r := NewReconciler(client, "http://device-state/graphql")

	devices, floor, err := r.AssertedActive(context.Background(), "acme", "sparkplug:h1")
	if err != nil {
		t.Fatalf("a fleet past the old page ceiling must still be walked, got %v", err)
	}
	if len(devices) != client.rows {
		t.Fatalf("the walk covered %d of %d devices", len(devices), client.rows)
	}
	if want := uint64(1_700_000_000_000_000_000 + client.rows); floor != want {
		t.Fatalf("the epoch floor is %d, want %d", floor, want)
	}
}

// cursorIgnoringClient always answers a full page starting at row 1, whatever cursor it is
// given — until it loses patience.
//
// 🔑 THE PATIENCE LIMIT IS WHAT KEEPS THE CONTROL FROM BEING A HANG. Without the walk's
// cursor-progress guard this fake would feed it forever, so the mutation removing that
// guard would be "caught" by the package timeout rather than by an assertion — naming no
// test and proving nothing.
const activePatienceLimit = 12

type cursorIgnoringClient struct{ calls int }

func (c *cursorIgnoringClient) Query(_ context.Context, _, _, _ string, vars map[string]any, out any) error {
	c.calls++
	if c.calls > activePatienceLimit {
		return errors.New("this fake has served the same page too many times")
	}
	pageSize, _ := vars["pageSize"].(int)
	resp := out.(*struct {
		AssertedDeviceStates []struct {
			Id         string  `json:"id"`
			ExternalId *string `json:"externalId"`
			SessionId  string  `json:"sessionId"`
		} `json:"assertedDeviceStates"`
	})
	for id := 1; id <= pageSize; id++ {
		ext := fmt.Sprintf("group/node-%04d", id)
		resp.AssertedDeviceStates = append(resp.AssertedDeviceStates, struct {
			Id         string  `json:"id"`
			ExternalId *string `json:"externalId"`
			SessionId  string  `json:"sessionId"`
		}{Id: strconv.Itoa(id), ExternalId: &ext, SessionId: "1700000000000000001"})
	}
	return nil
}
