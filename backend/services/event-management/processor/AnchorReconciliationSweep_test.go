// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

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
					Type string `json:"type"`
					Id   string `json:"id"`
				} `json:"refs"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out := []map[string]string{}
		for _, ref := range body.Variables.Refs {
			if exists[ref.Type+"|"+ref.Id] {
				out = append(out, map[string]string{"type": ref.Type, "id": ref.Id})
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
	api.Mock.On("DistinctAnchorRefs").Return([]emmodel.AnchorRef{
		{Type: "device", Id: 1},     // exists
		{Type: "customer", Id: 999}, // orphan
	}, nil)
	api.Mock.On("DeleteAnchorsForEntity", "customer", uint(999)).Return(3, nil)

	sweep := sweepHarness(t, api, map[string]bool{"device|1": true}, false)
	sweep.runOnce(context.Background())

	api.Mock.AssertCalled(t, "DeleteAnchorsForEntity", "customer", uint(999))
	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", "device", uint(1))
}

// FAIL SAFE: if device-management can't be reached, the sweep must delete NOTHING —
// treating a reachable-but-down owner as "everything absent" would nuke the anchors.
func TestSweep_FailsSafeOnResolveError(t *testing.T) {
	api := new(emtest.MockApi)
	api.Mock.On("DistinctAnchorTenants").Return([]string{"acme"}, nil)
	api.Mock.On("DistinctAnchorRefs").Return([]emmodel.AnchorRef{
		{Type: "customer", Id: 999},
	}, nil)

	sweep := sweepHarness(t, api, nil, true /* outage */)
	sweep.runOnce(context.Background())

	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", mock.Anything, mock.Anything)
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

// SAFETY: an unparseable id in device-management's answer must fail the resolve
// (skip the tenant), never bend toward deleting the "missing" ref's anchors.
func TestSweep_UnparseableIdFailsSafe(t *testing.T) {
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"existingEntityRefs": []map[string]string{{"type": "customer", "id": "not-a-number"}},
		}})
	}))
	defer dm.Close()

	api := new(emtest.MockApi)
	api.Mock.On("DistinctAnchorTenants").Return([]string{"acme"}, nil)
	api.Mock.On("DistinctAnchorRefs").Return([]emmodel.AnchorRef{{Type: "customer", Id: 999}}, nil)

	client := svcclient.New(mintOnly(t), "shh", "event-management", []string{string(auth.DeviceRead)})
	sweep := &AnchorReconciliationSweep{Api: api, client: client, dmURL: dm.URL}
	sweep.runOnce(context.Background())

	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", mock.Anything, mock.Anything)
}

// A ref set larger than one chunk is resolved across multiple requests (so a big
// tenant does not overflow the response cap and silently skip forever).
func TestSweep_ChunksLargeRefSet(t *testing.T) {
	var requests int32
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		var body struct {
			Variables struct {
				Refs []struct {
					Type string `json:"type"`
					Id   string `json:"id"`
				} `json:"refs"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out := make([]map[string]string, 0, len(body.Variables.Refs))
		for _, ref := range body.Variables.Refs {
			out = append(out, map[string]string{"type": ref.Type, "id": ref.Id}) // all exist
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"existingEntityRefs": out}})
	}))
	defer dm.Close()

	refs := make([]emmodel.AnchorRef, 0, maxRefsPerResolve+50)
	for i := 1; i <= maxRefsPerResolve+50; i++ {
		refs = append(refs, emmodel.AnchorRef{Type: "device", Id: uint(i)})
	}
	api := new(emtest.MockApi)
	api.Mock.On("DistinctAnchorTenants").Return([]string{"acme"}, nil)
	api.Mock.On("DistinctAnchorRefs").Return(refs, nil)

	client := svcclient.New(mintOnly(t), "shh", "event-management", []string{string(auth.DeviceRead)})
	sweep := &AnchorReconciliationSweep{Api: api, client: client, dmURL: dm.URL}
	sweep.runOnce(context.Background())

	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected 2 chunked requests for %d refs, got %d", maxRefsPerResolve+50, got)
	}
	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", mock.Anything, mock.Anything) // all exist
}

// A tenant with no anchors resolves nothing and deletes nothing.
func TestSweep_EmptyTenantNoop(t *testing.T) {
	api := new(emtest.MockApi)
	api.Mock.On("DistinctAnchorTenants").Return([]string{"acme"}, nil)
	api.Mock.On("DistinctAnchorRefs").Return([]emmodel.AnchorRef{}, nil)

	sweep := sweepHarness(t, api, nil, false)
	sweep.runOnce(context.Background())

	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", mock.Anything, mock.Anything)
}
