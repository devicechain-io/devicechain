// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
	nmmodel "github.com/devicechain-io/dc-notification-management/model"
)

// areaNotification is the notification-management functional area. It is ONE
// constant used for two things that are the same string in the deployment: the
// `/api/<area>` ingress prefix, and the Postgres schema each service's tables
// live in (the chart sets DC_MS_FUNCTIONAL_AREA from the same key). Keeping it
// single means the drill cannot look for the row in one place and the API in
// another.
const areaNotification = "notification-management"

// drillTenantTier is the tier the drill's tenant is packaged at (ADR-065). Every
// tenant is created at an explicit tier — the server infers no default, by design
// — and silver is the neutral middle of the seeded vocabulary. Like dcctl's sim,
// this is a literal rather than an import: the drill talks to the admin API over
// the wire and does not link user-management.
const drillTenantTier = "silver"

// The admin-plane mutations, mirroring dcctl's sim/admin.go for the same reason
// it states: this is wire traffic, not a linked API.
const (
	createTenantMutation = `mutation($token:String!,$name:String,$tier:String!){` +
		`createTenant(request:{token:$token,name:$name,tierToken:$tier}){token}}`
	createIdentityMutation = `mutation($email:String!,$password:String!){` +
		`createIdentity(request:{email:$email,password:$password,enabled:true,systemRoles:[]}){email}}`
	addMembershipMutation = `mutation($email:String!,$tenant:String!){` +
		`addMembership(email:$email,tenant:$tenant,roleTokens:["tenant-admin"]){email}}`

	createChannelMutation = `mutation($req:NotificationChannelCreateRequest!){` +
		`createNotificationChannel(request:$req){id token hasSecret}}`
	channelsByTokenQuery = `query($tokens:[String!]!){` +
		`notificationChannelsByToken(tokens:$tokens){id token hasSecret}}`
)

type seedOptions struct {
	server      string
	scheme      string
	instance    string
	adminEmail  string
	adminPass   string
	tenant      string
	receipt     string
	channelName string
}

