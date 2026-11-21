// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package apitest lets HTTP fixtures advertise the real, current served schema.
package apitest

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// servedSchemas maps "<area>/<file>" to the real served schema on disk. The request
// path only ever selects a key; it never reaches the filesystem.
func servedSchemas() (map[string]string, error) {
	_, here, _, _ := runtime.Caller(0)
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(here), "..", "..", "..", "services", "*", "graphql", "*schema.graphql"))
	if err != nil {
		return nil, err
	}
	known := make(map[string]string, len(paths))
	for _, p := range paths {
		known[filepath.Base(filepath.Dir(filepath.Dir(p)))+"/"+filepath.Base(p)] = p
	}
	return known, nil
}

// ServeCapabilities handles negotiation independently of a fixture's business
// operations, so existing mutation/refusal/call-count assertions stay meaningful.
// Root-mounted fixtures in dcctl simulate the user-management admin plane.
func ServeCapabilities(t testing.TB, w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	t.Helper()
	if r.Header.Get("Accept") != "application/vnd.devicechain.schema+json" || !hasBearer(r.Header) {
		t.Error("schema discovery omitted its media type or bearer")
		http.Error(w, "invalid discovery request", http.StatusUnauthorized)
		return true
	}
	area, file := "user-management", "admin_schema.graphql"
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "api" {
		area, file = parts[1], "schema.graphql"
		if len(parts) == 4 {
			file = parts[2] + "_schema.graphql"
		}
	}
	known, err := servedSchemas()
	if err != nil {
		t.Errorf("enumerate served schemas: %v", err)
		http.Error(w, "schema unavailable", http.StatusServiceUnavailable)
		return true
	}
	path, ok := known[area+"/"+file]
	if !ok {
		t.Errorf("unexpected schema discovery path %q", r.URL.Path)
		http.Error(w, "unknown schema", http.StatusNotFound)
		return true
	}
	schema, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read served schema: %v", err)
		http.Error(w, "schema unavailable", http.StatusServiceUnavailable)
		return true
	}
	w.Header().Set("Content-Type", "application/vnd.devicechain.schema+json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"format": 1, "schema": string(schema)})
	return true
}

func hasBearer(h http.Header) bool {
	token, ok := auth.BearerToken(h)
	return ok && token != ""
}
