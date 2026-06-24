// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	gql "github.com/graph-gophers/graphql-go"
)

// The schema is loaded with MustParseSchema at startup; this verifies it parses
// and every field has a matching resolver method (a mismatch would panic).
func TestSchemaParses(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	if schema == nil {
		t.Fatal("expected a parsed schema")
	}
}
