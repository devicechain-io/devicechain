// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// THE ADMIN-PLANE ROWS: single sign-on connections.
//
// Everything in entities.go is written and read by a TENANT administrator through a
// functional area's tenant schema. An identity-provider connection is neither: it is
// an instance-level record managed by a superuser through user-management's admin
// schema, and two of the tables that hang off it (which tenants it serves, and its
// group-to-role mappings) are tenant-scoped, so the table sweep counts them. Folding
// the connection into the tenant-plane table would have meant teaching every one of
// its consumers — the seed, verify, the tamper controls, the read sweep, the coverage
// test — about a second principal. It is its own short list instead, kept on the
// receipt beside the entities and read back by the same verify.
//
// 🔴 CREATING A CONNECTION FETCHES THE PROVIDER'S DISCOVERY DOCUMENT, through the egress
// guard, over https, verified against the system's trusted roots. So this row exists
// only when the caller names an issuer the instance can actually reach: `--idp-issuer`.
// The rig runs a stub provider for it (hack/lib/sso-stub-idp.sh). Without the flag the
// row is skipped BY NAME, exactly as an entity the baseline cannot express is, and the
// table sweep then reports the two tables it feeds as uncovered. That is deliberate:
// a drill that can no longer create a connection must fail, not quietly stop covering
// them.
//
// The connection is left DISABLED, for the reason the detection rules are authored
// disabled: the probe asserts the record survives, not that sign-in works, and an
// enabled connection left behind by a drill is a live sign-in route.

const (
	// ssoRowName is the receipt name of the connection row. Never rename it: a receipt
	// written before an upgrade is read after it.
	ssoRowName = "idp-connection"

	ssoGroup = "apiprobe-viewers"

	// ssoDomainSuffix is the parent of every derived email domain (domainFor). Freemail
	// providers and public suffixes are refused, so it has to be a name of its own;
	// .example is reserved and will never be anyone's.
	ssoDomainSuffix    = "apiprobe.example"
	ssoDefaultClientID = "apiprobe"
)

// 🔴 EVERY NAME THE SEED CREATES ON THE ADMIN PLANE IS PER TENANT. The role, the
// connection and the email domain are INSTANCE-wide records, each unique across the
// instance, and the upgrade drill seeds a connection twice on one instance once its
// baseline can: for the baseline's tenant before the upgrade, and for the sweep tenant
// after it. A constant name would make the second seed's create refused, which the rig
// reports as a finding about the release. (A tenant token near the length limit makes
// these exceed it; the platform then refuses the create, loudly.)

// ssoConnectionToken is the connection the seed creates for tenant.
func ssoConnectionToken(tenant string) string { return "apiprobe-idp-" + tenant }

// ssoRoleToken is the tenant role the seed's group mapping grants, for tenant.
func ssoRoleToken(tenant string) string { return "apiprobe-sso-viewer-" + tenant }

// domainFor is the email domain asserted for tenant's connection: --idp-domain when
// given, else a label derived from the tenant under ssoDomainSuffix. A tenant token may
// carry capitals and underscores, which a domain label may not, so those are folded.
func (o ssoOptions) domainFor(tenant string) string {
	if d := strings.TrimSpace(o.domain); d != "" {
		return d
	}
	label := strings.Trim(strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		default:
			return '-'
		}
	}, tenant), "-")
	if label == "" {
		label = "tenant"
	}
	return label + "." + ssoDomainSuffix
}

// idpConnectionFields is the connection's read selection, used verbatim by the seed's
// read-back and by verify (the same reason entity.Fields is one string).
//
// Only what is STORED and does not move for a reason other than data loss. The same
// test entities.go states, applied to this record: createdAt/updatedAt are omitted
// because Postgres truncates them to microseconds, domains' verifiedAt and verifiedBy
// for the same family of reason, and enabled/approved are included because a connection
// that comes back enabled, or with its approval withdrawn, is exactly the defect worth
// finding.
const idpConnectionFields = `token name kind enabled issuer clientId hasSecret scopes emailClaim ` +
	`groupsClaim jitEnabled defaultTenantRoles sessionMaxAgeSeconds requiredAmr ` +
	`requiredClaims{name value} domains{domain} tenants ` +
	`roleMappings{tenant group role fullAuthority} ` +
	`discovery{authorizationEndpoint tokenEndpoint jwksUri fingerprint approved}`

