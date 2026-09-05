// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// THE ONE WAY TO DESTROY THE ABSENT STATE THAT NOTHING IN THIS SERVICE'S GO CODE CAN SEE.
//
// A default value is not in Go. It is a token in the SDL — `expiresInDays: Int = 30` — and
// StructPacker packs a defaulted field as NON-NULL and seeds it into the result before
// packing, so an ABSENT field arrives with Set=true holding the default. Every update
// omitting it then WRITES it, and the mutation returns success.
//
// This service has seventeen converted update inputs, which is the largest such surface on
// the platform, and every layer of its own test suite misses this for its own reason: the
// resolver tests call resolvers with Go STRUCTS so the packer never runs; the model
// families drive the Api directly, below the packer; and core's optional_test.go proves
// the three states survive for the TYPE, which says nothing about a DECLARATION in this
// schema's text.
//
// 🔴 IT IS THE CONVERSION THAT OPENS THE HOLE, which is why this is the conversion's
// companion rather than optional tidiness. A default against a POINTER field fails schema
// construction outright — the library refuses it — so a full-replace input was protected by
// the schema itself. An Optional* field absorbs the default cleanly and the schema builds.
//
// The input set is DERIVED from the schema's update* mutations rather than listed, so the
// eighteenth update input is covered on the day it is added.
func TestNoUpdateInputCarriesAnSDLDefault(t *testing.T) {
	putest.AssertNoUpdateInputCarriesAnSDLDefault(t,
		putest.UpdateSchema{
			Name: "device-management", SDL: SchemaContent, Root: &SchemaResolver{},
			// updateDeviceType, updateDeviceProfile, updateMetricDefinition,
			// updateCommandDefinition, updateDetectionRule, updateGeoFence, updateDevice,
			// updateEntityGroup, updateDeviceCredential, updateProvisioningProfile,
			// updateEntityRelationshipType, updateAssetType, updateAsset,
			// updateCustomerType, updateCustomer, updateAreaType, updateArea.
			MinUpdateMutations: 17,
		},
	)
}
