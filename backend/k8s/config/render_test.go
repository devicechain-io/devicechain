// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// TestRenderOperatorContents checks the rendered overlay carries the expected
// resources and that the image override was applied (the placeholder
// "controller" image is rewritten to the requested reference).
func TestRenderOperatorContents(t *testing.T) {
	out, err := RenderOperator("localhost:5000/operator:dev")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"kind: CustomResourceDefinition",
		"kind: ClusterRole",
		"kind: Deployment",
		"namespace: dc-k8s-system",
		"name: dc-k8s-controller-manager",
		"image: localhost:5000/operator:dev",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered operator manifests missing %q", want)
		}
	}
	if strings.Contains(s, "image: controller:latest") {
		t.Error("placeholder image was not overridden")
	}
}

// TestRenderOperatorLeaderElection checks the production deployment and its
// namespaced lease permissions together, without starting a manager or cluster.
func TestRenderOperatorLeaderElection(t *testing.T) {
	out, err := RenderOperator("localhost:5000/operator:dev")
	if err != nil {
		t.Fatal(err)
	}
	var deployment *appsv1.Deployment
	var role *rbacv1.Role
	var binding *rbacv1.RoleBinding
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	for {
		var raw runtime.RawExtension
		if err := decoder.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		var meta struct {
			metav1.TypeMeta
			Metadata metav1.ObjectMeta `json:"metadata"`
		}
		if err := json.Unmarshal(raw.Raw, &meta); err != nil {
			t.Fatal(err)
		}
		var target any
		switch {
		case meta.Kind == "Deployment" && meta.Metadata.Name == "dc-k8s-controller-manager":
			deployment = &appsv1.Deployment{}
			target = deployment
		case meta.Kind == "Role" && meta.Metadata.Name == "dc-k8s-leader-election-role":
			role = &rbacv1.Role{}
			target = role
		case meta.Kind == "RoleBinding" && meta.Metadata.Name == "dc-k8s-leader-election-rolebinding":
			binding = &rbacv1.RoleBinding{}
			target = binding
		default:
			continue
		}
		if err := json.Unmarshal(raw.Raw, target); err != nil {
			t.Fatal(err)
		}
	}
	if deployment == nil || role == nil || binding == nil {
		t.Fatal("rendered operator must include the manager Deployment and election Role/RoleBinding")
	}
	for _, namespace := range []string{deployment.Namespace, role.Namespace, binding.Namespace} {
		if namespace != "dc-k8s-system" {
			t.Errorf("election resource namespace = %q, want dc-k8s-system", namespace)
		}
	}
	var managerFound bool
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != "manager" {
			continue
		}
		managerFound = true
		want := []string{"--health-probe-bind-address=:8081", "--metrics-bind-address=0", "--leader-elect"}
		if !reflect.DeepEqual(container.Args, want) {
			t.Errorf("manager args = %v, want %v", container.Args, want)
		}
	}
	if !managerFound {
		t.Error("rendered deployment has no manager container")
	}
	var leaseRuleFound bool
	for _, rule := range role.Rules {
		if !slices.Contains(rule.APIGroups, "coordination.k8s.io") || !slices.Contains(rule.Resources, "leases") {
			continue
		}
		leaseRuleFound = true
		for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
			if !slices.Contains(rule.Verbs, verb) {
				t.Errorf("election Role is missing lease verb %q", verb)
			}
		}
	}
	if !leaseRuleFound {
		t.Error("election Role has no coordination.k8s.io lease permissions")
	}
	wantRole := rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: role.Name}
	if binding.RoleRef != wantRole {
		t.Errorf("election RoleBinding roleRef = %+v, want %+v", binding.RoleRef, wantRole)
	}
	wantSubjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: deployment.Spec.Template.Spec.ServiceAccountName, Namespace: deployment.Namespace}}
	if deployment.Spec.Template.Spec.ServiceAccountName != "dc-k8s-controller-manager" {
		t.Errorf("unexpected manager service account: %q", deployment.Spec.Template.Spec.ServiceAccountName)
	}
	if !reflect.DeepEqual(binding.Subjects, wantSubjects) {
		t.Errorf("election RoleBinding subjects = %+v, want %+v", binding.Subjects, wantSubjects)
	}
}

// TestRenderOperatorOrdering guards the legacy reorder: the Namespace and CRDs
// must be emitted before the namespaced resources that depend on them, so the
// stream applies cleanly without kubectl's kind-sorting.
func TestRenderOperatorOrdering(t *testing.T) {
	out, err := RenderOperator("localhost:5000/operator:dev")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	ns := strings.Index(s, "kind: Namespace")
	sa := strings.Index(s, "kind: ServiceAccount")
	dep := strings.Index(s, "kind: Deployment")
	if ns < 0 {
		t.Fatal("no Namespace in rendered output")
	}
	if ns > sa {
		t.Errorf("Namespace (%d) must precede ServiceAccount (%d)", ns, sa)
	}
	if ns > dep {
		t.Errorf("Namespace (%d) must precede Deployment (%d)", ns, dep)
	}
}

// TestSplitImageRef covers the registry-port edge case.
func TestSplitImageRef(t *testing.T) {
	cases := []struct{ in, name, tag string }{
		{"localhost:5000/operator:dev", "localhost:5000/operator", "dev"},
		{"ghcr.io/devicechain-io/operator:1.2.3", "ghcr.io/devicechain-io/operator", "1.2.3"},
		{"controller", "controller", ""},
		{"localhost:5000/op", "localhost:5000/op", ""},
	}
	for _, c := range cases {
		name, tag := splitImageRef(c.in)
		if name != c.name || tag != c.tag {
			t.Errorf("splitImageRef(%q) = (%q,%q), want (%q,%q)", c.in, name, tag, c.name, c.tag)
		}
	}
}
