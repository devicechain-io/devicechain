// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"encoding/json"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/stretchr/testify/require"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	celvalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/listtype"
	apivalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
)

func TestInstanceShardGroupsOptionalAndDeepCopied(t *testing.T) {
	var spec dcv1beta1.InstanceSpec
	off, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NotContains(t, string(off), "shardGroups")
	raw := `{"provider":"local","managed":false,"shardGroups":{"count":2,"suffixes":["inbound-events"],"placementTags":{"1":["dc-nats-1"]}}}`
	require.NoError(t, json.Unmarshal([]byte(raw), &spec))
	require.NotNil(t, spec.ShardGroups)
	copy := spec.DeepCopy()
	copy.ShardGroups.Suffixes[0] = "changed"
	copy.ShardGroups.PlacementTags["1"][0] = "changed"
	require.Equal(t, "inbound-events", spec.ShardGroups.Suffixes[0])
	require.Equal(t, "dc-nats-1", spec.ShardGroups.PlacementTags["1"][0])
	props := specSchema(t, loadInstanceCRD(t))["properties"].(map[string]interface{})
	block := props["shardGroups"].(map[string]interface{})
	require.NotContains(t, block, "default")
	require.Equal(t, "object", block["type"])
	fields := block["properties"].(map[string]interface{})
	count := fields["count"].(map[string]interface{})
	require.EqualValues(t, 2, count["minimum"])
	require.EqualValues(t, 5, count["maximum"])
}

// Exercise the shipped artifact with Kubernetes' actual validators as well as
// the controller suite's API-server admission test. This needs no envtest binary.
func TestShardGroupsNativeCRDValidation(t *testing.T) {
	raw, err := json.Marshal(specSchema(t, loadInstanceCRD(t)))
	require.NoError(t, err)
	var external apiextensionsv1.JSONSchemaProps
	require.NoError(t, json.Unmarshal(raw, &external))
	var spec apiextensions.JSONSchemaProps
	require.NoError(t, apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&external, &spec, nil))
	openapi, _, err := apivalidation.NewSchemaValidator(&spec)
	require.NoError(t, err)
	structural, err := structuralschema.NewStructural(&spec)
	require.NoError(t, err)
	cel := celvalidation.NewValidator(structural, false, celconfig.PerCallLimit)
	require.NotNil(t, cel)
	for _, tc := range []struct {
		name  string
		block string
		valid bool
	}{
		{"off", "", true},
		{"on", `,"shardGroups":{"count":2,"suffixes":["inbound-events"]}`, true},
		{"placed", `,"shardGroups":{"count":2,"suffixes":["resolved-events"],"placementTags":{"0":["dc-nats-0"],"1":["dc-nats-1"]}}`, true},
		{"count-low", `,"shardGroups":{"count":1,"suffixes":["inbound-events"]}`, false},
		{"count-high", `,"shardGroups":{"count":6,"suffixes":["inbound-events"]}`, false},
		{"empty", `,"shardGroups":{"count":2,"suffixes":[]}`, false},
		{"unknown", `,"shardGroups":{"count":2,"suffixes":["typo"]}`, false},
		{"duplicate", `,"shardGroups":{"count":2,"suffixes":["inbound-events","inbound-events"]}`, false},
		{"wrong-tag", `,"shardGroups":{"count":2,"suffixes":["inbound-events"],"placementTags":{"1":["dc-nats-0"]}}`, false},
		{"outside", `,"shardGroups":{"count":2,"suffixes":["inbound-events"],"placementTags":{"2":["dc-nats-2"]}}`, false},
		{"ha", `,"ha":true,"shardGroups":{"count":2,"suffixes":["inbound-events"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var typed dcv1beta1.InstanceSpec
			require.NoError(t, json.Unmarshal([]byte(`{"provider":"local","managed":false`+tc.block+`}`), &typed))
			object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&typed)
			require.NoError(t, err)
			path := field.NewPath("spec")
			errs := apivalidation.ValidateCustomResource(path, object, openapi)
			errs = append(errs, listtype.ValidateListSetsAndMaps(path, structural, object)...)
			celErrs, _ := cel.Validate(t.Context(), path, structural, object, nil, celconfig.RuntimeCELCostBudget)
			errs = append(errs, celErrs...)
			if tc.valid {
				require.Empty(t, errs)
			} else {
				require.NotEmpty(t, errs)
			}
		})
	}
}
