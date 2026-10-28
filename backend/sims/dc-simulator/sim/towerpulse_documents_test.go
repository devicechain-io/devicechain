// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"testing"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

// The documents this scenario added to provisioning, validated against the schemas the services
// really serve, with the variables the code really sends. The schemas live in other modules, so
// nothing else reads these strings until a cluster does.
func TestTowerpulseProvisioningDocumentsValidateAgainstTheServedSchemas(t *testing.T) {
	dm := servedSchema(t, "device-management")
	um := servedSchema(t, "user-management")

	d := meterInstance(t)
	metricReq := map[string]any{
		"token": "p-cop", "deviceProfileToken": "p", "metricKey": "cop", "name": "COP",
		"dataType": "DOUBLE", "unit": "", "expression": TowerpulseCOPFormula,
	}
	cases := []struct {
		name   string
		schema string
		doc    string
		vars   map[string]any
	}{
		{"deviceProfilesByToken", "dm", queryDeviceProfilesByToken, map[string]any{"tokens": []any{"p"}}},
		{"createMetricDefinition (calculated)", "dm", mutationCreateMetricDefinition, map[string]any{"request": metricReq}},
		{"updateMetricDefinition (calculated)", "dm", mutationUpdateMetricDefinition,
			map[string]any{"token": "p-cop", "request": asUpdateRequest(metricReq)}},
		{"devicesByToken", "dm", queryDevicesByToken, map[string]any{"tokens": []any{d.Token}}},
		{"createDevice (constants)", "dm", mutationCreateDevice, map[string]any{"request": map[string]any{
			"token": d.Token, "externalId": d.ExternalId, "deviceTypeToken": d.DeviceTypeToken, "metadata": d.Metadata}}},
		{"updateDevice (constants)", "dm", mutationUpdateDevice,
			map[string]any{"token": d.Token, "request": map[string]any{"metadata": d.Metadata}}},

		{"members", "um", queryMembersByEmail, map[string]any{"criteria": map[string]any{
			"pageNumber": 1, "pageSize": 20, "emailPrefix": TowerpulseAnnexOwnerEmail}}},
		{"inviteMember", "um", mutationInviteMember, map[string]any{"request": map[string]any{
			"email": TowerpulseAnnexOwnerEmail, "roleTokens": []any{customerUserRole},
			"scope": []any{map[string]any{"kind": scopeKindCustomer, "token": TowerpulseAnnexOwnerToken}}}}},
		{"acceptInvitation", "um", mutationAcceptInvitation, map[string]any{"code": "c", "password": "p"}},
		{"setMemberScope", "um", mutationSetMemberScope, map[string]any{"email": TowerpulseAnnexOwnerEmail,
			"scope": []any{map[string]any{"kind": scopeKindCustomer, "token": TowerpulseAnnexOwnerToken}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			schema := dm
			if c.schema == "um" {
				schema = um
			}
			if errs := schema.ValidateWithVariables(c.doc, c.vars); len(errs) > 0 {
				t.Fatalf("does not validate against the served schema: %v", errs)
			}
			if err := gqlcore.CheckWork(c.doc); err != nil {
				t.Fatalf("exceeds the served root-field ceiling: %v", err)
			}
		})
	}

	// NEGATIVE CONTROL: a scope kind the platform does not have is refused. Without this the
	// passes above could be a validator that reads nothing.
	errs := um.ValidateWithVariables(mutationSetMemberScope, map[string]any{"email": "a@b.test",
		"scope": []any{map[string]any{"kind": "TENANT", "token": "x"}}})
	if len(errs) == 0 {
		t.Error("an unknown scope kind validated, so the validator is not reading the variables")
	}
	errs = dm.ValidateWithVariables(mutationUpdateDevice, map[string]any{"token": "d",
		"request": map[string]any{"token": "d", "metadata": "{}"}})
	if len(errs) == 0 {
		t.Error("an update carrying a payload token validated; the unknown-field guard is not in force")
	}
}
