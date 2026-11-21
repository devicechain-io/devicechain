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
)

// ServeCapabilities handles negotiation independently of a fixture's business
// operations, so existing mutation/refusal/call-count assertions stay meaningful.
// Root-mounted fixtures in dcctl simulate the user-management admin plane.
func ServeCapabilities(t testing.TB, w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	t.Helper()
	if r.Header.Get("Accept") != "application/vnd.devicechain.schema+json" || r.Header.Get("Authorization") == "" {
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
	_, here, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(here), "..", "..", "..", "services", area, "graphql", file)
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
