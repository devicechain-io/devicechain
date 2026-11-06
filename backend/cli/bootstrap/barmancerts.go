// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/hashicorp/terraform-exec/tfexec"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// The Barman Cloud plugin certificate healer.
//
// THE FAILURE
//
// The plugin's chart renders one self-signed Issuer and two Certificates that name it
// (barman-cloud-server and barman-cloud-client), all in ONE Helm install. cert-manager can
// reconcile a Certificate before it has seen the Issuer; the failed first issuance is then
// held back for an hour. Measured on a fresh GKE cluster: the server certificate issued, the
// client certificate did not, the plugin pod sat in ContainerCreating on the missing secret,
// and Helm's wait ran out. Deleting the stuck CertificateRequest changes nothing -- the
// back-off lives in the Certificate's status -- but deleting and re-creating the Certificate
// does.
//
// WHY THE FIX IS HERE AND NOT IN THE TOFU MODULE
//
// The race is between two objects of a third-party chart we cannot split or reorder (a
// pre-created Issuer would collide with the one the chart owns), and the tofu tree is
// provider-only: it has no way to act while helm_release is blocked waiting for the very
// pod this stalls. dcctl is the only party that can look at the cluster concurrently with
// the apply, so it does, exactly as the CNPG admission gate lives here for the same reason.
//
// The healer is best-effort and bounded: a Certificate is re-created at most once, and
// nothing it does can fail the install -- if it cannot help, the apply fails the way it
// would have without it.

const (
	// barmanCertGrace is how long a plugin Certificate may stay not Ready before it is
	// treated as stuck. A self-signed issuance takes seconds; this is long enough never to
	// fire on a slow API server and short enough to heal well inside Helm's 600s wait.
	barmanCertGrace = 3 * time.Minute
	// barmanCertPoll is the gap between looks.
	barmanCertPoll = 10 * time.Second
	// barmanCertNamespace is the operator/plugin namespace when the operator does not set the
	// cluster root's cnpg_namespace; see cnpgNamespaceOf.
	barmanCertNamespace = "cnpg-system"
)

// barmanPluginCertificates are the two Certificates the plugin chart renders.
var barmanPluginCertificates = []string{"barman-cloud-server", "barman-cloud-client"}

// barmanHealingApplier is a tofuApplier whose every Apply runs under the healer.
type barmanHealingApplier struct {
	tf          tofuApplier
	kubeContext string
	// namespace is where the plugin's Certificates live: the cluster root's cnpg_namespace. Empty
	// means the default.
	namespace string
}

func (b barmanHealingApplier) Apply(ctx context.Context, opts ...tfexec.ApplyOption) error {
	return withBarmanCertHealer(ctx, b.kubeContext, b.namespace, func() error { return b.tf.Apply(ctx, opts...) })
}

// withBarmanCertHealer runs apply while a healer watches the plugin's Certificates, and stops
// it when apply returns. The healer's own problems are reported, never returned.
func withBarmanCertHealer(ctx context.Context, kubeContext, namespace string, apply func() error) error {
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		// Not being able to watch must not stop the apply from running.
		fmt.Println(color.YellowString("   cannot watch the Barman Cloud plugin certificates: %v", err))
		return apply()
	}
	if namespace == "" {
		namespace = barmanCertNamespace
	}
	hctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		healBarmanCertificates(hctx, dyn, namespace, barmanCertGrace, barmanCertPoll)
	}()
	defer func() { cancel(); wg.Wait() }()
	return apply()
}

