// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"os"
	"reflect"
	"sort"
	"strconv"
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
	for _, action := range []string{"Upgrade", "Teardown", "Bootstrap"} {
		changed := cloneJSON(op)
		changed["action"] = action
		delete(changed, "instanceUID")
		checkCEL(t, &s, changed, nil, action == "Bootstrap")
		changed["instanceUID"] = ""
		checkCEL(t, &s, changed, nil, action == "Bootstrap")
	}
}

func TestClusterInstallSpecCEL(t *testing.T) {
	s := executorSchema(t, "instanceoperations").Properties["spec"]
	for _, action := range []string{"Install", "Bootstrap", "Upgrade", "Teardown"} {
		for _, instance := range []bool{false, true} {
			for _, uid := range []bool{false, true} {
				t.Run(action+"/instance="+strconv.FormatBool(instance)+"/uid="+strconv.FormatBool(uid), func(t *testing.T) {
					op := map[string]interface{}{"action": action, "enrollmentUID": "enrollment", "protocolVersion": int64(1)}
					if instance {
						op["instance"] = "prod"
					}
					if uid {
						op["instanceUID"] = "target"
					}
					valid := instance && (uid || action == "Bootstrap")
					if action == "Install" {
						valid = !instance && !uid
					}
					checkCEL(t, &s, op, nil, valid)
				})
			}
		}
	}
	// Presence matters even when a forbidden field is empty.
	for _, name := range []string{"instance", "instanceUID"} {
		op := map[string]interface{}{"action": "Install", "enrollmentUID": "enrollment", "protocolVersion": int64(1), name: ""}
		checkCEL(t, &s, op, nil, false)
	}
}

func TestOperationConditionsAndTypedCapability(t *testing.T) {
	fieldType, _ := reflect.TypeOf(api.ExecutorCapability{}).FieldByName("ExecutableActions")
	if fieldType.Type != reflect.TypeOf([]api.OperationAction{}) {
		t.Error("capability items must be OperationAction")
	}
	conditions, ok := reflect.TypeOf(api.InstanceOperationStatus{}).FieldByName("Conditions")
	if !ok || conditions.Tag.Get("patchStrategy") != "merge" || conditions.Tag.Get("patchMergeKey") != "type" {
		t.Error("operation conditions must support strategic merge by type")
	}
	s := executorSchema(t, "instanceoperations")
	c := s.Properties["status"].Properties["conditions"]
	if c.XListType == nil || *c.XListType != "map" || !reflect.DeepEqual(c.XListMapKeys, []string{"type"}) {
		t.Fatal("operation condition schema must merge by type")
	}
	old := map[string]interface{}{"status": map[string]interface{}{"phase": "Refused", "reason": "NotExecutableInThisBuild", "message": "Preview", "decidedAt": "2026-11-21T00:00:00Z", "conditions": []interface{}{map[string]interface{}{"type": "Decided", "status": "True", "reason": "NotExecutableInThisBuild", "message": "Preview", "lastTransitionTime": "2026-11-21T00:00:00Z"}}}}
	changed := cloneJSON(old)
	changed["status"].(map[string]interface{})["conditions"].([]interface{})[0].(map[string]interface{})["lastTransitionTime"] = "2026-11-22T00:00:00Z"
	checkCEL(t, s, changed, old, true)
}

func validateExecutorObject(t *testing.T, kind string, object map[string]interface{}, valid bool) {
	t.Helper()
	raw, err := os.ReadFile("../../config/crd/bases/core.devicechain.io_" + kind + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd extv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	var schema apiextensions.JSONSchemaProps
	if err := extv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &schema, nil); err != nil {
		t.Fatal(err)
	}
	validator, _, err := apivalidation.NewSchemaValidator(&schema)
	if err != nil {
		t.Fatal(err)
	}
	errs := apivalidation.ValidateCustomResource(field.NewPath("obj"), object, validator)
	if (len(errs) == 0) != valid {
		t.Fatalf("valid=%v, errors=%v", valid, errs)
	}
}

func TestExecutorSchemaBoundsAndActionItems(t *testing.T) {
	base := map[string]interface{}{"spec": map[string]interface{}{"instance": "prod", "instanceUID": "target", "enrollmentUID": "enrollment", "action": "Upgrade", "protocolVersion": int64(1)}, "status": map[string]interface{}{"phase": "Refused", "reason": "NotExecutableInThisBuild", "message": "Preview"}}
	for _, tc := range []struct {
		parent, name string
		limit        int
	}{{"spec", "instanceUID", 36}, {"spec", "enrollmentUID", 36}, {"status", "reason", 128}, {"status", "message", 32768}} {
		t.Run(tc.name, func(t *testing.T) {
			for _, size := range []int{tc.limit, tc.limit + 1} {
				obj := cloneJSON(base)
				obj[tc.parent].(map[string]interface{})[tc.name] = strings.Repeat("A", size)
				validateExecutorObject(t, "instanceoperations", obj, size == tc.limit)
			}
		})
	}
	for _, reason := range []string{"", "lowercase", "Has Space", "Has-Hyphen", "_Leading", "1Leading"} {
		t.Run("reason/"+reason, func(t *testing.T) {
			obj := cloneJSON(base)
			obj["status"].(map[string]interface{})["reason"] = reason
			validateExecutorObject(t, "instanceoperations", obj, false)
		})
	}
	for _, reason := range []string{"StaleEnrollment", "InstanceExists", "InstanceNotFound", "StaleTarget", "NotExecutableInThisBuild", "Executable", "Unenrolled"} {
		obj := cloneJSON(base)
		obj["status"].(map[string]interface{})["reason"] = reason
		validateExecutorObject(t, "instanceoperations", obj, true)
	}
	for _, size := range []int{512, 513} {
		image := strings.Repeat("r", size-72) + "@sha256:" + strings.Repeat("a", 64)
		obj := map[string]interface{}{"spec": map[string]interface{}{"owner": "owner", "executorImage": image, "escrowDelivery": "OwnerOffline", "protocolVersion": int64(1)}}
		validateExecutorObject(t, "executorenrollments", obj, size == 512)
	}
	for _, action := range []string{"Install", "Bootstrap", "Upgrade", "Teardown", "Future"} {
		obj := map[string]interface{}{"spec": map[string]interface{}{"owner": "owner", "executorImage": "r@sha256:" + strings.Repeat("a", 64), "escrowDelivery": "OwnerOffline", "protocolVersion": int64(1)}, "status": map[string]interface{}{"capability": map[string]interface{}{"protocolVersion": int64(1), "executableActions": []interface{}{action}}}}
		validateExecutorObject(t, "executorenrollments", obj, action != "Future")
	}
	install := map[string]interface{}{"spec": map[string]interface{}{"action": "Install", "enrollmentUID": "enrollment", "protocolVersion": int64(1)}}
	validateExecutorObject(t, "instanceoperations", install, true)
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
