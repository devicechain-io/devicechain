// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestExplicitServerSideOwnershipPreservesLegacyCallers(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
		gvk := schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
		mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gvk.GroupVersion()})
		mapper.Add(gvk, meta.RESTScopeNamespace)
		obj := unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "example", "namespace": "example"}}}
		calls := 0
		dyn.PrependReactor("patch", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
			calls++
			options := a.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions()
			if explicit {
				if options.FieldManager != "dcctl-executor" || options.Force == nil || *options.Force {
					t.Fatalf("explicit policy lost: %+v", options)
				}
			} else if options.FieldManager != "k8sutil" || options.Force == nil || !*options.Force {
				t.Fatalf("legacy policy changed: %+v", options)
			}
			return true, &unstructured.Unstructured{}, nil
		})
		var err error
		if explicit {
			force := false
			_, err = ApplyUnstructuredWithOptions(t.Context(), dyn, mapper, obj, true, metav1.PatchOptions{FieldManager: "dcctl-executor", Force: &force})
		} else {
			_, err = ApplyUnstructured(t.Context(), dyn, mapper, obj, true)
		}
		if err != nil || calls != 1 {
			t.Fatalf("calls=%d: %v", calls, err)
		}
	}
}
