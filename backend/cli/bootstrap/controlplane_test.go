// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	assets "github.com/devicechain-io/dc-deploy"
)

// Tests for which control-plane scrapes the monitoring stack switches off.
//
// 🔴 THE DANGEROUS DIRECTION IS SILENCE: a scrape turned off on a cluster that runs the
// component. So every error, every missing input and every path that never ran the detection
// must land on "scrape", and the assertions here are on the VALUES of the lists, not on the
// absence of an error.

const managedKubeContext = "gke_devicechain_us-central1_prod"

var allControlPlaneKeys = []string{"kubeControllerManager", "kubeScheduler", "kubeEtcd", "kubeProxy"}

func cpPod(ns, name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels}}
}

func TestAManagedControlPlaneIsNotScraped(t *testing.T) {
	typed := fake.NewSimpleClientset(cpPod("kube-system", "coredns", map[string]string{"k8s-app": "kube-dns"}))

	off, err := unscrapedControlPlane(t.Context(), typed)
	if err != nil {
		t.Fatalf("detection failed: %v", err)
	}
	if !reflect.DeepEqual(off, allControlPlaneKeys) {
		t.Fatalf("unscraped = %v, want %v", off, allControlPlaneKeys)
	}
}

func TestASelfManagedControlPlaneIsScraped(t *testing.T) {
	typed := fake.NewSimpleClientset(
		cpPod("kube-system", "cm", map[string]string{"component": "kube-controller-manager"}),
		cpPod("kube-system", "sched", map[string]string{"component": "kube-scheduler"}),
		cpPod("kube-system", "etcd", map[string]string{"component": "etcd"}),
		cpPod("kube-system", "proxy", map[string]string{"k8s-app": "kube-proxy"}),
	)

	off, err := unscrapedControlPlane(t.Context(), typed)
	if err != nil {
		t.Fatalf("detection failed: %v", err)
	}
	if len(off) != 0 {
		t.Fatalf("a cluster that runs all four had %v switched off", off)
	}
}

// The chart's selector decides, not the component's existence: a kube-proxy carrying another
// label is one the chart's Service cannot select, so its scrape could never succeed.
func TestAKubeProxyTheChartCannotSelectIsNotScraped(t *testing.T) {
	typed := fake.NewSimpleClientset(
		cpPod("kube-system", "kube-proxy-x", map[string]string{"component": "kube-proxy"}),
	)

	off, err := unscrapedControlPlane(t.Context(), typed)
	if err != nil {
		t.Fatalf("detection failed: %v", err)
	}
	if !reflect.DeepEqual(off, allControlPlaneKeys) {
		t.Fatalf("unscraped = %v, want %v", off, allControlPlaneKeys)
	}
}

func TestOnlyKubeSystemCounts(t *testing.T) {
	typed := fake.NewSimpleClientset(
		cpPod("default", "cm", map[string]string{"component": "kube-controller-manager"}),
		cpPod("default", "sched", map[string]string{"component": "kube-scheduler"}),
		cpPod("default", "etcd", map[string]string{"component": "etcd"}),
		cpPod("default", "proxy", map[string]string{"k8s-app": "kube-proxy"}),
	)

	off, err := unscrapedControlPlane(t.Context(), typed)
	if err != nil {
		t.Fatalf("detection failed: %v", err)
	}
	if !reflect.DeepEqual(off, allControlPlaneKeys) {
		t.Fatalf("pods outside kube-system counted: unscraped = %v, want %v", off, allControlPlaneKeys)
	}
}

func listRestriction(a k8stesting.Action) string {
	return a.(k8stesting.ListAction).GetListRestrictions().Labels.String()
}

