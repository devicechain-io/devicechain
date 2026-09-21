// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	emmodel "github.com/devicechain-io/dc-event-management/model"
	emtest "github.com/devicechain-io/dc-event-management/test"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/stretchr/testify/mock"
)

// sweepHarness wires a sweep against a stub mint endpoint and a stub
// device-management whose existingEntityRefs echoes back the refs named in `exists`
// (or 500s when `outage`), plus a MockApi.
func sweepHarness(t *testing.T, api *emtest.MockApi, exists map[string]bool, outage bool) *AnchorReconciliationSweep {
	t.Helper()
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc", ExpiresAt: 1 << 40})
	}))
	t.Cleanup(mint.Close)

	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if outage {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var body struct {
			Variables struct {
				Refs []struct {
					Type  string `json:"type"`
					Token string `json:"token"`
				} `json:"refs"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out := []map[string]string{}
		for _, ref := range body.Variables.Refs {
			if exists[ref.Type+"|"+ref.Token] {
				out = append(out, map[string]string{"type": ref.Type, "token": ref.Token})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"existingEntityRefs": out}})
	}))
	t.Cleanup(dm.Close)

	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port)}, "shh", "event-management", []string{string(auth.DeviceRead)})
	return &AnchorReconciliationSweep{Api: api, client: client, dmURL: dm.URL}
}

// The sweep deletes anchors for refs that no longer resolve, and leaves refs that
// still exist.
func TestSweep_DeletesOrphansOnly(t *testing.T) {
	api := new(emtest.MockApi)
	api.Mock.On("DistinctAnchorTenants").Return([]string{"acme"}, nil)
	api.Mock.On("DistinctAnchorTargetsAfter", "", "").Return([]emmodel.AnchorRef{
		{Type: "customer", Token: "cust-999"}, // orphan
	}, nil)
	api.Mock.On("DistinctAnchorTargetsAfter", "customer", "cust-999").Return([]emmodel.AnchorRef{}, nil)
	api.Mock.On("DistinctAnchorDeviceTokensAfter", "").Return([]emmodel.AnchorRef{
		{Type: "device", Token: "device-1"}, // exists
	}, nil)
	api.Mock.On("DistinctAnchorDeviceTokensAfter", "device-1").Return([]emmodel.AnchorRef{}, nil)
	api.Mock.On("DeleteAnchorsForEntity", "customer", "cust-999", mock.Anything).Return(3, nil)

	sweep := sweepHarness(t, api, map[string]bool{"device|device-1": true}, false)
	sweep.runOnce(context.Background())

	api.Mock.AssertCalled(t, "DeleteAnchorsForEntity", "customer", "cust-999", mock.Anything)
	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", "device", "device-1", mock.Anything)
}

// FAIL SAFE: if device-management can't be reached, the sweep must delete NOTHING —
// treating a reachable-but-down owner as "everything absent" would nuke the anchors.
func TestSweep_FailsSafeOnResolveError(t *testing.T) {
	api := new(emtest.MockApi)
	api.Mock.On("DistinctAnchorTenants").Return([]string{"acme"}, nil)
	api.Mock.On("DistinctAnchorTargetsAfter", "", "").Return([]emmodel.AnchorRef{
		{Type: "customer", Token: "cust-999"},
	}, nil)

	sweep := sweepHarness(t, api, nil, true /* outage */)
	sweep.runOnce(context.Background())

	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", mock.Anything, mock.Anything, mock.Anything)
}

// mintOnly builds a mint stub + a config pointing svcclient at it.
func mintOnly(t *testing.T) config.UserManagementConfiguration {
	t.Helper()
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc", ExpiresAt: 1 << 40})
	}))
	t.Cleanup(mint.Close)
	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return config.UserManagementConfiguration{Hostname: host, Port: uint32(port)}
}

// TestSweep_WalksATenantInPagesAndAsksAboutOnePageAtATime replaces a test that asserted
// the OPPOSITE ARRANGEMENT of the same concern, and the swap is the point of the change.
//
// The old test built one oversized ref slice, handed the whole thing to the sweep, and
// checked that the outbound existence query was sliced into two. It passed, and what it
// proved was that the REQUEST was bounded — while the read that produced the slice it was
// slicing had loaded the entire tenant into memory first. The cap was on the cheap half.
//
// What is asserted now is that the sweep never has more than a page in hand: each page
// read is followed by the existence query for exactly that page, and the next page is not
// read until the previous one has been reconciled and dropped.
//
// 🔑 THE CURSOR IS ASSERTED BY THE MOCK, NOT BY AN ASSERTION. Every expectation below is
// registered against a specific cursor value, so a sweep that failed to advance its
// cursor — the bug that turns this loop into an infinite one — calls with an argument no
// expectation matches and the mock fails the test. A stub that ignored the cursor and
// replayed one canned page could not produce that failure, which is why the mock takes it.
func TestSweep_WalksATenantInPagesAndAsksAboutOnePageAtATime(t *testing.T) {
	var perRequest []int
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables struct {
				Refs []struct {
					Type  string `json:"type"`
					Token string `json:"token"`
				} `json:"refs"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		perRequest = append(perRequest, len(body.Variables.Refs))
		out := make([]map[string]string, 0, len(body.Variables.Refs))
		for _, ref := range body.Variables.Refs {
			out = append(out, map[string]string{"type": ref.Type, "token": ref.Token}) // all exist
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"existingEntityRefs": out}})
	}))
	defer dm.Close()

	page := func(from, to int) []emmodel.AnchorRef {
		refs := make([]emmodel.AnchorRef, 0, to-from+1)
		for i := from; i <= to; i++ {
			refs = append(refs, emmodel.AnchorRef{Type: "customer", Token: fmt.Sprintf("cust-%03d", i)})
		}
		return refs
	}

	api := new(emtest.MockApi)
	api.Mock.On("DistinctAnchorTenants").Return([]string{"acme"}, nil)
	// Three reads: two pages of three, then the empty page that ends the source.
	api.Mock.On("DistinctAnchorTargetsAfter", "", "").Return(page(1, 3), nil)
	api.Mock.On("DistinctAnchorTargetsAfter", "customer", "cust-003").Return(page(4, 6), nil)
	api.Mock.On("DistinctAnchorTargetsAfter", "customer", "cust-006").Return([]emmodel.AnchorRef{}, nil)
	api.Mock.On("DistinctAnchorDeviceTokensAfter", "").Return([]emmodel.AnchorRef{}, nil)

	client := svcclient.New(mintOnly(t), "shh", "event-management", []string{string(auth.DeviceRead)})
	sweep := &AnchorReconciliationSweep{Api: api, client: client, dmURL: dm.URL}
	if err := sweep.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	// One existence query per non-empty page, each carrying that page and no more. The
	// SIZES are asserted rather than just the count: two requests of three refs and one
	// request of six are both "the refs were resolved", and only the first is paging.
	if len(perRequest) != 2 || perRequest[0] != 3 || perRequest[1] != 3 {
		t.Fatalf("expected one existence query per page of 3, got request sizes %v", perRequest)
	}
	api.Mock.AssertExpectations(t)
	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", mock.Anything, mock.Anything, mock.Anything) // all exist
}

