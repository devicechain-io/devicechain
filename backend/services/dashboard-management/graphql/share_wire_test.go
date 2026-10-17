// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	core "github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// knownTargets is the fixture's device-management: the anchors a dashboard can be shared
// with, as "kind:token" — the customers that exist, and the groups that are access scopes.
// Its questions are recorded, so a test can see which kinds were asked about.
type knownTargets struct {
	shareable map[string]bool
	asked     []string
}

func (k *knownTargets) Missing(_ context.Context, _, kind string, tokens []string) ([]string, error) {
	k.asked = append(k.asked, kind)
	var missing []string
	for _, t := range tokens {
		if !k.shareable[kind+":"+t] {
			missing = append(missing, t)
		}
	}
	return missing, nil
}

type shareFixture struct {
	api     *model.Api
	targets *knownTargets
	db      *gorm.DB
	base    context.Context
}

func newShareFixture(t *testing.T) *shareFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.Dashboard{}, &model.DashboardVersion{}, &model.DashboardShare{}))
	api := model.NewApi(&rdb.RdbManager{Database: db})
	// "south" is a group of the tenant that is NOT an access scope: device-management reports
	// it missing exactly as it reports a group that does not exist.
	targets := &knownTargets{shareable: map[string]bool{"customer:acme": true, "customer:globex": true, "group:north": true}}
	api.ShareTargets = targets
	base := context.WithValue(context.Background(), gqlcore.ContextApiKey, api)
	return &shareFixture{api: api, targets: targets, db: db, base: base}
}

func (f *shareFixture) as(tenant string, scope []string, authorities ...auth.Authority) context.Context {
	held := make([]string, 0, len(authorities))
	for _, a := range authorities {
		held = append(held, string(a))
	}
	return auth.WithClaims(core.WithTenant(f.base, tenant),
		&auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: held, ScopeAnchors: scope})
}

func (f *shareFixture) author(tenant string) context.Context {
	return f.as(tenant, nil, auth.DashboardRead, auth.DashboardWrite)
}

func (f *shareFixture) member(anchors ...string) context.Context {
	return f.as("acme-tenant", anchors, auth.DashboardRead, auth.DashboardWrite)
}

// dashboard creates a dashboard, publishes it unless told not to, and shares it with the
// named customers — or, for a "group:<token>" entry, with that group.
func (f *shareFixture) dashboard(t *testing.T, tenant, token string, publish bool, customers ...string) {
	t.Helper()
	ctx := f.author(tenant)
	_, err := f.api.CreateDashboard(ctx, &model.DashboardCreateRequest{Token: token, Definition: `{"schemaVersion":1}`})
	require.NoError(t, err)
	if publish {
		_, _, err = f.api.PublishDashboard(ctx, token, nil, nil, "author", nil)
		require.NoError(t, err)
	}
	if len(customers) > 0 {
		anchors := make([]any, 0, len(customers))
		for _, c := range customers {
			if group, ok := strings.CutPrefix(c, "group:"); ok {
				anchors = append(anchors, map[string]any{"kind": "GROUP", "token": group})
				continue
			}
			anchors = append(anchors, map[string]any{"kind": "CUSTOMER", "token": c})
		}
		res := exec(ctx, shareMutation, map[string]any{"t": token, "a": anchors})
		require.Empty(t, res.Errors, "share %s", token)
	}
}

const shareMutation = `mutation($t: String!, $a: [ScopeAnchorInput!]!) {
  shareDashboard(token: $t, anchors: $a) { token sharedWith { kind token } } }`

func exec(ctx context.Context, query string, vars map[string]any) *gql.Response {
	return gql.MustParseSchema(SchemaContent, &SchemaResolver{}).Exec(ctx, query, "", vars)
}

func listedTokens(t *testing.T, ctx context.Context) []string {
	t.Helper()
	res := exec(ctx, `{ dashboards(criteria: {pageNumber: 1, pageSize: 50}) { results { token } pagination { totalRecords } } }`, nil)
	require.Empty(t, res.Errors)
	var out struct {
		Dashboards struct {
			Results    []struct{ Token string }
			Pagination struct{ TotalRecords int }
		}
	}
	require.NoError(t, json.Unmarshal(res.Data, &out))
	tokens := make([]string, 0)
	for _, r := range out.Dashboards.Results {
		tokens = append(tokens, r.Token)
	}
	sort.Strings(tokens)
	require.Equal(t, len(tokens), out.Dashboards.Pagination.TotalRecords, "the page count must be the caller's")
	return tokens
}

func published(t *testing.T, ctx context.Context, token string) json.RawMessage {
	t.Helper()
	res := exec(ctx, `query($t: String!) { publishedDashboard(token: $t) { token version } }`, map[string]any{"t": token})
	require.Empty(t, res.Errors)
	return res.Data
}