func TestADetectionErrorKeepsTheScrape(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if listRestriction(a) == "component=kube-scheduler" {
			return true, nil, errors.New("connection reset")
		}
		return false, nil, nil
	})

	off, err := unscrapedControlPlane(t.Context(), typed)
	if want := []string{"kubeControllerManager", "kubeEtcd", "kubeProxy"}; !reflect.DeepEqual(off, want) {
		t.Fatalf("unscraped = %v, want %v (the scheduler's failed lookup must leave it scraped)", off, want)
	}
	if err == nil || !strings.Contains(err.Error(), "kube-scheduler") {
		t.Fatalf("the failure was not reported by component: %v", err)
	}
}

func TestEveryListFailingScrapesEverything(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("pods is forbidden")
	})

	off, err := unscrapedControlPlane(t.Context(), typed)
	if len(off) != 0 {
		t.Fatalf("an unreadable kube-system switched off %v", off)
	}
	if err == nil {
		t.Fatal("an unreadable kube-system reported no error")
	}
	for _, c := range controlPlaneComponents {
		if !strings.Contains(err.Error(), c.name) {
			t.Errorf("the error does not name %s: %v", c.name, err)
		}
	}
}

func unscrapedVar(t *testing.T, vars []string) string {
	t.Helper()
	var found []string
	for _, v := range vars {
		if strings.HasPrefix(v, "monitoring_unscraped_control_plane=") {
			found = append(found, strings.TrimPrefix(v, "monitoring_unscraped_control_plane="))
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one monitoring_unscraped_control_plane entry, got %v", found)
	}
	return found[0]
}

func TestInfraVarsCarryTheUnscrapedControlPlane(t *testing.T) {
	st := &State{KubeContext: managedKubeContext, UnscrapedControlPlane: []string{"kubeScheduler", "kubeEtcd"}}
	if got := unscrapedVar(t, infraVars(st)); got != `["kubeScheduler","kubeEtcd"]` {
		t.Fatalf("value = %s", got)
	}

	// Never `null`: the module's variable is non-nullable.
	st.UnscrapedControlPlane = nil
	if got := unscrapedVar(t, infraVars(st)); got != `[]` {
		t.Fatalf("an empty list was sent as %s", got)
	}
}

func countPodLists(typed *fake.Clientset) *atomic.Int32 {
	var n atomic.Int32
	typed.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		n.Add(1)
		return false, nil, nil
	})
	return &n
}

// The detection runs inside the producer of the variables the cluster root is applied with, it
// overwrites whatever the State carried, and its answer reaches the CLUSTER half.
func TestInstallClusterVarsRunTheDetection(t *testing.T) {
	typed := fake.NewSimpleClientset()
	st := &State{KubeContext: managedKubeContext, UnscrapedControlPlane: []string{"kubeProxy"}}

	vars, err := installClusterVars(t.Context(), st, typed)
	if err != nil {
		t.Fatal(err)
	}
	if got := unscrapedVar(t, vars); got != `["kubeControllerManager","kubeScheduler","kubeEtcd","kubeProxy"]` {
		t.Fatalf("value = %s", got)
	}
}

// Nothing is asked, and a stale answer is cleared, when there is nothing to decide.
func TestNoControlPlaneDetectionWhereThereIsNothingToDecide(t *testing.T) {
	cases := map[string]*State{
		"monitoring is not installed":                   {KubeContext: managedKubeContext, NoMonitoring: true},
		"the slim profile switches all four off itself": {KubeContext: "kind-test"},
	}
	for name, st := range cases {
		t.Run(name, func(t *testing.T) {
			typed := fake.NewSimpleClientset()
			lists := countPodLists(typed)
			st.UnscrapedControlPlane = []string{"kubeProxy"} // stale

			vars, err := installClusterVars(t.Context(), st, typed)
			if err != nil {
				t.Fatal(err)
			}
			if got := unscrapedVar(t, vars); got != `[]` {
				t.Fatalf("value = %s, want []", got)
			}
			if n := lists.Load(); n != 0 {
				t.Fatalf("%d pod lists were made", n)
			}
		})
	}
}

