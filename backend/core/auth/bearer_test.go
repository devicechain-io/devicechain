// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/auth/authtest"
)

func TestBearerContract(t *testing.T) {
	authtest.RunBearerCases(t, auth.PlaneUpload, func(t *testing.T, c authtest.BearerCase) string {
		a := auth.BearerAuth{Plane: auth.PlaneUpload, Verify: auth.AccessVerifier(func() *auth.Validator { return c.Validator }), Policy: func() *auth.PlanePolicy { return c.Policy }}
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", c.Header)
		result, err := a.Authenticate(r)
		if c.Outcome == "valid" {
			if err != nil || result.Claims == nil || result.Tenant != "tenant-a" || result.Token == "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			return "ok"
		}
		want := auth.BearerInvalid
		if c.Outcome == "missing" {
			want = auth.BearerMissing
		}
		if c.Outcome == "unavailable" {
			want = auth.BearerUnavailable
		}
		if err == nil {
			t.Fatal("accepted invalid credential")
		}
		if err.(*auth.BearerError).Reason != want {
			t.Errorf("reason=%v want=%v", err.(*auth.BearerError).Reason, want)
		}
		return err.Error()
	})
}
