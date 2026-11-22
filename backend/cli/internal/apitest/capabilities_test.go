// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package apitest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discover(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodOptions, path, nil)
	r.Header.Set("Accept", "application/vnd.devicechain.schema+json")
	r.Header.Set("Authorization", "Bearer x")
	w := httptest.NewRecorder()
	if !ServeCapabilities(t, w, r) {
		t.Fatalf("%s: OPTIONS was not handled", path)
	}
	return w
}

func TestServeCapabilitiesServesTheServedSchema(t *testing.T) {
	for _, path := range []string{"/", "/api/device-management/graphql", "/api/user-management/settings/graphql"} {
		w := discover(t, path)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "schema") {
			t.Errorf("%s: status %d body %.80q", path, w.Code, w.Body.String())
		}
	}
}

func TestServeCapabilitiesRefusesAPathOutsideTheServedSchemas(t *testing.T) {
	// The request path is data, never a filesystem path: a traversal segment is refused before any read.
	st := &failRecorder{TB: t}
	r := httptest.NewRequest(http.MethodOptions, "/api/../../../go.mod/x", nil)
	r.Header.Set("Accept", "application/vnd.devicechain.schema+json")
	r.Header.Set("Authorization", "Bearer x")
	w := httptest.NewRecorder()
	ServeCapabilities(st, w, r)
	if w.Code != http.StatusNotFound || !st.failed {
		t.Fatalf("status %d, reported %v; want 404 and a reported error", w.Code, st.failed)
	}
}

// failRecorder absorbs the helper's own t.Errorf so a refusal can be asserted.
type failRecorder struct {
	testing.TB
	failed bool
}

func (f *failRecorder) Errorf(string, ...any) { f.failed = true }
