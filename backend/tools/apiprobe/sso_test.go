// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
)

// fakeAdmin answers the admin-plane documents the way a platform that stored the
// connection would, and remembers what it was sent. It is a fixture for the seed's
// WALK and for verify's three outcomes; what the real platform does with each step
// is the rig's business, not a unit test's.
type fakeAdmin struct {
	sent       []string // the mutation/query name of each call, in order
	refuseOn   string   // a document name that answers with an error
	noPrint    bool     // answer an empty discovery fingerprint
	stored     map[string]any
	exists     bool
	approved   bool
	domains    []map[string]string
	tenants    []string
	mappings   []map[string]any
	lastTenant string
}

func docName(doc string) string {
	for _, n := range []string{
		"createRole", "createIdpConnection", "approveIdpDiscovery", "addIdpDomain",
		"setIdpConnectionTenants", "setIdpRoleMappings",
	} {
		if strings.Contains(doc, n+"(") {
			return n
		}
	}
	if strings.Contains(doc, "discovery{fingerprint}") {
		return "fingerprint"
	}
	return "read"
}

func (f *fakeAdmin) Query(_ context.Context, _ string, doc string, vars map[string]any, out any) error {
	name := docName(doc)
	f.sent = append(f.sent, name)
	if f.refuseOn == name {
		return errors.New("refused: " + name)
	}
	var answer any
	switch name {
	case "createIdpConnection":
		f.exists = true
	case "fingerprint":
		fp := "fp-1"
		if f.noPrint {
			fp = ""
		}
		answer = map[string]any{"idpConnection": map[string]any{"discovery": map[string]any{"fingerprint": fp}}}
	case "approveIdpDiscovery":
		f.approved = true
	case "addIdpDomain":
		f.domains = append(f.domains, map[string]string{"domain": vars["d"].(string)})
	case "setIdpConnectionTenants":
		f.tenants = vars["x"].([]string)
	case "setIdpRoleMappings":
		f.lastTenant = vars["t"].(string)
		f.mappings = nil
		for _, m := range vars["m"].([]map[string]any) {
			f.mappings = append(f.mappings, map[string]any{
				"tenant": f.lastTenant, "group": m["group"], "role": m["role"], "fullAuthority": false})
		}
	case "read":
		if !f.exists {
			answer = map[string]any{"idpConnection": nil}
		} else {
			answer = map[string]any{"idpConnection": map[string]any{
				"token": vars["t"], "name": "API probe identity provider", "kind": "tenant", "enabled": false,
				"issuer": "https://idp.invalid", "clientId": "apiprobe", "hasSecret": true,
				"scopes": []string{"openid"}, "domains": f.domains, "tenants": f.tenants,
				"roleMappings": f.mappings, "discovery": map[string]any{"approved": f.approved, "fingerprint": "fp-1"},
			}}
		}
	}
	if out == nil || answer == nil {
		return nil
	}
	raw, err := json.Marshal(answer)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func seededReceipt(t *testing.T, f *fakeAdmin) Receipt {
	t.Helper()
	row, err := seedConnection(context.Background(), f, "http://x/admin", "apiprobe",
		ssoOptions{issuer: "https://idp.invalid", clientID: "apiprobe"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return Receipt{Admin: []Recorded{row}}
}

// The seed walks the steps an operator takes, in the order that makes the later ones
// legal: a domain and a tenant cannot be attached to a connection that is not there, and
// mappings need the tenant the connection serves.
func TestTheSeedWalksTheConnectionThroughEveryStep(t *testing.T) {
	f := &fakeAdmin{}
	rec := seededReceipt(t, f)

	want := []string{"createRole", "createIdpConnection", "fingerprint", "approveIdpDiscovery",
		"addIdpDomain", "setIdpConnectionTenants", "setIdpRoleMappings", "read"}
	if strings.Join(f.sent, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %v, want %v", f.sent, want)
	}
	if len(rec.Admin) != 1 || rec.Admin[0].Name != ssoRowName || rec.Admin[0].Fields != idpConnectionFields {
		t.Fatalf("the recorded row is wrong: %+v", rec.Admin)
	}
	// The recorded object is the platform's ANSWER, so it carries what the walk wrote.
	var obj struct {
		Tenants      []string `json:"tenants"`
		RoleMappings []struct {
			Group string `json:"group"`
			Role  string `json:"role"`
		} `json:"roleMappings"`
		Discovery struct {
			Approved bool `json:"approved"`
		} `json:"discovery"`
	}
	if err := json.Unmarshal(rec.Admin[0].Object, &obj); err != nil {
		t.Fatal(err)
	}
	if len(obj.Tenants) != 1 || obj.Tenants[0] != "apiprobe" || !obj.Discovery.Approved ||
		len(obj.RoleMappings) != 1 || obj.RoleMappings[0].Group != ssoGroup || obj.RoleMappings[0].Role != ssoRoleToken("apiprobe") {
		t.Fatalf("the recorded connection does not carry the walk: %s", rec.Admin[0].Object)
	}
}

// A refusal at ANY step is exitRefused with the step named, never a partial receipt: a
// connection created but not approved would read back fine and prove less than it claims.
func TestARefusedStepFailsTheSeedAndNamesTheStep(t *testing.T) {
	for _, step := range []string{"createRole", "createIdpConnection", "approveIdpDiscovery",
		"addIdpDomain", "setIdpConnectionTenants", "setIdpRoleMappings"} {
		f := &fakeAdmin{refuseOn: step}
		_, err := seedConnection(context.Background(), f, "http://x/admin", "apiprobe",
			ssoOptions{issuer: "https://idp.invalid", clientID: "apiprobe"})
		if err == nil {
			t.Errorf("%s was refused and the seed still succeeded", step)
			continue
		}
		if codeOf(err) != exitRefused {
			t.Errorf("%s refusal exits %d, want exitRefused (%d)", step, codeOf(err), exitRefused)
		}
		if !strings.Contains(err.Error(), "refused: "+step) {
			t.Errorf("%s refusal does not carry the platform's own message: %v", step, err)
		}
	}
}

func TestAnEmptyDiscoveryFingerprintIsAShapeFindingNotAnApproval(t *testing.T) {
	f := &fakeAdmin{noPrint: true}
	_, err := seedConnection(context.Background(), f, "http://x/admin", "apiprobe",
		ssoOptions{issuer: "https://idp.invalid", clientID: "apiprobe"})
	if codeOf(err) != exitShape {
		t.Fatalf("exit = %d (%v), want exitShape", codeOf(err), err)
	}
	for _, n := range f.sent {
		if n == "approveIdpDiscovery" {
			t.Fatal("approved a snapshot the platform gave no fingerprint for")
		}
	}
}

// verify's three outcomes, each shown to fail on its own: the row is there and the same
// (PASS), the row is gone (MISSING), the row is there and different (MISMATCH). A verify
// that only ever passes is the vacuous green this tool exists to refuse.
func TestVerifyReadsTheConnectionBackAndFailsForEachKindOfDamage(t *testing.T) {
	ctx := context.Background()

	t.Run("unchanged", func(t *testing.T) {
		f := &fakeAdmin{}
		rec := seededReceipt(t, f)
		var out bytes.Buffer
		n, err := verifyAdminRows(ctx, f, "http://x/admin", rec, &out)
		if err != nil || n != 1 {
			t.Fatalf("n=%d err=%v", n, err)
		}
		if !strings.Contains(out.String(), ssoRowName) {
			t.Fatalf("the report does not name the row it checked:\n%s", out.String())
		}
	})
	t.Run("missing", func(t *testing.T) {
		f := &fakeAdmin{}
		rec := seededReceipt(t, f)
		f.exists = false
		_, err := verifyAdminRows(ctx, f, "http://x/admin", rec, &bytes.Buffer{})
		if codeOf(err) != exitMissing {
			t.Fatalf("exit = %d (%v), want exitMissing", codeOf(err), err)
		}
	})
	t.Run("changed", func(t *testing.T) {
		f := &fakeAdmin{}
		rec := seededReceipt(t, f)
		f.approved = false // the upgrade withdrew the approval
		_, err := verifyAdminRows(ctx, f, "http://x/admin", rec, &bytes.Buffer{})
		if codeOf(err) != exitMismatch {
			t.Fatalf("exit = %d (%v), want exitMismatch", codeOf(err), err)
		}
	})
	t.Run("a mapping lost", func(t *testing.T) {
		f := &fakeAdmin{}
		rec := seededReceipt(t, f)
		f.mappings = nil
		_, err := verifyAdminRows(ctx, f, "http://x/admin", rec, &bytes.Buffer{})
		if codeOf(err) != exitMismatch {
			t.Fatalf("exit = %d (%v), want exitMismatch", codeOf(err), err)
		}
	})
	t.Run("selection from another build", func(t *testing.T) {
		f := &fakeAdmin{}
		rec := seededReceipt(t, f)
		rec.Admin[0].Fields += " extra"
		_, err := verifyAdminRows(ctx, f, "http://x/admin", rec, &bytes.Buffer{})
		// assertCode, not codeOf: codeOf(nil) IS exitSetup, so a verify that wrongly
		// passed would satisfy a bare comparison.
		assertCode(t, err, exitSetup)
	})
	t.Run("an admin row this build cannot read", func(t *testing.T) {
		rec := Receipt{Admin: []Recorded{{Name: "something-new", Token: "x"}}}
		_, err := verifyAdminRows(ctx, &fakeAdmin{}, "http://x/admin", rec, &bytes.Buffer{})
		// assertCode, not codeOf: codeOf(nil) IS exitSetup, so a verify that wrongly
		// passed would satisfy a bare comparison.
		assertCode(t, err, exitSetup)
	})
}

// A rejected read QUERY is the schema moving, not data loss.
func TestARejectedReadIsAShapeFinding(t *testing.T) {
	q := queryFunc(func(string) error { return errors.New("Cannot query field \"approved\"") })
	_, err := readConnection(context.Background(), q, "http://x/admin", "apiprobe-idp")
	if codeOf(err) != exitShape {
		t.Fatalf("exit = %d (%v), want exitShape", codeOf(err), err)
	}
}

type queryFunc func(doc string) error

func (f queryFunc) Query(_ context.Context, _, doc string, _ map[string]any, _ any) error {
	return f(doc)
}

// The row is written only when the caller named a provider AND the baseline can express
// it; either way the skip carries its reason.
func TestWillSeedNamesWhyItDoesNot(t *testing.T) {
	if ok, why := (ssoOptions{}).willSeed(nil); ok || !strings.Contains(why, "--idp-issuer") {
		t.Fatalf("no issuer: ok=%v why=%q", ok, why)
	}
	if ok, why := (ssoOptions{issuer: "https://idp.invalid"}).willSeed(nil); !ok {
		t.Fatalf("an issuer and no baseline must seed, got why=%q", why)
	}
}

// 🔴 THE COUNTERWEIGHT: pointed at the CURRENT tree the baseline supports the row.
// A supportsConnection that always answered no would skip it on every drill and the
// connection would silently never be covered.
func TestTheCurrentTreeSupportsTheConnection(t *testing.T) {
	b, err := loadBaseline(filepath.Join("..", "..", "services"))
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := b.supportsConnection(); !ok {
		t.Fatalf("the current tree does not support the connection row: %s", why)
	}
}

// ...and the other half: a release without the operations skips it, naming the first one
// missing, and a release with no admin schema at all says that instead. A nil baseline
// (seeding the release under test) supports everything.
func TestAReleaseWithoutSingleSignOnSkipsTheConnection(t *testing.T) {
	if ok, _ := (*baseline)(nil).supportsConnection(); !ok {
		t.Error("a nil baseline must support the connection")
	}

	dir := t.TempDir()
	graphqlDir := filepath.Join(dir, "user-management", "graphql")
	if err := os.MkdirAll(graphqlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(graphqlDir, "schema.graphql"),
		[]byte("type Query { ping: String }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := b.supportsConnection(); ok || !strings.Contains(why, "no user-management admin schema") {
		t.Fatalf("no admin schema: ok=%v why=%q", ok, why)
	}

	if err := os.WriteFile(filepath.Join(graphqlDir, "admin_schema.graphql"), []byte(`
type Query { tenants: [String!]! }
type Mutation { createRole(request: String): String }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err = loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := b.supportsConnection(); ok || !strings.Contains(why, "createIdpConnection") {
		t.Fatalf("an admin schema without the operation: ok=%v why=%q", ok, why)
	}
}

// Every admin-plane document is validated against the schema it is sent to, by the
// server's own validator: a string nobody parses until a cluster is the way the
// provisioning documents once shipped broken (see documents_test.go).
func TestEveryConnectionDocumentValidatesAgainstTheAdminSchema(t *testing.T) {
	schema := servedSchema(t, "user-management", schemaplane.MountAdmin)

	for _, c := range []struct {
		name string
		doc  string
		vars map[string]any
	}{
		{"createRole", createRoleMutation, map[string]any{"r": map[string]any{
			"scope": "tenant", "token": ssoRoleToken("apiprobe"), "name": "n", "authorities": []string{}}}},
		{"createIdpConnection", createIdpConnectionMutation, map[string]any{"r": map[string]any{
			"token": ssoConnectionToken("apiprobe"), "name": "n", "kind": "tenant", "issuer": "https://idp.invalid",
			"clientId": "c", "clientSecret": "s", "scopes": []string{"openid"}}}},
		{"fingerprint", idpFingerprintQuery, map[string]any{"t": ssoConnectionToken("apiprobe")}},
		{"approveIdpDiscovery", approveIdpMutation, map[string]any{"t": ssoConnectionToken("apiprobe"), "f": "fp"}},
		{"addIdpDomain", addIdpDomainMut, map[string]any{"t": ssoConnectionToken("apiprobe"), "d": ssoOptions{}.domainFor("apiprobe")}},
		{"setIdpConnectionTenants", setIdpTenantsMut, map[string]any{"t": ssoConnectionToken("apiprobe"), "x": []string{"apiprobe"}}},
		{"setIdpRoleMappings", setIdpMappingsMut, map[string]any{"c": ssoConnectionToken("apiprobe"), "t": "apiprobe",
			"m": []any{map[string]any{"group": ssoGroup, "role": ssoRoleToken("apiprobe")}}}},
		{"read", idpConnectionReadQuery, map[string]any{"t": ssoConnectionToken("apiprobe")}},
	} {
		if errs := schema.ValidateWithVariables(c.doc, c.vars); len(errs) > 0 {
			t.Errorf("%s does not validate against the served admin schema: %v\n  document: %s", c.name, errs, c.doc)
		}
		if err := gqlcore.CheckWork(c.doc); err != nil {
			t.Errorf("%s exceeds the served root-field ceiling: %v", c.name, err)
		}
	}
}

// The sweep's two single-sign-on tables are reached by this seed, so they are no longer
// exempt: an exemption on a table that now holds rows is reported STALE, and one left in
// place would hide the day the seed stopped reaching them.
func TestSingleSignOnTablesAreNotExempt(t *testing.T) {
	for _, table := range []string{"user-management.iam_idp_connection_tenants", "user-management.iam_idp_role_mappings"} {
		if reason, ok := tableSweepExemptions[table]; ok {
			t.Errorf("%s is exempt (%q), but the seed writes it", table, reason)
		}
	}
}
