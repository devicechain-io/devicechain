// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// 🔴 THESE RULES ARE ENFORCED BY THE API SERVER, SO THEY ARE TESTED AGAINST ONE.
//
// An earlier version of this asserted only that the rules were PRESENT in the
// generated CRD, on the grounds that CI does not download envtest binaries. That
// was a mistake of exactly the kind this project keeps finding: a structural
// check cannot see whether a rule FIRES, and the rule it could not see was
// wrong. `self == oldSelf` on an optional field is skipped whenever the field is
// absent on either side, so `cluster` — the value `dcctl destroy` reads to decide
// which cluster to act on — could be repointed in two edits: remove it, then set
// it to anything. The message said that could not happen. The test said nothing.
//
// The binaries cost about seven seconds and CI now installs them; see the
// `envtest` job. If they are genuinely absent the suite FAILS rather than skips,
// because a silent skip in CI is the same defect wearing a different hat.
func withAPIServer(t *testing.T) client.Client {
	t.Helper()

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the test API server: %v.\n\n"+
			"This suite needs envtest binaries. Install them with:\n"+
			"  cd backend/k8s && make envtest && "+
			"KUBEBUILDER_ASSETS=$(bin/setup-envtest use 1.33.0 -p path) go test ./controllers/...\n"+
			"KUBEBUILDER_ASSETS is currently %q. It is deliberately NOT skipped when absent: "+
			"these rules are the only thing standing between a hand-edited CR and a destroy "+
			"pointed at the wrong cluster, and a green run that checked nothing is worse than a "+
			"red one.", err, os.Getenv("KUBEBUILDER_ASSETS"))
	}
	t.Cleanup(func() { _ = env.Stop() })

	if err := dcv1beta1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newInstance(name string, mutate func(*dcv1beta1.Instance)) *dcv1beta1.Instance {
	inst := &dcv1beta1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: dcv1beta1.InstanceSpec{
			Provider: "local",
			Cluster:  "kind-devicechain",
			Managed:  true,
			Profile:  "default",
		},
	}
	if mutate != nil {
		mutate(inst)
	}
	return inst
}

// The cluster binding is what `dcctl destroy` reads to decide which cluster to
// act on, and for `managed`, whether it may delete that cluster at all.
func TestTheAPIServerRefusesToRepointTheClusterBinding(t *testing.T) {
	c := withAPIServer(t)
	ctx := context.Background()

	update := func(t *testing.T, name string, mutate func(*dcv1beta1.Instance)) error {
		t.Helper()
		live := &dcv1beta1.Instance{}
		if err := c.Get(ctx, client.ObjectKey{Name: name}, live); err != nil {
			t.Fatal(err)
		}
		mutate(live)
		return c.Update(ctx, live)
	}

	t.Run("a named cluster cannot be renamed", func(t *testing.T) {
		inst := newInstance("bound", nil)
		if err := c.Create(ctx, inst); err != nil {
			t.Fatal(err)
		}
		if err := update(t, "bound", func(i *dcv1beta1.Instance) {
			i.Spec.Cluster = "someone-elses"
		}); err == nil {
			t.Fatal("the cluster was renamed in place")
		}
	})

	// 🔴 THE TWO-EDIT BYPASS, which a field-level rule did not catch. Removing an
	// optional field is not a transition the field's own rule ever sees, so
	// `remove, then set` walked straight through a rule whose message says it
	// cannot happen.
	t.Run("a named cluster cannot be removed and then re-set", func(t *testing.T) {
		inst := newInstance("twostep", nil)
		if err := c.Create(ctx, inst); err != nil {
			t.Fatal(err)
		}
		if err := update(t, "twostep", func(i *dcv1beta1.Instance) {
			i.Spec.Cluster = ""
		}); err == nil {
			t.Fatal("removing the cluster was accepted, which is half of a repoint")
		}
	})

	// ...and the same rule from the other side: an adopted instance records an
	// empty cluster honestly, and that emptiness must not become a free slot.
	t.Run("an adopted instance cannot be given a cluster later", func(t *testing.T) {
		inst := newInstance("adopted", func(i *dcv1beta1.Instance) { i.Spec.Cluster = "" })
		if err := c.Create(ctx, inst); err != nil {
			t.Fatal(err)
		}
		if err := update(t, "adopted", func(i *dcv1beta1.Instance) {
			i.Spec.Cluster = "somewhere-else"
		}); err == nil {
			t.Fatal("an adopted instance was bound to a named cluster after the fact")
		}
	})

	t.Run("provider is immutable", func(t *testing.T) {
		if err := c.Create(ctx, newInstance("prov", nil)); err != nil {
			t.Fatal(err)
		}
		if err := update(t, "prov", func(i *dcv1beta1.Instance) { i.Spec.Provider = "gcp" }); err == nil {
			t.Fatal("the provider was changed in place")
		}
	})

	t.Run("managed is immutable", func(t *testing.T) {
		if err := c.Create(ctx, newInstance("mgd", nil)); err != nil {
			t.Fatal(err)
		}
		if err := update(t, "mgd", func(i *dcv1beta1.Instance) { i.Spec.Managed = false }); err == nil {
			t.Fatal("managed was flipped, which changes whether destroy may delete the cluster")
		}
	})

	// 🔴 THE COUNTERWEIGHT, AND IT IS NOT A FORMALITY. Every rule above refuses a
	// change. A rule set that refused ORDINARY changes too would break every
	// upgrade while looking careful, and would pass all five tests above.
	t.Run("an ordinary re-run may change everything else", func(t *testing.T) {
		if err := c.Create(ctx, newInstance("rerun", nil)); err != nil {
			t.Fatal(err)
		}
		if err := update(t, "rerun", func(i *dcv1beta1.Instance) {
			i.Spec.ImageVersion = "v0.18.0"
			i.Spec.Profile = "full"
			i.Spec.HA = true
			i.Spec.Host = "dc.example.com"
			i.Spec.Monitoring = true
			i.Spec.CNPG = true
			i.Spec.GrafanaSSO = true
			i.Spec.ExtraFunctionalAreas = []string{"lwm2m-ingest"}
		}); err != nil {
			t.Fatalf("an ordinary re-run was refused: %v", err)
		}
	})
}