// TestSweep_ReconciledPagesSurviveALaterPageFailing pins the one behaviour the paged
// shape changed, so that it is a decision on the record rather than a side effect.
//
// The sweep still refuses to delete on an unconfirmed absence — that is what makes it
// safe — but it no longer discards the pages it already finished when a later one cannot
// be resolved. Every deletion it made was justified by a successful existence check on
// that ref, so none of them rests on the answer that never arrived, and the alternative
// would make a large tenant unsweepable by one flaky call.
func TestSweep_ReconciledPagesSurviveALaterPageFailing(t *testing.T) {
	var requests int32
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first page resolves; everything after it is an outage.
		if atomic.AddInt32(&requests, 1) > 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"existingEntityRefs": []any{}}})
	}))
	defer dm.Close()

	api := new(emtest.MockApi)
	api.Mock.On("DistinctAnchorTenants").Return([]string{"acme"}, nil)
	api.Mock.On("DistinctAnchorTargetsAfter", "", "").Return([]emmodel.AnchorRef{
		{Type: "customer", Token: "cust-001"},
	}, nil)
	api.Mock.On("DistinctAnchorTargetsAfter", "customer", "cust-001").Return([]emmodel.AnchorRef{
		{Type: "customer", Token: "cust-002"},
	}, nil)
	api.Mock.On("DeleteAnchorsForEntity", "customer", "cust-001", mock.Anything).Return(1, nil)

	client := svcclient.New(mintOnly(t), "shh", "event-management", []string{string(auth.DeviceRead)})
	sweep := &AnchorReconciliationSweep{Api: api, client: client, dmURL: dm.URL}
	sweep.runOnce(context.Background())

	// The first page's orphan was deleted and stays deleted...
	api.Mock.AssertCalled(t, "DeleteAnchorsForEntity", "customer", "cust-001", mock.Anything)
	// ...and the page that could not be resolved deleted nothing, which is the half that
	// would be a data-loss bug if it ever stopped holding.
	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", "customer", "cust-002", mock.Anything)
}

