// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	// cnpgClusterReadyTimeout matches the Cluster release's own timeout
	// (modules/cnpg-cluster/main.tf, timeout = 900).
	cnpgClusterReadyTimeout = 15 * time.Minute

	// healthyClusterPhase is CloudNativePG's phase for a Cluster with nothing to do.
	healthyClusterPhase = "Cluster in healthy state"
)

// cnpgClusterReady is a CloudNativePG Cluster's health, with no cluster in it. The reason
// names the first condition that failed: while a replica is joining that is the phase, not
// the count, which is why the wait reads the counts itself.
//
// One reader for every Cluster dcctl waits on: the relational store (install), an
// instance's event store (bootstrap, upgrade).
func cnpgClusterReady(cluster *unstructured.Unstructured, podImages []string) (bool, string) {
	if cluster == nil {
		return false, "the Cluster was not read"
	}
	phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
	if phase != healthyClusterPhase {
		return false, fmt.Sprintf("its phase is %q, not %q", phase, healthyClusterPhase)
	}
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	ready, _, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")
	if instances == 0 || ready != instances {
		return false, fmt.Sprintf("%d of %d instances are ready", ready, instances)
	}
	current, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	target, _, _ := unstructured.NestedString(cluster.Object, "status", "targetPrimary")
	if current == "" || current != target {
		return false, fmt.Sprintf("its primary is moving (current %q, target %q)", current, target)
	}
	image, _, _ := unstructured.NestedString(cluster.Object, "spec", "imageName")
	if int64(len(podImages)) != instances {
		return false, fmt.Sprintf("%d instance pods are running for %d instances", len(podImages), instances)
	}
	for _, img := range podImages {
		if image != "" && img != image {
			return false, fmt.Sprintf("an instance still runs %s, not %s", img, image)
		}
	}
	conds, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok || m["type"] != "Ready" {
			continue
		}
		if m["status"] != "True" {
			return false, fmt.Sprintf("its Ready condition is %v", m["status"])
		}
		// A condition with no observedGeneration cannot say which spec it judged, so it
		// is not held against the Cluster; one that has it must have seen this spec.
		if og, ok := asInt64(m["observedGeneration"]); ok && og != 0 && og < cluster.GetGeneration() {
			return false, fmt.Sprintf("it has not yet judged its latest spec (generation %d, observed %d)",
				cluster.GetGeneration(), og)
		}
	}
	return true, ""
}

// cnpgInstancePodImages lists the postgres image of every live instance pod of the named
// Cluster. A pod on its way out, a finished one, and a CloudNativePG job pod (initdb, a
// join) are not instances, and counting them would make a healthy store read as having
// more pods than instances.
func cnpgInstancePodImages(ctx context.Context, typed kubernetes.Interface, ns, cluster string) ([]string, error) {
	pods, err := typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "cnpg.io/cluster=" + cluster})
	if err != nil {
		return nil, fmt.Errorf("listing the pods of Cluster %s/%s: %w", ns, cluster, err)
	}
	var images []string
	for _, p := range pods.Items {
		if p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded ||
			p.Status.Phase == corev1.PodFailed || p.Labels["cnpg.io/jobRole"] != "" {
			continue
		}
		for _, c := range p.Spec.Containers {
			if c.Name == "postgres" {
				images = append(images, c.Image)
			}
		}
	}
	return images, nil
}

// waitForCNPGClusterReady blocks until the named Cluster is healthy on its current spec:
// every instance joined and ready, the primary settled, no pod on a stale image.
//
// 🔴 HELM'S WAIT DOES NOT COVER THE DATABASE: the release holds only the Cluster
// resource, which Helm reports ready the moment it is accepted. And a Cluster read in
// the instant after its spec changed, before CloudNativePG has acted on it, still reads
// healthy — so readiness must HOLD for three polls spanning two intervals, not be seen
// once.
//
// A timeout names the Cluster and how many of its instances are ready. The counts come
// from the last read rather than from cnpgClusterReady's reason, because the reason is
// the FIRST failing condition and while a replica joins that is the phase.
func waitForCNPGClusterReady(ctx context.Context, dyn dynamic.Interface, typed kubernetes.Interface,
	ns, name string, timeout, interval time.Duration) error {
	var read bool
	var ready, instances int64
	why, err := pollUntil(ctx, timeout, interval, 3, func() (bool, string, error) {
		read = false
		cl, err := dyn.Resource(clusterGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, "", err
		}
		read = true
		instances, _, _ = unstructured.NestedInt64(cl.Object, "spec", "instances")
		ready, _, _ = unstructured.NestedInt64(cl.Object, "status", "readyInstances")
		images, err := cnpgInstancePodImages(ctx, typed, ns, name)
		if err != nil {
			return false, "", err
		}
		ok, why := cnpgClusterReady(cl, images)
		return ok, why, nil
	})
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		// A cancel is not a timeout; it goes back as pollUntil gave it.
		return err
	case !read:
		return fmt.Errorf("Cluster %s/%s could not be read within %s: %s", ns, name, timeout, why)
	case ready == instances && instances > 0:
		return fmt.Errorf("Cluster %s/%s: all %d instances are ready, but it is not healthy after %s (%s)",
			ns, name, instances, timeout, why)
	}
	return fmt.Errorf("Cluster %s/%s: %d of %d instances ready after %s (%s)", ns, name, ready, instances, timeout, why)
}