// A restore is a fact about the instance, and the ordinary re-run that follows it
// restores nothing — so the naive read-modify-write erases it.
func TestTheAPIServerRefusesToUnsetRestored(t *testing.T) {
	c := withAPIServer(t)
	ctx := context.Background()

	now := metav1.Now()
	inst := newInstance("restored", func(i *dcv1beta1.Instance) {
		i.Spec.Restored = true
		i.Spec.RestoredAt = &now
	})
	if err := c.Create(ctx, inst); err != nil {
		t.Fatal(err)
	}

	live := &dcv1beta1.Instance{}
	if err := c.Get(ctx, client.ObjectKey{Name: "restored"}, live); err != nil {
		t.Fatal(err)
	}
	live.Spec.Restored = false
	live.Spec.RestoredAt = nil
	if err := c.Update(ctx, live); err == nil {
		t.Fatal("a re-run erased the fact that this instance was restored from an archive")
	}

	// The counterweight: an instance that was never restored must stay writable.
	if err := c.Create(ctx, newInstance("fresh", nil)); err != nil {
		t.Fatalf("an ordinary instance was refused: %v", err)
	}
}

// A restore timestamp on an instance that says it was not restored is two halves
// of one fact disagreeing. dcctl refuses it; so must the API server, or the two
// answers differ depending on who wrote the object.
func TestTheAPIServerRefusesARestoreTimestampWithoutTheFact(t *testing.T) {
	c := withAPIServer(t)
	now := metav1.Now()
	err := c.Create(context.Background(), newInstance("mismatch", func(i *dcv1beta1.Instance) {
		i.Spec.RestoredAt = &now
	}))
	if err == nil {
		t.Fatal("an instance carrying a restore timestamp and restored=false was accepted")
	}
}

// An Instance with no spec at all declares nothing. The required fields INSIDE
// the spec are only checked once a spec exists, so without `spec` being required
// at the root the object that declares nothing passes every rule in the schema.
func TestTheAPIServerRefusesAnInstanceThatDeclaresNothing(t *testing.T) {
	c := withAPIServer(t)
	err := c.Create(context.Background(), &dcv1beta1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "empty"},
	})
	if err == nil {
		t.Fatal("an Instance with an empty spec was accepted")
	}
	if !apierrors.IsInvalid(err) {
		t.Errorf("the refusal was not a validation error: %v", err)
	}
}
