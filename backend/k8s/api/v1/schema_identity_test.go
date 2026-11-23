// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	dcv1 "github.com/devicechain-io/dc-k8s/api/v1"
	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// 🔴 v1 AND v1beta1 MUST STAY STRUCTURALLY IDENTICAL FOR AS LONG AS BOTH ARE SERVED.
//
// The CRD has no conversion webhook, so the API server converts between the two by
// relabelling apiVersion and PRUNING the object to the schema of the version it is
// read or written through. A field one version has and the other lacks is therefore
// dropped by any write through the other, including the operator's status patch
// from a pod that still speaks v1beta1. Nothing errors; the data is simply gone.
// So every schema, rule, message and printer column is compared here, from the
// generated manifest the API server actually enforces, and the Go types are
// compared as well because the manifest is generated from them.

func loadCRD(t *testing.T) map[string]interface{} {
	t.Helper()
	path := filepath.Join("..", "..", "config", "crd", "bases", "core.devicechain.io_instances.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the generated CRD: %v (generate it with `make manifests`)", err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func versionsOf(t *testing.T, crd map[string]interface{}) map[string]map[string]interface{} {
	t.Helper()
	out := map[string]map[string]interface{}{}
	raw, _ := crd["spec"].(map[string]interface{})["versions"].([]interface{})
	for _, v := range raw {
		ver := v.(map[string]interface{})
		out[ver["name"].(string)] = ver
	}
	return out
}

// diffTrees lists every path at which two decoded documents differ. It is the
// instrument; the tests below point it at the CRD, and the negative controls point
// it at a CRD with one thing taken away.
func diffTrees(path string, a, b interface{}) []string {
	switch av := a.(type) {
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok {
			return []string{path + ": object vs " + fmt.Sprintf("%T", b)}
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		var names []string
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		var out []string
		for _, k := range names {
			x, inA := av[k]
			y, inB := bv[k]
			switch {
			case !inA:
				out = append(out, path+"."+k+": only in the second")
			case !inB:
				out = append(out, path+"."+k+": only in the first")
			default:
				out = append(out, diffTrees(path+"."+k, x, y)...)
			}
		}
		return out
	case []interface{}:
		bv, ok := b.([]interface{})
		if !ok {
			return []string{path + ": list vs " + fmt.Sprintf("%T", b)}
		}
		if len(av) != len(bv) {
			return []string{fmt.Sprintf("%s: %d entries vs %d", path, len(av), len(bv))}
		}
		var out []string
		for i := range av {
			out = append(out, diffTrees(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i])...)
		}
		return out
	default:
		if !reflect.DeepEqual(a, b) {
			return []string{fmt.Sprintf("%s: %v vs %v", path, a, b)}
		}
		return nil
	}
}

// versionDiff compares everything about two served versions except what names them
// and what marks the storage one.
func versionDiff(a, b map[string]interface{}) []string {
	strip := func(m map[string]interface{}) map[string]interface{} {
		cp := map[string]interface{}{}
		for k, v := range m {
			if k == "name" || k == "storage" {
				continue
			}
			cp[k] = v
		}
		return cp
	}
	return diffTrees("version", strip(a), strip(b))
}

func deepCopy(t *testing.T, v map[string]interface{}) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTheCRDServesBothVersionsAndStoresV1beta1(t *testing.T) {
	crd := loadCRD(t)
	vs := versionsOf(t, crd)
	if len(vs) != 2 || vs["v1"] == nil || vs["v1beta1"] == nil {
		t.Fatalf("the Instance CRD should serve exactly v1beta1 and v1, got %d versions", len(vs))
	}
	for name, v := range vs {
		if v["served"] != true {
			t.Errorf("%s is not served", name)
		}
	}
	if vs["v1beta1"]["storage"] != true || vs["v1"]["storage"] == true {
		t.Errorf("storage must stay v1beta1 until the storage flip (v1beta1=%v, v1=%v)",
			vs["v1beta1"]["storage"], vs["v1"]["storage"])
	}
	// No conversion webhook: with identical schemas None only relabels apiVersion,
	// and a webhook would be an availability dependency for every read.
	if conv, ok := crd["spec"].(map[string]interface{})["conversion"].(map[string]interface{}); ok {
		if conv["strategy"] != "None" {
			t.Errorf("conversion strategy is %v, want None", conv["strategy"])
		}
	}
	for name, v := range vs {
		if v["deprecated"] == true {
			t.Errorf("%s is marked deprecated; deprecating v1beta1 belongs to the storage flip, not this slice", name)
		}
	}
}

func TestV1AndV1beta1SchemasAreIdentical(t *testing.T) {
	vs := versionsOf(t, loadCRD(t))
	if d := versionDiff(vs["v1beta1"], vs["v1"]); len(d) != 0 {
		t.Fatalf("v1 and v1beta1 differ, so a write through one would prune data the other holds:\n  %s\n"+
			"A field is added to BOTH versions in the same release, or to neither.", strings.Join(d, "\n  "))
	}
}

// 🔴 NEGATIVE CONTROLS. An identity check that cannot tell two schemas apart is the
// gate that cannot fail. Each case plants one defect in v1 and demands the
// instrument report it.
func TestTheIdentityCheckSeesEachDefectItGuards(t *testing.T) {
	vs := versionsOf(t, loadCRD(t))
	root := func(v map[string]interface{}) map[string]interface{} {
		return v["schema"].(map[string]interface{})["openAPIV3Schema"].(map[string]interface{})["properties"].(map[string]interface{})
	}
	spec := func(v map[string]interface{}) map[string]interface{} {
		return root(v)["spec"].(map[string]interface{})
	}
	status := func(v map[string]interface{}) map[string]interface{} {
		return root(v)["status"].(map[string]interface{})
	}
	cases := []struct {
		name  string
		plant func(v map[string]interface{})
	}{
		{"a spec field missing from v1", func(v map[string]interface{}) {
			delete(spec(v)["properties"].(map[string]interface{}), "grafanaSSO")
		}},
		{"a status field missing from v1", func(v map[string]interface{}) {
			delete(status(v)["properties"].(map[string]interface{}), "areas")
		}},
		{"a CEL rule missing from v1", func(v map[string]interface{}) {
			rules := spec(v)["x-kubernetes-validations"].([]interface{})
			spec(v)["x-kubernetes-validations"] = rules[1:]
		}},
		{"a CEL message edited in v1", func(v map[string]interface{}) {
			rules := spec(v)["x-kubernetes-validations"].([]interface{})
			rules[0].(map[string]interface{})["message"] = "edited"
		}},
		{"a field's constraint relaxed in v1", func(v map[string]interface{}) {
			delete(spec(v)["properties"].(map[string]interface{})["provider"].(map[string]interface{}), "minLength")
		}},
		{"a required list shortened in v1", func(v map[string]interface{}) {
			spec(v)["required"] = []interface{}{"provider"}
		}},
		{"a printer column dropped from v1", func(v map[string]interface{}) {
			cols := v["additionalPrinterColumns"].([]interface{})
			v["additionalPrinterColumns"] = cols[1:]
		}},
		{"the status subresource dropped from v1", func(v map[string]interface{}) {
			delete(v, "subresources")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			planted := deepCopy(t, vs["v1"])
			if d := versionDiff(vs["v1beta1"], planted); len(d) != 0 {
				t.Fatalf("the control starts from differing versions: %v", d)
			}
			c.plant(planted)
			if d := versionDiff(vs["v1beta1"], planted); len(d) == 0 {
				t.Fatal("the planted defect went unreported, so the identity check cannot fail")
			}
		})
	}
}

// shape renders a Go type as its JSON surface: field names, tags and kinds, down
// through every struct this module declares.
func shape(rt reflect.Type, seen map[reflect.Type]bool) string {
	for rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
	}
	switch rt.Kind() {
	case reflect.Struct:
		if !strings.Contains(rt.PkgPath(), "dc-k8s/api") {
			return rt.PkgPath() + "." + rt.Name()
		}
		if seen[rt] {
			return rt.Name()
		}
		seen[rt] = true
		var parts []string
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			parts = append(parts, f.Name+" `"+string(f.Tag)+"` "+shape(f.Type, seen))
		}
		return rt.Name() + "{" + strings.Join(parts, "; ") + "}"
	case reflect.Slice, reflect.Array:
		return "[]" + shape(rt.Elem(), seen)
	case reflect.Map:
		return "map[" + shape(rt.Key(), seen) + "]" + shape(rt.Elem(), seen)
	default:
		return rt.PkgPath() + "." + rt.Name() + "/" + rt.Kind().String()
	}
}

