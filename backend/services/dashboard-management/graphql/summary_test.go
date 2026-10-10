// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"strings"
	"testing"

	gql "github.com/graph-gophers/graphql-go"
)

// The list type has no definition field: a client selecting one is refused by validation,
// up front, instead of being served a blank or erroring a non-null field at run time.
func TestSearchResultsCarryNoDefinition(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(dashboardTestCtx(t), `{ dashboards(criteria:{pageNumber:1,pageSize:1}) { results { token definition } } }`, "", nil)
	if len(res.Errors) == 0 {
		t.Fatal("selecting definition on a search row was accepted")
	}
	if !strings.Contains(res.Errors[0].Message, "definition") {
		t.Fatalf("unexpected error: %v", res.Errors[0])
	}
}
