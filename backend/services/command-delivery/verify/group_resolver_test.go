// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newGroupResolverAgainst wires a GroupResolver to a stub device-management answering
// with the supplied raw JSON body, and captures the variables it was sent.
func newGroupResolverAgainst(t *testing.T, body string) (*GroupResolver, *map[string]any) {
	t.Helper()
	captured := &map[string]any{}
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		*captured = request.Variables
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(dm.Close)
	return NewGroupResolver(newSvcClient(t), dm.URL), captured
}

// TestGroupPageIsRelayedWithItsCursorAndVersion pins the two fields a fleet write cannot
// lose: the cursor (without which the walk stops early and silently commands a fraction
// of the group) and the frozen version (without which the record cannot say what the
// group MEANT when it fired).
func TestGroupPageIsRelayedWithItsCursorAndVersion(t *testing.T) {
	resolver, _ := newGroupResolverAgainst(t, `{"data":{"resolveDeviceGroupTargets":{
	  "rejected":false,"code":null,"reason":null,
	  "deviceTokens":["pump-a","pump-b"],"nextCursor":"41","resolvedVersion":7
	}}}`)

	page, err := resolver.ResolveGroupTargets(batchCtx(), "pumps", nil, "", 1000)
	if err != nil {
		t.Fatalf("a well-formed page must not be an error: %v", err)
	}
	if page.Rejected {
		t.Fatalf("the page was not rejected, got %+v", page)
	}
	if len(page.DeviceTokens) != 2 {
		t.Fatalf("expected 2 members, got %+v", page.DeviceTokens)
	}
	if page.NextCursor != "41" {
		t.Fatalf("the cursor must survive or the walk stops early, got %q", page.NextCursor)
	}
	if page.ResolvedVersion == nil || *page.ResolvedVersion != 7 {
		t.Fatalf("the frozen version must survive, got %v", page.ResolvedVersion)
	}
}

// TestGroupRejectionIsAVerdictNotAnError guards the split this whole seam is built on: an
// unusable group is a DECIDED fact the caller relays to its client, while an unreachable
// device-management is an availability failure it fails closed on. Collapsing them would
// report a fixable authoring mistake as a platform outage.
func TestGroupRejectionIsAVerdictNotAnError(t *testing.T) {
	resolver, _ := newGroupResolverAgainst(t, `{"data":{"resolveDeviceGroupTargets":{
	  "rejected":true,"code":"GROUP_NOT_PUBLISHED","reason":"publish it first",
	  "deviceTokens":[],"nextCursor":null,"resolvedVersion":null
	}}}`)

	page, err := resolver.ResolveGroupTargets(batchCtx(), "pumps", nil, "", 1000)
	if err != nil {
		t.Fatalf("a rejection is a verdict, not an error: %v", err)
	}
	if !page.Rejected {
		t.Fatal("the rejection flag must survive the relay")
	}
	// The owner's code is relayed rather than re-derived. A caller that can only read the
	// prose cannot tell "not a device group" from "never published" without parsing it.
	if page.Code != "GROUP_NOT_PUBLISHED" {
		t.Fatalf("the owner's code must be relayed verbatim, got %q", page.Code)
	}
}

// TestAbsentGroupVerdictIsAnError is the fail-closed guard, and the fixture shape matters
// for the same reason it does in the batch validator: svcclient already handles non-200s
// and GraphQL error arrays, so only a 200 carrying no data reaches this code silently.
//
// Reading an absent `rejected` as false would treat a garbled response as a successfully
// resolved — and therefore EMPTY — group, which command-delivery would record as a fleet
// write that legitimately reached nobody.
func TestAbsentGroupVerdictIsAnError(t *testing.T) {
	for name, body := range map[string]string{
		"data absent":   `{}`,
		"field missing": `{"data":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			resolver, _ := newGroupResolverAgainst(t, body)

			page, err := resolver.ResolveGroupTargets(batchCtx(), "pumps", nil, "", 1000)
			if err == nil {
				t.Fatalf("a response carrying no verdict must be an ERROR; it was read as "+
					"a resolved group with %d members", len(page.DeviceTokens))
			}
		})
	}
}

// TestFirstPageSendsNullCursor pins how "start at the beginning" is expressed.
//
// The owner treats "" as equivalent to null today, so this is a contract test rather than
// a bug guard — null is what "no cursor" means, and pinning it here means a change to the
// owner's leniency cannot silently become our problem. The load-bearing half is the
// second assertion: a real cursor must travel unchanged, because a walk that keeps
// sending null re-commands the devices it already reached, every page, forever.
func TestFirstPageSendsNullCursor(t *testing.T) {
	resolver, captured := newGroupResolverAgainst(t, `{"data":{"resolveDeviceGroupTargets":{
	  "rejected":false,"deviceTokens":[],"nextCursor":null,"resolvedVersion":null
	}}}`)

	if _, err := resolver.ResolveGroupTargets(batchCtx(), "pumps", nil, "", 1000); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	cursor, present := (*captured)["afterId"]
	if !present {
		t.Fatal("afterId must be sent explicitly, not omitted")
	}
	if cursor != nil {
		t.Fatalf("the first page must send a null cursor, got %#v", cursor)
	}

	// The negative control: a real cursor must travel as itself, or the walk restarts
	// from the beginning every page and re-commands the devices it already reached.
	if _, err := resolver.ResolveGroupTargets(batchCtx(), "pumps", nil, "41", 1000); err != nil {
		t.Fatalf("resolve page 2: %v", err)
	}
	if got := (*captured)["afterId"]; got != "41" {
		t.Fatalf("a real cursor must be sent unchanged, got %#v", got)
	}
}
