// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/semantic/packs"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// semanticFixture is the scope fixture with the semantic context table migrated.
func semanticFixture(t *testing.T) scopeFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	return seedScopeFixture(t, db, &model.SemanticContext{})
}

const setSemanticContextDoc = `mutation($r: SemanticContextRequest!) {
  setSemanticContext(request: $r) { enabledPacks customPrefixes { prefix namespace } packVersions { pack version } } }`

// The served context speaks SemanticPack values, a bound type reads back as its expanded IRI
// with a display CURIE, and an unknown term is INVALID_ARGUMENT naming the term on the wire.
func TestSemanticBindingsOverTheWire(t *testing.T) {
	f := semanticFixture(t)
	op := f.operator()

	res := serve(t, op, setSemanticContextDoc, map[string]any{"r": map[string]any{
		"enabledPacks": []any{"SDM_STREETLIGHTING", "BRICK"}}})
	if len(res.errs) > 0 {
		t.Fatalf("setSemanticContext: %v", res.errs)
	}
	got, _ := json.Marshal(res.data["setSemanticContext"])
	want := fmt.Sprintf(`{"customPrefixes":[],"enabledPacks":["BRICK","SDM_STREETLIGHTING"],"packVersions":[{"pack":"BRICK","version":%q},{"pack":"SDM_STREETLIGHTING","version":%q}]}`,
		packs.Brick.Version, packs.SDMStreetlighting.Version)
	if string(got) != want {
		t.Fatalf("context\n got %s\nwant %s", got, want)
	}

	create := `mutation($r: DeviceTypeCreateRequest) { createDeviceType(request: $r) { token semanticTypes { iri curie } } }`
	res = serve(t, op, create, map[string]any{"r": map[string]any{"token": "lamp",
		"semanticTypes": []any{"sdm-streetlighting:Streetlight", "https://brickschema.org/schema/Brick#Luminaire"}}})
	if len(res.errs) > 0 {
		t.Fatalf("createDeviceType: %v", res.errs)
	}
	got, _ = json.Marshal(res.data["createDeviceType"])
	want = `{"semanticTypes":[{"curie":"sdm-streetlighting:Streetlight","iri":"https://smartdatamodels.org/dataModel.Streetlighting/Streetlight"},{"curie":"brick:Luminaire","iri":"https://brickschema.org/schema/Brick#Luminaire"}],"token":"lamp"}`
	if string(got) != want {
		t.Fatalf("device type\n got %s\nwant %s", got, want)
	}

	res = serve(t, op, create, map[string]any{"r": map[string]any{"token": "bin", "semanticTypes": []any{"s4city:District"}}})
	if len(res.errs) != 1 {
		t.Fatalf("a term of a pack the tenant did not enable was accepted: %v", res.data)
	}
	ext := res.errs[0].Extensions
	if ext["code"] != model.CodeInvalidArgument || ext["term"] != "s4city:District" {
		t.Fatalf("refusal extensions %v, want INVALID_ARGUMENT naming the term", ext)
	}

	// An unbound type serves an empty list, not null.
	res = serve(t, op, `{ deviceTypesByToken(tokens: ["pump"]) { semanticTypes { iri } } }`, nil)
	if got, _ := json.Marshal(res.data); len(res.errs) > 0 || string(got) != `{"deviceTypesByToken":[{"semanticTypes":[]}]}` {
		t.Fatalf("unbound type: %s %v", got, res.errs)
	}
}

// A member limited to part of the tenant reads the context and the bindings (catalogue reads)
// but cannot change the context, even holding device:write.
func TestAScopedMemberReadsTheSemanticContextButCannotSetIt(t *testing.T) {
	f := semanticFixture(t)
	if res := serve(t, f.operator(), setSemanticContextDoc, map[string]any{"r": map[string]any{
		"enabledPacks": []any{"BRICK"}}}); len(res.errs) > 0 {
		t.Fatalf("operator set: %v", res.errs)
	}
	// The member holds device:write too, so a refusal is the scope's, not a missing authority.
	member := f.withClaimsScoped("customer:acme", "device:read", "device:write")

	res := serve(t, member, `{ semanticContext { enabledPacks } deviceTypesByToken(tokens: ["pump"]) { semanticTypes { iri } } }`, nil)
	if len(res.errs) > 0 {
		t.Fatalf("a scoped member could not read the context: %v", res.errs)
	}
	if got, _ := json.Marshal(res.data["semanticContext"]); string(got) != `{"enabledPacks":["BRICK"]}` {
		t.Fatalf("scoped read: %s", got)
	}

	res = serve(t, member, setSemanticContextDoc, map[string]any{"r": map[string]any{"enabledPacks": []any{}}})
	if len(res.errs) == 0 || !isScopeRefusal(res.errs[0]) {
		t.Fatalf("a scoped member's set was not refused by the scope: %v %v", res.data, res.errs)
	}
	// The refusal is the authority's, not a validation accident: the stored context is unchanged.
	sc, err := f.api.SemanticContext(f.operator())
	if err != nil || len(sc.EnabledPacks) != 1 || sc.EnabledPacks[0] != "brick" {
		t.Fatalf("context after a refused set: %v, %v", sc, err)
	}
}