// healBarmanCertificates polls until ctx ends or both Certificates are Ready, re-creating
// each stuck one once.
func healBarmanCertificates(ctx context.Context, dyn dynamic.Interface, ns string, grace, poll time.Duration) {
	recreated := map[string]bool{}
	ready := map[string]bool{}
	for {
		for _, name := range barmanPluginCertificates {
			if ready[name] {
				continue
			}
			obj, err := dyn.Resource(certificateGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				continue // not created yet, or a transient API error: look again
			}
			if certificateReady(obj) {
				ready[name] = true
				continue
			}
			if recreated[name] || time.Since(obj.GetCreationTimestamp().Time) < grace {
				continue
			}
			recreated[name] = true // once, whatever happens: a second failure is not this race
			if err := recreateCertificate(ctx, dyn, obj); err != nil {
				fmt.Println(color.YellowString("   could not re-create the stuck Certificate %s/%s: %v", ns, name, err))
				continue
			}
			fmt.Println(color.YellowString("⚠  Certificate %s/%s was not issued after %s (cert-manager is backing off "+
				"a first attempt that raced the plugin's Issuer); deleted and re-created it.", ns, name, grace))
		}
		if len(ready) == len(barmanPluginCertificates) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// certificateReady reports whether the Certificate carries Ready=True.
func certificateReady(obj *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if ok && m["type"] == "Ready" {
			return m["status"] == "True"
		}
	}
	return false
}

// recreateCertificate deletes the Certificate and creates it again from its own spec,
// keeping labels and annotations so Helm still owns it. Everything the API server assigns
// (uid, resourceVersion, status, ...) is dropped.
func recreateCertificate(ctx context.Context, dyn dynamic.Interface, old *unstructured.Unstructured) error {
	fresh := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": old.GetAPIVersion(),
		"kind":       old.GetKind(),
	}}
	fresh.SetName(old.GetName())
	fresh.SetNamespace(old.GetNamespace())
	fresh.SetLabels(old.GetLabels())
	fresh.SetAnnotations(old.GetAnnotations())
	spec, _, _ := unstructured.NestedMap(old.Object, "spec")
	fresh.Object["spec"] = spec

	res := dyn.Resource(certificateGVR).Namespace(old.GetNamespace())
	fg := metav1.DeletePropagationForeground
	if err := res.Delete(ctx, old.GetName(), metav1.DeleteOptions{PropagationPolicy: &fg}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting: %w", err)
	}
	// Wait for the old object to be gone, or the create would hit AlreadyExists.
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := res.Get(ctx, old.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("old Certificate still present after delete")
		}
		time.Sleep(500 * time.Millisecond)
	}
	if _, err := res.Create(ctx, fresh, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating: %w", err)
	}
	return nil
}

// cnpgNamespaceOf is the namespace the operator declared for the CloudNativePG operator and the
// Barman Cloud plugin: cnpg_namespace in a variables file the cluster root loads on its own, or
// in TF_VAR_cnpg_namespace, or "" for the root's default. The healer must watch the namespace the
// plugin is installed into, not a hard-coded one, or it heals nothing on an install that moved it.
//
// OpenTofu's precedence is followed: the environment is the weakest source, then terraform.tfvars
// (and its .json form), then the *.auto.tfvars files in lexical order, each overriding the last.
// A commented-out assignment declares nothing.
func cnpgNamespaceOf(rootdir string) string {
	found := strings.TrimSpace(os.Getenv("TF_VAR_cnpg_namespace"))
	entries, err := os.ReadDir(rootdir)
	if err != nil {
		return found
	}
	blockComment := regexp.MustCompile(`(?s)/\*.*?\*/`)
	assign := regexp.MustCompile(`(?m)^[ \t]*cnpg_namespace[ \t]*=[ \t]*"([^"]+)"`)
	read := func(n string) {
		b, err := os.ReadFile(filepath.Join(rootdir, n))
		if err != nil {
			return
		}
		if strings.HasSuffix(n, ".json") {
			var m map[string]json.RawMessage
			var v string
			if json.Unmarshal(b, &m) == nil && json.Unmarshal(m["cnpg_namespace"], &v) == nil && v != "" {
				found = v
			}
			return
		}
		if all := assign.FindAllSubmatch(blockComment.ReplaceAll(b, nil), -1); len(all) > 0 {
			found = string(all[len(all)-1][1])
		}
	}
	for _, n := range []string{"terraform.tfvars", "terraform.tfvars.json"} {
		for _, e := range entries {
			if !e.IsDir() && e.Name() == n {
				read(n)
			}
		}
	}
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && (strings.HasSuffix(n, ".auto.tfvars") || strings.HasSuffix(n, ".auto.tfvars.json")) {
			read(n)
		}
	}
	return found
}
