// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-k8s/controllers"
)

// These drive the REAL reconciler, wired the way main.go wires it (including the
// label-restricted Deployment cache), against a real API server. There is no
// Deployment controller in envtest, so the tests play its part by writing the
// Deployment's status themselves — which is exactly what lets them make a rollout
// stop half way, something a real cluster would not hold still for.

type statusRig struct {
	c client.Client
}

func startStatusRig(t *testing.T) *statusRig {
	t.Helper()
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the test API server: %v (needs KUBEBUILDER_ASSETS; see the Makefile's envtest target)", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	if err := dcv1beta1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		Cache:   controllers.CacheOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&controllers.InstanceReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	return &statusRig{c: c}
}

func (r *statusRig) instance(t *testing.T, name, version string) *dcv1beta1.Instance {
	t.Helper()
	inst := newInstance(name, func(i *dcv1beta1.Instance) { i.Spec.ImageVersion = version })
	if err := r.c.Create(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	if err := r.c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "dci-" + name,
		Labels: map[string]string{dcv1beta1.InstanceNamespaceLabel: name},
	}}); err != nil {
		t.Fatal(err)
	}
	return inst
}

func (r *statusRig) deployment(t *testing.T, ns, instance, area, image string, replicas int32) *appsv1.Deployment {
	t.Helper()
	labels := map[string]string{"devicechain.io/instance": instance, "devicechain.io/functional-area": area}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: area, Namespace: ns, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: area, Image: image}}},
			},
		},
	}
	if err := r.c.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d
}

// settle writes the status a Deployment controller would report once it converged:
// every wanted replica updated and available at the current generation.
func (r *statusRig) settle(t *testing.T, d *appsv1.Deployment, available int32) {
	t.Helper()
	r.report(t, d, func(s *appsv1.DeploymentStatus, want int32) {
		s.Replicas, s.UpdatedReplicas, s.ReadyReplicas, s.AvailableReplicas = want, want, available, available
	})
}

func (r *statusRig) report(t *testing.T, d *appsv1.Deployment, mutate func(s *appsv1.DeploymentStatus, want int32)) {
	t.Helper()
	ctx := context.Background()
	if err := r.c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	d.Status.ObservedGeneration = d.Generation
	mutate(&d.Status, *d.Spec.Replicas)
	if err := r.c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
}

// await polls the Instance until ok accepts it, failing with the last status seen.
func (r *statusRig) await(t *testing.T, name, what string, ok func(*dcv1beta1.Instance) bool) *dcv1beta1.Instance {
	t.Helper()
	got := &dcv1beta1.Instance{}
	err := wait.PollUntilContextTimeout(context.Background(), 50*time.Millisecond, 15*time.Second, true,
		func(ctx context.Context) (bool, error) {
			if err := r.c.Get(ctx, client.ObjectKey{Name: name}, got); err != nil {
				return false, err
			}
			return ok(got), nil
		})
	if err != nil {
		t.Fatalf("waiting for %s: %v\nlast status: %+v", what, err, got.Status)
	}
	return got
}

func cond(i *dcv1beta1.Instance, typ string) *metav1.Condition {
	return meta.FindStatusCondition(i.Status.Conditions, typ)
}

func is(i *dcv1beta1.Instance, typ string, status metav1.ConditionStatus, reason string) bool {
	c := cond(i, typ)
	return c != nil && c.Status == status && c.Reason == reason
}

