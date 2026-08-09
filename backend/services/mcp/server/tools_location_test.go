// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A full position round-trip: the device/window criteria are forwarded, and every
// field of a fix survives the mapping. The fixture values are distinct, non-round
// and none is a multiple or negation of another, so a swap between any two fields
// (an accuracy that carries the speed, say) fails here — six near-identical Float
// fields is exactly the shape where that goes unnoticed.
func TestQueryLocations(t *testing.T) {
	tools, captured, done := toolsCapturing(t, `{"data":{"locationEvents":{"results":[{"deviceToken":"d1",`+
		`"latitude":33.749,"longitude":-84.388,"elevation":320.5,"accuracy":4.2,"speed":1.75,"heading":271.5,`+
		`"occurredTime":"2026-08-09T14:32:17Z"}],"pagination":{"totalRecords":1}}}}`)
	defer done()

	_, out, err := tools.QueryLocations(context.Background(), authedReq("tok"),
		QueryLocationsInput{DeviceToken: "d1", StartTime: "2026-08-09T00:00:00Z", PageSize: 1})
	if err != nil {
		t.Fatalf("QueryLocations: %v", err)
	}
	if len(out.Locations) != 1 || out.TotalRecords != 1 {
		t.Fatalf("unexpected result: %+v", out)
	}
	got := out.Locations[0]
	for _, c := range []struct {
		name string
		got  *float64
		want float64
	}{
		{"latitude", got.Latitude, 33.749},
		{"longitude", got.Longitude, -84.388},
		{"elevation", got.Elevation, 320.5},
		{"accuracy", got.Accuracy, 4.2},
		{"speed", got.Speed, 1.75},
		{"heading", got.Heading, 271.5},
	} {
		if c.got == nil {
			t.Errorf("%s came back nil from a reported value", c.name)
			continue
		}
		if *c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, *c.got, c.want)
		}
	}
	if got.DeviceToken != "d1" || got.OccurredTime != "2026-08-09T14:32:17Z" {
		t.Errorf("a position without its device and time is meaningless: %+v", got)
	}

	crit := (*captured)["variables"].(map[string]any)["criteria"].(map[string]any)
	if crit["deviceToken"] != "d1" || crit["startTime"] != "2026-08-09T00:00:00Z" {
		t.Errorf("criteria not forwarded: %v", crit)
	}
	if crit["pageSize"].(float64) != 1 {
		t.Errorf("pageSize not forwarded (pageSize 1 is how an agent asks 'where is it now'): %v", crit)
	}
	// An unset optional filter is ABSENT, not sent as "" — a blank window would match
	// nothing and silently report a device as having never moved.
	if _, present := crit["endTime"]; present {
		t.Errorf("an unset endTime must be absent from the criteria: %v", crit)
	}
}

// A field the receiver did not report stays nil, never 0. A zero speed claims a
// stationary device and a zero heading claims due north — a wrong reading an agent
// cannot tell apart from a real one.
func TestQueryLocations_UnreportedFieldsStayNil(t *testing.T) {
	tools, done := toolsAgainst(t, `{"data":{"locationEvents":{"results":[{"deviceToken":"d1",`+
		`"latitude":33.749,"longitude":-84.388,"occurredTime":"2026-08-09T14:32:17Z"}],`+
		`"pagination":{"totalRecords":1}}}}`)
	defer done()

	_, out, err := tools.QueryLocations(context.Background(), authedReq("tok"), QueryLocationsInput{DeviceToken: "d1"})
	if err != nil {
		t.Fatalf("QueryLocations: %v", err)
	}
	got := out.Locations[0]
	if got.Speed != nil {
		t.Errorf("an unreported speed must stay nil, not %v", *got.Speed)
	}
	if got.Heading != nil {
		t.Errorf("an unreported heading must stay nil, not %v", *got.Heading)
	}
	if got.Elevation != nil || got.Accuracy != nil {
		t.Errorf("unreported elevation/accuracy must stay nil: %+v", got)
	}
	if got.Latitude == nil || *got.Latitude != 33.749 {
		t.Errorf("the reported position must survive the missing fields: %+v", got)
	}
}

func TestQueryLocations_RequiresDeviceToken(t *testing.T) {
	tools := NewTools(NewGraphQLClient())
	if _, _, err := tools.QueryLocations(context.Background(), authedReq("tok"), QueryLocationsInput{}); err == nil {
		t.Errorf("missing deviceToken should error before any call")
	}
}

