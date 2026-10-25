// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"errors"
	"maps"
	"net/netip"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// 🔴 THESE DRIVE THE REAL PATH, as the fail-closed tests do: nodes -> settle -> State ->
// declaration -> the document the services load with their own strict loader, and back through
// an upgrade. The default being protected is the one that matters: with no trusted proxy every
// device behind the ingress shares one self-registration allowance.

func podNode(name string, cidrs ...string) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	n.Spec.PodCIDRs = cidrs
	if len(cidrs) > 0 {
		n.Spec.PodCIDR = cidrs[0]
	}
	return n
}

func clusterOf(nodes ...*corev1.Node) *kubefake.Clientset {
	objs := make([]runtime.Object, len(nodes))
	for i, n := range nodes {
		objs[i] = n
	}
	return kubefake.NewSimpleClientset(objs...)
}

func prefixes(t *testing.T, in ...string) []netip.Prefix {
	t.Helper()
	out := make([]netip.Prefix, len(in))
	for i, s := range in {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func TestAggregatePrefixes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{"one", []string{"10.244.0.0/24"}, []string{"10.244.0.0/24"}},
		{"siblings merge", []string{"10.244.0.0/24", "10.244.1.0/24"}, []string{"10.244.0.0/23"}},
		{"four consecutive collapse", []string{"10.244.3.0/24", "10.244.0.0/24", "10.244.2.0/24", "10.244.1.0/24"}, []string{"10.244.0.0/22"}},
		{"non-siblings stay apart", []string{"10.244.1.0/24", "10.244.2.0/24"}, []string{"10.244.1.0/24", "10.244.2.0/24"}},
		{"contained dropped", []string{"10.244.0.0/16", "10.244.7.0/24"}, []string{"10.244.0.0/16"}},
		{"duplicates", []string{"10.1.0.0/24", "10.1.0.0/24"}, []string{"10.1.0.0/24"}},
		{"ipv4 and ipv6", []string{"fd00:10::/64", "10.0.0.0/24"}, []string{"10.0.0.0/24", "fd00:10::/64"}},
		{"ipv6 siblings", []string{"fd00:10::/65", "fd00:10:0:0:8000::/65"}, []string{"fd00:10::/64"}},
		{"host bits masked", []string{"10.244.0.7/24"}, []string{"10.244.0.0/24"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, prefixStrings(aggregatePrefixes(prefixes(t, tc.in...))))
		})
	}
	// Merging must never widen: the union of the inputs is exactly the union of the outputs.
	in := prefixes(t, "10.244.0.0/24", "10.244.1.0/24", "10.244.5.0/24")
	out := aggregatePrefixes(in)
	for _, probe := range []string{"10.244.0.9", "10.244.1.200", "10.244.5.1", "10.244.2.1", "10.244.4.1", "10.244.6.1"} {
		a := netip.MustParseAddr(probe)
		var inIn, inOut bool
		for _, p := range in {
			inIn = inIn || p.Contains(a)
		}
		for _, p := range out {
			inOut = inOut || p.Contains(a)
		}
		assert.Equal(t, inIn, inOut, probe)
	}
}