func runSeed(ctx context.Context, argv []string) error {
	fs := flagSetFor("seed")
	var o seedOptions
	fs.StringVar(&o.server, "server", "localhost", "instance ingress host (and :port) the API is reachable on")
	fs.StringVar(&o.scheme, "scheme", "http", "http or https (https skips certificate verification for a self-signed local cert)")
	fs.StringVar(&o.instance, "instance", "", "instance id, recorded on the receipt (required)")
	fs.StringVar(&o.adminEmail, "admin-email", "superuser@devicechain.local", "superuser identity that creates the drill tenant")
	fs.StringVar(&o.adminPass, "admin-password", "devicechain", "superuser password")
	fs.StringVar(&o.tenant, "tenant", "drdrill", "tenant token the drill secret is written under")
	fs.StringVar(&o.channelName, "channel-token", "drdrill-channel", "notification-channel token the secret hangs off")
	fs.StringVar(&o.receipt, "receipt", "", "path to write the receipt verify will read (required)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(o.instance) == "" || strings.TrimSpace(o.receipt) == "" {
		return failWith(exitSetup, "--instance and --receipt are both required")
	}

	base := fmt.Sprintf("%s://%s", o.scheme, o.server)
	userGraphQL := base + "/api/user-management/graphql"
	adminGraphQL := base + "/api/user-management/admin/graphql"
	channelGraphQL := fmt.Sprintf("%s/api/%s/graphql", base, areaNotification)

	httpc := drillHTTPClient(o.scheme)

	// 1. Admin plane: a tenant, and a scoped identity that administers only it.
	admin := userclient.NewAdminSession(httpc, userGraphQL, o.adminEmail, o.adminPass)
	su, err := admin.Superuser(ctx)
	if err != nil {
		return failWith(exitSetup, "admin login at %s failed: %w", userGraphQL, err)
	}
	if !su {
		return failWith(exitSetup, "%s is not a superuser; the drill needs tenant:write + user:write", o.adminEmail)
	}

	identityEmail := fmt.Sprintf("%s@drdrill.invalid", o.tenant)
	identityPass, err := randomToken()
	if err != nil {
		return failWith(exitSetup, "generate identity password: %w", err)
	}

	if err := admin.Query(ctx, adminGraphQL, createTenantMutation, map[string]any{
		"token": o.tenant, "name": "DR drill", "tier": drillTenantTier,
	}, nil); err != nil {
		return failWith(exitSetup, "create tenant %q: %w", o.tenant, err)
	}
	if err := admin.Query(ctx, adminGraphQL, createIdentityMutation, map[string]any{
		"email": identityEmail, "password": identityPass,
	}, nil); err != nil {
		return failWith(exitSetup, "create identity %q: %w", identityEmail, err)
	}
	if err := admin.Query(ctx, adminGraphQL, addMembershipMutation, map[string]any{
		"email": identityEmail, "tenant": o.tenant,
	}, nil); err != nil {
		return failWith(exitSetup, "add tenant-admin membership for %q: %w", identityEmail, err)
	}

	// 2. Tenant plane: the secret, written the way an operator writes one.
	//
	// This is the half that makes the drill more than a unit test with extra
	// steps. The value is sealed by the DEPLOYED notification-management, using
	// the KEK it formed from the root key the chart handed it — so the ciphertext
	// the dump carries is the real thing, not something this tool manufactured.
	secret, err := randomToken()
	if err != nil {
		return failWith(exitSetup, "generate drill secret: %w", err)
	}
	tenantSession := userclient.NewTenantSession(httpc, userGraphQL, identityEmail, identityPass, o.tenant)

	config, err := json.Marshal(map[string]any{
		"url":    "https://drdrill.invalid/hook",
		"method": "POST",
	})
	if err != nil {
		return failWith(exitSetup, "encode channel config: %w", err)
	}
	var created struct {
		CreateNotificationChannel struct {
			ID        string `json:"id"`
			Token     string `json:"token"`
			HasSecret bool   `json:"hasSecret"`
		} `json:"createNotificationChannel"`
	}
	if err := tenantSession.Query(ctx, channelGraphQL, createChannelMutation, map[string]any{
		"req": map[string]any{
			"token":       o.channelName,
			"name":        "DR drill channel",
			"channelType": "webhook",
			"config":      string(config),
			"secret":      secret,
			"enabled":     true,
		},
	}, &created); err != nil {
		return failWith(exitSetup, "create notification channel %q: %w", o.channelName, err)
	}

	// hasSecret is the service telling us it stored one. Without this the drill
	// could seed nothing and still "pass" its own restore, because a missing row
	// and a missing secret look the same from the far side.
	if !created.CreateNotificationChannel.HasSecret {
		return failWith(exitSetup, "channel %q was created but reports no secret; nothing was sealed, so there is nothing to drill",
			o.channelName)
	}

	// The handle is keyed by the channel's row id, so a truncated or zero id would
	// record a DIFFERENT channel's handle in the receipt — which verify then
	// reports as "the restore did not bring it back", three steps from the cause.
	// 32 bits, not 64: uint is 32-bit on some builds and the conversion below is
	// where the truncation would happen.
	id, err := strconv.ParseUint(created.CreateNotificationChannel.ID, 10, 32)
	if err != nil {
		return failWith(exitSetup, "channel id %q is not a usable row id: %w", created.CreateNotificationChannel.ID, err)
	}
	if id == 0 {
		return failWith(exitSetup, "the API returned channel id 0, which is not a real row")
	}

	receipt := Receipt{
		Instance:     o.instance,
		Tenant:       o.tenant,
		Schema:       areaNotification,
		SecretName:   nmmodel.ChannelSecretName(uint(id)),
		ChannelToken: created.CreateNotificationChannel.Token,
		ChannelID:    uint(id),
		Identity:     identityEmail,
		Password:     identityPass,
		Secret:       secret,
	}
	if err := WriteReceipt(o.receipt, receipt); err != nil {
		return failWith(exitSetup, "write receipt: %w", err)
	}

	fmt.Printf("seeded: tenant=%s channel=%s (id %d) handle=%s\n",
		receipt.Tenant, receipt.ChannelToken, receipt.ChannelID, receipt.SecretName)
	fmt.Printf("receipt written to %s\n", o.receipt)
	return nil
}

// randomToken returns 32 bytes of randomness as URL-safe base64. Used for both
// the drill secret and the scoped identity's password: fresh per run, so a pass
// cannot come from a row an earlier run left behind.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