// No verified token on the request → refused before anything is dialled. This is the
// fail-closed half of the anti-confused-deputy design: with no caller credential
// there is nothing to act with, and the server has no credential of its own to fall
// back on.
func TestQueryLocations_Unauthenticated(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"data":{"locationEvents":{"results":[],"pagination":{"totalRecords":0}}}}`))
	}))
	defer ts.Close()

	tools := NewTools(testClient(ts.URL))
	if _, _, err := tools.QueryLocations(context.Background(), &mcp.CallToolRequest{},
		QueryLocationsInput{DeviceToken: "d1"}); err == nil {
		t.Errorf("a request with no verified token must fail closed")
	}
	if called {
		t.Errorf("an unauthenticated tool call must never reach event-management")
	}
}

// 🔴 The confused-deputy red line, asserted rather than assumed: the ONLY credential
// on the downstream request is the caller's own token, verbatim. Any service token,
// API key or extra credential this server introduced would show up as a second
// authorization-bearing header here.
func TestQueryLocations_CarriesOnlyTheCallerToken(t *testing.T) {
	var headers http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		_, _ = w.Write([]byte(`{"data":{"locationEvents":{"results":[],"pagination":{"totalRecords":0}}}}`))
	}))
	defer ts.Close()

	tools := NewTools(testClient(ts.URL))
	if _, _, err := tools.QueryLocations(context.Background(), authedReq("caller-token-abc"),
		QueryLocationsInput{DeviceToken: "d1"}); err != nil {
		t.Fatalf("QueryLocations: %v", err)
	}

	if got := headers.Get("Authorization"); got != "Bearer caller-token-abc" {
		t.Errorf("Authorization = %q, want the caller's own token", got)
	}
	for name := range headers {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "content-type" {
			continue
		}
		if strings.Contains(lower, "auth") || strings.Contains(lower, "token") ||
			strings.Contains(lower, "key") || strings.Contains(lower, "dc-service") {
			t.Errorf("a credential the caller did not supply was added: %s: %v", name, headers[name])
		}
	}
}

// A caller who lacks the position authority is refused, and the refusal REACHES the
// agent. The gate itself lives on event-management's locationEvents resolver (it is
// the caller's own token that runs there), so what this pins is that the tool does
// not swallow the denial into an empty-but-successful answer — "no positions" and
// "you may not see positions" must not look the same to a model.
func TestQueryLocations_AuthorityRefusalSurfaces(t *testing.T) {
	tools, done := toolsAgainst(t, `{"errors":[{"message":"forbidden: missing required authority"}]}`)
	defer done()

	_, out, err := tools.QueryLocations(context.Background(), authedReq("tok"), QueryLocationsInput{DeviceToken: "d1"})
	if err == nil {
		t.Fatal("a refused position read must be a failed tool call, not an empty success")
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("the refusal should reach the agent intact: %v", err)
	}
	if len(out.Locations) != 0 {
		t.Errorf("a refused read must yield no position data: %+v", out.Locations)
	}
}

// The tool is REGISTERED and reachable over a real MCP session: a client connected to
// the server built by registerTools finds query_locations in tools/list, with the
// input schema that lets an agent call it. Constructing Tools directly (as every test
// above does) proves nothing about whether the catalog exposes it.
func TestQueryLocationsToolIsRegistered(t *testing.T) {
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

	var found *mcp.Tool
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Name == "query_locations" {
			found = tool
		}
	}
	if found == nil {
		t.Fatalf("query_locations is not in the catalog; got %v", names)
	}
	if found.Description == "" {
		t.Error("a tool with no description is unusable by a model")
	}
	// InputSchema crosses the wire as untyped JSON, so read it the way a client would.
	schema, ok := found.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("query_locations declares no input schema object, got %T", found.InputSchema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("input schema declares no properties: %v", schema)
	}
	if _, ok := props["deviceToken"]; !ok {
		t.Errorf("query_locations must declare deviceToken; schema has %v", props)
	}
	// The neighbouring tools are still there — a registration change must add, not
	// displace. This also proves the listing is real rather than a lucky empty match.
	for _, want := range []string{"list_devices", "query_measurements", "list_alarms"} {
		if !containsName(names, want) {
			t.Errorf("%s vanished from the catalog: %v", want, names)
		}
	}
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
