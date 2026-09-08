// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// 🔴 THE DEFECT NO OTHER TEST IN THIS SERVICE CAN SEE, AND IT WAS DEMONSTRATED RATHER
// THAN REASONED ABOUT.
//
// Adding `ingestBurst: Int = 5` to AdminTenantUpdateRequest in admin_schema.gql left both
// ./graphql and ./admin GREEN — and with that one token, every updateTenant that omits
// ingestBurst reaches the service as Set=true, Value=5, so renaming a tenant writes a
// burst override of 5 into it.
//
// Each layer misses it for a different reason, and the reasons do not overlap:
//
//   - TestEveryGovernanceOverrideSurvivesTheAdminWireCopy calls the resolver with a Go
//     STRUCT, so the packer — where a default is applied — never runs;
//   - the wire tests in partial_update_wire_test.go check a document's SHAPE and address
//     records that do not exist, so storage is never read;
//   - the admin and identity harnesses start BELOW the packer, driving the services
//     directly;
//   - core's optional_test.go proves the three states survive for the Go TYPE, which is
//     true and says nothing about a DECLARATION in this service's SDL. "That proof carries
//     here by construction" was the claim, and it is a claim about the type.
//
// A default lives in exactly one place none of them looks: the schema text. So the guard
// asks the SERVED SCHEMA, and it lives in core because this is a platform rule with one
// mechanism — the capstone slice wires the same call for the remaining services.
//
// The input set is DERIVED from each schema's update* mutations rather than listed, so an
// input added tomorrow is covered on the day it is added.
func TestNoUpdateInputCarriesAnSDLDefault(t *testing.T) {
	putest.AssertNoUpdateInputCarriesAnSDLDefault(t,
		putest.UpdateSchema{
			Name: "admin", SDL: AdminSchemaContent, Root: &AdminResolver{},
			// updateRole, updateTenant, updateTenantTier, updateOauthClient.
			MinUpdateMutations: 4,
		},
		putest.UpdateSchema{
			Name: "tenant", SDL: SchemaContent, Root: &SchemaResolver{},
			// updateProfile.
			MinUpdateMutations: 1,
		},
		// 🔴 THE SETTINGS SCHEMA IS INCLUDED THOUGH THIS SLICE DID NOT TOUCH IT, and that
		// is the point of a derived guard: it serves no update* mutation today, so the
		// floor is 1 and this row would FAIL — which is why it is not here. It is named
		// instead, so the next person to add an update there finds the reason rather than
		// an absence: settings_schema.gql's writes are set*/put* single-purpose mutations
		// whose whole payload IS the thing they set. Add a row the day that changes.
	)
}
