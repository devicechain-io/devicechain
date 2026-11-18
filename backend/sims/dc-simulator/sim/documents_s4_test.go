// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"testing"
	"time"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	graphql "github.com/graph-gophers/graphql-go"
)

// The documents the fleet-operation steps send (fleetops.go and the S4 changes to bootstrap.go),
// with the request maps the call sites build, against the tenant plane of the area that serves
// each.
func TestTheFleetOpsDocumentsValidateAgainstTheServedSchemas(t *testing.T) {
	m := NewHarborpulse(1, Load{}).Manifest()
	g, w, c := m.EntityGroups[0], m.MaintenanceWindows[0], m.Campaigns[0]
	s := harborWith(t)
	_, day, _ := s.dayJobs(time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), 0)
	cases := []struct {
		name, area, doc string
		vars            map[string]any
	}{
		{"entityGroupsByToken", "device-management", queryEntityGroupsByToken, map[string]any{"tokens": []string{g.Token}}},
		{"createEntityGroup", "device-management", mutationCreateEntityGroup, map[string]any{"request": map[string]any{
			"token": g.Token, "name": g.Name, "description": g.Description, "memberType": groupMemberDevice,
			"membershipMode": groupMembershipDyn, "selector": g.Selector}}},
		{"updateEntityGroup", "device-management", mutationUpdateEntityGroup, map[string]any{"token": g.Token, "request": map[string]any{
			"name": g.Name, "description": g.Description, "selector": g.Selector}}},
		{"publishEntityGroup", "device-management", mutationPublishEntityGroup, map[string]any{"token": g.Token, "label": "sim"}},
		{"entityAttributes", "device-management", queryDeviceAttributes, map[string]any{"c": map[string]any{
			"pageNumber": 1, "pageSize": 1000, "entityType": entityTypeDevice, "scope": attributeScopeShared}}},
		{"setEntityAttribute", "device-management", mutationSetEntityAttribute, map[string]any{"request": map[string]any{
			"entityType": entityTypeDevice, "entity": "hp-pump-east", "scope": attributeScopeShared,
			"attrKey": "critical", "valueType": "BOOLEAN", "value": "true"}}},
		{"devicesByToken positions", "device-management", queryInstalledPositions, map[string]any{"tokens": []string{"hp-light-east-1"}}},
		{"setInstalledPosition", "device-management", mutationSetInstalledPosition, map[string]any{"family": positionFamilyDevice,
			"token": "hp-light-east-1", "position": map[string]any{"latitude": 49.8, "longitude": -12.2, "source": positionSourceSurvey}}},
		{"clearInstalledPosition", "device-management", mutationClearInstalledPosition, map[string]any{
			"family": positionFamilyDevice, "token": "hp-light-east-1"}},
		{"entityRelationshipsByToken", "device-management", queryEntityRelationshipsByToken, map[string]any{"tokens": []string{"hp-feeds-hp-light-east-1"}}},
		{"removeEntityRelationship", "device-management", mutationRemoveEntityRelationship, map[string]any{"token": "hp-feeds-hp-light-east-1"}},
		{"assetParent", "device-management", queryAssetParent, map[string]any{"token": HarborpulseLoftsBuilding}},
		{"setAssetParent", "device-management", mutationSetAssetParent, map[string]any{"child": HarborpulseLoftsBuilding, "parent": HarborpulseDistrictAsset}},
		{"entityRelationshipTypesByToken", "device-management", queryRelationshipTypesByToken, map[string]any{"tokens": []string{HarborpulseFeedsType}}},
		{"createEntityRelationshipType", "device-management", mutationCreateRelationshipType, map[string]any{"request": map[string]any{
			"token": HarborpulseFeedsType, "name": "Feeds", "description": "d", "tracked": false}}},
		{"maintenanceWindowsByToken", "device-management", queryMaintenanceWindowsByToken, map[string]any{"tokens": []string{w.Token}}},
		{"createMaintenanceWindow", "device-management", mutationCreateMaintenanceWindow, map[string]any{"request": map[string]any{
			"token": w.Token, "name": w.Name, "description": w.Description, "timezone": w.Timezone, "slots": slotsJSON(w.Slots)}}},
		{"updateMaintenanceWindow", "device-management", mutationUpdateMaintenanceWindow, map[string]any{"token": w.Token, "request": map[string]any{
			"timezone": w.Timezone, "slots": slotsJSON(w.Slots)}}},
		{"deviceTypesByToken", "device-management", queryDeviceTypesByToken, map[string]any{"tokens": []string{HarborpulsePumpType}}},
		{"updateDeviceType window", "device-management", mutationUpdateDeviceTypeWindow, map[string]any{"token": HarborpulsePumpType,
			"request": map[string]any{"maintenanceWindowToken": w.Token}}},
		{"createDeviceType window", "device-management", mutationCreateDeviceType, map[string]any{"request": map[string]any{
			"token": HarborpulsePumpType, "name": "n", "profileToken": HarborpulsePumpProfile, "maintenanceWindowToken": w.Token}}},
		{"deviceProfilesByToken", "device-management", queryDeviceProfilesByToken, map[string]any{"tokens": []string{HarborpulsePumpProfile}}},
		{"createCommandDefinition impact", "device-management", mutationCreateCommandDefinition, map[string]any{"request": map[string]any{
			"token": HarborpulseFlushDef, "deviceProfileToken": HarborpulsePumpProfile, "commandKey": HarborpulseFlushKey,
			"name": "Flush", "impact": impactDisruptive}}},
		{"createDetectionRule scoped", "device-management", mutationCreateDetectionRule, map[string]any{"request": map[string]any{
			"token": HarborpulseWellWatchRule, "deviceProfileToken": HarborpulsePumpProfile, "name": "n", "definition": "{}",
			"enabled": true, "entityGroupToken": HarborpulseCriticalPumpsGroup, "entityGroupVersion": 1}}},
		{"updateDetectionRule unscoped", "device-management", mutationUpdateDetectionRule, map[string]any{"token": HarborpulseWellWatchRule,
			"request": map[string]any{"definition": "{}", "enabled": true, "entityGroupToken": nil, "entityGroupVersion": nil}}},
		{"campaign", "command-delivery", queryCampaign, map[string]any{"token": c.Token}},
		{"campaigns", "command-delivery", queryCampaigns, map[string]any{"limit": 200}},
		{"createCampaign solar", "command-delivery", mutationCreateCampaign, map[string]any{"request": c.createRequest()}},
		{"createCampaign calendar", "command-delivery", mutationCreateCampaign, map[string]any{"request": day[0].createRequest()}},
		{"deleteCampaign", "command-delivery", mutationDeleteCampaign, map[string]any{"token": c.Token}},
	}
	schemas := map[string]*graphql.Schema{}
	for _, tc := range cases {
		sch, ok := schemas[tc.area]
		if !ok {
			sch = servedSchema(t, tc.area)
			schemas[tc.area] = sch
		}
		raw, err := json.Marshal(tc.vars)
		if err != nil {
			t.Fatal(err)
		}
		var vars map[string]any
		if err := json.Unmarshal(raw, &vars); err != nil {
			t.Fatal(err)
		}
		if errs := sch.ValidateWithVariables(tc.doc, vars); len(errs) > 0 {
			t.Errorf("%s does not validate against %s's tenant schema: %v", tc.name, tc.area, errs)
		}
		if err := gqlcore.CheckWork(tc.doc); err != nil {
			t.Errorf("%s exceeds the served root-field ceiling: %v", tc.name, err)
		}
	}
	// The control: a request key the schema does not define is refused.
	bad := c.createRequest()
	bad["holidays"] = []string{"2026-11-18"}
	if errs := schemas["command-delivery"].ValidateWithVariables(mutationCreateCampaign, map[string]any{"request": bad}); len(errs) == 0 {
		t.Fatal("an unknown campaign request key validated")
	}
}
