// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func deviceConfigurationCtx(t *testing.T) context.Context {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Device{}, &model.DeviceType{}, &model.DeviceCredential{},
		&model.DeviceReplacement{}, &model.EntityAttribute{}, &model.EntityGroup{},
		&model.EntityGroupVersion{}, &model.EntityGroupMembership{}, &model.EntityGroupFacetRef{},
		&model.EntityRelationship{}, &model.Alarm{}, &model.DeviceProfile{}, &model.DeviceProfileVersion{},
		&model.MetricDefinition{}, &model.CommandDefinition{}, &model.DetectionRule{},
		&model.DetectionRuleScopeRef{}, &model.DeviceConfigurationRevision{},
		&model.DeviceConfigurationState{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	ctx = withAuthorities(ctx, auth.DeviceWrite, auth.DeviceRead)
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))

	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	if _, err := api.CreateDeviceProfile(ctx, &model.DeviceProfileCreateRequest{Token: "prof"}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", []model.ConfigurationKey{
		{Key: "interval", ValueType: "LONG"}, {Key: "label", ValueType: "STRING"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	prof := "prof"
	if _, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "sensor", ProfileToken: &prof}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: "dev", DeviceTypeToken: "sensor"}); err != nil {
		t.Fatal(err)
	}
	return ctx
}

const setSharedAttrMutation = `
mutation ($key: String!, $type: String!, $value: String) {
  setEntityAttribute(request: {entityType: "device", entity: "dev", scope: "SHARED", attrKey: $key, valueType: $type, value: $value}) { attrKey }
}`

const deviceConfigurationQuery = `
query ($token: String!) {
  deviceConfiguration(deviceToken: $token) {
    desired { revision digest document actor }
    reported { revision }
    pending stale undeclared invalid
  }
}`

// The configuration is read over the wire: the document is the canonical text, the
// digest is over exactly it, an undeclared SHARED key is listed and not sent, and nothing
// reported means pending.
func TestGraphQLReadsDeviceConfiguration(t *testing.T) {
	ctx := deviceConfigurationCtx(t)
	execProfileGql(t, ctx, setSharedAttrMutation, map[string]any{"key": "interval", "type": "LONG", "value": "30.0"})
	execProfileGql(t, ctx, setSharedAttrMutation, map[string]any{"key": "zone", "type": "STRING", "value": "north"})

	data := execProfileGql(t, ctx, deviceConfigurationQuery, map[string]any{"token": "dev"})
	want := `{"deviceConfiguration":{"desired":{"revision":1,"digest":"` +
		model.ConfigurationDigest([]byte(`{"interval":30}`)) +
		`","document":"{\"interval\":30}","actor":""},"reported":null,"pending":true,"stale":false,"undeclared":["zone"],"invalid":[]}}`
	if string(data) != want {
		t.Fatalf("read\n%s\nwant\n%s", data, want)
	}

	data = execProfileGql(t, ctx, deviceConfigurationQuery, map[string]any{"token": "nope"})
	if string(data) != `{"deviceConfiguration":null}` {
		t.Fatalf("unknown device read %s", data)
	}
	data = execProfileGql(t, ctx, `query { deviceConfigurationRevisions(deviceToken: "dev", pagination: {pageNumber: 1, pageSize: 5}) { results { revision document } } }`, nil)
	if string(data) != `{"deviceConfigurationRevisions":{"results":[{"revision":1,"document":"{\"interval\":30}"}]}}` {
		t.Fatalf("revisions read %s", data)
	}
}

// The over-cap refusal reaches the client with its code.
func TestGraphQLOversizeConfigurationCarriesItsCode(t *testing.T) {
	ctx := deviceConfigurationCtx(t)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(ctx, setSharedAttrMutation, "", map[string]any{
		"key": "label", "type": "STRING", "value": strings.Repeat("x", model.MaxConfigurationDocumentBytes),
	})
	if len(resp.Errors) != 1 || resp.Errors[0].Extensions["code"] != model.CodeConfigurationTooLarge {
		t.Fatalf("errors = %v, want one carrying %s", resp.Errors, model.CodeConfigurationTooLarge)
	}
}

// No mutation writes a device's reported configuration state: it is the device's own
// report, written only by the device-report path. Pinned by listing every mutation.
func TestNoMutationWritesReportedConfigurationState(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(context.Background(),
		`{ __type(name: "Mutation") { fields { name type { name ofType { name } } } } }`, "", nil)
	if len(resp.Errors) > 0 {
		t.Fatal(resp.Errors)
	}
	var parsed struct {
		Type struct {
			Fields []struct {
				Name string `json:"name"`
				Type struct {
					Name   *string `json:"name"`
					OfType *struct {
						Name *string `json:"name"`
					} `json:"ofType"`
				} `json:"type"`
			} `json:"fields"`
		} `json:"__type"`
	}
	if err := json.Unmarshal(resp.Data, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Type.Fields) < 50 {
		t.Fatalf("listed only %d mutations; the introspection read is broken", len(parsed.Type.Fields))
	}
	for _, f := range parsed.Type.Fields {
		lower := strings.ToLower(f.Name)
		returns := ""
		if f.Type.Name != nil {
			returns = *f.Type.Name
		} else if f.Type.OfType != nil && f.Type.OfType.Name != nil {
			returns = *f.Type.OfType.Name
		}
		if strings.Contains(lower, "reported") || strings.Contains(lower, "configurationstate") ||
			strings.Contains(lower, "configurationreport") || strings.Contains(lower, "configurationrevision") ||
			strings.HasPrefix(returns, "DeviceConfiguration") {
			t.Errorf("mutation %s (returns %s) writes device configuration state", f.Name, returns)
		}
	}
}

// Every field of the new types carries a description the served schema actually exposes.
func TestDeviceConfigurationTypesAreDescribed(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	for _, typ := range []string{"DeviceConfiguration", "DeviceConfigurationRevision", "DeviceConfigurationReport",
		"DeviceConfigurationRevisionSearchResults"} {
		resp := schema.Exec(context.Background(),
			`query ($n: String!) { __type(name: $n) { description fields { name description } } }`, "",
			map[string]any{"n": typ})
		if len(resp.Errors) > 0 {
			t.Fatal(resp.Errors)
		}
		var parsed struct {
			Type *struct {
				Description string `json:"description"`
				Fields      []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"fields"`
			} `json:"__type"`
		}
		if err := json.Unmarshal(resp.Data, &parsed); err != nil || parsed.Type == nil {
			t.Fatalf("%s: not in the schema (%v)", typ, err)
		}
		if strings.TrimSpace(parsed.Type.Description) == "" {
			t.Errorf("%s has no description", typ)
		}
		if typ == "DeviceConfigurationRevisionSearchResults" {
			continue // results/pagination follow the shared search-results shape
		}
		for _, f := range parsed.Type.Fields {
			if strings.TrimSpace(f.Description) == "" {
				t.Errorf("%s.%s has no description", typ, f.Name)
			}
		}
	}
}
