// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/devicechain-io/dc-update-management/schema"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// TestSchemaParses validates that the GraphQL schema parses against the resolver -
// every schema field must have a matching resolver method.
func TestSchemaParses(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("schema failed to parse against resolver: %v", r)
		}
	}()
	gql.MustParseSchema(SchemaContent, &SchemaResolver{})
}

// introspect runs an introspection query with string descriptions enabled, because
// descriptions written as """ strings are only served when the schema is built that way
// (otherwise graphql-go serves # comments as descriptions).
func introspect(t *testing.T, query string, into any) {
	t.Helper()
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{}, gql.UseStringDescriptions())
	resp := schema.Exec(context.Background(), query, "", nil)
	require.Empty(t, resp.Errors)
	require.NoError(t, json.Unmarshal(resp.Data, into))
}

// The plane's operations are exactly the ones the catalogue offers. A new operation is a
// deliberate edit to this list, and brings its authority gate with it.
func TestTheOperationSetIsExactlyTheCatalogue(t *testing.T) {
	var got struct {
		Schema struct {
			QueryType        struct{ Fields []struct{ Name string } }
			MutationType     struct{ Fields []struct{ Name string } }
			SubscriptionType *struct{ Name string }
		} `json:"__schema"`
	}
	introspect(t, `{ __schema { queryType { fields { name } } mutationType { fields { name } } subscriptionType { name } } }`, &got)
	names := func(fs []struct{ Name string }) []string {
		out := []string{}
		for _, f := range fs {
			out = append(out, f.Name)
		}
		sort.Strings(out)
		return out
	}
	require.Equal(t, []string{"artifact", "artifactSigner", "artifactSigners", "artifacts", "updateAssignment", "updateAssignments", "updateManagementInfo", "updateRollout", "updateRolloutTargets", "updateRollouts"}, names(got.Schema.QueryType.Fields))
	require.Equal(t, []string{"abandonUnknownUpdate", "abortUpdateRollout", "addArtifactSignature", "assignArtifact", "cancelUpdate", "createArtifact", "createUpdateRollout", "pauseUpdateRollout", "registerArtifactSigner", "resumeUpdateRollout", "retireArtifact", "revokeArtifactSigner", "startUpdateRollout"}, names(got.Schema.MutationType.Fields))
	require.Nil(t, got.Schema.SubscriptionType)
}

// Every type, field, argument, input field and enum value this SDL declares carries a
// description, so the generated reference documents the whole plane.
func TestEverySchemaElementIsDescribed(t *testing.T) {
	type described struct {
		Name        string
		Description *string
	}
	var got struct {
		Schema struct {
			Types []struct {
				Name        string
				Description *string
				Fields      []struct {
					Name        string
					Description *string
					Args        []described
				}
				InputFields []described
				EnumValues  []described
			}
		} `json:"__schema"`
	}
	introspect(t, `{ __schema { types { name description
	  fields { name description args { name description } }
	  inputFields { name description } enumValues { name description } } } }`, &got)

	var undescribed []string
	check := func(what string, d *string) {
		described := d != nil && strings.TrimSpace(*d) != ""
		if !described {
			undescribed = append(undescribed, what)
		}
	}
	for _, ty := range got.Schema.Types {
		if strings.HasPrefix(ty.Name, "__") || isBuiltin(ty.Name) {
			continue
		}
		check("type "+ty.Name, ty.Description)
		for _, f := range ty.Fields {
			check(ty.Name+"."+f.Name, f.Description)
			for _, a := range f.Args {
				check(ty.Name+"."+f.Name+"("+a.Name+")", a.Description)
			}
		}
		for _, f := range ty.InputFields {
			check(ty.Name+"."+f.Name, f.Description)
		}
		for _, v := range ty.EnumValues {
			check(ty.Name+"."+v.Name, v.Description)
		}
	}
	require.Empty(t, undescribed, "SDL elements without a description")
}

func isBuiltin(name string) bool {
	switch name {
	case "String", "Int", "Float", "Boolean", "ID":
		return true
	}
	return false
}

// ---- operations through the real schema ----

type fakeProfiles struct{}

