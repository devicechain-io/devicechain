// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/errors"
)

// The wire half of dashboard publishing, driven through the real schema so the gates and
// the SDL are exercised together. The draft is author-only (dashboard:write); the
// published snapshot is what dashboard:read serves.

const (
	publishedDef = `{"schemaVersion":1,"widgets":[{"id":"published"}]}`
	draftDef     = `{"schemaVersion":1,"widgets":[{"id":"draft"}]}`
)

func execDoc(t *testing.T, ctx context.Context, doc string) *gql.Response {
	t.Helper()
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	return schema.Exec(ctx, doc, "", nil)
}

// seedPublishedThenEdited leaves a board whose live version differs from its draft.
func seedPublishedThenEdited(t *testing.T) (*model.Api, context.Context) {
	t.Helper()
	api, ctx := newWireFixture(t)
	if _, err := api.CreateDashboard(ctx, &model.DashboardCreateRequest{Token: "d", Definition: publishedDef}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := api.PublishDashboard(ctx, "d", nil, nil, "alice", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := api.UpdateDashboard(ctx, "d", &model.DashboardUpdateRequest{
		Definition: gqlcore.OptionalStringOf(draftDef)}, nil); err != nil {
		t.Fatal(err)
	}
	return api, ctx
}

func hasErr(res *gql.Response) bool { return len(res.Errors) > 0 }

func code(e *errors.QueryError) string {
	if c, ok := e.Extensions["code"].(string); ok {
		return c
	}
	return ""
}

func formatTime(t time.Time) string { return *gqlcore.FormatTime(t) }

// A read-only member must not read the draft, and is told so rather than served "".
func TestDraftDefinitionIsAuthorOnly(t *testing.T) {
	_, ctx := seedPublishedThenEdited(t)
	const doc = `{ dashboard(token: "d") { token definition } }`

	res := execDoc(t, withAuthorities(ctx, viewerBaseline...), doc)
	if !hasErr(res) {
		t.Fatalf("a read-only caller read the draft definition: %s", res.Data)
	}
	if string(res.Data) != "null" && string(res.Data) != `{"dashboard":null}` {
		t.Fatalf("the draft leaked alongside the error: %s", res.Data)
	}

	res = execDoc(t, withAuthorities(ctx, auth.DashboardRead, auth.DashboardWrite), doc)
	if hasErr(res) {
		t.Fatalf("an author could not read their own draft: %v", res.Errors)
	}
}

// Metadata stays readable with dashboard:read, including the pointer.
func TestDraftMetadataStaysReadableWithDashboardRead(t *testing.T) {
	_, ctx := seedPublishedThenEdited(t)
	res := execDoc(t, withAuthorities(ctx, auth.DashboardRead),
		`{ dashboard(token: "d") { token publishedVersion } dashboards(criteria:{pageNumber:1,pageSize:10}) { results { token publishedVersion } } }`)
	if hasErr(res) {
		t.Fatalf("metadata read refused: %v", res.Errors)
	}
	var data struct {
		Dashboard struct {
			PublishedVersion *int32 `json:"publishedVersion"`
		} `json:"dashboard"`
		Dashboards struct {
			Results []struct {
				PublishedVersion *int32 `json:"publishedVersion"`
			} `json:"results"`
		} `json:"dashboards"`
	}
	_ = json.Unmarshal(res.Data, &data)
	if data.Dashboard.PublishedVersion == nil || *data.Dashboard.PublishedVersion != 1 {
		t.Fatalf("publishedVersion = %v, want 1: %s", data.Dashboard.PublishedVersion, res.Data)
	}
	if len(data.Dashboards.Results) != 1 || data.Dashboards.Results[0].PublishedVersion == nil {
		t.Fatalf("the list row lost the pointer: %s", res.Data)
	}
}

func TestVersionBodiesAreAuthorOnly(t *testing.T) {
	_, ctx := seedPublishedThenEdited(t)
	const doc = `{ dashboardVersion(token: "d", version: 1) { version definition } }`

	res := execDoc(t, withAuthorities(ctx, viewerBaseline...), doc)
	if !hasErr(res) {
		t.Fatalf("a read-only caller read a version body: %s", res.Data)
	}
	res = execDoc(t, withAuthorities(ctx, auth.DashboardRead, auth.DashboardWrite), doc)
	if hasErr(res) {
		t.Fatalf("an author could not read a version body: %v", res.Errors)
	}
	var data struct {
		DashboardVersion struct{ Definition string } `json:"dashboardVersion"`
	}
	_ = json.Unmarshal(res.Data, &data)
	if data.DashboardVersion.Definition != publishedDef {
		t.Fatalf("version body = %q, want %q", data.DashboardVersion.Definition, publishedDef)
	}
}

// dashboard:read serves the published snapshot, never the draft.
func TestPublishedDashboardIsServedToAReader(t *testing.T) {
	_, ctx := seedPublishedThenEdited(t)
	res := execDoc(t, withAuthorities(ctx, auth.DashboardRead),
		`{ publishedDashboard(token: "d") { token version definition } }`)
	if hasErr(res) {
		t.Fatalf("published read refused: %v", res.Errors)
	}
	var data struct {
		PublishedDashboard struct {
			Version    int32
			Definition string
		} `json:"publishedDashboard"`
	}
	_ = json.Unmarshal(res.Data, &data)
	if data.PublishedDashboard.Definition != publishedDef || data.PublishedDashboard.Version != 1 {
		t.Fatalf("served %+v, want version 1 of %q (the draft is %q)", data.PublishedDashboard, publishedDef, draftDef)
	}
}

func TestPublishedDashboardRefusesWithoutTheAuthority(t *testing.T) {
	_, ctx := seedPublishedThenEdited(t)
	const doc = `{ publishedDashboard(token: "d") { version } }`
	res := execDoc(t, withAuthorities(ctx, auth.DeviceRead), doc)
	if !hasErr(res) {
		t.Fatalf("a caller without dashboard:read was served: %s", res.Data)
	}
	anon := context.WithValue(core.WithTenant(context.Background(), "acme"),
		gqlcore.ContextApiKey, ctx.Value(gqlcore.ContextApiKey))
	res = execDoc(t, anon, doc)
	if !hasErr(res) {
		t.Fatalf("an anonymous caller was served: %s", res.Data)
	}
}

// A never-published board answers a typed error, not a plausible blank board.
func TestPublishedDashboardOfAnUnpublishedBoardIsNotPublished(t *testing.T) {
	api, ctx := newWireFixture(t)
	if _, err := api.CreateDashboard(ctx, &model.DashboardCreateRequest{Token: "d", Definition: publishedDef}); err != nil {
		t.Fatal(err)
	}
	res := execDoc(t, withAuthorities(ctx, auth.DashboardRead), `{ publishedDashboard(token: "d") { version } }`)
	if len(res.Errors) != 1 || code(res.Errors[0]) != "NOT_PUBLISHED" {
		t.Fatalf("errors = %v, want one NOT_PUBLISHED", res.Errors)
	}
}

// An unknown token (and another tenant's token) is simply null, like `dashboard`.
func TestPublishedDashboardIsTenantScoped(t *testing.T) {
	_, ctx := seedPublishedThenEdited(t)
	other := core.WithTenant(ctx, "other")
	res := execDoc(t, withAuthorities(other, auth.DashboardRead), `{ publishedDashboard(token: "d") { version } }`)
	if hasErr(res) || string(res.Data) != `{"publishedDashboard":null}` {
		t.Fatalf("another tenant saw %s / %v", res.Data, res.Errors)
	}
}

func TestPublishReportsTheUnchangedUpdatedAtAndMovesThePointer(t *testing.T) {
	api, ctx := newWireFixture(t)
	created, err := api.CreateDashboard(ctx, &model.DashboardCreateRequest{Token: "d", Definition: publishedDef})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	res := execDoc(t, ctx, `mutation { publishDashboard(token: "d") { version { version } dashboard { updatedAt publishedVersion } } }`)
	if hasErr(res) {
		t.Fatalf("publish: %v", res.Errors)
	}
	var data struct {
		PublishDashboard struct {
			Version   struct{ Version int32 }
			Dashboard struct {
				UpdatedAt        string
				PublishedVersion *int32
			}
		} `json:"publishDashboard"`
	}
	_ = json.Unmarshal(res.Data, &data)
	got := data.PublishDashboard
	if got.Dashboard.PublishedVersion == nil || *got.Dashboard.PublishedVersion != 1 || got.Version.Version != 1 {
		t.Fatalf("publish result %s", res.Data)
	}
	if want := formatTime(created.UpdatedAt); got.Dashboard.UpdatedAt != want {
		t.Fatalf("updatedAt moved on publish: created %s, reported %s", want, got.Dashboard.UpdatedAt)
	}
}

func TestActivateRequiresWriteAndMovesThePointer(t *testing.T) {
	api, ctx := seedPublishedThenEdited(t)
	if _, _, err := api.PublishDashboard(ctx, "d", nil, nil, "alice", nil); err != nil { // v2
		t.Fatal(err)
	}
	const doc = `mutation { activateDashboardVersion(token: "d", version: 1) { token publishedVersion } }`

	res := execDoc(t, withAuthorities(ctx, viewerBaseline...), doc)
	if !hasErr(res) {
		t.Fatalf("a read-only caller activated a version: %s", res.Data)
	}
	res = execDoc(t, withAuthorities(ctx, auth.DashboardRead, auth.DashboardWrite), doc)
	if hasErr(res) || string(res.Data) != `{"activateDashboardVersion":{"token":"d","publishedVersion":1}}` {
		t.Fatalf("activate = %s / %v", res.Data, res.Errors)
	}
}

func TestActivateIsTenantScoped(t *testing.T) {
	_, ctx := seedPublishedThenEdited(t)
	other := withAuthorities(core.WithTenant(ctx, "other"), auth.DashboardRead, auth.DashboardWrite)
	res := execDoc(t, other, `mutation { activateDashboardVersion(token: "d", version: 1) { token } }`)
	if !hasErr(res) {
		t.Fatalf("another tenant activated a version: %s", res.Data)
	}
}

// publishedAt on Dashboard is the live version's publish time, and null before the first
// publish.
func TestDashboardPublishedAt(t *testing.T) {
	api, ctx := newWireFixture(t)
	for _, tok := range []string{"d", "other"} {
		if _, err := api.CreateDashboard(ctx, &model.DashboardCreateRequest{Token: tok, Definition: publishedDef}); err != nil {
			t.Fatal(err)
		}
	}
	const doc = `{ dashboard(token: "d") { publishedVersion publishedAt } }`
	res := execDoc(t, ctx, doc)
	if hasErr(res) || string(res.Data) != `{"dashboard":{"publishedVersion":null,"publishedAt":null}}` {
		t.Fatalf("unpublished = %s / %v", res.Data, res.Errors)
	}

	// "other" publishes first, so a lookup that ignored the dashboard would find its row.
	if _, _, err := api.PublishDashboard(ctx, "other", nil, nil, "alice", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	v, _, err := api.PublishDashboard(ctx, "d", nil, nil, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	res = execDoc(t, ctx, doc)
	if hasErr(res) {
		t.Fatalf("published read: %v", res.Errors)
	}
	var data struct {
		Dashboard struct {
			PublishedAt string `json:"publishedAt"`
		} `json:"dashboard"`
	}
	_ = json.Unmarshal(res.Data, &data)
	got, err := time.Parse(time.RFC3339Nano, data.Dashboard.PublishedAt)
	if err != nil {
		t.Fatalf("publishedAt %q: %v", data.Dashboard.PublishedAt, err)
	}
	if got.UnixMicro() != v.CreatedAt.UnixMicro() {
		t.Fatalf("publishedAt = %s, want the version's creation time %s", got, v.CreatedAt)
	}
}
