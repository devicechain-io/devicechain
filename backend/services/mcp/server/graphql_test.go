// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// testClient points a GraphQLClient at a single test server for every area.
func testClient(url string) *GraphQLClient {
	c := NewGraphQLClient()
	c.baseURL = func(string) string { return url }
	return c
}

// The client forwards the caller's token as a bearer and unmarshals the data field.
func TestGraphQLClient_ForwardsTokenAndUnmarshalsData(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"data":{"ping":"pong"}}`))
	}))
	defer ts.Close()

	var out struct {
		Ping string `json:"ping"`
	}
	err := testClient(ts.URL).Query(context.Background(),
		document{area: "device-management", text: "query { ping }"}, "the-token", map[string]any{"x": 1}, &out)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if gotAuth != "Bearer the-token" {
		t.Errorf("Authorization = %q, want Bearer the-token", gotAuth)
	}
	if gotBody["query"] != "query { ping }" {
		t.Errorf("query not sent: %v", gotBody)
	}
	if out.Ping != "pong" {
		t.Errorf("data not unmarshaled: %+v", out)
	}
}

// A GraphQL errors array surfaces as an error (never a partial success).
func TestGraphQLClient_GraphQLErrorSurfaces(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"forbidden"}]}`))
	}))
	defer ts.Close()

	err := testClient(ts.URL).Query(context.Background(), document{area: "device-management", text: "q"}, "t", nil, nil)
	if err == nil {
		t.Fatal("expected an error from the GraphQL errors array")
	}
}

// A non-2xx HTTP status is an error.
func TestGraphQLClient_HTTPErrorSurfaces(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer ts.Close()

	err := testClient(ts.URL).Query(context.Background(), document{area: "device-management", text: "q"}, "t", nil, nil)
	if err == nil {
		t.Fatal("expected an error from the 401 status")
	}
}

// Query sends a document to the area the document names. testClient throws the area away,
// so this test keeps it: if Query stopped reading doc.area, every device-state,
// event-management and command-delivery tool would be posted to device-management.
func TestGraphQLClient_RoutesToTheDocumentsArea(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer ts.Close()

	for _, doc := range []document{getDeviceStateQuery, queryMeasurementsQuery, listCommandsQuery, listAlarmsQuery} {
		var gotArea string
		c := NewGraphQLClient()
		c.baseURL = func(area string) string { gotArea = area; return ts.URL }
		if err := c.Query(context.Background(), doc, "t", nil, nil); err != nil {
			t.Fatalf("Query: %v", err)
		}
		if gotArea != doc.area {
			t.Errorf("%s: sent to area %q, want %q", operationName(doc), gotArea, doc.area)
		}
	}
	// The fixture must span areas, or a constant area would pass.
	if getDeviceStateQuery.area != "device-state" || listCommandsQuery.area != "command-delivery" {
		t.Fatalf("fixture documents changed area; pick documents that are not all device-management")
	}
}
