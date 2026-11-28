// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// The tenant purge half of the upgrade drill's attrguard phase.
//
// The attribute guard refuses a write to a device's SHARED attribute row, a DELETE included, unless
// the transaction carries the writer protocol marker. The tenant purge deletes every one of a
// tenant's attribute rows in one transaction, so it marks that transaction itself; a purge that did
// not would be refused and leave the rows behind. The drill purges a throwaway tenant with the
// BASELINE's user-management as the only one running, which is the roll's real hazard: an old pod's
// purge executing under a trigger its own release never installed.
//
//	seed    through the instance (the upgraded pods): a tenant with its own administrator, one
//	        device with no profile, and --keys SHARED attributes on it. Writes --out.
//	delete  as the superuser, at --user-url when given (the rig points it at the baseline pod): remove
//	        the administrator's membership, then deleteTenant, which must be accepted.
//
// Whether the rows are then erased is the rig's to judge, in SQL, because only the database can say
// that nothing is left.

const (
	purgeTypeToken   = "purgedrill-type"
	purgeDeviceToken = "purgedrill-device"
	purgeKeyPrefix   = "purgedrill-"

	removeMembershipMutation = `mutation($email:String!,$tenant:String!){removeMembership(email:$email,tenant:$tenant){email}}`
	deleteTenantMutation     = `mutation($token:String!){deleteTenant(token:$token)}`
)

// purgeState is what seed hands delete.
type purgeState struct {
	Tenant string   `json:"tenant"`
	Email  string   `json:"email"`
	Device string   `json:"device"`
	Keys   []string `json:"keys"`
}

// purgeSeedDocs are the device-management writes seed makes, in order.
func purgeSeedDocs(keys int) []probeDoc {
	docs := []probeDoc{
		{"create the device type", "device-management", createTypeMutation, map[string]any{"req": map[string]any{
			"token": purgeTypeToken, "name": "purge drill type"}}},
		{"create the device", "device-management", createDeviceMutation, map[string]any{"req": map[string]any{
			"token": purgeDeviceToken, "name": "purge drill device", "deviceTypeToken": purgeTypeToken}}},
	}
	for i := 1; i <= keys; i++ {
		docs = append(docs, probeDoc{fmt.Sprintf("set SHARED attribute %d", i), "device-management", attrWriteMutation,
			map[string]any{"req": map[string]any{
				"entityType": "device", "entity": purgeDeviceToken, "scope": probeAttrScope,
				"attrKey": fmt.Sprintf("%s%d", purgeKeyPrefix, i), "valueType": probeValueType, "value": "v"}}})
	}
	return docs
}

// purgeDeleteDocs are the admin-plane mutations delete sends, in order.
func purgeDeleteDocs(st purgeState) []probeDoc {
	return []probeDoc{
		{"remove the administrator's membership", "user-management", removeMembershipMutation,
			map[string]any{"email": st.Email, "tenant": st.Tenant}},
		{"delete the tenant", "user-management", deleteTenantMutation, map[string]any{"token": st.Tenant}},
	}
}

func runPurgeTenant(ctx context.Context, argv []string) error {
	return purgeTenantTo(ctx, argv, os.Stdout)
}

func purgeTenantTo(ctx context.Context, argv []string, stdout io.Writer) error {
	if len(argv) < 1 {
		return failWith(exitSetup, "purgetenant needs an action: seed or delete")
	}
	switch argv[0] {
	case "seed":
		return purgeSeed(ctx, argv[1:], stdout)
	case "delete":
		return purgeDelete(ctx, argv[1:], stdout)
	default:
		return failWith(exitSetup, "unknown purgetenant action %q (want seed or delete)", argv[0])
	}
}

func purgeSeed(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("purgetenant seed")
	var c connection
	c.bind(fs)
	var out string
	var keys int
	fs.StringVar(&out, "out", "", "where to write the state delete reads (required)")
	fs.IntVar(&keys, "keys", 3, "how many SHARED attributes to set on the device")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(out) == "" {
		return failWith(exitSetup, "--out is required")
	}
	if keys < 1 {
		return failWith(exitSetup, "--keys must be at least 1: a purge of a tenant with no attribute rows proves nothing")
	}
	session, cred, err := c.provision(ctx)
	if err != nil {
		return err
	}
	st := purgeState{Tenant: c.tenant, Email: cred.Email, Device: purgeDeviceToken}
	for _, d := range purgeSeedDocs(keys) {
		if err := session.Query(ctx, c.areaURL(d.area), d.doc, d.vars, nil); err != nil {
			return failWith(exitRefused, "%s in tenant %q: %w", d.what, c.tenant, err)
		}
		if req, ok := d.vars["req"].(map[string]any); ok && d.doc == attrWriteMutation {
			st.Keys = append(st.Keys, fmt.Sprint(req["attrKey"]))
		}
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return failWith(exitSetup, "marshal the state: %w", err)
	}
	if err := os.WriteFile(out, raw, 0o600); err != nil {
		return failWith(exitSetup, "write %s: %w", out, err)
	}
	fmt.Fprintf(stdout, "purgetenant: tenant %q seeded with device %q and %d SHARED attribute(s)\n", st.Tenant, st.Device, len(st.Keys))
	return nil
}

func purgeDelete(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("purgetenant delete")
	var c connection
	c.bind(fs)
	var statePath, userURL string
	fs.StringVar(&statePath, "state", "", "the state seed wrote (required)")
	fs.StringVar(&userURL, "user-url", "", "user-management's base URL, serving /graphql and /admin/graphql (default: behind the ingress)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(statePath) == "" {
		return failWith(exitSetup, "--state is required")
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return failWith(exitSetup, "read %s: %w", statePath, err)
	}
	var st purgeState
	if err := json.Unmarshal(raw, &st); err != nil || st.Tenant == "" || st.Email == "" {
		return failWith(exitSetup, "%s is not a purgetenant state (tenant and email are required)", statePath)
	}
	login, admin := c.userURL(), c.adminURL()
	if u := strings.TrimRight(userURL, "/"); u != "" {
		login, admin = u+"/graphql", u+"/admin/graphql"
	}
	pw, err := c.adminPassword()
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	session := userclient.NewAdminSession(c.httpClient(), login, c.adminEmail, pw)
	super, err := session.Superuser(ctx)
	if err != nil {
		return failWith(exitSetup, "admin login at %s failed: %w", login, err)
	}
	if !super {
		return failWith(exitSetup, "%s is not a superuser; deleting a tenant needs tenant:write", c.adminEmail)
	}
	docs := purgeDeleteDocs(st)
	if err := session.Query(ctx, admin, docs[0].doc, docs[0].vars, nil); err != nil {
		return failWith(exitRefused, "%s at %s: %w", docs[0].what, admin, err)
	}
	var del struct {
		DeleteTenant bool `json:"deleteTenant"`
	}
	if err := session.Query(ctx, admin, docs[1].doc, docs[1].vars, &del); err != nil {
		return failWith(exitRefused, "%s at %s: %w", docs[1].what, admin, err)
	}
	if !del.DeleteTenant {
		return failWith(exitRefused, "deleteTenant(%q) at %s answered false: no such tenant, or it is already being deleted", st.Tenant, admin)
	}
	fmt.Fprintf(stdout, "purgetenant: tenant %q deleted at %s; its purge is now the coordinator's\n", st.Tenant, admin)
	return nil
}
