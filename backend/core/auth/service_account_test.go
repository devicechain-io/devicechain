// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// A service account's token is an ordinary tenant access token whose subject is the
// account: no email, never a superuser, the account's scope as "scp" when it has one, and
// the scoped (shorter) lifetime whether or not it is scoped.
func TestServiceAccountAccessToken(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "test", 15*time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)
	sub := ServiceAccountSubject("ci-bot")
	if strings.Contains(sub, "@") || !strings.HasPrefix(sub, ServiceAccountSubjectPrefix) {
		t.Fatalf("service account subject %q could collide with a person's", sub)
	}

	before := time.Now()
	tok, err := iss.IssueServiceAccountAccess("acme", sub, []string{"ops"}, []string{string(DeviceRead)},
		"", []string{"https://mcp.example"}, Scope{}, "dcsa_1", "j1")
	if err != nil {
		t.Fatal(err)
	}
	c, err := v.Validate(tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if c.Tenant != "acme" || c.Username != sub || c.Subject != sub || c.Email != "" || c.ActingAsSuperuser ||
		c.ClientId != "dcsa_1" || c.ScopeAnchors != nil || !reflect.DeepEqual([]string(c.Audience), []string{"https://mcp.example"}) {
		t.Fatalf("unexpected claims %+v", c)
	}
	if life := tok.ExpiresAt.Sub(before); life > DefaultScopedAccessTTL+time.Second {
		t.Fatalf("a service account token lives %v, want at most the scoped TTL %v", life, DefaultScopedAccessTTL)
	}

	scoped, err := iss.IssueServiceAccountAccess("acme", sub, nil, nil, "", nil, mustScope(t, "customer:globex"), "dcsa_1", "j2")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := v.Validate(scoped.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := ScopeFromContext(ctxWith(sc)); !ok || !reflect.DeepEqual(got.Anchors(), []string{"customer:globex"}) {
		t.Fatalf("scoped service account token reads as scoped=%v anchors=%v", ok, got.Anchors())
	}
}

// Fail closed at the mint: no tenant, no subject, no client id, or a scoped Scope with no
// anchors (which would otherwise be written with no "scp" — the whole tenant) is refused.
func TestServiceAccountMintRefusals(t *testing.T) {
	iss := NewIssuer(mustKey(t), "test", 0, 0)
	anchorless, _ := ScopeFromContext(ctxWith(scopedAccessClaims([]string{})))
	cases := map[string]func() error{
		"no tenant": func() error {
			_, e := iss.IssueServiceAccountAccess("", "s", nil, nil, "", nil, Scope{}, "c", "j")
			return e
		},
		"no subject": func() error {
			_, e := iss.IssueServiceAccountAccess("t", "", nil, nil, "", nil, Scope{}, "c", "j")
			return e
		},
		"no client": func() error {
			_, e := iss.IssueServiceAccountAccess("t", "s", nil, nil, "", nil, Scope{}, "", "j")
			return e
		},
		"anchorless scope": func() error {
			_, e := iss.IssueServiceAccountAccess("t", "s", nil, nil, "", nil, anchorless, "c", "j")
			return e
		},
	}
	for name, f := range cases {
		if f() == nil {
			t.Errorf("%s: minted", name)
		}
	}
}
