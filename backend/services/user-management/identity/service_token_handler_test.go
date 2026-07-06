// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
)

// The mint endpoint is the trust root of the sync-call primitive, so every branch
// is covered: happy path (and the minted token actually validates), method guard,
// fail-closed on an unconfigured secret, wrong secret, missing subject/authorities,
// and an unknown authority.
func TestServiceTokenHandler(t *testing.T) {
	key, err := auth.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	iss := auth.NewIssuer(key, "test", time.Minute, time.Hour)
	v := auth.NewValidator(&key.PublicKey)
	issue := func(subject string, authorities []string) (auth.IssuedToken, error) {
		return iss.IssueService(subject, authorities, "jti-test")
	}
	const secret = "shared-service-secret"
	withSecret := func() string { return secret }
	noSecret := func() string { return "" }

	do := func(method, presented string, body any, secretFn func() string) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, auth.ServiceTokenPath, &buf)
		if presented != "" {
			req.Header.Set(auth.ServiceSecretHeader, presented)
		}
		rec := httptest.NewRecorder()
		ServiceTokenHandler(secretFn, issue).ServeHTTP(rec, req)
		return rec
	}

	okReq := auth.ServiceTokenRequest{Subject: "command-delivery", Authorities: []string{string(auth.DeviceRead)}}

	t.Run("happy path mints a validatable token", func(t *testing.T) {
		rec := do(http.MethodPost, secret, okReq, withSecret)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
		}
		var resp auth.ServiceTokenResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		claims, err := v.ValidateService(resp.Token)
		if err != nil {
			t.Fatalf("minted token did not validate as a service token: %v", err)
		}
		if claims.Username != "command-delivery" || !claims.HasAuthority(auth.DeviceRead) || claims.Tenant != "" {
			t.Fatalf("unexpected claims: %+v", claims)
		}
	})

	cases := map[string]struct {
		method    string
		presented string
		body      any
		secretFn  func() string
		want      int
	}{
		"non-POST rejected":          {http.MethodGet, secret, nil, withSecret, http.StatusMethodNotAllowed},
		"unconfigured secret 503":    {http.MethodPost, secret, okReq, noSecret, http.StatusServiceUnavailable},
		"wrong secret 401":           {http.MethodPost, "nope", okReq, withSecret, http.StatusUnauthorized},
		"empty presented secret 401": {http.MethodPost, "", okReq, withSecret, http.StatusUnauthorized},
		"missing subject 400":        {http.MethodPost, secret, auth.ServiceTokenRequest{Authorities: []string{string(auth.DeviceRead)}}, withSecret, http.StatusBadRequest},
		"missing authorities 400":    {http.MethodPost, secret, auth.ServiceTokenRequest{Subject: "x"}, withSecret, http.StatusBadRequest},
		"unknown authority 400":      {http.MethodPost, secret, auth.ServiceTokenRequest{Subject: "x", Authorities: []string{"device:raed"}}, withSecret, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := do(tc.method, tc.presented, tc.body, tc.secretFn); rec.Code != tc.want {
				t.Fatalf("code=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
