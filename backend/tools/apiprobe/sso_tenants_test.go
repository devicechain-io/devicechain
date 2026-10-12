// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// sharedInstance is ONE instance that two seeds write to, enforcing the uniqueness the
// platform enforces on the admin-plane records the seed creates: a role is unique by
// (scope, token), a connection by token, and an email domain across every connection.
// Each connection's own state is a fakeAdmin.
type sharedInstance struct {
	roles, domains map[string]bool
	conns          map[string]*fakeAdmin
}

func newSharedInstance() *sharedInstance {
	return &sharedInstance{roles: map[string]bool{}, domains: map[string]bool{}, conns: map[string]*fakeAdmin{}}
}

func (s *sharedInstance) Query(ctx context.Context, url, doc string, vars map[string]any, out any) error {
	var conn string
	switch docName(doc) {
	case "createRole":
		tok := vars["r"].(map[string]any)["token"].(string)
		if s.roles[tok] {
			return errors.New("role " + tok + " already exists")
		}
		s.roles[tok] = true
		return nil
	case "createIdpConnection":
		conn = vars["r"].(map[string]any)["token"].(string)
		if s.conns[conn] != nil {
			return errors.New("connection " + conn + " already exists")
		}
		s.conns[conn] = &fakeAdmin{}
	case "addIdpDomain":
		d := vars["d"].(string)
		if s.domains[d] {
			return errors.New("domain " + d + " is already claimed by a connection")
		}
		s.domains[d] = true
		conn = vars["t"].(string)
	case "setIdpRoleMappings":
		conn = vars["c"].(string)
	default:
		conn = vars["t"].(string)
	}
	f := s.conns[conn]
	if f == nil {
		return errors.New("no connection " + conn)
	}
	return f.Query(ctx, url, doc, vars, out)
}

// 🔴 THE DRILL SEEDS A CONNECTION TWICE ON ONE INSTANCE, the day its baseline can: once
// for the baseline's tenant before the upgrade, and once for the sweep tenant after it.
// Both seeds run with the rig's flags (no --idp-domain), so anything the seed names by a
// constant is created twice — and the second create is refused, which the rig reports as
// "the UPGRADED release could not create one of every entity": a false finding about the
// release, on every gated PR, from the first baseline that has single sign-on.
func TestTwoTenantsCanEachSeedAConnectionOnOneInstance(t *testing.T) {
	inst := newSharedInstance()
	opts := ssoOptions{issuer: "https://idp.invalid", clientID: ssoDefaultClientID}
	var tokens []string
	for _, tenant := range []string{"apiprobe", "apiprobe-coverage"} {
		row, err := seedConnection(context.Background(), inst, "http://x/admin", tenant, opts)
		if err != nil {
			t.Fatalf("seeding tenant %s after another tenant on the same instance: %v", tenant, err)
		}
		tokens = append(tokens, row.Token)
	}
	if tokens[0] == tokens[1] {
		t.Fatalf("both tenants recorded connection %q", tokens[0])
	}
}

// The derived domain has to be one the platform accepts for any tenant token, and a
// tenant token may carry underscores and capitals, which a domain label may not.
func TestTheDerivedDomainIsADomainForAnyTenantToken(t *testing.T) {
	for _, tenant := range []string{"apiprobe", "apiprobe-coverage", "Probe_Tenant", "x-", "a_b-"} {
		d := ssoOptions{}.domainFor(tenant)
		if d != strings.ToLower(d) || strings.ContainsAny(d, "_ ") || !strings.HasSuffix(d, "."+ssoDomainSuffix) {
			t.Errorf("tenant %q derives domain %q", tenant, d)
		}
		for _, label := range strings.Split(d, ".") {
			if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				t.Errorf("tenant %q derives domain %q with the bad label %q", tenant, d, label)
			}
		}
	}
	if got := (ssoOptions{domain: "corp.example"}).domainFor("apiprobe"); got != "corp.example" {
		t.Errorf("an explicit --idp-domain was not used: %q", got)
	}
}
