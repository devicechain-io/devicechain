// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// envtestVersionHint is how to get the binaries, printed when they are missing. The
// version is backend/k8s/Makefile's ENVTEST_K8S_VERSION, which CI reads for both
// modules.
const envtestVersionHint = `cd backend/k8s && make envtest && ` +
	`export KUBEBUILDER_ASSETS="$(bin/setup-envtest use "$(sed -n 's/^ENVTEST_K8S_VERSION[[:space:]]*=[[:space:]]*//p' Makefile)" -p path)"`

// renderAPIServer starts an envtest kube-apiserver and etcd, with stub CRDs for the
// custom kinds the chart renders, and returns a client and a kubeconfig for Helm.
//
// It FAILS, never skips, when the binaries are missing: a skip would turn every run
// without them green while checking nothing, which is the gap this exists to close.
func renderAPIServer(t *testing.T) (client.Client, string) {
	t.Helper()
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("testdata", "apiserver-crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the test API server: %v (KUBEBUILDER_ASSETS=%q). Install it with: %s",
			err, os.Getenv("KUBEBUILDER_ASSETS"), envtestVersionHint)
	}
	t.Cleanup(func() { _ = env.Stop() })

	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatalf("building a client for the test API server: %v", err)
	}

	u, err := env.AddUser(envtest.User{Name: "helm", Groups: []string{"system:masters"}}, nil)
	if err != nil {
		t.Fatalf("adding a Helm user to the test API server: %v", err)
	}
	b, err := u.KubeConfig()
	if err != nil {
		t.Fatalf("building Helm's kubeconfig: %v", err)
	}
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, b, 0o600); err != nil {
		t.Fatal(err)
	}
	ensureTestNamespace(t, c, helmReleaseNamespace)
	return c, kubeconfig
}

// ensureTestNamespace creates the namespace, or finds it already there.
func ensureTestNamespace(t *testing.T, c client.Client, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := c.Create(context.Background(), ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("creating namespace %s: %v", name, err)
	}
}

// helmInto returns a Helm action configuration against kubeconfig, storing releases
// in Secrets in helmReleaseNamespace, as dcctl does. The kubeconfig is handed over
// explicitly, so nothing here reads KUBECONFIG or the developer's current context.
func helmInto(t *testing.T, kubeconfig string) *action.Configuration {
	t.Helper()
	flags := genericclioptions.NewConfigFlags(false)
	// A discovery cache per test: envtest's port changes every run.
	cache := t.TempDir()
	ns := helmReleaseNamespace
	flags.KubeConfig, flags.Namespace, flags.CacheDir = &kubeconfig, &ns, &cache
	cfg := new(action.Configuration)
	if err := cfg.Init(flags, helmReleaseNamespace, "secret", func(string, ...interface{}) {}); err != nil {
		t.Fatalf("initialising Helm: %v", err)
	}
	return cfg
}

// namespacesNamedBy returns every namespace a document in manifest places an object
// in that the manifest does not itself create.
func namespacesNamedBy(t *testing.T, manifest string) []string {
	t.Helper()
	created := map[string]bool{}
	var named []string
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding a rendered document: %v\n%s", err, doc)
		}
		if obj.Kind == "Namespace" {
			created[obj.Metadata.Name] = true
		}
		if ns := obj.Metadata.Namespace; ns != "" && !slices.Contains(named, ns) {
			named = append(named, ns)
		}
	}
	var out []string
	for _, ns := range named {
		if !created[ns] {
			out = append(out, ns)
		}
	}
	return out
}

// installProfile installs ch under helmReleaseNameFor(instance) with vals, as
// helmInstall does but with Wait off: envtest runs no controllers, so nothing would
// ever become ready. Every namespace a document names that the manifest does not
// itself create is created first, as the cluster install does for them.
func installProfile(t *testing.T, cfg *action.Configuration, c client.Client, ch *chart.Chart,
	instance string, vals map[string]interface{}) (*release.Release, error) {
	t.Helper()
	manifest, err := renderChartClientSide(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering for %s: %v", instance, err)
	}
	for _, ns := range namespacesNamedBy(t, manifest) {
		ensureTestNamespace(t, c, ns)
	}
	inst := action.NewInstall(cfg)
	inst.ReleaseName = helmReleaseNameFor(instance)
	inst.Namespace = helmReleaseNamespace
	inst.Wait = false
	return inst.RunWithContext(t.Context(), ch, vals)
}

