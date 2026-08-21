// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"sort"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 🔴 THE GATE FOR A WHOLE DEFECT CLASS, NOT ONE BUG.
//
// query_locations could not be called by ANYBODY — including a tenant superuser —
// for the whole of its existence, because the authority its resolver requires
// (location:read) was not admitted by any OAuth scope this AS grants. Nothing
// detected that, and nothing could have: the tool lives here, the scope ceilings live
// in core/auth, the resolver that refuses lives in a third module, and the only thing
// joining them was a sentence in a comment.
//
// This test joins them. Every tool the catalog exposes must name the authority its
// downstream resolver gates on, and that authority must be reachable through some
// scope the AS will grant. A new read tool over dashboards, notifications, connectors
// or the audit journal — every one of which is gated on an authority NO scope admits
// today — now fails here instead of shipping as a tool that refuses every caller.
//
// 🔴 The map is hand-written on purpose and cannot be derived: the gate is an
// auth.Authorize call in another module's resolver, not anything this module can
// reach. So adding a tool means looking up what its query is gated on and writing it
// down — which is exactly the step that was skipped. Keep the resolver reference
// beside each entry so the next person can re-check it in one grep.
var toolAuthorities = map[string][]coreauth.Authority{
	// device-management/graphql/queries_devices.go — Devices, DevicesByToken
	"list_devices": {coreauth.DeviceRead},
	"get_device":   {coreauth.DeviceRead},
	// device-state/graphql/queries.go — DeviceStatesByDeviceToken, LatestMeasurements
	"get_device_state":        {coreauth.StateRead},
	"get_latest_measurements": {coreauth.StateRead},
	// device-management: DevicesByToken + DeviceCommandVocabulary
	// (queries_command_enqueue.go), both device:read.
	"get_device_capabilities": {coreauth.DeviceRead},
	// event-management/graphql/queries_events.go — MeasurementEvents, BucketedMeasurements
	"query_measurements":     {coreauth.EventRead},
	"aggregate_measurements": {coreauth.EventRead},
	// event-management/graphql/queries_events.go — LocationEvents. Gated on
	// location:read ALONE, deliberately not event:read.
	"query_locations": {coreauth.LocationRead},
	// device-management/graphql/queries_alarm_state.go — Alarms, AlarmsByToken
	"list_alarms": {coreauth.AlarmRead},
	"get_alarm":   {coreauth.AlarmRead},
	// command-delivery/graphql/queries.go — Commands
	"list_commands": {coreauth.CommandRead},
}

