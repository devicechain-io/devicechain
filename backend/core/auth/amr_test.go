// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func amrFixture(t *testing.T) (*Issuer, *Validator, *rsa.PrivateKey) {
	t.Helper()
	key := mustKey(t)
	return NewIssuer(key, "test", time.Minute, time.Hour), NewValidator(&key.PublicKey), key
}

// The three canonical amr lists survive a mint and a parse on every tier a sign-in
// session mints, and Mfa() reads the second factor from them.
func TestAmrRoundTripOnEverySessionTier(t *testing.T) {
	iss, v, _ := amrFixture(t)
	cases := []struct {
		name string
		amr  []string
		mfa  bool
	}{
		{"password only", AmrPasswordOnly(), false},
		{"password and otp", AmrPasswordOTP(), true},
		{"password and recovery code", AmrPasswordRecovery(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := iss.IssueIdentity("a@example.com", "epoch", "sid", nil, nil, "j1", WithAmr(tc.amr))
			require.NoError(t, err)
			acc, err := iss.IssueTenantAccess("t", "a@example.com", nil, nil, false, "j2", WithAmr(tc.amr))
			require.NoError(t, err)
			ref, err := iss.IssueRefresh("t", "a@example.com", "epoch", "sid", nil, nil, "j3", WithAmr(tc.amr))
			require.NoError(t, err)
			oacc, err := iss.IssueOAuthAccess("t", "a@example.com", nil, nil, "devices:read", nil, false, "c", "j4", WithAmr(tc.amr))
			require.NoError(t, err)
			oref, err := iss.IssueOAuthRefresh("t", "a@example.com", "epoch", "sid", nil, nil, "devices:read", nil, "c", "j5", WithAmr(tc.amr))
			require.NoError(t, err)

			for name, tok := range map[string]string{"identity": id.Token, "access": acc.Token, "refresh": ref.Token,
				"oauth access": oacc.Token, "oauth refresh": oref.Token} {
				claims, err := v.Parse(tok)
				require.NoError(t, err, name)
				require.Equal(t, tc.amr, claims.Amr, name)
				require.Equal(t, tc.mfa, claims.Mfa(), name)
			}
		})
	}
}

// A mint given no WithAmr writes no claim, which reads as "no second factor": the
// default for every caller that has not been taught about it, and for a record written
// before this existed.
func TestNoAmrOptionWritesNoClaim(t *testing.T) {
	iss, v, _ := amrFixture(t)
	id, err := iss.IssueIdentity("a@example.com", "epoch", "sid", nil, nil, "j")
	require.NoError(t, err)
	claims, err := v.Parse(id.Token)
	require.NoError(t, err)
	require.Nil(t, claims.Amr)
	require.False(t, claims.Mfa())
}

// The issuer will not write an amr the validator would refuse.
func TestMintRefusesANonCanonicalAmr(t *testing.T) {
	iss, _, _ := amrFixture(t)
	for _, bad := range [][]string{nil, {}, {"mfa"}, {"otp"}, {"pwd", "otp"}, {"pwd", "mfa", "otp"}, {"pwd", "mfa", "extra"}, {"PWD"}} {
		_, err := iss.IssueIdentity("a@example.com", "epoch", "sid", nil, nil, "j", WithAmr(bad))
		require.ErrorIs(t, err, ErrMalformedAmr, "%v", bad)
	}
}

// T10 (validator half): amr on a machine credential is refused, whether the token was
// forged or minted by some future path that forgot the rule.
func TestParseRefusesAmrOnMachineCredentials(t *testing.T) {
	_, v, key := amrFixture(t)
	k := key
	exp := time.Now().Add(time.Minute)
	mk := func(typ, username string, amr []string) string {
		c := accessClaims("t", exp)
		c.TokenType, c.Username, c.Subject, c.Amr = typ, username, username, amr
		if typ != TokenTypeAccess {
			c.Tenant = ""
		}
		return signRaw(t, jwt.SigningMethodRS256, k, c)
	}

	// Controls first: the same tokens without amr parse, so each refusal below is about
	// the claim and not about how the token was built.
	for _, tc := range []struct{ typ, user string }{
		{TokenTypeService, "svc"}, {TokenTypeAccess, ServiceAccountSubject("sa-1")},
	} {
		_, err := v.Parse(mk(tc.typ, tc.user, nil))
		require.NoError(t, err, "%s control", tc.typ)
		_, err = v.Parse(mk(tc.typ, tc.user, AmrPasswordOTP()))
		require.ErrorIs(t, err, ErrMalformedAmr, "%s with amr", tc.typ)
	}
	// And a person's access token with the same amr is accepted.
	_, err := v.Parse(mk(TokenTypeAccess, "a@example.com", AmrPasswordOTP()))
	require.NoError(t, err)
}

// A present-but-unrecognised or empty amr refuses the whole token; it is never read as
// "no second factor".
func TestParseRefusesAMalformedAmr(t *testing.T) {
	_, v, key := amrFixture(t)
	k := key
	for _, bad := range [][]string{{"mfa"}, {"pwd", "otp"}, {"pwd", "mfa", "hwk"}} {
		c := accessClaims("t", time.Now().Add(time.Minute))
		c.Amr = bad
		_, err := v.Parse(signRaw(t, jwt.SigningMethodRS256, k, c))
		require.True(t, errors.Is(err, ErrMalformedAmr), "%v: %v", bad, err)
	}
}

// A service account's token, minted through its own issuer method, carries no amr: that
// method has no option to set one.
func TestServiceAccountMintCarriesNoAmr(t *testing.T) {
	iss, v, _ := amrFixture(t)
	tok, err := iss.IssueServiceAccountAccess("t", ServiceAccountSubject("sa-1"), nil, nil, "", nil, Scope{}, "c", "j")
	require.NoError(t, err)
	claims, err := v.Parse(tok.Token)
	require.NoError(t, err)
	require.Nil(t, claims.Amr)
	require.False(t, claims.Mfa())
}