func TestDerivePodCIDRsReadsTheNodes(t *testing.T) {
	got, err := derivePodCIDRs(t.Context(), clusterOf(podNode("a", "10.244.0.0/24"), podNode("b", "10.244.1.0/24"), podNode("c")))
	require.NoError(t, err)
	assert.Equal(t, []string{"10.244.0.0/23"}, got, "a node with no pod range contributes nothing")

	// spec.podCIDR alone (an older API server) is read too.
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "old"}}
	n.Spec.PodCIDR = "10.9.0.0/24"
	got, err = derivePodCIDRs(t.Context(), clusterOf(n))
	require.NoError(t, err)
	assert.Equal(t, []string{"10.9.0.0/24"}, got)

	got, err = derivePodCIDRs(t.Context(), clusterOf(podNode("cilium")))
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSettleTrustedProxies(t *testing.T) {
	derived := func(l ...string) func() ([]string, error) { return func() ([]string, error) { return l, nil } }
	failing := func() ([]string, error) { return nil, errors.New("nodes is forbidden") }
	for _, tc := range []struct {
		name         string
		recorded     []string
		explicit     bool
		override     []string
		derive       func() ([]string, error)
		want         []string
		wantExplicit bool
		wantSource   trustedProxiesSource
		wantWarning  bool
		wantErr      bool
	}{
		{"fresh instance derives", nil, false, nil, derived("10.244.0.0/16"), []string{"10.244.0.0/16"}, false, trustedFromNodes, false, false},
		{"a grown cluster is followed", []string{"10.244.0.0/24"}, false, nil, derived("10.244.0.0/23"), []string{"10.244.0.0/23"}, false, trustedFromNodes, false, false},
		{"override wins over nodes", nil, false, []string{"192.168.0.0/16"}, derived("10.244.0.0/16"), []string{"192.168.0.0/16"}, true, trustedFromOperator, false, false},
		{"override is canonicalised", nil, false, []string{"10.0.0.0/25", "10.0.0.128/25"}, failing, []string{"10.0.0.0/24"}, true, trustedFromOperator, false, false},
		{"recorded explicit list is kept as written", []string{"192.168.0.0/16"}, true, nil, derived("10.244.0.0/16"), []string{"192.168.0.0/16"}, true, trustedFromRecord, false, false},
		{"override replaces a recorded explicit list", []string{"192.168.0.0/16"}, true, []string{"172.16.0.0/12"}, derived("10.244.0.0/16"), []string{"172.16.0.0/12"}, true, trustedFromOperator, false, false},
		{"auto drops an explicit list", []string{"192.168.0.0/16"}, true, []string{"auto"}, derived("10.244.0.0/16"), []string{"10.244.0.0/16"}, false, trustedFromNodes, false, false},
		{"unreadable nodes keep the recorded list", []string{"10.244.0.0/16"}, false, nil, failing, []string{"10.244.0.0/16"}, false, trustedFromRecordWarn, true, false},
		{"nodes with no range keep the recorded list", []string{"10.244.0.0/16"}, false, nil, derived(), []string{"10.244.0.0/16"}, false, trustedFromRecordWarn, true, false},
		{"nothing anywhere renders none, loudly", nil, false, nil, derived(), nil, false, trustedNone, true, false},
		{"unreadable nodes and no record", nil, false, nil, failing, nil, false, trustedNone, true, false},
		{"a malformed override is refused", nil, false, []string{"10.0.0.1/8"}, derived("10.244.0.0/16"), nil, false, "", false, true},
		{"a non-CIDR override is refused", nil, false, []string{"ingress"}, derived("10.244.0.0/16"), nil, false, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := settleTrustedProxies(tc.recorded, tc.explicit, tc.override, tc.derive)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "--trusted-proxies")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.List)
			assert.Equal(t, tc.wantExplicit, got.Explicit)
			assert.Equal(t, tc.wantSource, got.Source)
			assert.Equal(t, tc.wantWarning, got.Warning != "")
		})
	}
}

// documentWithProxies composes the instance configuration document for st and loads it with the
// services' own strict loader.
func documentWithProxies(t *testing.T, st *State) (loaded []string, raw string) {
	t.Helper()
	ch, err := loadEmbeddedChart()
	require.NoError(t, err)
	_, doc, err := releaseValues(t.Context(), ch, st, nil)
	require.NoError(t, err)
	cfg, err := loadInstanceConfigDocument(doc)
	require.NoError(t, err, "the services would refuse this document")
	return cfg.Infrastructure.Http.TrustedProxies, string(doc)
}

// The settled list reaches the document the services load, and an instance with none renders
// no key at all (a rendered empty list would change every existing document).
func TestTrustedProxiesReachTheServicesDocument(t *testing.T) {
	st := compactState(false)
	got, _ := documentWithProxies(t, st)
	assert.Empty(t, got)
	_, raw := documentWithProxies(t, st)
	assert.NotContains(t, raw, "trustedProxies", "an instance with no list rendered the key")

	st.TrustedProxies = []string{"10.244.0.0/23", "fd00:10::/64"}
	got, raw = documentWithProxies(t, st)
	assert.Equal(t, []string{"10.244.0.0/23", "fd00:10::/64"}, got, "the services loaded something other than the pod ranges:\n%s", raw)
}

// bootstrapped is the declaration a bootstrap over these nodes writes, as the cluster stores it.
func bootstrapped(t *testing.T, typed kubernetes.Interface, override []string) (st *State, _ struct{}) {
	t.Helper()
	st = compactState(false)
	st.Instance = "prod"
	st.TrustedProxiesOverride = override
	require.NoError(t, applyTrustedProxies(t.Context(), st, typed, nil, false, override))
	return st, struct{}{}
}