func TestTheGoTypesAreStructurallyIdentical(t *testing.T) {
	pairs := []struct {
		name string
		a, b reflect.Type
	}{
		{"Instance", reflect.TypeOf(dcv1beta1.Instance{}), reflect.TypeOf(dcv1.Instance{})},
		{"InstanceList", reflect.TypeOf(dcv1beta1.InstanceList{}), reflect.TypeOf(dcv1.InstanceList{})},
	}
	for _, p := range pairs {
		if a, b := shape(p.a, map[reflect.Type]bool{}), shape(p.b, map[reflect.Type]bool{}); a != b {
			t.Errorf("%s differs between v1beta1 and v1:\n  v1beta1: %s\n  v1:      %s", p.name, a, b)
		}
	}
}

// The generated manifest could be stale against the v1 Go type (someone edits the
// type and forgets `make manifests`); the schema-identity test would then compare
// two stale halves. This ties v1's served spec to the v1 Go type directly.
func TestTheGeneratedV1SpecMatchesTheV1GoType(t *testing.T) {
	vs := versionsOf(t, loadCRD(t))
	props := vs["v1"]["schema"].(map[string]interface{})["openAPIV3Schema"].(map[string]interface{})["properties"].(map[string]interface{})["spec"].(map[string]interface{})["properties"].(map[string]interface{})
	var inCRD, inType []string
	for k := range props {
		inCRD = append(inCRD, k)
	}
	rt := reflect.TypeOf(dcv1.InstanceSpec{})
	for i := 0; i < rt.NumField(); i++ {
		inType = append(inType, strings.Split(rt.Field(i).Tag.Get("json"), ",")[0])
	}
	sort.Strings(inCRD)
	sort.Strings(inType)
	if !reflect.DeepEqual(inCRD, inType) {
		t.Fatalf("the v1 spec in the generated CRD does not match the v1 Go type (run `make manifests`):\n  CRD:  %v\n  type: %v", inCRD, inType)
	}
}