func TestOperatorWritesObservedStatus(t *testing.T) {
	r := startStatusRig(t)
	const (
		F = metav1.ConditionFalse
		T = metav1.ConditionTrue
	)

	t.Run("an instance with no workloads is reported with a reason, never as healthy", func(t *testing.T) {
		inst := r.instance(t, "none", "1.0.0")
		got := r.await(t, "none", "NoWorkloads", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionReady, F, dcv1beta1.ReasonNoWorkloads)
		})
		for _, typ := range []string{dcv1beta1.ConditionWorkloadsAvailable, dcv1beta1.ConditionRolloutComplete} {
			if !is(got, typ, F, dcv1beta1.ReasonNoWorkloads) {
				t.Errorf("%s = %+v, want False/NoWorkloads", typ, cond(got, typ))
			}
		}
		if got.Status.ObservedGeneration != inst.Generation {
			t.Errorf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, inst.Generation)
		}
		if got.Status.Version == nil || got.Status.Version.Requested != "1.0.0" || got.Status.Version.Running != "" {
			t.Errorf("version = %+v, want requested 1.0.0 and nothing running", got.Status.Version)
		}
	})

	t.Run("conditions follow the Deployments through unavailable, degraded, ready and back", func(t *testing.T) {
		r.instance(t, "follow", "1.0.0")
		a := r.deployment(t, "dci-follow", "follow", "device-management", "reg/device-management:1.0.0", 2)
		b := r.deployment(t, "dci-follow", "follow", "event-management", "reg/event-management:1.0.0", 1)

		r.await(t, "follow", "AreaUnavailable", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionWorkloadsAvailable, F, dcv1beta1.ReasonAreaUnavailable)
		})

		r.settle(t, b, 1)
		r.settle(t, a, 1) // one of two pods
		got := r.await(t, "follow", "AreaDegraded", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionWorkloadsAvailable, F, dcv1beta1.ReasonAreaDegraded)
		})
		if !is(got, dcv1beta1.ConditionReady, F, dcv1beta1.ReasonAreaDegraded) {
			t.Errorf("Ready = %+v, want it to carry the first failing condition's reason", cond(got, dcv1beta1.ConditionReady))
		}

		r.settle(t, a, 2)
		got = r.await(t, "follow", "Ready", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionReady, T, dcv1beta1.ReasonReady)
		})
		if !is(got, dcv1beta1.ConditionRolloutComplete, T, dcv1beta1.ReasonRolloutComplete) {
			t.Errorf("RolloutComplete = %+v", cond(got, dcv1beta1.ConditionRolloutComplete))
		}
		if got.Status.Version.Running != "1.0.0" {
			t.Errorf("running = %q, want 1.0.0", got.Status.Version.Running)
		}
		if len(got.Status.Areas) != 2 || got.Status.Areas[0].Name != "device-management" ||
			got.Status.Areas[0].DesiredReplicas != 2 || got.Status.Areas[0].ReadyReplicas != 2 ||
			got.Status.Areas[0].UpdatedReplicas != 2 || got.Status.Areas[0].Image != "reg/device-management:1.0.0" {
			t.Errorf("areas = %+v", got.Status.Areas)
		}

		// The same loop in reverse: an area losing every pod must flip Ready back,
		// which only a Deployment WATCH can do (nothing touched the Instance).
		r.settle(t, a, 0)
		r.await(t, "follow", "AreaUnavailable again", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionReady, F, dcv1beta1.ReasonAreaUnavailable)
		})
	})

	t.Run("a half-finished rollout is not complete even though every pod is available", func(t *testing.T) {
		r.instance(t, "rollout", "1.0.1")
		d := r.deployment(t, "dci-rollout", "rollout", "device-state", "reg/device-state:1.0.0", 2)
		r.settle(t, d, 2)
		// Requested 1.0.1 but running 1.0.0: converged, wrong version.
		r.await(t, "rollout", "VersionMismatch", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionRolloutComplete, F, dcv1beta1.ReasonVersionMismatch)
		})

		// The chart applies the new image: generation moves, and the Deployment
		// controller has updated only one of two pods. Old pods still count as
		// available, which is the trap a "available >= desired" test falls into.
		if err := r.c.Get(context.Background(), client.ObjectKeyFromObject(d), d); err != nil {
			t.Fatal(err)
		}
		d.Spec.Template.Spec.Containers[0].Image = "reg/device-state:1.0.1"
		if err := r.c.Update(context.Background(), d); err != nil {
			t.Fatal(err)
		}
		r.report(t, d, func(s *appsv1.DeploymentStatus, want int32) {
			s.Replicas, s.UpdatedReplicas, s.ReadyReplicas, s.AvailableReplicas = 3, 1, 2, 2
		})
		// 🔴 Wait for the status THIS report wrote (one pod updated), not merely for the
		// reason: between the template edit and the report the Deployment is also
		// "in progress" because its generation is unobserved, and a wait that accepts
		// that moment passes against an implementation that ignores the replica counts.
		got := r.await(t, "rollout", "RolloutInProgress with one pod updated", func(i *dcv1beta1.Instance) bool {
			return len(i.Status.Areas) == 1 && i.Status.Areas[0].UpdatedReplicas == 1 &&
				is(i, dcv1beta1.ConditionRolloutComplete, F, dcv1beta1.ReasonRolloutInProgress)
		})
		if !is(got, dcv1beta1.ConditionWorkloadsAvailable, T, dcv1beta1.ReasonAllAreasAvailable) {
			t.Errorf("WorkloadsAvailable = %+v, want it still True mid-rollout", cond(got, dcv1beta1.ConditionWorkloadsAvailable))
		}
		if !is(got, dcv1beta1.ConditionReady, F, dcv1beta1.ReasonRolloutInProgress) {
			t.Errorf("Ready = %+v, want False/RolloutInProgress", cond(got, dcv1beta1.ConditionReady))
		}

		r.settle(t, d, 2)
		r.await(t, "rollout", "Ready on the requested version", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionReady, T, dcv1beta1.ReasonReady) && i.Status.Version.Running == "1.0.1"
		})
	})

	t.Run("a stalled rollout is named", func(t *testing.T) {
		r.instance(t, "stalled", "1.0.0")
		d := r.deployment(t, "dci-stalled", "stalled", "device-state", "reg/device-state:1.0.0", 1)
		r.report(t, d, func(s *appsv1.DeploymentStatus, want int32) {
			s.Replicas, s.UpdatedReplicas, s.AvailableReplicas, s.ReadyReplicas = 2, 1, 1, 1
			s.Conditions = []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
				Reason: "ProgressDeadlineExceeded", LastUpdateTime: metav1.Now(), LastTransitionTime: metav1.Now(),
			}}
		})
		r.await(t, "stalled", "ProgressDeadlineExceeded", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionRolloutComplete, F, dcv1beta1.ReasonProgressDeadlineExceeded)
		})
	})

	t.Run("observedGeneration tracks the spec", func(t *testing.T) {
		r.instance(t, "gen", "1.0.0")
		d := r.deployment(t, "dci-gen", "gen", "device-state", "reg/device-state:1.0.0", 1)
		r.settle(t, d, 1)
		r.await(t, "gen", "Ready", func(i *dcv1beta1.Instance) bool { return is(i, dcv1beta1.ConditionReady, T, dcv1beta1.ReasonReady) })

		live := &dcv1beta1.Instance{}
		if err := r.c.Get(context.Background(), client.ObjectKey{Name: "gen"}, live); err != nil {
			t.Fatal(err)
		}
		before := live.Generation
		live.Spec.ImageVersion = "1.0.2"
		if err := r.c.Update(context.Background(), live); err != nil {
			t.Fatal(err)
		}
		if live.Generation == before {
			t.Fatalf("a spec edit did not move the generation (%d)", before)
		}
		got := r.await(t, "gen", "observedGeneration to catch up", func(i *dcv1beta1.Instance) bool {
			return i.Status.ObservedGeneration == live.Generation
		})
		if !is(got, dcv1beta1.ConditionRolloutComplete, F, dcv1beta1.ReasonVersionMismatch) {
			t.Errorf("RolloutComplete = %+v, want VersionMismatch once 1.0.2 is requested", cond(got, dcv1beta1.ConditionRolloutComplete))
		}
		for _, c := range got.Status.Conditions {
			if c.ObservedGeneration != live.Generation {
				t.Errorf("condition %s carries generation %d, want %d", c.Type, c.ObservedGeneration, live.Generation)
			}
		}
	})

	t.Run("dcctl's annotations never change a condition", func(t *testing.T) {
		r.instance(t, "annot", "1.0.0")
		d := r.deployment(t, "dci-annot", "annot", "device-state", "reg/device-state:1.0.0", 1)
		r.settle(t, d, 1)
		first := r.await(t, "annot", "Ready", func(i *dcv1beta1.Instance) bool { return is(i, dcv1beta1.ConditionReady, T, dcv1beta1.ReasonReady) })
		sawReady := *cond(first, dcv1beta1.ConditionReady)

		for _, phase := range []string{dcv1beta1.PhaseUpgrading, dcv1beta1.PhaseDestroying, dcv1beta1.PhaseFailed} {
			live := &dcv1beta1.Instance{}
			if err := r.c.Get(context.Background(), client.ObjectKey{Name: "annot"}, live); err != nil {
				t.Fatal(err)
			}
			if live.Annotations == nil {
				live.Annotations = map[string]string{}
			}
			live.Annotations[dcv1beta1.AnnotationPhase] = phase
			if err := r.c.Update(context.Background(), live); err != nil {
				t.Fatal(err)
			}
			// Let the reconciler see the write, then require the condition is untouched.
			time.Sleep(300 * time.Millisecond)
			if err := r.c.Get(context.Background(), client.ObjectKey{Name: "annot"}, live); err != nil {
				t.Fatal(err)
			}
			got := cond(live, dcv1beta1.ConditionReady)
			if got == nil || got.Status != T || !got.LastTransitionTime.Equal(&sawReady.LastTransitionTime) {
				t.Errorf("after phase %s, Ready = %+v, want it untouched (%+v)", phase, got, sawReady)
			}
		}
	})

	t.Run("the broker certificate's condition follows the clock with no event to announce it", func(t *testing.T) {
		inst := r.instance(t, "cert", "1.0.0")
		d := r.deployment(t, "dci-cert", "cert", "device-state", "reg/device-state:1.0.0", 1)
		r.settle(t, d, 1)

		// Nothing published: Unknown, and it must not make a serving instance unready.
		got := r.await(t, "cert", "Ready with the certificate Unknown", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionReady, T, dcv1beta1.ReasonReady)
		})
		if c := cond(got, dcv1beta1.ConditionBrokerCertificateValid); c == nil ||
			c.Status != metav1.ConditionUnknown || c.Reason != dcv1beta1.ReasonCertificateNotPublished {
			t.Fatalf("BrokerCertificateValid = %+v, want Unknown/CertificateNotPublished", c)
		}

		// Published as expiring in a few seconds: inside the warning window, so
		// ExpiringSoon, and Ready is untouched.
		live := &dcv1beta1.Instance{}
		if err := r.c.Get(context.Background(), client.ObjectKeyFromObject(inst), live); err != nil {
			t.Fatal(err)
		}
		live.Annotations = map[string]string{
			dcv1beta1.AnnotationBrokerCertificateNotAfter: time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339),
		}
		if err := r.c.Update(context.Background(), live); err != nil {
			t.Fatal(err)
		}
		got = r.await(t, "cert", "ExpiringSoon", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionBrokerCertificateValid, T, dcv1beta1.ReasonCertificateExpiringSoon)
		})
		if !is(got, dcv1beta1.ConditionReady, T, dcv1beta1.ReasonReady) {
			t.Errorf("Ready = %+v, want it untouched by an expiring certificate", cond(got, dcv1beta1.ConditionReady))
		}

		// 🔴 NOTHING WRITES TO THE CLUSTER FROM HERE. The condition has to flip to Expired,
		// and Ready with it, on the operator's own requeue.
		got = r.await(t, "cert", "Expired by the clock alone", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionBrokerCertificateValid, F, dcv1beta1.ReasonCertificateExpired)
		})
		if !is(got, dcv1beta1.ConditionReady, F, dcv1beta1.ReasonCertificateExpired) {
			t.Errorf("Ready = %+v, want False/CertificateExpired", cond(got, dcv1beta1.ConditionReady))
		}

		// A renewal publishes a later date and everything recovers.
		if err := r.c.Get(context.Background(), client.ObjectKeyFromObject(inst), live); err != nil {
			t.Fatal(err)
		}
		live.Annotations[dcv1beta1.AnnotationBrokerCertificateNotAfter] = time.Now().Add(365 * 24 * time.Hour).UTC().Format(time.RFC3339)
		if err := r.c.Update(context.Background(), live); err != nil {
			t.Fatal(err)
		}
		r.await(t, "cert", "valid again", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionBrokerCertificateValid, T, dcv1beta1.ReasonCertificateValid) &&
				is(i, dcv1beta1.ConditionReady, T, dcv1beta1.ReasonReady)
		})
	})

	t.Run("a Deployment carrying the label in another namespace counts for nothing", func(t *testing.T) {
		r.instance(t, "stray", "1.0.0")
		r.await(t, "stray", "NoWorkloads", func(i *dcv1beta1.Instance) bool {
			return is(i, dcv1beta1.ConditionReady, F, dcv1beta1.ReasonNoWorkloads)
		})
		if err := r.c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere"}}); err != nil {
			t.Fatal(err)
		}
		d := r.deployment(t, "elsewhere", "stray", "device-state", "reg/device-state:1.0.0", 1)
		r.settle(t, d, 1)
		time.Sleep(500 * time.Millisecond)
		got := r.await(t, "stray", "status", func(i *dcv1beta1.Instance) bool { return len(i.Status.Conditions) > 0 })
		if !is(got, dcv1beta1.ConditionReady, F, dcv1beta1.ReasonNoWorkloads) || len(got.Status.Areas) != 0 {
			t.Errorf("a stray Deployment moved the status: %+v", got.Status)
		}
	})
}
