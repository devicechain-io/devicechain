// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSchemaCapabilitiesRequireAuthentication(t *testing.T) {
	t.Setenv(EnvGraphQLDevTools, "false")
	h := NewHttpHandler(MustParseSchema(`type Query { hello: String! }`, &introspectRoot{}), nil, nil)
	for _, bearer := range []string{"", "Bearer invalid"} {
		r := httptest.NewRequest(http.MethodOptions, "/graphql", nil)
		r.Header.Set("Accept", schemaCapabilityMediaType)
		r.Header.Set("Authorization", bearer)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "type Query") {
			t.Fatalf("unauthenticated schema disclosure: status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestSchemaCapabilityContract(t *testing.T) {
	t.Setenv(EnvGraphQLDevTools, "false")
	const sdl = `type Query { hello: String! }`
	h := NewHttpHandler(MustParseSchema(sdl, &introspectRoot{}), nil, nil)
	r := httptest.NewRequest(http.MethodOptions, "/graphql", nil)
	r.Header.Set("Accept", schemaCapabilityMediaType)
	w := httptest.NewRecorder()
	// Exercise the post-authentication dispatch separately from the bearer gate.
	h.serveExec(w, r)
	var manifest struct {
		Format int    `json:"format"`
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Format != 1 || manifest.Schema != sdl || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("wrong contract: %+v headers=%v", manifest, w.Header())
	}
	r = httptest.NewRequest(http.MethodOptions, "/graphql", strings.NewReader("{}"))
	r.Header.Set("Accept", schemaCapabilityMediaType)
	w = httptest.NewRecorder()
	h.serveExec(w, r)
	if w.Code != http.StatusBadRequest || w.Header().Get("Connection") != "close" {
		t.Fatalf("discovery accepted a body: status=%d headers=%v", w.Code, w.Header())
	}
}