// upgradeRelease upgrades through newHelmUpgrade, the action every dcctl re-run of the
// chart goes through, with Wait off (nothing becomes ready under envtest).
func upgradeRelease(t *testing.T, cfg *action.Configuration, name string, ch *chart.Chart,
	vals map[string]interface{}) (*release.Release, error) {
	t.Helper()
	upg := newHelmUpgrade(cfg, helmReleaseNamespace)
	upg.Wait = false
	return upg.RunWithContext(t.Context(), name, ch, vals)
}

// everyObjectIsThere reports what in the release manifest the server does not hold.
// Every document must be GET-able by its kind, namespace and name, so an install that
// was a dry run, or that skipped an object, cannot pass. An unmapped kind is fatal:
// a new custom kind needs its stub CRD in testdata/apiserver-crds, not a skip.
func everyObjectIsThere(t *testing.T, c client.Client, manifest string) []string {
	t.Helper()
	var missing []string
	docs := releaseutil.SplitManifests(manifest)
	if len(docs) == 0 {
		t.Fatal("the release manifest holds no documents: nothing was installed to check")
	}
	for _, doc := range docs {
		var u unstructured.Unstructured
		if err := yaml.Unmarshal([]byte(doc), &u.Object); err != nil {
			t.Fatalf("decoding a released document: %v\n%s", err, doc)
		}
		ns := u.GetNamespace()
		if ns == "" {
			mapping, err := c.RESTMapper().RESTMapping(u.GroupVersionKind().GroupKind(), u.GroupVersionKind().Version)
			if err != nil {
				t.Fatalf("the server cannot map %s: add its stub CRD to testdata/apiserver-crds: %v",
					u.GroupVersionKind(), err)
			}
			if mapping.Scope.Name() == "namespace" {
				ns = helmReleaseNamespace
			}
		}
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(u.GroupVersionKind())
		if err := c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: u.GetName()}, got); err != nil {
			if apierrors.IsNotFound(err) {
				missing = append(missing, fmt.Sprintf("%s %s/%s", u.GetKind(), ns, u.GetName()))
				continue
			}
			t.Fatalf("reading %s %s/%s back from the server: %v", u.GroupVersionKind(), ns, u.GetName(), err)
		}
	}
	return missing
}

// renderedDeployment returns the named Deployment from manifest, decoded.
func renderedDeployment(t *testing.T, manifest, name string) *appsv1.Deployment {
	t.Helper()
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var d appsv1.Deployment
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			t.Fatalf("decoding a rendered document: %v\n%s", err, doc)
		}
		if d.Kind == "Deployment" && d.Name == name {
			return &d
		}
	}
	t.Fatalf("the manifest renders no Deployment %q", name)
	return nil
}

