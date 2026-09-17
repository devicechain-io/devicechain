// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// localMQTTNodePort is the node port the local kind configuration maps host 1883 to
// (deploy/local/kind-cluster.yaml). One cluster has one of it.
const localMQTTNodePort = 31883

// clusterSingletons are the things on a cluster only one instance can hold: the local MQTT
// node port, and an ingress host. Everything else an instance runs is in its own
// namespace; these are cluster-wide by Kubernetes' own rules.
type clusterSingletons struct {
	// MQTTNodePortHolder is the namespace of a Service already holding localMQTTNodePort,
	// other than this instance's own. Empty when the port is free for this instance.
	MQTTNodePortHolder string
	// HostHolder is the namespace of an Ingress already serving this instance's host,
	// other than this instance's own. Empty when no other instance serves it.
	HostHolder string
}

// readClusterSingletons is the render step's read of what other instances already hold.
// Indirected, like the step's other cluster reads, so the decision can be exercised
// without one.
var readClusterSingletons = func(ctx context.Context, kubeContext, instance, host string) (clusterSingletons, error) {
	_, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return clusterSingletons{}, fmt.Errorf("connecting to the cluster to see what other instances hold: %w", err)
	}
	svcs, err := typed.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return clusterSingletons{}, fmt.Errorf("listing Services to see whether the MQTT node port is taken: %w", err)
	}
	ings, err := typed.NetworkingV1().Ingresses(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return clusterSingletons{}, fmt.Errorf("listing Ingresses to see whether host %q is taken: %w", host, err)
	}
	return singletonsFrom(svcs.Items, ings.Items, instance, host), nil
}

// singletonsFrom is readClusterSingletons' decision, with no cluster in it.
func singletonsFrom(svcs []corev1.Service, ings []networkingv1.Ingress, instance, host string) clusterSingletons {
	var out clusterSingletons
	own := instanceNamespace(instance)
	for _, s := range svcs {
		if s.Namespace == own {
			continue
		}
		for _, p := range s.Spec.Ports {
			if p.NodePort == localMQTTNodePort {
				out.MQTTNodePortHolder = s.Namespace
			}
		}
	}
	for _, ing := range ings {
		if ing.Namespace == own {
			continue
		}
		for _, r := range ing.Spec.Rules {
			if r.Host == host {
				out.HostHolder = ing.Namespace
			}
		}
	}
	return out
}

// refuseAHostAnotherInstanceServes stops a bootstrap whose ingress host is already
// another instance's.
//
// 🔴 THE INGRESS CONTROLLER DOES NOT REFUSE THIS; IT PICKS ONE. Two Ingress objects
// claiming the same host on one class are both accepted, and the controller serves one
// of them — so the second instance's console and API routes quietly never answer, or
// quietly replace the first's. Neither instance reports anything wrong.
func refuseAHostAnotherInstanceServes(held clusterSingletons, instance, host string) error {
	if held.HostHolder == "" {
		return nil
	}
	return fmt.Errorf("host %q is already served by the instance in namespace %q, and an ingress "+
		"controller given two instances on one host serves only one of them — silently. Bootstrap %q "+
		"on a host of its own with --host (for a local cluster, e.g. --host %s.localhost)",
		host, held.HostHolder, instance, instance)
}
