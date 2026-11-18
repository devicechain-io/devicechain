// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package authtest

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/golang-jwt/jwt/v5"
)

// BearerCase supplies real signed tokens, a validator and an isolated recorder.
// Outcome is missing, valid, invalid, or unavailable. Adopters assert their own wire response.
type BearerCase struct {
	Header          string
	Validator       *auth.Validator
	Policy          *auth.PlanePolicy
	Outcome         string
	EmptyCredential bool
	CanonicalScheme bool
}

type recorder struct {
	count int
	plane auth.Plane
	t     *testing.T
}

func (r *recorder) Record(p auth.Plane, _ auth.AudienceClass, _ auth.AudienceVerdict) {
	r.count++
	if p != r.plane {
		r.t.Errorf("plane = %q, want %q", p, r.plane)
	}
}

// RunBearerCases tests parsing, verification, audience indistinguishability and
// the security ordering. do returns the complete observable response for comparison.
func RunBearerCases(t *testing.T, plane auth.Plane, do func(*testing.T, BearerCase) string) {
	t.Helper()
	key, err := auth.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	other, err := auth.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	v := auth.NewValidator(&key.PublicKey)
	sign := func(kind string, expired, foreign, forged bool) string {
		exp := time.Now().Add(time.Hour)
		if expired {
			exp = time.Now().Add(-time.Hour)
		}
		c := &auth.Claims{Tenant: "tenant-a", Username: "alice", TokenType: kind,
			RegisteredClaims: jwt.RegisteredClaims{Subject: "alice", ExpiresAt: jwt.NewNumericDate(exp)}}
		if foreign {
			c.Audience = jwt.ClaimStrings{"https://foreign.example/resource"}
		}
		k := key
		if forged {
			k = other
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(k)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	valid := sign(auth.TokenTypeAccess, false, false, false)
	cases := []struct {
		name, header, outcome string
		observations          int
	}{
		{"no_header", "", "missing", 0},
		{"basic", "Basic x", "missing", 0},
		{"scheme_only", "Bearer", "missing", 0},
		{"spaces_only", "Bearer    ", "missing", 0},
		{"lowercase_spaces_only", "bearer    ", "missing", 0},
		{"lowercase", "bearer " + valid, "valid", 1},
		{"trimmed", "Bearer  " + valid + " ", "valid", 1},
		{"bad_signature", "Bearer " + sign(auth.TokenTypeAccess, false, false, true), "invalid", 0},
		{"expired", "Bearer " + sign(auth.TokenTypeAccess, true, false, false), "invalid", 0},
		{"refresh", "Bearer " + sign(auth.TokenTypeRefresh, false, false, false), "invalid", 0},
		{"identity", "Bearer " + sign(auth.TokenTypeIdentity, false, false, false), "invalid", 0},
		{"foreign_audience", "Bearer " + sign(auth.TokenTypeAccess, false, true, false), "invalid", 1},
		{"forged_foreign", "Bearer " + sign(auth.TokenTypeAccess, false, true, true), "invalid", 0},
		{"unavailable", "Bearer " + valid, "unavailable", 0},
	}
	var invalid string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{plane: plane, t: t}
			p, err := auth.NewPlanePolicy(config.TokenAudienceConfiguration{Enforcement: "enforce"}, "user-management", rec)
			if err != nil {
				t.Fatal(err)
			}
			validator := v
			if tc.outcome == "unavailable" {
				validator = nil
			}
			response := do(t, BearerCase{Header: tc.header, Validator: validator, Policy: p, Outcome: tc.outcome, EmptyCredential: tc.name == "spaces_only" || tc.name == "lowercase_spaces_only", CanonicalScheme: tc.name == "spaces_only"})
			if rec.count != tc.observations {
				t.Errorf("audience recorder = %d, want %d", rec.count, tc.observations)
			}
			if tc.name == "bad_signature" {
				invalid = response
			}
			if tc.name == "foreign_audience" || tc.name == "forged_foreign" {
				if response != invalid {
					t.Errorf("response differs from invalid token: %q != %q", response, invalid)
				}
			}
		})
	}
}

// Response includes status, headers and body so challenges and connection handling
// participate in the byte-identical refusal contract.
func Response(status int, header http.Header, body string) string {
	var wire strings.Builder
	wire.WriteString(http.StatusText(status))
	wire.WriteByte('\n')
	if err := header.Write(&wire); err != nil {
		panic(err)
	}
	wire.WriteByte('\n')
	wire.WriteString(body)
	return wire.String()
}
