// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// THE ONE WAY TO DESTROY THE ABSENT STATE THAT NOTHING IN THIS SERVICE'S GO CODE CAN SEE.
//
// A default value is not in Go. It is a token in the SDL — `enabled: Boolean = true` — and
// StructPacker packs a defaulted field as NON-NULL and seeds it into the result before
// packing, so an ABSENT field arrives with Set=true holding the default. Every update
// omitting it then WRITES it, and the mutation returns success. On a connector that means
// an edit to a description silently re-enabling a connector an operator had switched off,
// with REACT dispatching to it again.
//
// Nothing else here is positioned to see it: partial_update_wire_test.go checks a
// document's SHAPE rather than reading storage, the model families drive the Api directly
// (below the packer), and core's optional_test.go proves the three states survive for the
// TYPE, which says nothing about a DECLARATION in this schema's text.
//
// 🔴 IT IS THE CONVERSION THAT OPENS THE HOLE. A default against a POINTER field fails
// schema construction outright, so a full-replace input was protected by the library
// itself; an Optional* field absorbs the default cleanly and the schema builds.
//
// The resolver root is constructed bare rather than with main.go's Area, because what is
// parsed here is the schema's SHAPE against its resolver METHODS — a field's value cannot
// change which types the walk reaches.
func TestNoUpdateInputCarriesAnSDLDefault(t *testing.T) {
	putest.AssertNoUpdateInputCarriesAnSDLDefault(t,
		putest.UpdateSchema{
			Name: "outbound-connectors", SDL: SchemaContent, Root: &SchemaResolver{},
			// updateConnector.
			MinUpdateMutations: 1,
		},
	)
}