// An Acme member lists, and opens, only the published dashboards shared with Acme: not
// Globex's, not one shared with nobody, not an Acme draft nobody published, and not one a
// share row in ANOTHER tenant names. A dashboard outside its scope answers null, exactly as
// a token naming nothing does. The author is the negative control.
func TestScopedCallerSeesOnlySharedDashboards(t *testing.T) {
	f := newShareFixture(t)
	f.dashboard(t, "acme-tenant", "d-acme", true, "acme")
	f.dashboard(t, "acme-tenant", "d-globex", true, "globex")
	f.dashboard(t, "acme-tenant", "d-both", true, "globex", "acme")
	f.dashboard(t, "acme-tenant", "d-draft", false, "acme")
	f.dashboard(t, "acme-tenant", "d-none", true)
	// Another tenant's share naming Acme, keyed to d-none's id.
	var none model.Dashboard
	require.NoError(t, f.db.WithContext(core.WithTenant(context.Background(), "acme-tenant")).
		Where("token = ?", "d-none").First(&none).Error)
	require.NoError(t, f.db.WithContext(core.WithTenant(context.Background(), "other-tenant")).
		Create(&model.DashboardShare{DashboardID: none.ID, AnchorKind: "customer", AnchorToken: "acme"}).Error)

	require.Equal(t, []string{"d-acme", "d-both", "d-draft", "d-globex", "d-none"}, listedTokens(t, f.author("acme-tenant")),
		"control: the author sees every dashboard")
	require.Equal(t, []string{"d-acme", "d-both"}, listedTokens(t, f.member("customer:acme")))
	require.Equal(t, []string{"d-both", "d-globex"}, listedTokens(t, f.member("customer:globex")))
	require.Empty(t, listedTokens(t, f.member("customer:initech")))

	member := f.member("customer:acme")
	require.JSONEq(t, `{"publishedDashboard":{"token":"d-acme","version":1}}`, string(published(t, member, "d-acme")))
	require.JSONEq(t, `{"publishedDashboard":{"token":"d-both","version":1}}`, string(published(t, member, "d-both")))
	for _, token := range []string{"d-globex", "d-none", "no-such-dashboard"} {
		require.JSONEq(t, `{"publishedDashboard":null}`, string(published(t, member, token)), token)
	}
}

// 🔴 Every customer a share names must exist; one that does not fails the whole request with
// NOT_FOUND and leaves the dashboard's shares as they were. A share of a customer nobody
// checked would grant a customer created later under that token.
func TestShareWithUnknownCustomerIsNotFound(t *testing.T) {
	f := newShareFixture(t)
	f.dashboard(t, "acme-tenant", "d1", true, "acme")
	res := exec(f.author("acme-tenant"), shareMutation, map[string]any{"t": "d1",
		"a": []any{map[string]any{"kind": "CUSTOMER", "token": "globex"}, map[string]any{"kind": "CUSTOMER", "token": "ghost"}}})
	require.Len(t, res.Errors, 1)
	require.True(t, errors.Is(res.Errors[0].ResolverError, gorm.ErrRecordNotFound), "%v", res.Errors[0])
	require.Contains(t, res.Errors[0].Message, `"ghost"`)

	shares, err := f.api.SharesOf(f.author("acme-tenant"), 1)
	require.NoError(t, err)
	require.Equal(t, []string{"customer:acme"}, shares[1], "a refused share changed the shares")

	res = exec(f.author("acme-tenant"), shareMutation, map[string]any{"t": "no-such",
		"a": []any{map[string]any{"kind": "CUSTOMER", "token": "acme"}}})
	require.Len(t, res.Errors, 1)
	require.True(t, errors.Is(res.Errors[0].ResolverError, gorm.ErrRecordNotFound), "%v", res.Errors[0])
}

