// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	gql "github.com/graph-gophers/graphql-go"
)

// TestSchemaParses validates that the GraphQL schema parses against the resolver
// — every schema field must have a matching resolver method.
func TestSchemaParses(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("schema failed to parse against resolver: %v", r)
		}
	}()
	gql.MustParseSchema(SchemaContent, &SchemaResolver{})
}