// TestEveryRenderedProfileIsAcceptedByAnAPIServer installs the chart, as dcctl
// composes its values, for every profile in renderProfiles into a real API server,
// and fails on any refusal. A render cannot see what the server enforces about a pod
// spec: a repeated (topologyKey, whenUnsatisfiable) pair is valid YAML, and it made
// every --ha install fail when Helm created event-management.
func TestEveryRenderedProfileIsAcceptedByAnAPIServer(t *testing.T) {
	c, kubeconfig := renderAPIServer(t)
	cfg := helmInto(t, kubeconfig)
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading the embedded chart: %v", err)
	}

	// Negative control first: the server is a validator, not a sink. The --ha
	// event-management Deployment with a second hostname/ScheduleAnyway constraint
	// over its own pods (the shape this check exists for) is refused, by value.
	t.Run("negative control: a repeated spread pair is refused", func(t *testing.T) {
		manifest, err := renderChartClientSide(t.Context(), ch, profileValues(t, ch, renderProfiles[1], "ctl"))
		if err != nil {
			t.Fatalf("rendering: %v", err)
		}
		d := renderedDeployment(t, manifest, "event-management")
		cs := d.Spec.Template.Spec.TopologySpreadConstraints
		if len(cs) != 1 {
			t.Fatalf("the --ha event-management renders %d spread constraints; the control needs one to copy", len(cs))
		}
		dup := *cs[0].DeepCopy()
		dup.LabelSelector = &metav1.LabelSelector{MatchLabels: d.Spec.Selector.MatchLabels}
		d.Spec.Template.Spec.TopologySpreadConstraints = append(cs, dup)
		ensureTestNamespace(t, c, d.Namespace)
		err = c.Create(t.Context(), d)
		if err == nil || !strings.Contains(err.Error(), "topologySpreadConstraints") ||
			!strings.Contains(err.Error(), "Duplicate value") {
			t.Fatalf("creating a Deployment with a repeated spread pair returned %v, want the server's "+
				"Duplicate value refusal on topologySpreadConstraints", err)
		}
	})

	for i, p := range renderProfiles {
		t.Run(p.name, func(t *testing.T) {
			instance := fmt.Sprintf("p%02d", i+1)
			rel, err := installProfile(t, cfg, c, ch, instance, profileValues(t, ch, p, instance))
			if err != nil {
				t.Fatalf("the API server refused the %s install: %v", p.name, err)
			}
			if missing := everyObjectIsThere(t, c, rel.Manifest); len(missing) != 0 {
				t.Errorf("the %s install reported success but the server does not hold: %v", p.name, missing)
			}
		})
	}
}

// placement is the part of a Deployment's pod template the chart's placement rules
// write: its spread and its affinity. The API server defaults neither.
type placement struct {
	Spread   []corev1.TopologySpreadConstraint
	Affinity *corev1.Affinity
}

func placementOf(d *appsv1.Deployment) placement {
	return placement{Spread: d.Spec.Template.Spec.TopologySpreadConstraints, Affinity: d.Spec.Template.Spec.Affinity}
}

// livePlacement reads the placement the server holds for the named Deployment.
func livePlacement(t *testing.T, c client.Client, namespace, name string) placement {
	t.Helper()
	var d appsv1.Deployment
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &d); err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", namespace, name, err)
	}
	return placementOf(&d)
}

// probeChart is a chart whose only template is the Deployment it is handed, so an
// upgrade can be driven between two hand-built shapes of one object through Helm's
// own three-way patch, which is what decides what a live object ends up holding.
func probeChart() *chart.Chart {
	return &chart.Chart{
		Metadata: &chart.Metadata{APIVersion: "v2", Name: "placement-probe", Version: "0.1.0"},
		Templates: []*chart.File{{
			Name: "templates/deployment.yaml",
			Data: []byte("{{ .Values.deployment }}"),
		}},
	}
}

func probeValues(t *testing.T, d *appsv1.Deployment) map[string]interface{} {
	t.Helper()
	b, err := yaml.Marshal(d)
	if err != nil {
		t.Fatalf("marshalling the probe Deployment: %v", err)
	}
	return map[string]interface{}{"deployment": string(b)}
}

// probeInstall installs d through the probe chart.
func probeInstall(t *testing.T, cfg *action.Configuration, name string, d *appsv1.Deployment) error {
	t.Helper()
	inst := action.NewInstall(cfg)
	inst.ReleaseName = name
	inst.Namespace = helmReleaseNamespace
	_, err := inst.RunWithContext(t.Context(), probeChart(), probeValues(t, d))
	return err
}

// probeUpgrade upgrades the probe release to d, through newHelmUpgrade.
func probeUpgrade(t *testing.T, cfg *action.Configuration, name string, d *appsv1.Deployment) error {
	t.Helper()
	_, err := upgradeRelease(t, cfg, name, probeChart(), probeValues(t, d))
	return err
}

// haEventManagement renders the --ha profile for instance and returns its
// event-management Deployment, the fixed shape every sequence below converges to.
func haEventManagement(t *testing.T, ch *chart.Chart, instance string) *appsv1.Deployment {
	t.Helper()
	manifest, err := renderChartClientSide(t.Context(), ch, profileValues(t, ch, renderProfiles[1], instance))
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	return renderedDeployment(t, manifest, "event-management")
}