// A list of types bound to a CUSTOM-prefix term (which no shipped pack explains) reads the
// tenant's prefixes once per request, not once per row.
func TestCustomPrefixCuriesAreReadOncePerRequest(t *testing.T) {
	f := semanticFixture(t)
	op := f.operator()
	if res := serve(t, op, setSemanticContextDoc, map[string]any{"r": map[string]any{"enabledPacks": []any{},
		"customPrefixes": []any{map[string]any{"prefix": "city", "namespace": "https://city.example/ontology#"}}}}); len(res.errs) > 0 {
		t.Fatalf("set: %v", res.errs)
	}
	tokens := []string{}
	for i := 0; i < 5; i++ {
		tok := fmt.Sprintf("bin-%d", i)
		tokens = append(tokens, tok)
		terms := []string{"city:SmartBin"}
		if _, err := f.api.CreateDeviceType(op, &model.DeviceTypeCreateRequest{Token: tok, SemanticTypes: &terms}); err != nil {
			t.Fatal(err)
		}
	}

	var reads atomic.Int32
	const cb = "test:count-semantic-context-reads"
	if err := f.db.Callback().Query().Before("gorm:query").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Table == "semantic_contexts" {
			reads.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(cb) })

	quoted, _ := json.Marshal(tokens)
	res := serve(t, gqlcore.WithRequestMemo(op), `{ deviceTypesByToken(tokens: `+string(quoted)+`) { semanticTypes { curie } } }`, nil)
	if len(res.errs) > 0 {
		t.Fatal(res.errs)
	}
	if got, _ := json.Marshal(res.data); strings.Count(string(got), `"city:SmartBin"`) != len(tokens) {
		t.Fatalf("custom CURIEs: %s", got)
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("%d rows read the semantic context %d times; want once per request", len(tokens), n)
	}
}

// The served SemanticPack enum names exactly the packs this build ships, so neither can
// grow without the other.
func TestSemanticPackEnumMatchesTheShippedPacks(t *testing.T) {
	block := regexp.MustCompile(`(?s)\nenum SemanticPack \{(.*?)\n\}`).FindStringSubmatch(SchemaContent)
	if block == nil {
		t.Fatal("no SemanticPack enum in the schema")
	}
	var served []string
	for _, line := range strings.Split(block[1], "\n") {
		line = strings.TrimSpace(line)
		if regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`).MatchString(line) {
			served = append(served, line)
		}
	}
	var shipped []string
	for _, p := range packs.All() {
		v, err := semanticPackEnum(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if id, err := semanticPackID(v); err != nil || id != p.ID {
			t.Fatalf("%s does not map back to %s: %q, %v", v, p.ID, id, err)
		}
		shipped = append(shipped, v)
	}
	sort.Strings(served)
	sort.Strings(shipped)
	if strings.Join(served, ",") != strings.Join(shipped, ",") {
		t.Fatalf("SemanticPack enum %v, shipped packs %v", served, shipped)
	}
}

// Mutation fields run in order, so a field after setSemanticContext renders CURIEs under the
// context it set, not under one memoized earlier in the same document.
func TestACurieAfterSetSemanticContextUsesTheNewPrefix(t *testing.T) {
	f := semanticFixture(t)
	op := f.operator()
	const ns = "https://city.example/ontology#"
	if res := serve(t, op, setSemanticContextDoc, map[string]any{"r": map[string]any{"enabledPacks": []any{},
		"customPrefixes": []any{map[string]any{"prefix": "ex", "namespace": ns}}}}); len(res.errs) > 0 {
		t.Fatalf("set: %v", res.errs)
	}
	doc := `mutation($r: SemanticContextRequest!) {
  before: createDeviceType(request: {token: "bin", semanticTypes: ["ex:SmartBin"]}) { semanticTypes { curie } }
  setSemanticContext(request: $r) { enabledPacks }
  after: createDeviceType(request: {token: "bin2", semanticTypes: ["city:SmartBin"]}) { semanticTypes { curie } } }`
	res := serve(t, gqlcore.WithRequestMemo(op), doc, map[string]any{"r": map[string]any{"enabledPacks": []any{},
		"customPrefixes": []any{map[string]any{"prefix": "city", "namespace": ns}}}})
	if len(res.errs) > 0 {
		t.Fatal(res.errs)
	}
	got, _ := json.Marshal([]any{res.data["before"], res.data["after"]})
	if want := `[{"semanticTypes":[{"curie":"ex:SmartBin"}]},{"semanticTypes":[{"curie":"city:SmartBin"}]}]`; string(got) != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}
