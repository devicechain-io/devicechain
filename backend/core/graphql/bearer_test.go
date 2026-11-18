// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/auth/authtest"
	"github.com/devicechain-io/dc-microservice/core"
)

func TestBearerHTTPContract(t *testing.T) {
	authtest.RunBearerCases(t, auth.PlaneGraphQL, func(t *testing.T, c authtest.BearerCase) string {
		gate := core.NewReadinessGate()
		gate.SetAudiencePolicy(c.Policy)
		if c.Validator != nil {
			gate.MarkReady(c.Validator)
		}
		// Malformed JSON gives a deterministic 400 only after optional authentication succeeds.
		h := NewHttpHandler(nil, nil, gate)
		r := httptest.NewRequest("POST", "/", strings.NewReader("!"))
		r.Header.Set("Authorization", c.Header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		status, body := 401, "invalid or expired token\n"
		switch c.Outcome {
		case "missing", "valid":
			status, body = 400, "invalid character '!' looking for beginning of value\n"
		case "unavailable":
			body = "authentication is not available\n"
		}
		if w.Code != status || w.Body.String() != body {
			t.Errorf("response=%d %q want %d %q", w.Code, w.Body.String(), status, body)
		}
		return authtest.Response(w.Code, w.Header(), w.Body.String())
	})
}
func TestBearerWSContract(t *testing.T) {
	authtest.RunBearerCases(t, auth.PlaneGraphQLWS, func(t *testing.T, c authtest.BearerCase) string {
		gate := core.NewReadinessGate()
		gate.SetAudiencePolicy(c.Policy)
		if c.Validator != nil {
			gate.MarkReady(c.Validator)
		}
		conn := &wsConnection{handler: NewSubscriptionHandler(nil, nil, gate)}
		payload, _ := json.Marshal(map[string]string{"Authorization": c.Header})
		_, _, err := conn.authenticate(context.Background(), payload)
		want := "invalid or expired token"
		switch c.Outcome {
		case "missing", "valid":
			want = ""
		case "unavailable":
			want = "authentication is not available"
		}
		if c.EmptyCredential {
			want = "invalid or expired token"
		}
		if c.Outcome == "valid" {
			token, _ := auth.BearerFromValue(c.Header)
			gate.SetAudiencePolicy(nil) // The second check must not add a table observation.
			raw, _ := json.Marshal(map[string]string{"token": " " + token + " "})
			ctx, expiry, bareErr := conn.authenticate(context.Background(), raw)
			if bareErr != nil {
				t.Fatalf("bare token: %v", bareErr)
			}
			tenant, _ := core.TenantFromContext(ctx)
			if tenant != "tenant-a" || expiry.IsZero() {
				t.Fatalf("bare token: tenant=%q expiry=%v err=%v", tenant, expiry, bareErr)
			}
		}
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("error=%q want=%q", got, want)
		}
		return got
	})
}
