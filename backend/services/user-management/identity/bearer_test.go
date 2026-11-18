// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/auth/authtest"
)

func TestBearerContract(t *testing.T) {
	authtest.RunBearerCases(t, auth.PlaneUserinfo, func(t *testing.T, c authtest.BearerCase) string {
		h := UserinfoHandler(func(token string) (*auth.Claims, error) {
			if c.Validator == nil {
				return nil, auth.ErrValidatorUnavailable
			}
			return c.Validator.Validate(token)
		}, func() *auth.PlanePolicy { return c.Policy })
		r := httptest.NewRequest("GET", "/", strings.NewReader("unread"))
		r.Header.Set("Authorization", c.Header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		ok := w.Code == 200
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
			if c.Outcome == "unavailable" {
				msg = "invalid token\n"
			}
			if w.Code != http.StatusUnauthorized || w.Body.String() != msg {
				t.Errorf("response = %d %q, want 401 %q", w.Code, w.Body.String(), msg)
			}
			challenge := `Bearer error="invalid_token"`
			if c.Outcome == "missing" {
				challenge = "Bearer"
			}
			if w.Header().Get("WWW-Authenticate") != challenge {
				t.Errorf("challenge = %q", w.Header().Get("WWW-Authenticate"))
			}
		}
		return authtest.Response(w.Code, w.Header(), w.Body.String())
	})
}
