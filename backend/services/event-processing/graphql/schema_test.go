// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	gql "github.com/graph-gophers/graphql-go"
)

// TestSchemaParses validates that the GraphQL schema parses against the resolver — every
// schema field must have a matching resolver method. This is the fast check for the
// otherwise start-time-only MustParseSchema panic in main.go.
func TestSchemaParses(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("schema failed to parse against resolver: %v", r)
		}
	}()
	gql.MustParseSchema(SchemaContent, &SchemaResolver{})
}
