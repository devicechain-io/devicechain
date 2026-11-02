// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"sort"
	"testing"

	"github.com/devicechain-io/dc-microservice/resolvedevent"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaReasons is the values the served schema declares for DroppedMeasurementReason.
func schemaReasons(t *testing.T) []string {
	t.Helper()
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	enum, ok := schema.ASTSchema().Types["DroppedMeasurementReason"].(*types.EnumTypeDefinition)
	require.True(t, ok, "the schema declares the DroppedMeasurementReason enum")
	var out []string
	for _, v := range enum.EnumValuesDefinition {
		out = append(out, v.EnumValue)
	}
	sort.Strings(out)
	return out
}

func enumOf(reasons []string) []string {
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, droppedReasonEnum(r))
	}
	sort.Strings(out)
	return out
}

// The enum is the shared vocabulary (resolvedevent.DroppedReasons) and nothing else: a reason
// added there without the enum, or an enum value with no reason, fails here. This is the
// "fails the build" half of the spec's shared vocabulary.
func TestTheReasonEnumIsTheSharedVocabulary(t *testing.T) {
	assert.Equal(t, enumOf(resolvedevent.DroppedReasons), schemaReasons(t))

	// Negative control: a fourth reason the enum does not name is a mismatch, so the
	// assertion above is able to fail.
	withFourth := append(append([]string{}, resolvedevent.DroppedReasons...), "a_fourth_reason")
	assert.NotEqual(t, enumOf(withFourth), schemaReasons(t))
}
