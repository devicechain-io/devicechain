// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"context"
	"os"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	celvalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"
)

func executorSchema(t *testing.T, name string) *schema.Structural {
	t.Helper()
	b, err := os.ReadFile("../../config/crd/bases/core.devicechain.io_" + name + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd extv1.CustomResourceDefinition
	if err := yaml.Unmarshal(b, &crd); err != nil {
		t.Fatal(err)
	}
	var internal apiextensions.JSONSchemaProps
	if err := extv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &internal, nil); err != nil {
		t.Fatal(err)
	}
	s, err := schema.NewStructural(&internal)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func checkCEL(t *testing.T, s *schema.Structural, obj, old interface{}, valid bool) {
	t.Helper()
	v := celvalidation.NewValidator(s, true, 10000000)
	if v == nil {
		t.Fatal("schema has no CEL validator")
	}
	errs, _ := v.Validate(context.Background(), field.NewPath("object"), s, obj, old, 10000000)
	if (len(errs) == 0) != valid {
		t.Fatalf("valid=%v, errors=%v", valid, errs)
	}
}

func TestExecutorEnrollmentSingletonCEL(t *testing.T) {
	s := executorSchema(t, "executorenrollments")
	for _, name := range []string{"cluster", "other"} {
		checkCEL(t, s, map[string]interface{}{"metadata": map[string]interface{}{"name": name}}, nil, name == "cluster")
	}
}

func TestInstanceOperationTerminalCEL(t *testing.T) {
	s := executorSchema(t, "instanceoperations")
	status := func(phase, reason string) map[string]interface{} {
		return map[string]interface{}{"phase": phase, "reason": reason, "message": "Preview", "decidedAt": "2026-11-21T00:00:00Z"}
	}
	for _, phase := range []string{"Refused", "Succeeded", "Failed"} {
		old := map[string]interface{}{"status": status(phase, "Original")}
		checkCEL(t, s, map[string]interface{}{"status": status(phase, "Original")}, old, true)
		checkCEL(t, s, map[string]interface{}{"status": status(phase, "Changed")}, old, false)
		checkCEL(t, s, map[string]interface{}{"status": status("Pending", "Original")}, old, false)
		checkCEL(t, s, map[string]interface{}{}, old, false)
		checkCEL(t, s, map[string]interface{}{"status": map[string]interface{}{"reason": "Original"}}, old, false)
	}
}
