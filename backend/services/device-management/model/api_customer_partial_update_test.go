// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"gorm.io/gorm"
)

// The shared harness already drives every customer field through absent/null/value.
// These checks cover SQL omission, an interleaved writer and the no-write contract.
func customerPartialFixture(t *testing.T) (*Api, context.Context, *rdbtest.StatementCounter) {
	t.Helper()
	family := customerFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	if err := api.RDB.DB(ctx).Model(&Customer{}).Where("token = ?", family.Token).
		Update("description", "old").Error; err != nil {
		t.Fatalf("seed description: %v", err)
	}
	// A space matches every rendered statement, including association and audit
	// writes. Recording only UPDATE would miss an association upsert.
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, statements
}

func reloadPartialCustomer(t *testing.T, api *Api, ctx context.Context) *Customer {
	t.Helper()
	rows, err := api.CustomersByToken(ctx, []string{"c-1"})
	return requireOne(t, "customer", rows, err)
}

func customerPartialWrites(statements *rdbtest.StatementCounter) []string {
	var writes []string
	for _, sql := range statements.MarkedSQL() {
		if strings.HasPrefix(sql, "UPDATE ") || strings.HasPrefix(sql, "INSERT ") || strings.HasPrefix(sql, "DELETE ") {
			writes = append(writes, sql)
		}
	}
	return writes
}

func TestCustomerConcurrentPartialUpdatePreservesOmittedFields(t *testing.T) {
	api, ctx, statements := customerPartialFixture(t)
	fired := false
	// Land another writer after the customer (and its preload) has been read,
	// before UpdateCustomer builds its write. The loaded description is still old.
	err := api.RDB.Database.Callback().Query().After("gorm:after_query").
		Register("test:customer_other_writer", func(tx *gorm.DB) {
			if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "customers" {
				return
			}
			fired = true
			tx.AddError(api.RDB.DB(ctx).Model(&Customer{}).Where("token = ?", "c-1").
				Update("description", "other writer").Error)
		})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := api.UpdateCustomer(ctx, "c-1", &CustomerUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !fired {
		t.Fatal("the other writer never ran")
	}
	stored := reloadPartialCustomer(t, api, ctx)
	for _, row := range []*Customer{stored, updated} {
		if nullStr(row.Name) != "Renamed" || nullStr(row.Description) != "other writer" {
			t.Fatalf("name/description = %q/%q; want Renamed/other writer", nullStr(row.Name), nullStr(row.Description))
		}
		if jsonStr(row.Metadata) != memberSeed.Metadata {
			t.Fatalf("metadata = %s; want %s", jsonStr(row.Metadata), memberSeed.Metadata)
		}
	}
	nameWrites := 0
	for _, sql := range customerPartialWrites(statements) {
		normalized := strings.NewReplacer("`", "", "\"", "").Replace(sql)
		if strings.HasPrefix(normalized, "INSERT INTO customer_types ") || strings.HasPrefix(normalized, "UPDATE customer_types ") {
			t.Fatalf("resaved the type association: %s", sql)
		}
		if !strings.HasPrefix(normalized, "UPDATE customers SET ") || !strings.Contains(normalized, "name=") {
			continue
		}
		nameWrites++
		if strings.Contains(normalized, "description=") || strings.Contains(normalized, "metadata=") {
			t.Fatalf("name-only update assigned an omitted field: %s", sql)
		}
	}
	if nameWrites != 1 {
		t.Fatalf("got %d name UPDATEs, want one; SQL: %v", nameWrites, statements.MarkedSQL())
	}
}

func TestCustomerPartialUpdateExplicitNullDescription(t *testing.T) {
	api, ctx, _ := customerPartialFixture(t)
	before := reloadPartialCustomer(t, api, ctx)
	updated, err := api.UpdateCustomer(ctx, "c-1", &CustomerUpdateRequest{
		Description: dcgraphql.ClearedString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := reloadPartialCustomer(t, api, ctx)
	if stored.Description.Valid || updated.Description.Valid {
		t.Fatal("explicit null did not clear description")
	}
	if stored.Name != before.Name || jsonStr(stored.Metadata) != jsonStr(before.Metadata) || stored.CustomerTypeId != before.CustomerTypeId {
		t.Fatal("clearing description changed an omitted field")
	}
}

func TestCustomerPartialUpdateInvalidTypeWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token dcgraphql.OptionalString
	}{
		{"unknown", dcgraphql.OptionalStringOf("no-such-type")},
		{"null", dcgraphql.ClearedString()},
		{"blank", dcgraphql.OptionalStringOf("   ")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx, statements := customerPartialFixture(t)
			before := reloadPartialCustomer(t, api, ctx)
			_, err := api.UpdateCustomer(ctx, "c-1", &CustomerUpdateRequest{
				Name: dcgraphql.OptionalStringOf("Refused"), CustomerTypeToken: tc.token,
			})
			if err == nil {
				t.Fatal("accepted an invalid type")
			}
			if writes := customerPartialWrites(statements); len(writes) != 0 {
				t.Fatalf("refused request wrote: %v", writes)
			}
			stored := reloadPartialCustomer(t, api, ctx)
			if stored.Name != before.Name || !stored.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatal("refused request changed the row")
			}
		})
	}
}

func TestCustomerEmptyUpdateRetainsExactTimestamp(t *testing.T) {
	api, ctx, statements := customerPartialFixture(t)
	before := reloadPartialCustomer(t, api, ctx)
	updated, err := api.UpdateCustomer(ctx, "c-1", &CustomerUpdateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if writes := customerPartialWrites(statements); len(writes) != 0 {
		t.Fatalf("empty request wrote: %v", writes)
	}
	stored := reloadPartialCustomer(t, api, ctx)
	if !stored.UpdatedAt.Equal(before.UpdatedAt) || !updated.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("empty request advanced updated_at")
	}
}

func TestCustomerPartialUpdateTypeWritesOnlyForeignKey(t *testing.T) {
	api, ctx, statements := customerPartialFixture(t)
	updated, err := api.UpdateCustomer(ctx, "c-1", &CustomerUpdateRequest{
		CustomerTypeToken: dcgraphql.OptionalStringOf(otherTypeToken),
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := reloadPartialCustomer(t, api, ctx)
	for _, row := range []*Customer{updated, stored} {
		if row.CustomerType == nil || row.CustomerType.Token != otherTypeToken || row.CustomerTypeId != row.CustomerType.ID {
			t.Fatalf("type was not assigned and reloaded: %#v", row)
		}
	}
	updates := 0
	for _, sql := range customerPartialWrites(statements) {
		normalized := strings.NewReplacer("`", "", "\"", "").Replace(sql)
		if strings.HasPrefix(normalized, "INSERT INTO audit_events ") {
			continue
		}
		if !strings.HasPrefix(normalized, "UPDATE customers SET ") || !strings.Contains(normalized, "customer_type_id=") {
			t.Fatalf("unexpected write during type change: %s", sql)
		}
		updates++
		if strings.Contains(normalized, "name=") || strings.Contains(normalized, "description=") || strings.Contains(normalized, "metadata=") {
			t.Fatalf("type change assigned an omitted scalar: %s", sql)
		}
	}
	if updates != 1 {
		t.Fatalf("got %d customer UPDATEs, want one", updates)
	}
	_, err = api.UpdateCustomer(ctx, "no-such-customer", &CustomerUpdateRequest{})
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing customer: %v, want not found", err)
	}
}
