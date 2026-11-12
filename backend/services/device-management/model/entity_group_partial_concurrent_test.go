// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"gorm.io/gorm"
)

const (
	groupPartialSelectorA = `attr["climate"] == "arid"`
	groupPartialSelectorB = `attr["climate"] == "humid"`
)

func entityGroupPartialFixture(t *testing.T, dynamic bool) (*Api, context.Context, *rdbtest.StatementCounter) {
	t.Helper()
	api := newGroupTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	name, metadata := "Original", `{"seed":true}`
	req := &EntityGroupCreateRequest{Token: "g1", MemberType: "device", Name: &name, Metadata: &metadata}
	if dynamic {
		mode, source := string(MembershipDynamic), groupPartialSelectorA
		req.MembershipMode, req.Selector = &mode, &source
	}
	if _, err := api.CreateEntityGroup(ctx, req); err != nil {
		t.Fatalf("seed: %v", err)
	}
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, statements
}

func reloadPartialEntityGroup(t *testing.T, api *Api, ctx context.Context) *EntityGroup {
	t.Helper()
	rows, err := api.EntityGroupsByToken(ctx, []string{"g1"})
	return requireOne(t, "entity group", rows, err)
}

func TestEntityGroupPartialConcurrentRenamePreservesSelectorAndPublishedPointer(t *testing.T) {
	api, ctx, statements := entityGroupPartialFixture(t, true)
	// Compile the same device-family fixture used by the existing selector tests.
	compiled, err := selector.Compile(groupPartialSelectorB, "device")
	if err != nil {
		t.Fatal(err)
	}
	fired := false
	const callback = "test:entity_group_other_writer"
	err = api.RDB.Database.Callback().Query().After("gorm:after_query").
		Register(callback, func(tx *gorm.DB) {
			if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "entity_groups" {
				return
			}
			fired = true
			// The rename has read A. Land the newly compiled live selector and a
			// published-pointer move before its write, without timing or goroutines.
			tx.AddError(api.RDB.DB(ctx).Model(&EntityGroup{}).Where("token = ?", "g1").
				Updates(map[string]any{
					"selector": compiled.Source(), "selector_schema": selector.SchemaVersion,
					"active_version": 2,
				}).Error)
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { api.RDB.Database.Callback().Query().Remove(callback) })
	updated, err := api.UpdateEntityGroup(ctx, "g1", &EntityGroupUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the interleaved writer never ran")
	}
	stored := reloadPartialEntityGroup(t, api, ctx)
	for _, row := range []*EntityGroup{stored, updated} {
		if row.Name.String != "Renamed" || !row.Name.Valid ||
			row.Selector != (sql.NullString{String: compiled.Source(), Valid: true}) ||
			row.SelectorSchema != selector.SchemaVersion ||
			row.ActiveVersion != (sql.NullInt32{Int32: 2, Valid: true}) {
			t.Fatalf("rename lost a concurrent value: %#v", row)
		}
	}
	renames := 0
	for _, statement := range customerPartialWrites(statements) {
		normalized := strings.NewReplacer("`", "", "\"", "").Replace(statement)
		if !strings.HasPrefix(normalized, "UPDATE entity_groups SET ") || !strings.Contains(normalized, "name=") {
			continue
		}
		renames++
		for _, column := range []string{"selector", "selector_schema", "active_version", "token", "tenant_id", "member_type", "membership_mode", "metadata", "access_scope_at", "created_at"} {
			setClause := strings.SplitN(normalized, " WHERE ", 2)[0]
			if strings.Contains(setClause, column+"=") {
				t.Fatalf("rename assigned omitted/immutable %s: %s", column, statement)
			}
		}
	}
	if renames != 1 {
		t.Fatalf("got %d rename UPDATEs, want one; SQL: %v", renames, statements.MarkedSQL())
	}
}

func TestEntityGroupPartialConcurrentSelectorReplacementWritesPair(t *testing.T) {
	api, ctx, statements := entityGroupPartialFixture(t, true)
	// Make the schema assertion prove that replacement stamps the compiler's
	// schema, rather than merely retaining the value seeded by create.
	if err := api.RDB.DB(ctx).Model(&EntityGroup{}).Where("token = ?", "g1").
		Update("selector_schema", 0).Error; err != nil {
		t.Fatal(err)
	}
	updated, err := api.UpdateEntityGroup(ctx, "g1", &EntityGroupUpdateRequest{
		Selector: dcgraphql.OptionalStringOf(groupPartialSelectorB),
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := reloadPartialEntityGroup(t, api, ctx)
	for _, row := range []*EntityGroup{stored, updated} {
		if !row.Selector.Valid || row.Selector.String != groupPartialSelectorB || row.SelectorSchema != selector.SchemaVersion || row.Name.String != "Original" {
			t.Fatalf("selector pair was not replaced: %#v", row)
		}
	}
	pairs := 0
	for _, statement := range customerPartialWrites(statements) {
		normalized := strings.NewReplacer("`", "", "\"", "").Replace(statement)
		if !strings.HasPrefix(normalized, "UPDATE entity_groups SET ") || !strings.Contains(normalized, "selector=") {
			continue
		}
		pairs++
		if !strings.Contains(normalized, "selector_schema=") || strings.Contains(normalized, "active_version=") || strings.Contains(normalized, "name=") || strings.Contains(normalized, "metadata=") {
			t.Fatalf("selector replacement did not write only the pair: %s", statement)
		}
	}
	if pairs != 1 {
		t.Fatalf("got %d selector-pair UPDATEs, want one", pairs)
	}
}

func TestEntityGroupPartialConcurrentInvalidSelectorWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dynamic bool
		field   dcgraphql.OptionalString
	}{
		{"static", false, dcgraphql.OptionalStringOf(groupPartialSelectorB)},
		{"null", true, dcgraphql.ClearedString()},
		{"invalid", true, dcgraphql.OptionalStringOf(`attr["a"] == attr["b"]`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx, statements := entityGroupPartialFixture(t, tc.dynamic)
			before := reloadPartialEntityGroup(t, api, ctx)
			_, err := api.UpdateEntityGroup(ctx, "g1", &EntityGroupUpdateRequest{
				Name: dcgraphql.OptionalStringOf("Refused"), Selector: tc.field,
			})
			if err == nil {
				t.Fatal("accepted an invalid selector")
			}
			if writes := customerPartialWrites(statements); len(writes) != 0 {
				t.Fatalf("refused request wrote: %v", writes)
			}
			stored := reloadPartialEntityGroup(t, api, ctx)
			if stored.Name != before.Name || stored.Selector != before.Selector || stored.SelectorSchema != before.SelectorSchema || !stored.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatal("refused request changed the row")
			}
		})
	}
}

func TestEntityGroupPartialEmptyRetainsExactTimestamp(t *testing.T) {
	api, ctx, statements := entityGroupPartialFixture(t, true)
	before := reloadPartialEntityGroup(t, api, ctx)
	updated, err := api.UpdateEntityGroup(ctx, "g1", &EntityGroupUpdateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if writes := customerPartialWrites(statements); len(writes) != 0 {
		t.Fatalf("empty request wrote: %v", writes)
	}
	stored := reloadPartialEntityGroup(t, api, ctx)
	if !stored.UpdatedAt.Equal(before.UpdatedAt) || !updated.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("empty request advanced updated_at")
	}
	_, err = api.UpdateEntityGroup(ctx, "missing", &EntityGroupUpdateRequest{})
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing group: %v; want not found", err)
	}
}
