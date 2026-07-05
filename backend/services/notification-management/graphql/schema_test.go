// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

// TestSchemaParses is the compile-time contract between the SDL and the resolver
// methods: MustParseSchema panics if any schema field/argument has no matching
// resolver method (or vice-versa), so parsing the real schema against the real
// root resolver here fails the build the moment they drift.
func TestSchemaParses(t *testing.T) {
	require.NotPanics(t, func() {
		schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
		require.NotNil(t, schema)
	})
}
