// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

// This file is also copied into the released baseline by the activation gate.
func TestFoundationProbe(t *testing.T) {
	for _, phase := range []string{"", "Refused"} {
		t.Run("phase="+phase, func(t *testing.T) {
			op := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "core.devicechain.io/v1beta1", "kind": "InstanceOperation",
				"metadata": map[string]interface{}{"name": "probe-operation"},
				"spec":     map[string]interface{}{"instance": "probe", "action": "Upgrade"},
			}}
			if phase != "" {
				op.Object["status"] = map[string]interface{}{"phase": phase}
			}
			gvr := schema.GroupVersionResource{Group: "core.devicechain.io", Version: "v1beta1", Resource: "instanceoperations"}
			// Refer to the released request API as well as exercising its refusal.
			// A baseline without that API must fail to compile this probe.
			// The typed kinds are deliberately NOT registered: the dynamic fake would then
			// convert list items into them and fail on the unstructured fixture.
			_ = api.InstanceOperationList{}
			dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "InstanceOperationList"}, op)
			err := RefuseForPendingOperation(t.Context(), dyn, "probe", "an upgrade")
			if (err != nil) != (phase == "") {
				t.Fatalf("phase %q: refusal error = %v", phase, err)
			}
		})
	}
}