// A rehearsal of a first install has no cluster to ask. It must neither panic nor decide.
func TestNoClientScrapesEverything(t *testing.T) {
	st := &State{KubeContext: managedKubeContext, UnscrapedControlPlane: []string{"kubeProxy"}}

	vars, err := installClusterVars(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := unscrapedVar(t, vars); got != `[]` {
		t.Fatalf("value = %s, want []", got)
	}
}

func TestTheOperatorIsToldWhatIsNotScrapedAndWhatCouldNotBeRead(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if listRestriction(a) == "component=etcd" {
			return true, nil, errors.New("timeout")
		}
		return false, nil, nil
	})
	st := &State{KubeContext: managedKubeContext}

	out := captureOutput(t, func() { settleControlPlaneScrapes(t.Context(), st, typed) })

	for _, want := range []string{
		"not scraping kube-controller-manager, kube-scheduler, kube-proxy:",
		"could not tell whether these run in kube-system",
		"etcd: timeout",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The selectors above were read from one chart version's Service templates. A stale selector
// fails SILENT (it finds no pod, so a working scrape is switched off on a self-managed
// cluster), which is the direction this file exists to forbid. This pins the shipped default;
// an operator's own monitoring_chart_version is not overridden by dcctl and is not guarded.
// What it checks is the chart VERSION string, not the selectors: a bump fails here so someone
// re-reads the four exporter Service templates; the selectors themselves are not compared.
func TestControlPlaneSelectorsWereReadFromTheShippedChart(t *testing.T) {
	src, err := fs.ReadFile(assets.OpenTofuCluster(), "variables.tf")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)variable\s+"monitoring_chart_version"\s*\{(.*?)\n\}`).FindSubmatch(src)
	if block == nil {
		t.Fatal("monitoring_chart_version is not declared in the cluster root")
	}
	m := regexp.MustCompile(`(?m)^\s*default\s*=\s*"([^"]*)"`).FindSubmatch(block[1])
	if m == nil {
		t.Fatal("monitoring_chart_version has no default")
	}
	if got := string(m[1]); got != "65.1.1" {
		t.Fatalf("monitoring_chart_version defaults to %q: the control-plane selectors in controlplane.go "+
			"were read from chart 65.1.1's templates/exporters/*/service.yaml; re-read them for the new "+
			"version, then update this pin", got)
	}
}

// The cluster root has to hand the variable on to the monitoring module. Delete that one line
// and every other test still passes: dcctl sends the variable, the root accepts and drops it,
// the module falls back to [] and a managed cluster scrapes everything again. The wiring is HCL,
// so this reads the module block out of the shipped source.
func TestTheClusterRootPassesTheListToTheMonitoringModule(t *testing.T) {
	src, err := fs.ReadFile(assets.OpenTofuCluster(), "main.tf")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)\nmodule\s+"monitoring"\s*\{(.*?)\n\}`).FindSubmatch(src)
	if block == nil {
		t.Fatal(`module "monitoring" is not declared in the cluster root`)
	}
	if !regexp.MustCompile(`(?m)^\s*unscraped_control_plane\s*=\s*var\.monitoring_unscraped_control_plane\s*$`).Match(block[1]) {
		t.Fatalf("module \"monitoring\" does not set unscraped_control_plane = var.monitoring_unscraped_control_plane:\n%s", block[1])
	}
}

// A failed lookup for one component leaves THAT component scraped and still decides the rest:
// the answer must reach the State, and so the variables, not only the operator's terminal.
func TestAFailedLookupKeepsTheOtherAnswers(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if listRestriction(a) == "component=etcd" {
			return true, nil, errors.New("timeout")
		}
		return false, nil, nil
	})
	st := &State{KubeContext: managedKubeContext}

	var vars []string
	var err error
	captureOutput(t, func() { vars, err = installClusterVars(t.Context(), st, typed) })
	if err != nil {
		t.Fatal(err)
	}
	if got := unscrapedVar(t, vars); got != `["kubeControllerManager","kubeScheduler","kubeProxy"]` {
		t.Fatalf("value = %s, want etcd left scraped and the other three off", got)
	}
}