const (
	createRoleMutation = `mutation($r:AdminRoleCreateRequest!){createRole(request:$r){token}}`

	createIdpConnectionMutation = `mutation($r:IdpConnectionCreateRequest!){` +
		`createIdpConnection(request:$r){token}}`
	idpFingerprintQuery = `query($t:String!){idpConnection(token:$t){discovery{fingerprint}}}`
	approveIdpMutation  = `mutation($t:String!,$f:String!){approveIdpDiscovery(token:$t,fingerprint:$f){token}}`
	addIdpDomainMut     = `mutation($t:String!,$d:String!){addIdpDomain(token:$t,domain:$d){token}}`
	setIdpTenantsMut    = `mutation($t:String!,$x:[String!]!){setIdpConnectionTenants(token:$t,tenants:$x){token}}`
	setIdpMappingsMut   = `mutation($c:String!,$t:String!,$m:[IdpRoleMappingInput!]!){` +
		`setIdpRoleMappings(connection:$c,tenant:$t,mappings:$m){token}}`

	// idpConnectionReadQuery is the read-back document, generated from the shared
	// selection.
	idpConnectionReadQuery = `query($t:String!){idpConnection(token:$t){` + idpConnectionFields + `}}`
)

// ssoOptions are the seed flags that gate the connection row.
type ssoOptions struct {
	issuer   string
	clientID string
	domain   string
}

// enabled reports whether the caller named an issuer to seed against.
func (o ssoOptions) enabled() bool { return strings.TrimSpace(o.issuer) != "" }

// willSeed decides whether the seed writes the connection, and says why not when it
// does not. Both reasons are printed beside the skipped row: "no provider named" is the
// caller's choice, "the baseline cannot express it" is the release's.
func (o ssoOptions) willSeed(base *baseline) (bool, string) {
	if !o.enabled() {
		return false, "no --idp-issuer was given, so there is no provider to create a connection against"
	}
	return base.supportsConnection()
}

// seedConnection creates the probe's role and connection, walks it through the steps an
// operator takes to make it usable (approve the discovery snapshot the platform fetched,
// assert a domain, serve the probe tenant, map a group), and returns the connection AS
// THE PLATFORM READS IT BACK — the same answer verify will compare against.
func seedConnection(ctx context.Context, q querier, adminURL, tenant string, o ssoOptions) (Recorded, error) {
	refuse := func(what string, err error) (Recorded, error) {
		// exitRefused, not exitSetup: the API answered and said no. A connection the
		// platform will not create, or an approval it will not accept, is a verdict
		// about the platform — unless the message names a certificate or a blocked
		// address, which is the rig's trust or egress edit not taking effect, and which
		// the message below says in as many words.
		return Recorded{}, failWith(exitRefused, "%s: %w\n   (a certificate or blocked-address message here means the instance "+
			"cannot reach the identity provider at %s: see the rig's trust and egress setup)", what, err, o.issuer)
	}
	conn, role := ssoConnectionToken(tenant), ssoRoleToken(tenant)

	secret, err := randomToken()
	if err != nil {
		return Recorded{}, failWith(exitSetup, "generate a client secret: %w", err)
	}

	if err := q.Query(ctx, adminURL, createRoleMutation, map[string]any{
		"r": map[string]any{
			"scope": "tenant", "token": role, "name": "API probe SSO viewer",
			"authorities": []string{},
		},
	}, nil); err != nil {
		return refuse("create role "+role, err)
	}

	if err := q.Query(ctx, adminURL, createIdpConnectionMutation, map[string]any{
		"r": map[string]any{
			"token": conn, "name": "API probe identity provider", "kind": "tenant",
			"issuer": o.issuer, "clientId": o.clientID, "clientSecret": secret,
			"scopes": []string{"openid", "email", "profile", "groups"},
		},
	}, nil); err != nil {
		return refuse("create identity-provider connection "+conn, err)
	}

	var fp struct {
		IdpConnection struct {
			Discovery struct {
				Fingerprint string `json:"fingerprint"`
			} `json:"discovery"`
		} `json:"idpConnection"`
	}
	if err := q.Query(ctx, adminURL, idpFingerprintQuery, map[string]any{"t": conn}, &fp); err != nil {
		return refuse("read the discovery fingerprint of "+conn, err)
	}
	if fp.IdpConnection.Discovery.Fingerprint == "" {
		return Recorded{}, failWith(exitShape, "connection %s reports an empty discovery fingerprint, so there is nothing to approve", conn)
	}

	steps := []struct {
		what string
		doc  string
		vars map[string]any
	}{
		{"approve the discovery snapshot", approveIdpMutation,
			map[string]any{"t": conn, "f": fp.IdpConnection.Discovery.Fingerprint}},
		{"assert the email domain", addIdpDomainMut,
			map[string]any{"t": conn, "d": o.domainFor(tenant)}},
		{"serve the probe tenant", setIdpTenantsMut,
			map[string]any{"t": conn, "x": []string{tenant}}},
		{"map a group to a tenant role", setIdpMappingsMut,
			map[string]any{"c": conn, "t": tenant,
				"m": []map[string]any{{"group": ssoGroup, "role": role}}}},
	}
	for _, s := range steps {
		if err := q.Query(ctx, adminURL, s.doc, s.vars, nil); err != nil {
			return refuse(s.what+" for "+conn, err)
		}
	}

	object, err := readConnection(ctx, q, adminURL, conn)
	if err != nil {
		return Recorded{}, err
	}
	return Recorded{
		Name: ssoRowName, Area: "user-management", Token: conn,
		Object: object, Fields: idpConnectionFields,
	}, nil
}

