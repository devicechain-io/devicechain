// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

func executorObjects(t *testing.T) []unstructured.Unstructured {
	t.Helper()
	b, err := RenderExecutor("registry/operator@sha256:"+strings.Repeat("a", 64), EnrollmentInput{Owner: "owner", ProtocolVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	d := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	var out []unstructured.Unstructured
	for {
		var o unstructured.Unstructured
		err := d.Decode(&o)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if o.GetKind() != "" {
			out = append(out, o)
		}
	}
	return out
}

func TestExecutorNamespaceOwnershipMarker(t *testing.T) {
	for _, obj := range executorObjects(t) {
		if obj.GetKind() == "Namespace" && obj.GetName() == "dc-executor-system" {
			if obj.GetLabels()["app.kubernetes.io/managed-by"] != "dcctl-executor" {
				t.Fatal("executor namespace lacks ownership label")
			}
			return
		}
	}
	t.Fatal("executor namespace missing")
}
func grants(rules []rbacv1.PolicyRule) []string {
	var out []string
	for _, r := range rules {
		for _, g := range r.APIGroups {
			for _, resource := range r.Resources {
				for _, verb := range r.Verbs {
					out = append(out, g+"/"+resource+"/"+verb)
				}
			}
		}
		for _, u := range r.NonResourceURLs {
			out = append(out, "URL/"+u)
		}
		for _, n := range r.ResourceNames {
			out = append(out, "NAME/"+n)
		}
	}
	sort.Strings(out)
	return out
}
func expectedGrants(group string, resources []string, verbs []string) []string {
	return grants([]rbacv1.PolicyRule{{APIGroups: []string{group}, Resources: resources, Verbs: verbs}})
}
func TestExecutorRBACExact(t *testing.T) {
	read := []string{"get", "list", "watch"}
	status := []string{"get", "update", "patch"}
	expected := map[string][]string{
		"ClusterRole/dc-executor":           append(expectedGrants("core.devicechain.io", []string{"instances", "executorenrollments", "instanceoperations"}, read), expectedGrants("core.devicechain.io", []string{"executorenrollments/status", "instanceoperations/status"}, status)...),
		"ClusterRole/dc-executor-requester": append(expectedGrants("core.devicechain.io", []string{"instanceoperations"}, []string{"create", "get", "list", "watch"}), expectedGrants("core.devicechain.io", []string{"instances", "executorenrollments"}, read)...),
		"Role/dc-executor":                  append(expectedGrants("", []string{"pods"}, []string{"get"}), expectedGrants("coordination.k8s.io", []string{"leases"}, []string{"get", "list", "watch", "create", "update", "patch"})...),
	}
	for _, o := range executorObjects(t) {
		key := o.GetKind() + "/" + o.GetName()
		if o.GetKind() == "RoleBinding" || o.GetKind() == "ClusterRoleBinding" {
			name, _, _ := unstructured.NestedString(o.Object, "roleRef", "name")
			if name == "dc-executor-requester" {
				t.Fatal("requester is bound")
			}
		}
		if o.GetKind() != "Role" && o.GetKind() != "ClusterRole" {
			continue
		}
		want, ok := expected[key]
		if !ok {
			t.Fatalf("unexpected role %s", key)
		}
		b, _ := yaml.Marshal(o.Object)
		var role rbacv1.ClusterRole
		if err := yaml.Unmarshal(b, &role); err != nil {
			t.Fatal(err)
		}
		sort.Strings(want)
		if got := grants(role.Rules); !reflect.DeepEqual(got, want) {
			t.Errorf("%s grants=%v, want %v", key, got, want)
		}
		delete(expected, key)
	}
	if len(expected) != 0 {
		t.Fatalf("missing roles: %v", expected)
	}
}

func TestExecutorClusterRBACOwnershipMarkers(t *testing.T) {
	seen := 0
	for _, obj := range executorObjects(t) {
		if obj.GetKind() == "ClusterRole" || obj.GetKind() == "ClusterRoleBinding" {
			seen++
			if obj.GetLabels()["app.kubernetes.io/managed-by"] != "dcctl-executor" {
				t.Errorf("%s/%s lacks ownership label", obj.GetKind(), obj.GetName())
			}
		}
	}
	if seen != 3 {
		t.Fatalf("got %d cluster RBAC objects, want 3", seen)
	}
}
func TestRenderExecutorValidationAndCredentials(t *testing.T) {
	for _, image := range []string{"operator:latest", "operator@sha256:abc", "operator@sha256:" + strings.Repeat("A", 64), "bad image@sha256:" + strings.Repeat("a", 64)} {
		if _, err := RenderExecutor(image, EnrollmentInput{Owner: "owner", ProtocolVersion: 1}); err == nil {
			t.Errorf("accepted %q", image)
		}
	}
	for _, owner := range []string{"", "UPPER", "bad/name", strings.Repeat("x", 64)} {
		if _, err := RenderExecutor("operator@sha256:"+strings.Repeat("a", 64), EnrollmentInput{Owner: owner, ProtocolVersion: 1}); err == nil {
			t.Errorf("accepted owner %q", owner)
		}
	}
	if _, err := RenderExecutor("operator@sha256:"+strings.Repeat("a", 64), EnrollmentInput{Owner: "owner"}); err == nil {
		t.Fatal("accepted protocol zero")
	}
	seen := map[string]bool{}
	for _, o := range executorObjects(t) {
		seen[o.GetKind()] = true
		if o.GetKind() == "Secret" {
			t.Fatal("rendered Secret")
		}
		var check func(interface{})
		check = func(v interface{}) {
			switch x := v.(type) {
			case map[string]interface{}:
				for k, v := range x {
					if strings.EqualFold(k, "token") || strings.EqualFold(k, "kubeconfig") {
						t.Fatalf("credential field %s", k)
					}
					check(v)
				}
			case []interface{}:
				for _, v := range x {
					check(v)
				}
			}
		}
		check(o.Object)
		if o.GetKind() == "Deployment" {
			containers, _, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
			c := containers[0].(map[string]interface{})
			if c["image"] != "registry/operator@sha256:"+strings.Repeat("a", 64) {
				t.Fatal(c["image"])
			}
			raw, _ := yaml.Marshal(o.Object)
			for _, want := range []string{"--executor", "--leader-elect", "--health-probe-bind-address=:8081", "--metrics-bind-address=0", "readOnlyRootFilesystem: true", "allowPrivilegeEscalation: false", "runAsNonRoot: true", "ALL", "POD_NAME", "POD_NAMESPACE", "automountServiceAccountToken: true"} {
				if !strings.Contains(string(raw), want) {
					t.Errorf("Deployment missing %s", want)
				}
			}
		}
		if o.GetKind() == "ExecutorEnrollment" {
			if o.GetName() != "cluster" {
				t.Fatal(o.GetName())
			}
			delivery, _, _ := unstructured.NestedString(o.Object, "spec", "escrowDelivery")
			if delivery != "OwnerOffline" {
				t.Fatal(delivery)
			}
		}
	}
	if !seen["ExecutorEnrollment"] || !seen["Deployment"] {
		t.Fatal(seen)
	}
}
func TestObserverPermissionsAndExecutorMarkers(t *testing.T) {
	b, err := os.ReadFile("rbac/role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(b, &role); err != nil {
		t.Fatal(err)
	}
	for _, r := range role.Rules {
		for _, g := range r.APIGroups {
			if g != "*" && g != "core.devicechain.io" {
				continue
			}
			for _, resource := range r.Resources {
				if resource == "*" || strings.HasPrefix(resource, "executorenrollments") || strings.HasPrefix(resource, "instanceoperations") {
					t.Fatal("observer gained executor grants")
				}
			}
		}
	}
	err = filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && filepath.Base(path) == "controllers" {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(b, []byte("+kubebuilder:"+"rbac")) {
				return fmt.Errorf("executor RBAC marker leaks into observer: %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir("../../../deploy/helm/devicechain/templates", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(b, []byte("dc-executor")) {
				return fmt.Errorf("chart installs executor: %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestExecutorCRDsParticipateInIdentity(t *testing.T) {
	res, err := renderOverlay("")
	if err != nil {
		t.Fatal(err)
	}
	before, err := identityOf(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"executorenrollments.core.devicechain.io", "instanceoperations.core.devicechain.io"} {
		changed := res.DeepCopy()
		found := false
		for _, r := range changed.Resources() {
			if r.GetName() == name {
				found = true
				if err := r.SetAnnotations(map[string]string{"test": "changed"}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if !found {
			t.Fatalf("identity omits %s", name)
		}
		after, err := identityOf(changed)
		if err != nil {
			t.Fatal(err)
		}
		if before == after {
			t.Fatal("CRD excluded from identity")
		}
	}
	for _, r := range res.Resources() {
		if strings.HasPrefix(r.GetName(), "dc-executor") {
			t.Fatal("executor overlay contributes to operator identity")
		}
	}
}

func TestRequesterNeverBoundInManifests(t *testing.T) {
	for _, root := range []string{".", "../../../deploy/helm"} {
		t.Run(root, func(t *testing.T) {
			if err := checkRequesterBindings(os.DirFS(root)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func checkRequesterBindings(manifests fs.FS) error {
	return fs.WalkDir(manifests, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") && !strings.HasSuffix(path, ".tpl")) {
			return nil
		}
		raw, err := fs.ReadFile(manifests, path)
		if err != nil {
			return err
		}
		// Helm source is not YAML; forbid requester references even in disabled templates.
		if bytes.Contains(raw, []byte("{{")) || strings.HasSuffix(path, ".tpl") {
			if bytes.Contains(raw, []byte("dc-executor-requester")) {
				return fmt.Errorf("requester reference in template %s", path)
			}
			return nil
		}
		decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
		for {
			var document interface{}
			err := decoder.Decode(&document)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("decoding %s: %w", path, err)
			}
			object, ok := document.(map[string]interface{})
			if !ok {
				continue
			}
			if bindsRequester(object) {
				return fmt.Errorf("requester is bound in %s", path)
			}
		}
	})
}

func bindsRequester(document map[string]interface{}) bool {
	object := unstructured.Unstructured{Object: document}
	switch object.GetKind() {
	case "RoleBinding", "ClusterRoleBinding":
		name, _, _ := unstructured.NestedString(document, "roleRef", "name")
		return name == "dc-executor-requester"
	case "List":
		items, _, _ := unstructured.NestedSlice(document, "items")
		for _, item := range items {
			if nested, ok := item.(map[string]interface{}); ok && bindsRequester(nested) {
				return true
			}
		}
	}
	return false
}

func TestRequesterBindingWalker(t *testing.T) {
	const binding = "apiVersion: rbac.authorization.k8s.io/v1\nkind: %s\nmetadata:\n  name: planted\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: dc-executor-requester\n"
	const nonObjects = "- op: replace\n  path: /metadata/name\n  value: dc-executor-requester\n---\ndc-executor-requester\n---\nnull\n---\n"
	for _, root := range []string{"backend/k8s/config", "deploy/helm"} {
		for _, kind := range []string{"RoleBinding", "ClusterRoleBinding"} {
			t.Run(root+"/"+kind, func(t *testing.T) {
				path := root + "/nested/planted.yml"
				manifests := fstest.MapFS{
					path: {Data: []byte(nonObjects + "kind: ClusterRole\nmetadata:\n  name: dc-executor-requester\n")},
				}
				if err := checkRequesterBindings(manifests); err != nil {
					t.Fatalf("non-object documents and an unbound role must pass: %v", err)
				}
				manifests[path].Data = []byte(nonObjects + fmt.Sprintf(binding, kind))
				if err := checkRequesterBindings(manifests); err == nil || !strings.Contains(err.Error(), "requester is bound in "+path) {
					t.Fatalf("planted binding was not detected: %v", err)
				}
			})
		}
	}
	t.Run("Kubernetes List", func(t *testing.T) {
		raw := "kind: List\nitems:\n- " + strings.ReplaceAll(strings.TrimSuffix(fmt.Sprintf(binding, "RoleBinding"), "\n"), "\n", "\n  ") + "\n"
		if err := checkRequesterBindings(fstest.MapFS{"list.yaml": {Data: []byte(raw)}}); err == nil || !strings.Contains(err.Error(), "requester is bound") {
			t.Fatalf("binding in Kubernetes List was not detected: %v", err)
		}
	})
	for _, ext := range []string{"yaml", "tpl"} {
		t.Run("Helm template "+ext, func(t *testing.T) {
			path := "deploy/helm/chart/templates/binding." + ext
			raw := "{{- if .Values.enabled }}\n" + fmt.Sprintf(binding, "ClusterRoleBinding") + "{{- end }}\n"
			if err := checkRequesterBindings(fstest.MapFS{path: {Data: []byte(raw)}}); err == nil || !strings.Contains(err.Error(), "requester reference in template "+path) {
				t.Fatalf("binding in Helm template was not detected: %v", err)
			}
		})
	}
	t.Run("malformed YAML", func(t *testing.T) {
		if err := checkRequesterBindings(fstest.MapFS{"broken.yaml": {Data: []byte("kind: [")}}); err == nil {
			t.Fatal("malformed YAML must fail closed")
		}
	})
}