// registeredToolNames asks the real server what it exposes, over a real MCP session.
// Reading registerTools' source, or the map above, would prove nothing about the
// catalog a client actually sees.
func registeredToolNames(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	s := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	registerTools(s, NewTools(NewGraphQLClient()))

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

// scopeReachableAuthorities is the union of every ceiling the AS will grant — the
// set an OAuth token can possibly carry, whatever scopes are requested and however
// privileged the subject.
func scopeReachableAuthorities(t *testing.T) map[string]string {
	t.Helper()
	reachable := map[string]string{}
	for _, scope := range coreauth.SupportedScopes {
		allow, ok := coreauth.ScopeAllowance(scope)
		if !ok {
			t.Fatalf("supported scope %q has no allowance defined", scope)
		}
		for _, a := range allow {
			reachable[a] = scope
		}
	}
	if len(reachable) == 0 {
		t.Fatal("no authority is reachable through any scope; every tool would refuse")
	}
	return reachable
}

// The catalog and the authority map describe the same set of tools. This is the half
// that makes the coverage assertion below binding: without it, a newly registered
// tool would simply be absent from the map and sail past every check.
func TestEveryRegisteredToolDeclaresItsAuthority(t *testing.T) {
	names := registeredToolNames(t)
	if len(names) == 0 {
		t.Fatal("the catalog is empty, so this test is measuring nothing")
	}
	for _, name := range names {
		// 🔴 NON-EMPTY, NOT MERELY PRESENT. A bare presence check is satisfied by
		// `"list_dashboards": {}` and by `"list_dashboards": nil`, and the reachability
		// test below then iterates zero authorities and passes — so a tool that is
		// refused for everyone reads as covered. The empty entry is the dangerous one
		// because it takes no wrong belief to write: it is the natural way to say "this
		// tool needs no special authority", which is never true of a tool that reaches
		// a gated resolver.
		if a, ok := toolAuthorities[name]; !ok || len(a) == 0 {
			t.Errorf("tool %q is registered but names no required authority. Find the "+
				"auth.Authorize call on the resolver its query hits and add it to "+
				"toolAuthorities — a tool whose authority no scope admits refuses every "+
				"caller, silently, forever", name)
		}
	}
	for name := range toolAuthorities {
		if !containsName(names, name) {
			t.Errorf("toolAuthorities names %q, which is not in the catalog %v — a stale "+
				"entry keeps the coverage check green for a tool that no longer exists",
				name, names)
		}
	}
}

// 🔴 THE ASSERTION THE DEFECT WOULD HAVE FAILED. Every authority any tool needs must
// be admitted by SOME scope this AS grants, or that tool is unreachable for every
// caller on every install.
func TestEveryToolAuthorityIsReachableThroughSomeScope(t *testing.T) {
	reachable := scopeReachableAuthorities(t)
	for _, name := range registeredToolNames(t) {
		for _, needed := range toolAuthorities[name] {
			if _, ok := reachable[string(needed)]; !ok {
				t.Errorf("tool %q requires %q, which NO supported scope admits (%v). Every "+
					"call to it will be refused — for a tenant superuser too, since the "+
					"token endpoint caps \"*\" to the scope allowance rather than expanding "+
					"it. Either give the authority a scope or do not ship the tool",
					name, needed, coreauth.SupportedScopes)
			}
		}
	}
}

// Reachable is not the same as bundled, and this is where that distinction is
// pinned from the consumer's side: position must require a scope of its own, so a
// resource owner can authorize an agent to observe a fleet while withholding where
// it — or the person driving it — has been. The consent screen renders the raw scope
// strings, so a scope that silently covered position could not be told apart from
// one that did not.
func TestPositionIsNotBundledIntoTheGeneralReadScope(t *testing.T) {
	readOnly, ok := coreauth.ScopeAllowance(coreauth.ScopeReadOnly)
	if !ok {
		t.Fatal("read-only is not a defined scope")
	}
	for _, a := range readOnly {
		if a == string(coreauth.LocationRead) {
			t.Errorf("the read-only ceiling %v admits %q, so a client authorized for "+
				"read-only alone would reach query_locations and the resource owner would "+
				"never have been shown the choice", readOnly, coreauth.LocationRead)
		}
	}
	// ...and the tool is not merely unreachable instead: some scope must admit it.
	if _, ok := scopeReachableAuthorities(t)[string(coreauth.LocationRead)]; !ok {
		t.Errorf("no scope admits %q, so query_locations refuses every caller",
			coreauth.LocationRead)
	}
}

// The middleware requires ScopeReadOnly on every request, so a scope that only ever
// arrives ALONGSIDE it (like `location`) must not be the one a client presents by
// itself expecting the server to answer. Pinned so the day a write scope appears,
// whoever adds it sees that this requirement is the floor for reaching ANY tool.
func TestReadOnlyIsTheFloorForReachingTheServer(t *testing.T) {
	if !coreauth.IsSupportedScope(coreauth.ScopeReadOnly) {
		t.Fatalf("%q is not a supported scope", coreauth.ScopeReadOnly)
	}
	if !coreauth.IsSupportedScope(coreauth.ScopeLocation) {
		t.Fatalf("%q is not a supported scope, so query_locations can never be authorized",
			coreauth.ScopeLocation)
	}
	// Every tool except the position one is satisfied by read-only on its own — which
	// is what makes requiring it as the floor correct rather than arbitrary.
	readOnly, _ := coreauth.ScopeAllowance(coreauth.ScopeReadOnly)
	inReadOnly := map[string]bool{}
	for _, a := range readOnly {
		inReadOnly[a] = true
	}
	for _, name := range registeredToolNames(t) {
		if name == "query_locations" {
			continue
		}
		for _, needed := range toolAuthorities[name] {
			if !inReadOnly[string(needed)] {
				t.Errorf("tool %q needs %q, which read-only does not admit. Either it needs "+
					"its own scope like query_locations does — in which case exclude it here "+
					"deliberately — or read-only's ceiling is wrong", name, needed)
			}
		}
	}
}