// Sharing replaces the list, collapses duplicates, unshares with an empty list, refuses an
// over-long list, and refuses to write anything it could not check.
func TestShareDashboardReplacesTheList(t *testing.T) {
	f := newShareFixture(t)
	f.dashboard(t, "acme-tenant", "d1", true, "acme")
	author := f.author("acme-tenant")

	res := exec(author, shareMutation, map[string]any{"t": "d1", "a": []any{
		map[string]any{"kind": "CUSTOMER", "token": "globex"}, map[string]any{"kind": "CUSTOMER", "token": "acme"},
		map[string]any{"kind": "CUSTOMER", "token": "globex"}}})
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"shareDashboard":{"token":"d1","sharedWith":[{"kind":"CUSTOMER","token":"acme"},{"kind":"CUSTOMER","token":"globex"}]}}`, string(res.Data))

	many := make([]any, 0, model.MaxDashboardShares+1)
	for i := 0; i <= model.MaxDashboardShares; i++ {
		many = append(many, map[string]any{"kind": "CUSTOMER", "token": fmt.Sprintf("c%d", i)})
	}
	res = exec(author, shareMutation, map[string]any{"t": "d1", "a": many})
	require.Len(t, res.Errors, 1)
	var limited *limit.Error
	require.True(t, errors.As(res.Errors[0].ResolverError, &limited), "%v", res.Errors[0])

	f.api.ShareTargets = nil
	for _, kind := range []string{"CUSTOMER", "GROUP"} {
		res = exec(author, shareMutation, map[string]any{"t": "d1", "a": []any{map[string]any{"kind": kind, "token": "north"}}})
		require.Len(t, res.Errors, 1)
		require.True(t, errors.Is(res.Errors[0].ResolverError, model.ErrSharesUnverifiable), "%s: %v", kind, res.Errors[0])
	}

	// Unsharing names nobody, so it needs nobody's answer.
	res = exec(author, shareMutation, map[string]any{"t": "d1", "a": []any{}})
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"shareDashboard":{"token":"d1","sharedWith":[]}}`, string(res.Data))
	require.Empty(t, listedTokens(t, f.member("customer:acme")))
}