func (fakeProfiles) Missing(_ context.Context, _ string, tokens []string) ([]string, error) {
	var missing []string
	for _, t := range tokens {
		if t != "gateway" {
			missing = append(missing, t)
		}
	}
	return missing, nil
}

type plane struct {
	t      *testing.T
	schema *gql.Schema
	api    *model.Api
}

func newPlane(t *testing.T) *plane {
	t.Helper()
	db := sqlitetest.OpenMemory(t, nil)
	// The chain runs before the tenant callbacks exist, as core/rdb runs it in the system
	// context: gorm's AutoMigrate reads an existing table through a statement the tenant
	// scope cannot classify.
	for _, m := range schema.Migrations {
		require.NoError(t, m.Migrate(db))
	}
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	store, err := blob.New(context.Background(), blob.Config{Backend: blob.BackendFilesystem, Directory: t.TempDir()}, "inst")
	require.NoError(t, err)
	api := model.NewApi(&rdb.RdbManager{Database: db}, store, model.Limits{
		MaxArtifactBytes: 1 << 20, ConfiguredMaxArtifactBytes: 1 << 20, MaxTenantArtifactBytes: 1 << 30,
	})
	api.Profiles = fakeProfiles{}
	return &plane{t: t, api: api, schema: gql.MustParseSchema(SchemaContent, &SchemaResolver{Area: "update-management"})}
}

func (p *plane) ctx(tenant string, authorities ...auth.Authority) context.Context {
	as := make([]string, len(authorities))
	for i, a := range authorities {
		as[i] = string(a)
	}
	ctx := context.WithValue(context.Background(), gqlcore.ContextApiKey, p.api)
	ctx = core.WithTenant(ctx, tenant)
	return auth.WithClaims(ctx, &auth.Claims{Tenant: tenant, TokenType: auth.TokenTypeAccess, Authorities: as})
}

func (p *plane) exec(ctx context.Context, query string, vars map[string]any) *gql.Response {
	return p.schema.Exec(ctx, query, "", vars)
}

func code(r *gql.Response) string {
	if len(r.Errors) == 0 {
		return ""
	}
	if c, ok := r.Errors[0].Extensions["code"].(string); ok {
		return c
	}
	return r.Errors[0].Message
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

const createMutation = `mutation($r: ArtifactCreateRequest!) {
  createArtifact(request: $r) { token kind name version component sizeBytes digest mediaType
    compatibleProfiles trustStatus provenance state } }`

func createVars(token string, content []byte) map[string]any {
	return map[string]any{"r": map[string]any{
		"token": token, "kind": "FIRMWARE", "name": "fw-" + token, "version": "1.0", "component": "firmware",
		"sizeBytes": fmt.Sprint(len(content)), "digest": digest(content), "compatibleProfiles": []any{"gateway"},
	}}
}

func TestOperationsAreGatedByTheirAuthority(t *testing.T) {
	p := newPlane(t)
	content := []byte("image")

	// No claims at all.
	bare := context.WithValue(context.Background(), gqlcore.ContextApiKey, p.api)
	require.Equal(t, auth.ErrUnauthenticated.Error(), code(p.exec(bare, `{ artifact(token:"x"){ token } }`, nil)))
	require.Equal(t, auth.ErrUnauthenticated.Error(), code(p.exec(bare, createMutation, createVars("fw", content))))

	// Read does not grant write, and write does not grant read.
	reader := p.ctx("acme", auth.ArtifactRead)
	writer := p.ctx("acme", auth.ArtifactWrite)
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(reader, createMutation, createVars("fw", content))))
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(reader, `mutation { retireArtifact(token:"fw"){ token } }`, nil)))
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(writer, `{ artifact(token:"fw"){ token } }`, nil)))
	require.Equal(t, auth.ErrForbidden.Error(), code(p.exec(writer,
		`{ artifacts(criteria:{pageNumber:1,pageSize:5}){ results { token } } }`, nil)))
	// The identity probe is open to any caller of the plane.
	require.Empty(t, p.exec(bare, `{ updateManagementInfo { area } }`, nil).Errors)
}

