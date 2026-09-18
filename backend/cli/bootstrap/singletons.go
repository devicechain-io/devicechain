// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/fatih/color"

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
	own := InstanceNamespace(instance)
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
	return &ErrHostTaken{Instance: instance, Host: host, Holder: held.HostHolder}
}

// ErrConnectionBudget is the refusal of an instance the shared relational store has no
// connections left for, raised before anything is written. Typed for the same reason as
// ErrHostTaken.
type ErrConnectionBudget struct{ Err error }

func (e *ErrConnectionBudget) Error() string { return e.Err.Error() }
func (e *ErrConnectionBudget) Unwrap() error { return e.Err }

// precheckConnectionBudget asks the shared store whether this instance's connection
// limit would be admitted, without admitting it. Indirected so the step can be driven
// without a cluster.
var precheckConnectionBudget = func(ctx context.Context, st *State) error {
	if st.Install == nil {
		return nil
	}
	limit, err := instanceConnectionLimit(st)
	if err != nil {
		return fmt.Errorf("sizing this instance's connection limit: %w", err)
	}
	rdb := st.Install.Outputs.Rdb
	err = withProvisionerSession(ctx, st.KubeContext, rdb, func(q instanceDBQuerier) error {
		return admitInstance(ctx, q, st.Instance, connectionAdmission{Limit: limit, Budget: rdb.MaxConnections})
	})
	return asBudgetRefusal(err)
}

// asBudgetRefusal types an admission refusal so the command layer can tell it from
// every other failure; anything else passes through unchanged.
func asBudgetRefusal(err error) error {
	if errors.Is(err, errNoConnectionBudget) {
		return &ErrConnectionBudget{Err: err}
	}
	return err
}

// ErrHostTaken is the refusal of a host another instance serves. Typed, because the
// command layer undoes the local record this run wrote on exactly this refusal: it fires
// before anything is written, so that record describes an instance that was never built.
// See PriorLocalState.
type ErrHostTaken struct {
	Instance string
	Host     string
	Holder   string
}

func (e *ErrHostTaken) Error() string {
	return fmt.Sprintf("host %q is already served by the instance in namespace %q, and an ingress "+
		"controller given two instances on one host serves only one of them — silently. Bootstrap %q "+
		"on a host of its own with --host (for a local cluster, e.g. --host %s.localhost)",
		e.Host, e.Holder, e.Instance, e.Instance)
}

// stepCheckClusterSingletons asks what this cluster already holds that this instance
// would have to take from it, and refuses what is not free: a host another instance
// serves, the namespace this instance is named after, and room on the shared store.
//
// 🔴 BEFORE ANYTHING IS WRITTEN. It sits right after the rebuild refusal, ahead of the
// operator install and the instance declaration: a refusal after those would leave a
// declaration for an instance that was never built, and the cluster would report holding
// it. The node port is not refused — the instance is built without it — but it is decided
// here, from the same read, and said. TestTheSingletonStepRunsBeforeAnythingIsWritten is
// what holds this step in that position, and the command layer's record rollback is keyed
// on the refusals raised here because of it.
func stepCheckClusterSingletons(ctx context.Context, st *State) error {
	if st.Values == nil {
		st.Values = map[string]string{}
	}
	host := ingressHostFor(st)
	if st.DryRun {
		// A rehearsal is often aimed at a cluster that does not exist yet, so the read is
		// best-effort — and what a real run would refuse is still said.
		held, err := readClusterSingletons(ctx, st.KubeContext, st.Instance, host)
		switch {
		case err != nil:
			wouldDo(fmt.Sprintf("check whether another instance already serves host %q — the read "+
				"failed (%v), and a real run would stop here", host, err))
		case refuseAHostAnotherInstanceServes(held, st.Instance, host) != nil:
			wouldDo(fmt.Sprintf("REFUSE: host %q is already served by the instance in namespace %q", host, held.HostHolder))
		}
		// The namespace half of the rehearsal, rehearsed separately because it is a second
		// cluster read rather than a second reading of the one above: against a cluster
		// that is not there yet NEITHER half can answer, and each says so about its own
		// question instead of one failure standing in for both.
		var refusal *ErrNamespaceUnavailable
		switch err := precheckInstanceNamespace(ctx, st); {
		case errors.As(err, &refusal):
			wouldDo("REFUSE: " + refusal.Error())
		case err != nil:
			wouldDo(fmt.Sprintf("check whether namespace %q is this instance's to build in — the "+
				"read failed (%v), and a real run would stop here", InstanceNamespace(st.Instance), err))
		}
		return nil
	}

	doing("checking what other instances on this cluster hold")
	held, err := readClusterSingletons(ctx, st.KubeContext, st.Instance, host)
	if err != nil {
		return fail("checking what other instances on this cluster hold", err)
	}
	if err := refuseAHostAnotherInstanceServes(held, st.Instance, host); err != nil {
		return err
	}
	// 🔴 AND THE NAMESPACE THIS INSTANCE IS ABOUT TO BE BUILT IN, WHICH IS THE ONE WHERE
	// BEING LATE COSTS A ROOT KEY. ensureNamespaceForRelease reaches the same verdict, but
	// it reaches it inside the infrastructure apply — on a real cluster a bootstrap named
	// after the monitoring namespace got that far having already written the secret-store
	// root key, a TLS private key and four database credentials into it.
	if err := precheckInstanceNamespace(ctx, st); err != nil {
		return err
	}
	// 🔴 AND THE CONNECTION BUDGET, FOR THE SAME REASON: on a shared store it is the refusal
	// an operator is likeliest to meet, and meeting it at the apply would leave a declared,
	// namespaced, credentialed instance behind. The apply admits again, under a lock —
	// this is the early answer, not the enforcement.
	if err := precheckConnectionBudget(ctx, st); err != nil {
		return err
	}
	done()
	if held.MQTTNodePortHolder != "" {
		st.Values[mqttNodePortHolderKey] = held.MQTTNodePortHolder
		fmt.Printf("  %s\n", color.WhiteString(fmt.Sprintf("MQTT node port %d is held by the instance in "+
			"namespace %q, so this instance's broker is reachable in-cluster only", localMQTTNodePort, held.MQTTNodePortHolder)))
	}
	return nil
}