// A dashboard can be shared with a group that is an access scope, beside customers, and the
// list names each anchor by its kind: a customer and a group with the same token are two
// shares, not one.
func TestShareWithAnAccessScopeGroup(t *testing.T) {
	f := newShareFixture(t)
	f.dashboard(t, "acme-tenant", "d1", true)
	f.targets.shareable["customer:north"] = true
	res := exec(f.author("acme-tenant"), shareMutation, map[string]any{"t": "d1", "a": []any{
		map[string]any{"kind": "GROUP", "token": "north"}, map[string]any{"kind": "CUSTOMER", "token": "north"},
		map[string]any{"kind": "GROUP", "token": "north"}}})
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"shareDashboard":{"token":"d1","sharedWith":[{"kind":"CUSTOMER","token":"north"},{"kind":"GROUP","token":"north"}]}}`, string(res.Data))
	require.Equal(t, []string{"customer", "group"}, f.targets.asked, "each kind is asked about once")
}

// 🔴 A group that does not exist, and one that exists but is not an access scope, fail the
// whole request with the SAME NOT_FOUND and leave the shares as they were: no member's access
// follows a group that is not an access scope, so a share with it would be a promise nobody
// could see, and the answer must not tell the author which of the two it was.
func TestShareWithAGroupThatIsNotAnAccessScopeIsNotFound(t *testing.T) {
	f := newShareFixture(t)
	f.dashboard(t, "acme-tenant", "d1", true, "acme")
	messages := map[string]string{}
	for _, group := range []string{"south", "ghost"} {
		res := exec(f.author("acme-tenant"), shareMutation, map[string]any{"t": "d1",
			"a": []any{map[string]any{"kind": "GROUP", "token": "north"}, map[string]any{"kind": "GROUP", "token": group}}})
		require.Len(t, res.Errors, 1, group)
		require.True(t, errors.Is(res.Errors[0].ResolverError, gorm.ErrRecordNotFound), "%s: %v", group, res.Errors[0])
		messages[group] = strings.ReplaceAll(res.Errors[0].Message, group, "<group>")
	}
	require.Equal(t, messages["south"], messages["ghost"], "a group that is not an access scope answered unlike a missing one")

	shares, err := f.api.SharesOf(f.author("acme-tenant"), 1)
	require.NoError(t, err)
	require.Equal(t, []string{"customer:acme"}, shares[1], "a refused share changed the shares")
}

// A member limited to a group lists and opens the published dashboards shared with that group
// — and not one shared with a customer named like it, nor with another group, nor a draft.
// Limited to a customer AND a group, it sees what is shared with either.
func TestGroupMemberSeesDashboardsSharedWithItsGroup(t *testing.T) {
	f := newShareFixture(t)
	f.targets.shareable["group:east"] = true
	f.targets.shareable["customer:north"] = true
	f.dashboard(t, "acme-tenant", "d-north", true, "group:north")
	f.dashboard(t, "acme-tenant", "d-north-draft", false, "group:north")
	f.dashboard(t, "acme-tenant", "d-east", true, "group:east")
	f.dashboard(t, "acme-tenant", "d-customer-north", true, "north")
	f.dashboard(t, "acme-tenant", "d-acme", true, "acme")

	require.Equal(t, []string{"d-north"}, listedTokens(t, f.member("group:north")))
	require.Equal(t, []string{"d-acme", "d-north"}, listedTokens(t, f.member("customer:acme", "group:north")))
	require.Equal(t, []string{"d-customer-north"}, listedTokens(t, f.member("customer:north")))

	member := f.member("group:north")
	require.JSONEq(t, `{"publishedDashboard":{"token":"d-north","version":1}}`, string(published(t, member, "d-north")))
	for _, token := range []string{"d-north-draft", "d-east", "d-customer-north", "d-acme", "no-such-dashboard"} {
		res := exec(member, `query($t: String!) { publishedDashboard(token: $t) { token version } }`, map[string]any{"t": token})
		if token == "d-north-draft" {
			// Shared with the group but never published: the same refusal the author gets.
			require.NotEmpty(t, res.Errors, token)
			continue
		}
		require.Empty(t, res.Errors, token)
		require.JSONEq(t, `{"publishedDashboard":null}`, string(res.Data), token)
	}
}

// 🔴 A member limited to part of the tenant may not share — holding dashboard:write or `*` —
// and may not read whom a dashboard is shared with: a dashboard shared with Acme and Globex
// would name Globex to Acme's users.
func TestScopedMembersCannotShareOrSeeTheShareList(t *testing.T) {
	f := newShareFixture(t)
	f.dashboard(t, "acme-tenant", "d1", true, "acme", "globex")
	member := f.as("acme-tenant", []string{"customer:acme"}, auth.AuthorityAll)

	res := exec(member, shareMutation, map[string]any{"t": "d1", "a": []any{}})
	require.NotEmpty(t, res.Errors)
	require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrScopeUnsupported), "%v", res.Errors[0])

	res = exec(member, `{ dashboard(token: "d1") { sharedWith { token } } }`, nil)
	require.NotEmpty(t, res.Errors)
	require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrScopeUnsupported), "%v", res.Errors[0])
}

// A dashboard's shares leave with it.
func TestDeletingADashboardRemovesItsShares(t *testing.T) {
	f := newShareFixture(t)
	f.dashboard(t, "acme-tenant", "d1", true, "acme")
	deleted, err := f.api.DeleteDashboard(f.author("acme-tenant"), "d1")
	require.NoError(t, err)
	require.True(t, deleted)
	var n int64
	require.NoError(t, f.db.WithContext(core.WithSystemContext(context.Background())).
		Model(&model.DashboardShare{}).Count(&n).Error)
	require.Zero(t, n)
}

// Every operation is classified for scoped members, so a new one is decided rather than
// inherited; the refused ones answer the scope refusal holding `*`.
var scopedOptIns = []string{"dashboards", "publishedDashboard"}

var scopedRefusals = map[string]string{
	"dashboard":                `{ dashboard(token: "d1") { token } }`,
	"dashboardVersions":        `{ dashboardVersions(token: "d1") { version } }`,
	"dashboardVersion":         `{ dashboardVersion(token: "d1", version: 1) { version } }`,
	"createDashboard":          `mutation { createDashboard(request: {token: "x", definition: "{}"}) { token } }`,
	"updateDashboard":          `mutation { updateDashboard(token: "d1", request: {name: "n"}) { token } }`,
	"publishDashboard":         `mutation { publishDashboard(token: "d1") { version { version } } }`,
	"rollbackDashboard":        `mutation { rollbackDashboard(token: "d1", version: 1) { token } }`,
	"activateDashboardVersion": `mutation { activateDashboardVersion(token: "d1", version: 1) { token } }`,
	"shareDashboard":           `mutation { shareDashboard(token: "d1", anchors: []) { token } }`,
	"deleteDashboard":          `mutation { deleteDashboard(token: "d1") }`,
}

func TestEveryOperationIsClassifiedForScopedMembers(t *testing.T) {
	var fields []string
	for _, block := range regexp.MustCompile(`(?s)type (?:Query|Mutation) \{(.*?)\n\}`).FindAllStringSubmatch(SchemaContent, -1) {
		for _, m := range regexp.MustCompile(`(?m)^    ([a-zA-Z]+)(?:\(|: [A-Z\[])`).FindAllStringSubmatch(block[1], -1) {
			fields = append(fields, m[1])
		}
	}
	sort.Strings(fields)
	classified := append([]string{}, scopedOptIns...)
	for name := range scopedRefusals {
		classified = append(classified, name)
	}
	sort.Strings(classified)
	require.Equal(t, fields, classified)

	f := newShareFixture(t)
	f.dashboard(t, "acme-tenant", "d1", true, "acme")
	member := f.as("acme-tenant", []string{"customer:acme"}, auth.AuthorityAll)
	for name, query := range scopedRefusals {
		res := exec(member, query, nil)
		require.NotEmpty(t, res.Errors, "%s answered a scoped member", name)
		require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrScopeUnsupported),
			"%s: want the scope refusal, got %v", name, res.Errors[0])
	}
}