// TestSweep_RefusesAPageThatDidNotAdvanceTheCursorInsteadOfSpinning covers the failure
// the paged walk introduced, which the old load-everything shape could not have.
//
// 🔴 THE TEST IS RUN UNDER A DEADLINE, AND THAT IS NOT BELT AND BRACES. The defect being
// pinned is a loop that never ends, and a test for it that simply calls the function
// would not FAIL on a regression — it would hang, take the whole package's timeout with
// it, and report as a timeout somewhere else entirely. That is how this was found: a
// mutant that stopped the cursor advancing produced no failure at all, just a test run
// that never came back. So the call happens on its own goroutine and the assertion is
// against the deadline.
func TestSweep_RefusesAPageThatDidNotAdvanceTheCursorInsteadOfSpinning(t *testing.T) {
	stuck := []emmodel.AnchorRef{{Type: "customer", Token: "cust-001"}}

	api := new(emtest.MockApi)
	api.Mock.On("DistinctAnchorTenants").Return([]string{"acme"}, nil)
	// The same page whatever it is asked for: a read whose ordering and keyset filter
	// disagree looks exactly like this from here.
	api.Mock.On("DistinctAnchorTargetsAfter", "", "").Return(stuck, nil)
	api.Mock.On("DistinctAnchorTargetsAfter", "customer", "cust-001").Return(stuck, nil)

	sweep := sweepHarness(t, api, map[string]bool{"customer|cust-001": true}, false)

	done := make(chan error, 1)
	go func() { done <- sweep.runOnce(context.Background()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a read that never advanced its cursor was accepted; the sweep must refuse it")
		}
		if !strings.Contains(err.Error(), "cust-001") {
			t.Errorf("the refusal should name the key that repeated, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep did not return: a page that failed to advance the cursor is " +
			"being re-read forever")
	}
}

// A tenant with no anchors resolves nothing and deletes nothing.
func TestSweep_EmptyTenantNoop(t *testing.T) {
	api := new(emtest.MockApi)
	api.Mock.On("DistinctAnchorTenants").Return([]string{"acme"}, nil)
	api.Mock.On("DistinctAnchorTargetsAfter", "", "").Return([]emmodel.AnchorRef{}, nil)
	api.Mock.On("DistinctAnchorDeviceTokensAfter", "").Return([]emmodel.AnchorRef{}, nil)

	sweep := sweepHarness(t, api, nil, false)
	sweep.runOnce(context.Background())

	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", mock.Anything, mock.Anything, mock.Anything)
}