// withoutOwnTerm is d with the affinity term over the area's own pods removed: the
// placement a development build rendered before that term existed (the event-store
// primary term and the one event-path constraint).
func withoutOwnTerm(t *testing.T, d *appsv1.Deployment) *appsv1.Deployment {
	t.Helper()
	out := d.DeepCopy()
	terms := out.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	var kept []corev1.WeightedPodAffinityTerm
	for _, term := range terms {
		if _, own := term.PodAffinityTerm.LabelSelector.MatchLabels["devicechain.io/functional-area"]; !own {
			kept = append(kept, term)
		}
	}
	if len(kept) != len(terms)-1 {
		t.Fatalf("expected to remove exactly one own-pods term from %d, kept %d", len(terms), len(kept))
	}
	out.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution = kept
	return out
}

// withSecondConstraint is d with a second hostname/ScheduleAnyway constraint over the
// Deployment's own selector: the shape a development build rendered above one replica.
func withSecondConstraint(d *appsv1.Deployment) *appsv1.Deployment {
	out := d.DeepCopy()
	out.Spec.Template.Spec.TopologySpreadConstraints = append(out.Spec.Template.Spec.TopologySpreadConstraints,
		corev1.TopologySpreadConstraint{
			MaxSkew:           1,
			TopologyKey:       "kubernetes.io/hostname",
			WhenUnsatisfiable: corev1.ScheduleAnyway,
			LabelSelector:     &metav1.LabelSelector{MatchLabels: out.Spec.Selector.MatchLabels},
		})
	return out
}

// unionSelector is the selector a Helm upgrade from the one-constraint shape to the
// two-constraint shape left live: the two hostname entries merged into one.
func unionSelector(instance string) map[string]string {
	return map[string]string{
		"devicechain.io/event-path":      "true",
		"devicechain.io/functional-area": "event-management",
		"devicechain.io/instance":        instance,
	}
}

// mergedConstraint reports what is wrong with cs, or "" when it is exactly the one
// constraint the merging upgrade leaves: a preferred hostname spread whose selector is
// the union of the event-path label and the area's own labels.
func mergedConstraint(cs []corev1.TopologySpreadConstraint, instance string) string {
	if len(cs) != 1 {
		return fmt.Sprintf("%d constraints, want one: %+v", len(cs), cs)
	}
	m := cs[0]
	switch {
	case m.MaxSkew != 1 || m.TopologyKey != "kubernetes.io/hostname" || m.WhenUnsatisfiable != corev1.ScheduleAnyway:
		return fmt.Sprintf("not a maxSkew 1 hostname/ScheduleAnyway constraint: %+v", m)
	case m.LabelSelector == nil || len(m.LabelSelector.MatchExpressions) != 0 ||
		!reflect.DeepEqual(m.LabelSelector.MatchLabels, unionSelector(instance)):
		return fmt.Sprintf("the selector is %+v, want exactly %v", m.LabelSelector, unionSelector(instance))
	case len(m.MatchLabelKeys) != 0 || m.MinDomains != nil || m.NodeAffinityPolicy != nil || m.NodeTaintsPolicy != nil:
		return fmt.Sprintf("sets a field neither shape did: %+v", m)
	}
	return ""
}

