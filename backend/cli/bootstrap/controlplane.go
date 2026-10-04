// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fatih/color"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// controlPlaneComponent is one control-plane scrape kube-prometheus-stack ships, with the selector
// ITS OWN Service uses to find the pods behind it.
//
// 🔴 THE CHART'S SELECTOR, NOT "THE" LABEL OF THE COMPONENT: what decides whether the scrape can
// work is whether the chart's Service selects a pod, because that Service is the only way the scrape
// reaches one. A component that runs under another label is unscrapeable by this chart anyway.
// Read from chart 65.1.1 templates/exporters/*/service.yaml (the defaults when service.selector
// and endpoints are unset, which DeviceChain leaves them).
type controlPlaneComponent struct {
	key      string // the chart's values key, and the monitoring module's vocabulary
	name     string // what the operator is told
	selector string
}

var controlPlaneComponents = []controlPlaneComponent{
	{key: "kubeControllerManager", name: "kube-controller-manager", selector: "component=kube-controller-manager"},
	{key: "kubeScheduler", name: "kube-scheduler", selector: "component=kube-scheduler"},
	{key: "kubeEtcd", name: "etcd", selector: "component=etcd"},
	{key: "kubeProxy", name: "kube-proxy", selector: "k8s-app=kube-proxy"},
}

// controlPlaneNamespace is where the chart renders those Services, and so where it looks.
const controlPlaneNamespace = "kube-system"

// monitoringSlim reports whether the monitoring stack runs in its slim profile. ONE expression
// for it: infraVars sends it to the cluster root, and the control-plane detection stands down
// under it because the module then switches all four scrapes off whatever it is told.
func monitoringSlim(st *State) bool {
	return looksLocal(st.KubeContext)
}

// unscrapedControlPlane returns the chart keys of the components whose selector finds no pod in
// kube-system, in controlPlaneComponents order.
//
// 🔴 AN ERROR KEEPS THE SCRAPE. A component whose list failed is NOT returned (so it stays
// scraped) and is named in the returned error. Noisy, a *Down alert that may be false, is
// recoverable; silent, no scrape of a component the cluster does run, is not.
//
// Known residual: a component's pod can be briefly
// absent on a self-managed cluster (kubelet mirror pods are recreated when their static manifest
// changes). A list in that window records it as unscraped until the next install. With the chart's
// default selectors a scrape that finds no pod reached nothing, so the loss is of a target that
// was not there to be seen.
func unscrapedControlPlane(ctx context.Context, typed kubernetes.Interface) ([]string, error) {
	var off []string
	var errs []error
	for _, c := range controlPlaneComponents {
		pods, err := typed.CoreV1().Pods(controlPlaneNamespace).List(ctx,
			metav1.ListOptions{LabelSelector: c.selector, Limit: 1})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.name, err))
			continue
		}
		// Pending pods count as present: the alert is right to fire for a component that
		// exists and is not ready.
		if len(pods.Items) == 0 {
			off = append(off, c.key)
		}
	}
	return off, errors.Join(errs...)
}

// controlPlaneNames maps chart keys to what the operator is told.
func controlPlaneNames(keys []string) []string {
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		name := k
		for _, c := range controlPlaneComponents {
			if c.key == k {
				name = c.name
			}
		}
		names = append(names, name)
	}
	return names
}

// settleControlPlaneScrapes records on st which control-plane scrapes the monitoring stack will
// switch off, and says so. It decides nothing (the list stays nil, which scrapes everything) when
// the stack is not installed, when the slim profile switches them all off anyway, or when there is
// no client to ask.
func settleControlPlaneScrapes(ctx context.Context, st *State, typed kubernetes.Interface) {
	// Reset first, so a reused State cannot carry a previous run's list.
	st.UnscrapedControlPlane = nil
	if st.NoMonitoring || monitoringSlim(st) || typed == nil {
		return
	}
	doing("checking which control-plane components run in the cluster")
	off, err := unscrapedControlPlane(ctx, typed)
	st.UnscrapedControlPlane = off
	if err != nil {
		fmt.Println(color.YellowString("incomplete."))
		fmt.Println(color.YellowString("warning: could not tell whether these run in kube-system, so they stay "+
			"scraped; if the cluster runs them elsewhere their *Down alerts will fire: %v", err))
	} else {
		done()
	}
	if len(off) > 0 {
		fmt.Printf("  not scraping %s: the monitoring chart's selector finds no pod for them in %s, "+
			"so their scrapes could never succeed\n",
			strings.Join(controlPlaneNames(off), ", "), controlPlaneNamespace)
	}
}

// installClusterVars is the variables install applies the cluster root with. The control-plane
// detection runs HERE, inside the only producer of those variables, rather than beside the call: a
// detection called as a separate line in Install is one line nothing notices missing, and the
// function's tests would still pass.
func installClusterVars(ctx context.Context, st *State, typed kubernetes.Interface) ([]string, error) {
	settleControlPlaneScrapes(ctx, st, typed)
	cluster, _, err := splitVars(infraVars(st))
	return cluster, err
}