// 🔴 THE WHOLE LIFE: bootstrap derives from the nodes and records it; a plain upgrade renders the
// same list; a cluster that grew is followed; nodes that cannot be read keep the recorded list;
// an explicit override wins, is recorded, and survives later upgrades that name nothing.
func TestTrustedProxiesLifeAcrossBootstrapAndUpgrades(t *testing.T) {
	two := clusterOf(podNode("a", "10.244.0.0/24"), podNode("b", "10.244.1.0/24"))

	// Bootstrap.
	bst, _ := bootstrapped(t, two, nil)
	assert.Equal(t, []string{"10.244.0.0/23"}, bst.TrustedProxies)
	assert.False(t, bst.TrustedProxiesExplicit)
	spec := InstanceSpecFrom(bst, ClusterBinding{Cluster: "kind-devicechain", Managed: true}, "local")
	spec.ImageRegistry, spec.ImageVersion = "ghcr.io/devicechain-io", "v1.2.0"
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	var wire dcv1beta1.InstanceSpec
	require.NoError(t, json.Unmarshal(raw, &wire))
	inst := atVersion(t, "ghcr.io/devicechain-io", "v1.2.0")
	inst.Spec = wire
	dyn := declaring(t, inst)

	upgrade := func(typed kubernetes.Interface, override []string) *State {
		t.Helper()
		declared := readBack(t, dyn, "prod")
		st := &State{Instance: "prod", Evolving: true, Values: maps.Clone(compactState(false).Values)}
		require.NoError(t, applyUpgradeDeclaration(st, declared, UpgradeOptions{Options: Options{Instance: "prod"}}))
		require.NoError(t, applyTrustedProxies(t.Context(), st, typed, declared.Spec.TrustedProxies,
			declared.Spec.TrustedProxiesExplicit, override))
		up := upgradingTo("ghcr.io/devicechain-io", "v1.3.0")
		up.TrustedProxies, up.TrustedProxiesExplicit, up.TrustedProxiesChange = st.TrustedProxies, st.TrustedProxiesExplicit, st.TrustedProxiesChange
		require.NoError(t, recordUpgradedVersion(t.Context(), dyn, "prod", up))
		return st
	}

	// 1. A plain upgrade renders the same list, in the document the services load.
	st := upgrade(two, nil)
	got, _ := documentWithProxies(t, st)
	assert.Equal(t, []string{"10.244.0.0/23"}, got, "a plain upgrade lost the pod ranges")
	assert.False(t, st.TrustedProxiesChange, "nothing changed, so nothing is written")

	// 2. The cluster grew: the upgrade follows it and records the new list.
	grown := clusterOf(podNode("a", "10.244.0.0/24"), podNode("b", "10.244.1.0/24"), podNode("c", "10.244.2.0/24"), podNode("d", "10.244.3.0/24"))
	st = upgrade(grown, nil)
	got, _ = documentWithProxies(t, st)
	assert.Equal(t, []string{"10.244.0.0/22"}, got)
	assert.Equal(t, []string{"10.244.0.0/22"}, readBack(t, dyn, "prod").Spec.TrustedProxies, "the refreshed list was not recorded")

	// 3. Nodes that cannot be read keep what is recorded rather than rendering the instance without.
	forbidden := kubefake.NewSimpleClientset()
	forbidden.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("nodes is forbidden")
	})
	st = upgrade(forbidden, nil)
	got, _ = documentWithProxies(t, st)
	assert.Equal(t, []string{"10.244.0.0/22"}, got, "an upgrade that could not read the nodes dropped the list")

	// 4. An explicit override wins over the nodes and is recorded as explicit.
	st = upgrade(grown, []string{"192.168.0.0/16"})
	got, _ = documentWithProxies(t, st)
	assert.Equal(t, []string{"192.168.0.0/16"}, got)
	rec := readBack(t, dyn, "prod").Spec
	assert.Equal(t, []string{"192.168.0.0/16"}, rec.TrustedProxies)
	assert.True(t, rec.TrustedProxiesExplicit)

	// 5. And survives a flagless upgrade, whatever the nodes now say.
	st = upgrade(two, nil)
	got, _ = documentWithProxies(t, st)
	assert.Equal(t, []string{"192.168.0.0/16"}, got, "a flagless upgrade replaced the operator's list with the nodes'")

	// 6. "auto" gives the instance back to the cluster.
	st = upgrade(two, []string{"auto"})
	got, _ = documentWithProxies(t, st)
	assert.Equal(t, []string{"10.244.0.0/23"}, got)
	assert.False(t, readBack(t, dyn, "prod").Spec.TrustedProxiesExplicit)
}

// A bootstrap given the flag renders and records it, whatever the nodes say.
func TestBootstrapOverrideWins(t *testing.T) {
	st, _ := bootstrapped(t, clusterOf(podNode("a", "10.244.0.0/24")), []string{"172.20.0.0/14"})
	assert.Equal(t, []string{"172.20.0.0/14"}, st.TrustedProxies)
	assert.True(t, st.TrustedProxiesExplicit)
	got, _ := documentWithProxies(t, st)
	assert.Equal(t, []string{"172.20.0.0/14"}, got)
	spec := InstanceSpecFrom(st, ClusterBinding{Cluster: "kind-devicechain", Managed: true}, "local")
	assert.Equal(t, []string{"172.20.0.0/14"}, spec.TrustedProxies)
	assert.True(t, spec.TrustedProxiesExplicit)
}

// A declaration carrying a malformed list is refused on the supported path, before anything is
// applied.
func TestAMalformedRecordedListIsRefused(t *testing.T) {
	spec := InstanceSpecFrom(compactState(false), ClusterBinding{Cluster: "kind-devicechain", Managed: true}, "local")
	spec.TrustedProxies = []string{"10.0.0.1/8"}
	err := ValidateInstanceSpec(spec)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "trustedProxies"))
	spec.TrustedProxies = []string{"10.0.0.0/8"}
	assert.NoError(t, ValidateInstanceSpec(spec))
}
