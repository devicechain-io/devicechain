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
	err := testClient(ts.URL).Query(context.Background(), "device-management", "the-token",
		"query { ping }", map[string]any{"x": 1}, &out)
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

	err := testClient(ts.URL).Query(context.Background(), "device-management", "t", "q", nil, nil)
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

	err := testClient(ts.URL).Query(context.Background(), "device-management", "t", "q", nil, nil)
	if err == nil {
		t.Fatal("expected an error from the 401 status")
	}
}
