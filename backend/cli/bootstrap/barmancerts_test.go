// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func barmanCert(name string, ready bool, age time.Duration) *unstructured.Unstructured {
	status := "False"
	if ready {
		status = "True"
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata": map[string]interface{}{
			"name": name, "namespace": barmanCertNamespace,
			"uid": "old-uid", "resourceVersion": "7",
			"annotations": map[string]interface{}{"meta.helm.sh/release-name": "plugin-barman-cloud"},
		},
		"spec": map[string]interface{}{"secretName": name + "-tls"},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": status}}},
	}}
	u.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-age)))
	return u
}

func TestHealBarmanCertificates_RecreatesOnlyTheStuckOne(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{certificateGVR: "CertificateList"},
		barmanCert("barman-cloud-server", true, time.Hour),
		barmanCert("barman-cloud-client", false, time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	healBarmanCertificates(ctx, dyn, barmanCertNamespace, time.Minute, 50*time.Millisecond)

	res := dyn.Resource(certificateGVR).Namespace(barmanCertNamespace)
	client, err := res.Get(context.Background(), "barman-cloud-client", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructured.NestedMap(client.Object, "status"); found {
		t.Fatal("stuck Certificate was not re-created (still carries the old status)")
	}
	if got, _, _ := unstructured.NestedString(client.Object, "spec", "secretName"); got != "barman-cloud-client-tls" {
		t.Fatalf("spec not preserved: %q", got)
	}
	if client.GetAnnotations()["meta.helm.sh/release-name"] != "plugin-barman-cloud" {
		t.Fatal("Helm ownership annotation lost")
	}
	server, _ := res.Get(context.Background(), "barman-cloud-server", metav1.GetOptions{})
	if _, found, _ := unstructured.NestedMap(server.Object, "status"); !found {
		t.Fatal("a Ready Certificate must be left alone")
	}
}

func TestHealBarmanCertificates_LeavesAYoungNotReadyCertificate(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{certificateGVR: "CertificateList"},
		barmanCert("barman-cloud-client", false, 5*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	healBarmanCertificates(ctx, dyn, barmanCertNamespace, time.Minute, 50*time.Millisecond)
	got, _ := dyn.Resource(certificateGVR).Namespace(barmanCertNamespace).Get(context.Background(), "barman-cloud-client", metav1.GetOptions{})
	if got.GetUID() != "old-uid" {
		t.Fatal("a Certificate still inside its grace period must not be touched")
	}
}

// The healer must watch the namespace the plugin is installed into. Negative control: returning ""
// unconditionally (the earlier hard-coded namespace) fails every case but the default.
func TestCnpgNamespaceOfReadsWhatTheOperatorDeclared(t *testing.T) {
	write := func(t *testing.T, dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("default", func(t *testing.T) {
		if got := cnpgNamespaceOf(t.TempDir()); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("environment", func(t *testing.T) {
		t.Setenv("TF_VAR_cnpg_namespace", "databases")
		if got := cnpgNamespaceOf(t.TempDir()); got != "databases" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("tfvars, overridden by an auto file", func(t *testing.T) {
		d := t.TempDir()
		write(t, d, "terraform.tfvars", "cnpg_namespace = \"first\"\n")
		write(t, d, "z.auto.tfvars", "cnpg_namespace = \"second\"\n")
		if got := cnpgNamespaceOf(d); got != "second" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("a file beats the environment, and a commented assignment declares nothing", func(t *testing.T) {
		t.Setenv("TF_VAR_cnpg_namespace", "fromenv")
		d := t.TempDir()
		write(t, d, "terraform.tfvars", "/*\ncnpg_namespace = \"old\"\n*/\n# cnpg_namespace = \"older\"\ncnpg_namespace = \"new\"\n")
		if got := cnpgNamespaceOf(d); got != "new" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("json", func(t *testing.T) {
		d := t.TempDir()
		write(t, d, "terraform.tfvars.json", `{"cnpg_namespace":"jsonns"}`)
		if got := cnpgNamespaceOf(d); got != "jsonns" {
			t.Fatalf("got %q", got)
		}
	})
}