// readConnection reads one connection back with the shared selection. Absence is
// exitMissing; a rejected QUERY is exitShape, for the reason readObject gives.
func readConnection(ctx context.Context, q querier, adminURL, token string) (json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if err := q.Query(ctx, adminURL, idpConnectionReadQuery, map[string]any{"t": token}, &envelope); err != nil {
		var decided *codedError
		if errors.As(err, &decided) {
			return nil, err
		}
		return nil, failWith(exitShape, "read %s %q back: %w", ssoRowName, token, err)
	}
	raw, ok := envelope["idpConnection"]
	if !ok {
		return nil, failWith(exitShape, "read %s returned no %q key; the query's shape has changed", ssoRowName, "idpConnection")
	}
	if isJSONNull(raw) {
		return nil, failWith(exitMissing, "%s %q is GONE: the API created it, and idpConnection now returns null for that token",
			ssoRowName, token)
	}
	return raw, nil
}

// verifyAdminRows reads every admin-plane row the receipt carries back and requires it
// unchanged. A receipt with none verifies nothing here, which is the ordinary case for a
// baseline that has no connections.
//
// It needs the superuser, and says so rather than skipping: a receipt that records a
// connection and a verify that cannot read one would otherwise report a pass over rows it
// never looked at.
func verifyAdminRows(ctx context.Context, q querier, adminURL string, rec Receipt, out io.Writer) (int, error) {
	checked := 0
	for _, r := range rec.Admin {
		if r.Name != ssoRowName {
			return checked, failWith(exitSetup, "receipt records the admin row %q, which this build of apiprobe cannot read back", r.Name)
		}
		if r.Fields != idpConnectionFields {
			return checked, failWith(exitSetup,
				"%s was seeded with a different selection than this build reads back, so the recorded "+
					"object and a fresh read cannot be compared. This is a receipt from another build of "+
					"apiprobe, not a platform that changed.\n   seeded with: %s\n   reads back:  %s",
				r.Name, r.Fields, idpConnectionFields)
		}
		found, err := readConnection(ctx, q, adminURL, r.Token)
		if err != nil {
			return checked, err
		}
		want, err := canonical(r.Object)
		if err != nil {
			return checked, failWith(exitSetup, "receipt entry %s is unreadable: %w", r.Name, err)
		}
		got, err := canonical(found)
		if err != nil {
			return checked, failWith(exitShape, "read %s returned unparseable JSON: %w", r.Name, err)
		}
		if want != got {
			return checked, failWith(exitMismatch, "%s %q CHANGED across the upgrade:\n   written: %s\n   read:    %s",
				r.Name, r.Token, want, got)
		}
		checked++
		fmt.Fprintf(out, "  ok      %-26s %s\n", r.Name, r.Token)
	}
	return checked, nil
}

// adminSession signs in as the superuser the receipt's admin rows need.
func (c *connection) adminSession(ctx context.Context) (*userclient.AdminSession, error) {
	pw, err := c.adminPassword()
	if err != nil {
		return nil, failWith(exitSetup, "the receipt records admin-plane rows, which verify reads as the superuser: %w", err)
	}
	admin := userclient.NewAdminSession(c.httpClient(), c.userURL(), c.adminEmail, pw)
	super, err := admin.Superuser(ctx)
	if err != nil {
		return nil, failWith(exitSetup, "admin login at %s failed: %w", c.userURL(), err)
	}
	if !super {
		return nil, failWith(exitSetup, "%s is not a superuser; verify needs one to read the admin-plane rows", c.adminEmail)
	}
	return admin, nil
}

// runSSOBaseline answers, for a rig, whether a seed against the baseline tree will write
// the connection: it prints `supported` or `unsupported: <reason>` and exits 0 for either
// (a non-zero exit is a setup failure, never an answer). The rig asks BEFORE the baseline
// install so it can stand up the stub provider the instance has to be able to reach when,
// and only when, the release being upgraded from can create a connection at all.
func runSSOBaseline(argv []string) error {
	fs := flagSetFor("ssobaseline")
	var dir string
	fs.StringVar(&dir, "baseline-schemas", "", "a `backend/services` tree from the release being seeded (required)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(dir) == "" {
		return failWith(exitSetup, "--baseline-schemas is required")
	}
	base, err := loadBaseline(dir)
	if err != nil {
		return err
	}
	if ok, why := base.supportsConnection(); !ok {
		fmt.Printf("unsupported: %s\n", why)
		return nil
	}
	fmt.Println("supported")
	return nil
}
