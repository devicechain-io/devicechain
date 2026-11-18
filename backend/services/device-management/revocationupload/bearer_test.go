// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package revocationupload

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/auth/authtest"
)

func TestBearerContract(t *testing.T) {
	authtest.RunBearerCases(t, auth.PlaneUpload, func(t *testing.T, c authtest.BearerCase) string {
		h := &Handler{validator: func() *auth.Validator { return c.Validator }, policy: func() *auth.PlanePolicy { return c.Policy }}
		r := httptest.NewRequest("GET", "/", strings.NewReader("unread"))
		r.Header.Set("Authorization", c.Header)
		w := httptest.NewRecorder()
		_, ok := h.authenticate(w, r)
		if ok != (c.Outcome == "valid") {
			t.Errorf("authenticated = %v, outcome %s", ok, c.Outcome)
		}
		if !ok {
			msg := "invalid token\n"
			switch c.Outcome {
			case "missing":
				msg = "missing bearer token\n"
			case "unavailable":
				msg = "authentication is not available\n"
			}
			if w.Code != http.StatusUnauthorized || w.Body.String() != msg {
				t.Errorf("response = %d %q, want 401 %q", w.Code, w.Body.String(), msg)
			}
			if w.Header().Get("Connection") != "close" {
				t.Error("missing Connection: close")
			}
		}
		return authtest.Response(w.Code, w.Header(), w.Body.String())
	})
}
