// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apivalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"
)

func TestOperationSpecFieldSet(t *testing.T) {
	typ := reflect.TypeOf(api.InstanceOperationSpec{})
	var got []string
	for i := 0; i < typ.NumField(); i++ {
		got = append(got, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	sort.Strings(got)
	want := []string{"action", "enrollmentUID", "imageVersion", "instance", "instanceUID", "protocolVersion"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public request field set moved: %v", got)
	}
	s := executorSchema(t, "instanceoperations").Properties["spec"]
	var fields []string
	for name := range s.Properties {
		fields = append(fields, name)
	}
	sort.Strings(fields)
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("schema request field set moved: %v", fields)
	}
}
func TestEnrollmentFieldSetAndListSemantics(t *testing.T) {
	typ := reflect.TypeOf(api.ExecutorEnrollmentSpec{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	sort.Strings(fields)
	if !reflect.DeepEqual(fields, []string{"escrowDelivery", "executorImage", "owner", "protocolVersion"}) {
		t.Fatal(fields)
	}
	s := executorSchema(t, "executorenrollments")
	status := s.Properties["status"]
	conditions := status.Properties["conditions"]
	capability := status.Properties["capability"]
	actions := capability.Properties["executableActions"]
	if conditions.XListType == nil || *conditions.XListType != "map" || !reflect.DeepEqual(conditions.XListMapKeys, []string{"type"}) || actions.XListType == nil || *actions.XListType != "set" {
		t.Fatal("condition/capability merge semantics changed")
	}
}
func cloneJSON(v map[string]interface{}) map[string]interface{} {
	return runtime.DeepCopyJSON(v)
}
func TestExecutorSpecCEL(t *testing.T) {
	enrollment := executorSchema(t, "executorenrollments")
	s := enrollment.Properties["spec"]
	base := map[string]interface{}{"owner": "owner", "escrowDelivery": "OwnerOffline", "executorImage": "a@sha256:" + strings.Repeat("a", 64), "protocolVersion": int64(1)}
	checkCEL(t, &s, base, nil, true)
	for _, name := range []string{"owner", "escrowDelivery"} {
		changed := cloneJSON(base)
		changed[name] = "changed"
		checkCEL(t, &s, changed, base, false)
	}
	changed := cloneJSON(base)
	changed["executorImage"] = "b@sha256:" + strings.Repeat("b", 64)
	checkCEL(t, &s, changed, base, true)
	operation := executorSchema(t, "instanceoperations")
	s = operation.Properties["spec"]
	op := map[string]interface{}{"instance": "prod", "instanceUID": "target", "enrollmentUID": "enrollment", "action": "Upgrade", "protocolVersion": int64(1)}
	checkCEL(t, &s, op, nil, true)
	for name, value := range map[string]interface{}{"instance": "other", "instanceUID": "other", "enrollmentUID": "other", "action": "Teardown", "imageVersion": "v2", "protocolVersion": int64(2)} {
		changed := cloneJSON(op)
		changed[name] = value
		checkCEL(t, &s, changed, op, false)
	}
	for _, action := range []string{"Install", "Upgrade", "Teardown", "Bootstrap"} {
		changed := cloneJSON(op)
		changed["action"] = action
		delete(changed, "instanceUID")
		checkCEL(t, &s, changed, nil, action == "Bootstrap")
		changed["instanceUID"] = ""
		checkCEL(t, &s, changed, nil, action == "Bootstrap")
	}
}
func TestTerminalVerdictPresenceAndMutationCEL(t *testing.T) {
	s := executorSchema(t, "instanceoperations")
	for _, phase := range []string{"Refused", "Succeeded", "Failed"} {
		old := map[string]interface{}{"status": map[string]interface{}{"phase": phase, "reason": "Original", "message": "Preview", "decidedAt": "2026-11-21T00:00:00Z"}}
		for _, name := range []string{"reason", "message", "decidedAt"} {
			changed := cloneJSON(old)
			delete(changed["status"].(map[string]interface{}), name)
			checkCEL(t, s, changed, old, false)
			changed = cloneJSON(old)
			value := "changed"
			if name == "decidedAt" {
				value = "2026-11-22T00:00:00Z"
			}
			changed["status"].(map[string]interface{})[name] = value
			checkCEL(t, s, changed, old, false)
		}
		minimal := map[string]interface{}{"status": map[string]interface{}{"phase": phase}}
		checkCEL(t, s, minimal, minimal, true)
		changed := cloneJSON(minimal)
		changed["status"].(map[string]interface{})["reason"] = "added"
		checkCEL(t, s, changed, minimal, false)
		changed = cloneJSON(old)
		changed["status"].(map[string]interface{})["observedGeneration"] = int64(2)
		checkCEL(t, s, changed, old, true)
	}
	checkCEL(t, s, map[string]interface{}{"status": map[string]interface{}{}}, nil, false)
	checkCEL(t, s, map[string]interface{}{"status": map[string]interface{}{"reason": "reason"}}, nil, false)
	checkCEL(t, s, map[string]interface{}{}, nil, true)
	checkCEL(t, s, map[string]interface{}{"status": map[string]interface{}{"phase": "Refused"}}, map[string]interface{}{"status": map[string]interface{}{"phase": "Pending"}}, true)
}
func TestExecutorSchemaValidation(t *testing.T) {
	for _, kind := range []string{"executorenrollments", "instanceoperations"} {
		raw, err := os.ReadFile("../../config/crd/bases/core.devicechain.io_" + kind + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		var crd extv1.CustomResourceDefinition
		_ = yaml.Unmarshal(raw, &crd)
		var schema apiextensions.JSONSchemaProps
		if err := extv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &schema, nil); err != nil {
			t.Fatal(err)
		}
		validator, _, err := apivalidation.NewSchemaValidator(&schema)
		if err != nil {
			t.Fatal(err)
		}
		spec := map[string]interface{}{"owner": "owner", "executorImage": "r/operator@sha256:" + strings.Repeat("a", 64), "escrowDelivery": "OwnerOffline", "protocolVersion": float64(1)}
		if kind == "instanceoperations" {
			spec = map[string]interface{}{"instance": "prod", "instanceUID": "target", "enrollmentUID": "enrollment", "action": "Upgrade", "protocolVersion": float64(1)}
		}
		check := func(spec map[string]interface{}, status interface{}, valid bool) {
			t.Helper()
			o := map[string]interface{}{"spec": spec}
			if status != nil {
				o["status"] = status
			}
			errs := apivalidation.ValidateCustomResource(field.NewPath("obj"), o, validator)
			if (len(errs) == 0) != valid {
				t.Fatalf("%s valid=%v: %v", kind, valid, errs)
			}
		}
		check(spec, nil, true)
		for _, name := range schema.Properties["spec"].Required {
			changed := cloneJSON(spec)
			delete(changed, name)
			check(changed, nil, false)
		}
		cases := map[string][]interface{}{"protocolVersion": {float64(0)}}
		if kind == "executorenrollments" {
			cases["owner"] = []interface{}{"", "Upper", "a/b", strings.Repeat("a", 64)}
			cases["executorImage"] = []interface{}{"r:v1", "r@sha256:abc", "a b@sha256:" + strings.Repeat("a", 64)}
			cases["escrowDelivery"] = []interface{}{"Other"}
		} else {
			cases["instance"] = []interface{}{"", "Upper", "a.b", strings.Repeat("a", 64)}
			cases["enrollmentUID"] = []interface{}{""}
			cases["action"] = []interface{}{"Future"}
			cases["imageVersion"] = []interface{}{"r/v1", "r@abc", "r:v1", ""}
			check(spec, map[string]interface{}{"reason": "missing phase"}, false)
			check(spec, map[string]interface{}{"phase": "Future"}, false)
			check(spec, map[string]interface{}{"phase": "Refused"}, true)
		}
		for name, values := range cases {
			for _, value := range values {
				changed := cloneJSON(spec)
				changed[name] = value
				check(changed, nil, false)
			}
		}
	}
}
