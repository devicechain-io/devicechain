// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// connection is the flag set both subcommands share.
type connection struct {
	server     string
	scheme     string
	tenant     string
	adminEmail string
	adminPass  string
}

func (c *connection) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.server, "server", "localhost", "instance ingress host (and :port) the API is reachable on")
	fs.StringVar(&c.scheme, "scheme", "http", "http or https (https skips certificate verification for a self-signed local cert)")
	fs.StringVar(&c.tenant, "tenant", "apiprobe", "tenant token the probe entities are written under")
	fs.StringVar(&c.adminEmail, "admin-email", "superuser@devicechain.local", "superuser identity that creates the probe tenant")
	fs.StringVar(&c.adminPass, "admin-password", "devicechain", "superuser password")
}

func (c *connection) base() string { return fmt.Sprintf("%s://%s", c.scheme, c.server) }

// areaURL is the GraphQL endpoint for a functional area. Every service serves
// its schema at the same path shape behind the instance ingress.
func (c *connection) areaURL(area string) string {
	return fmt.Sprintf("%s/api/%s/graphql", c.base(), area)
}

func (c *connection) userURL() string  { return c.base() + "/api/user-management/graphql" }
func (c *connection) adminURL() string { return c.base() + "/api/user-management/admin/graphql" }

// httpClient mirrors drdrill's: a local instance serves a self-signed
// certificate, and a drill that cannot reach the platform proves nothing, so
// https skips verification rather than failing setup.
func (c *connection) httpClient() *http.Client {
	client := &http.Client{Timeout: 60 * time.Second}
	if strings.EqualFold(c.scheme, "https") {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // drill against a local self-signed instance
		}
	}
	return client
}

const (
	createTenantMutation = `mutation($token:String!,$name:String,$tier:String!){` +
		`createTenant(request:{token:$token,name:$name,tierToken:$tier}){token}}`
	createIdentityMutation = `mutation($email:String!,$password:String!){` +
		`createIdentity(request:{email:$email,password:$password,enabled:true,systemRoles:[]}){email}}`
	addMembershipMutation = `mutation($email:String!,$tenant:String!){` +
		`addMembership(email:$email,tenant:$tenant,roleTokens:["tenant-admin"]){email}}`
)

// probeTenantTier is the tier the probe tenant is created under. Gold: the probe
// must never be shed, or a governance decision would read as a missing entity.
const probeTenantTier = "gold"

// provision creates the probe tenant and a tenant-scoped identity for it, and
// returns a session authenticated as that identity.
//
// 🔑 The probe deliberately does NOT run as the superuser. It writes as an
// ordinary tenant administrator, because that is the principal a real client is,
// and a permission that tightened in an upgrade is exactly the kind of
// regression this is here to catch. Running as a superuser would step over it.
func (c *connection) provision(ctx context.Context) (*userclient.TenantSession, Credential, error) {
	var cred Credential
	httpc := c.httpClient()

	admin := userclient.NewAdminSession(httpc, c.userURL(), c.adminEmail, c.adminPass)
	super, err := admin.Superuser(ctx)
	if err != nil {
		return nil, cred, failWith(exitSetup, "admin login at %s failed: %w", c.userURL(), err)
	}
	if !super {
		return nil, cred, failWith(exitSetup, "%s is not a superuser; the probe needs tenant:write + user:write", c.adminEmail)
	}

	password, err := randomToken()
	if err != nil {
		return nil, cred, failWith(exitSetup, "generate identity password: %w", err)
	}
	cred = Credential{Email: fmt.Sprintf("%s@apiprobe.invalid", c.tenant), Password: password}

	if err := admin.Query(ctx, c.adminURL(), createTenantMutation, map[string]any{
		"token": c.tenant, "name": "API probe", "tier": probeTenantTier,
	}, nil); err != nil {
		return nil, cred, failWith(exitSetup, "create tenant %q: %w", c.tenant, err)
	}
	if err := admin.Query(ctx, c.adminURL(), createIdentityMutation, map[string]any{
		"email": cred.Email, "password": cred.Password,
	}, nil); err != nil {
		return nil, cred, failWith(exitSetup, "create identity %q: %w", cred.Email, err)
	}
	if err := admin.Query(ctx, c.adminURL(), addMembershipMutation, map[string]any{
		"email": cred.Email, "tenant": c.tenant,
	}, nil); err != nil {
		return nil, cred, failWith(exitSetup, "add tenant-admin membership for %q: %w", cred.Email, err)
	}

	return userclient.NewTenantSession(httpc, c.userURL(), cred.Email, cred.Password, c.tenant), cred, nil
}

// session re-authenticates as an identity a previous seed created, for verify.
func (c *connection) session(cred Credential) *userclient.TenantSession {
	return userclient.NewTenantSession(c.httpClient(), c.userURL(), cred.Email, cred.Password, c.tenant)
}

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
