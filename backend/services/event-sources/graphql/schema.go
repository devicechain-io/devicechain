// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	_ "embed"
)

//go:embed schema.graphql
var SchemaContent string

type SchemaResolver struct{}

// Empty resolves the _empty placeholder field on both the Query and Mutation
// roots. event-sources has no GraphQL operations of its own — it ingests over the
// device-plane transports — but the GraphQL runtime (graphql-go 1.10) rejects an
// object type with no fields, so each root carries this single nullable field.
func (s *SchemaResolver) Empty() *bool {
	return nil
}