func TestCreateReadSearchRetireRoundTrip(t *testing.T) {
	p := newPlane(t)
	content := []byte("image")
	writer, reader := p.ctx("acme", auth.ArtifactWrite), p.ctx("acme", auth.ArtifactRead)

	resp := p.exec(writer, createMutation, createVars("fw-1", content))
	require.Empty(t, resp.Errors)
	var created struct {
		CreateArtifact struct {
			Token, Kind, Name, Version, Component, SizeBytes, Digest, MediaType, TrustStatus, State string
			CompatibleProfiles                                                                      []string
			Provenance                                                                              *string
		}
	}
	require.NoError(t, json.Unmarshal(resp.Data, &created))
	a := created.CreateArtifact
	require.Equal(t, "UPLOADING", a.State)
	require.Equal(t, "UNVERIFIED", a.TrustStatus)
	require.Equal(t, "5", a.SizeBytes, "sizeBytes is a decimal string")
	require.Equal(t, digest(content), a.Digest)
	require.Equal(t, []string{"gateway"}, a.CompatibleProfiles)

	resp = p.exec(reader, `{ artifact(token:"fw-1"){ token state } artifacts(criteria:{pageNumber:1,pageSize:5, state: UPLOADING}){
	  results { token } pagination { totalRecords } } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"artifact":{"token":"fw-1","state":"UPLOADING"},
	  "artifacts":{"results":[{"token":"fw-1"}],"pagination":{"totalRecords":1}}}`, string(resp.Data))

	resp = p.exec(writer, `mutation { retireArtifact(token:"fw-1"){ state } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"retireArtifact":{"state":"RETIRED"}}`, string(resp.Data))
}

func TestCreateRefusalsCarryTheirCodes(t *testing.T) {
	p := newPlane(t)
	writer := p.ctx("acme", auth.ArtifactWrite)
	content := []byte("image")

	vars := createVars("fw-1", content)
	vars["r"].(map[string]any)["compatibleProfiles"] = []any{"ghost"}
	require.Equal(t, model.CodeUnknownCompatibleProfile, code(p.exec(writer, createMutation, vars)))

	vars = createVars("fw-1", content)
	vars["r"].(map[string]any)["sizeBytes"] = "not a number"
	require.Equal(t, model.CodeInvalidArtifact, code(p.exec(writer, createMutation, vars)))

	vars = createVars("fw-1", content)
	vars["r"].(map[string]any)["sizeBytes"] = "99999999999"
	require.Equal(t, model.CodeArtifactTooLarge, code(p.exec(writer, createMutation, vars)))

	// An enum value outside the vocabulary never reaches the resolver.
	vars = createVars("fw-1", content)
	vars["r"].(map[string]any)["kind"] = "BOOTLOADER"
	require.NotEmpty(t, p.exec(writer, createMutation, vars).Errors)
}

func TestTenantBCannotSeeTenantAsArtifactsThroughTheAPI(t *testing.T) {
	p := newPlane(t)
	require.Empty(t, p.exec(p.ctx("acme", auth.ArtifactWrite), createMutation, createVars("fw-1", []byte("image"))).Errors)

	other := p.ctx("globex", auth.ArtifactRead, auth.ArtifactWrite)
	resp := p.exec(other, `{ artifact(token:"fw-1"){ token } artifacts(criteria:{pageNumber:1,pageSize:5}){ results { token } } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"artifact":null,"artifacts":{"results":[]}}`, string(resp.Data))
	require.Equal(t, model.CodeArtifactNotFound, code(p.exec(other, `mutation { retireArtifact(token:"fw-1"){ state } }`, nil)))
}

// A plane served without its API is a wiring error and says so; it must not answer as an
// empty catalogue.
func TestAPlaneWithoutItsAPIFailsLoudly(t *testing.T) {
	p := newPlane(t)
	ctx := auth.WithClaims(core.WithTenant(context.Background(), "acme"),
		&auth.Claims{Tenant: "acme", TokenType: auth.TokenTypeAccess, Authorities: []string{string(auth.ArtifactRead)}})
	resp := p.exec(ctx, `{ artifacts(criteria:{pageNumber:1,pageSize:5}){ results { token } } }`, nil)
	require.NotEmpty(t, resp.Errors)
	require.Contains(t, resp.Errors[0].Message, "not wired")
}