// TestAnUpgradeLeavesTheChartsPlacementInPlace drives Helm upgrades of the
// event-management Deployment through a real API server and compares what the server
// holds with what the chart renders. The placement lists have different patch rules
// (the spread is merged on topologyKey, the preferred affinity terms are replaced
// whole), so what an upgrade leaves is a property of the patch, not of the render.
func TestAnUpgradeLeavesTheChartsPlacementInPlace(t *testing.T) {
	c, kubeconfig := renderAPIServer(t)
	cfg := helmInto(t, kubeconfig)
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading the embedded chart: %v", err)
	}

	// A. The released path. The last release rendered no affinity and no spread, no
	// event-path label, and ran event-management as one pod under --ha.
	t.Run("from the released placement, one upgrade", func(t *testing.T) {
		fixed := haEventManagement(t, ch, "ua")
		released := fixed.DeepCopy()
		released.Spec.Template.Spec.Affinity = nil
		released.Spec.Template.Spec.TopologySpreadConstraints = nil
		delete(released.Spec.Template.Labels, "devicechain.io/event-path")
		one := int32(1)
		released.Spec.Replicas = &one
		ensureTestNamespace(t, c, fixed.Namespace)
		if err := probeInstall(t, cfg, "probe-ua", released); err != nil {
			t.Fatalf("installing the released shape: %v", err)
		}
		if got := livePlacement(t, c, fixed.Namespace, fixed.Name); got.Affinity != nil || len(got.Spread) != 0 {
			t.Fatalf("control: the released shape is live with placement %+v, want none", got)
		}
		if err := probeUpgrade(t, cfg, "probe-ua", fixed); err != nil {
			t.Fatalf("upgrading to the fixed shape: %v", err)
		}
		if got, want := livePlacement(t, c, fixed.Namespace, fixed.Name), placementOf(fixed); !reflect.DeepEqual(got, want) {
			t.Errorf("after one upgrade the server holds\n%+v\nwant the chart's\n%+v", got, want)
		}
	})

	// B. --ha off and on again with the whole chart, as dcctl upgrades it: the
	// affinity goes from two terms to one and back, each in one upgrade, because the
	// preferred list is replaced whole.
	t.Run("--ha off and on, with the real chart", func(t *testing.T) {
		const instance = "up"
		ha := profileValues(t, ch, renderProfiles[1], instance)
		rel, err := installProfile(t, cfg, c, ch, instance, ha)
		if err != nil {
			t.Fatalf("installing --ha: %v", err)
		}
		ns := renderedDeployment(t, rel.Manifest, "event-management").Namespace
		steps := []struct {
			name string
			vals map[string]interface{}
		}{
			{"--ha", ha},
			{"event-management at one pod", profileValues(t, ch, renderProfile{
				name: "one", st: profileState(true, false, "default"),
				overrides: areaOverride("event-management", map[string]interface{}{"replicas": 1}),
			}, instance)},
			{"--ha again", ha},
		}
		for i, s := range steps {
			if i > 0 {
				if rel, err = upgradeRelease(t, cfg, helmReleaseNameFor(instance), ch, s.vals); err != nil {
					t.Fatalf("upgrading to %s: %v", s.name, err)
				}
			}
			want := placementOf(renderedDeployment(t, rel.Manifest, "event-management"))
			if got := livePlacement(t, c, ns, "event-management"); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: the server holds\n%+v\nwant the chart's\n%+v", s.name, got, want)
			}
			terms := want.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
			if wantTerms := map[int]int{0: 2, 1: 1, 2: 2}[i]; len(terms) != wantTerms {
				t.Errorf("%s: the chart renders %d preferred terms, want %d: the step changes nothing to check",
					s.name, len(terms), wantTerms)
			}
		}
	})

	// D. The state a refused --ha install leaves: a failed release whose manifest
	// holds the two-constraint shape, and no event-management Deployment, because the
	// server refused it. An upgrade creates it from the fixed shape.
	t.Run("a refused install, then an upgrade", func(t *testing.T) {
		fixed := haEventManagement(t, ch, "ud")
		broken := withSecondConstraint(withoutOwnTerm(t, fixed))
		ensureTestNamespace(t, c, fixed.Namespace)
		err := probeInstall(t, cfg, "probe-ud", broken)
		if err == nil || !strings.Contains(err.Error(), "Duplicate value") {
			t.Fatalf("installing the two-constraint shape returned %v, want the server's Duplicate value refusal", err)
		}
		if err := probeUpgrade(t, cfg, "probe-ud", fixed); err != nil {
			t.Fatalf("upgrading the refused install to the fixed shape: %v", err)
		}
		if got, want := livePlacement(t, c, fixed.Namespace, fixed.Name), placementOf(fixed); !reflect.DeepEqual(got, want) {
			t.Errorf("after the upgrade the server holds\n%+v\nwant the chart's\n%+v", got, want)
		}
	})

	// C. An instance built from an unreleased development build that rendered a
	// second hostname/ScheduleAnyway constraint, and was UPGRADED onto it (a fresh
	// install of that build is refused, D above). The upgrade merged the two entries
	// into one constraint whose selector is their union, and what a later upgrade does
	// with it depends on whether Helm recorded that upgrade as deployed or as failed
	// (a Wait that timed out). Neither history converges by upgrading alone; deleting
	// the Deployment and upgrading does, for both. No production code handles this:
	// the state never shipped in a release.
	for _, history := range []struct {
		name     string
		instance string
		// failed: the merging upgrade waited and timed out, so Helm recorded it as
		// failed and keeps diffing against the one-constraint release before it.
		failed bool
	}{
		{"the merging upgrade recorded as deployed", "uc", false},
		{"the merging upgrade recorded as failed", "uf", true},
	} {
		t.Run("from a merged constraint, "+history.name, func(t *testing.T) {
			fixed := haEventManagement(t, ch, history.instance)
			oneConstraint := withoutOwnTerm(t, fixed)
			twoConstraints := withSecondConstraint(oneConstraint)
			name := "probe-" + history.instance
			ensureTestNamespace(t, c, fixed.Namespace)
			if err := probeInstall(t, cfg, name, oneConstraint); err != nil {
				t.Fatalf("installing the one-constraint shape: %v", err)
			}
			if history.failed {
				upg := newHelmUpgrade(cfg, helmReleaseNamespace)
				upg.Timeout = 2 * time.Second // envtest runs no controllers: the Wait cannot succeed
				if _, err := upg.RunWithContext(t.Context(), name, probeChart(), probeValues(t, twoConstraints)); err == nil {
					t.Fatal("control: an upgrade that waits on envtest succeeded, so the history is not the failed one")
				}
				last, err := cfg.Releases.Last(name)
				if err != nil || last.Info.Status != release.StatusFailed {
					t.Fatalf("control: the last revision is %v (err %v), want failed", last.Info.Status, err)
				}
			} else if err := probeUpgrade(t, cfg, name, twoConstraints); err != nil {
				t.Fatalf("upgrading to the two-constraint shape: %v", err)
			}

			// Positive control: the merge happened. If it did not, nothing below means anything.
			if why := mergedConstraint(livePlacement(t, c, fixed.Namespace, fixed.Name).Spread, history.instance); why != "" {
				t.Fatalf("control: the upgrade onto two constraints did not leave the merged constraint: %s", why)
			}

			// Two upgrades to the fixed chart. Neither converges.
			for i := 1; i <= 2; i++ {
				err := probeUpgrade(t, cfg, name, fixed)
				if history.failed {
					// Helm diffs against the last DEPLOYED manifest, which holds the
					// one event-path constraint the fixed chart also renders, so the
					// patch says nothing about the list: the union stays, silently.
					if err != nil {
						t.Errorf("fixed upgrade %d: %v, want success (the silent case)", i, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "topologySpreadConstraints[0].maxSkew") {
					// The stored manifest holds two hostname entries; the patch pairs
					// one with the new entry (as a field deletion) and deletes the
					// other, which appends a constraint with no maxSkew. The server
					// refuses it, and every retry computes the same patch.
					t.Errorf("fixed upgrade %d returned %v, want the server refusing a constraint with no maxSkew", i, err)
				}
				if why := mergedConstraint(livePlacement(t, c, fixed.Namespace, fixed.Name).Spread, history.instance); why != "" {
					t.Errorf("after fixed upgrade %d the merged constraint is gone: %s (this pins what an upgrade "+
						"alone does; if it now converges, the recovery note can go)", i, why)
				}
			}

			// The recovery: delete the Deployment, then upgrade. Helm creates a target
			// object that is missing live from the target alone, whatever it stored.
			live := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: fixed.Namespace, Name: fixed.Name}}
			if err := c.Delete(t.Context(), live, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
				t.Fatalf("deleting the Deployment: %v", err)
			}
			if err := probeUpgrade(t, cfg, name, fixed); err != nil {
				t.Fatalf("upgrading after the delete: %v", err)
			}
			if got, want := livePlacement(t, c, fixed.Namespace, fixed.Name), placementOf(fixed); !reflect.DeepEqual(got, want) {
				t.Errorf("after delete and upgrade the server holds\n%+v\nwant the chart's\n%+v", got, want)
			}
		})
	}
}
