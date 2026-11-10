// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"go/ast"
	"testing"

	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/action"
	"k8s.io/client-go/kubernetes/fake"
)

func legacyObserveValues() map[string]interface{} {
	return map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{
		"infrastructure": map[string]interface{}{"tokenAudience": map[string]interface{}{"enforcement": "observe"}},
	}}}
}

func TestV14RefusesObserveOverlayAndFile(t *testing.T) {
	require.ErrorContains(t, ValidateValuesOverlay(legacyObserveValues()), "removed in v1.4")
	raw, err := json.Marshal(legacyObserveValues())
	require.NoError(t, err)
	_, err = LoadValuesOverlay([]string{writeOverlayFile(t, "legacy.json", string(raw))})
	require.ErrorContains(t, err, "Remove")
	for _, mode := range []string{"", "enforce"} {
		values := legacyObserveValues()
		ta := values["instance"].(map[string]interface{})["config"].(map[string]interface{})["infrastructure"].(map[string]interface{})["tokenAudience"].(map[string]interface{})
		if mode == "" {
			delete(ta, "enforcement")
		} else {
			ta["enforcement"] = mode
		}
		ta["unscopedClientCredentials"] = "observe"
		require.NoError(t, ValidateValuesOverlay(values))
	}
}

func TestV14UpgradeRefusesStoredObserveBeforeRecordingAnything(t *testing.T) {
	inst := atVersion(t, "ghcr.io/devicechain-io", "v1.3.0")
	raw, err := json.Marshal(legacyObserveValues())
	require.NoError(t, err)
	if inst.Annotations == nil {
		inst.Annotations = map[string]string{}
	}
	inst.Annotations[AnnotationValuesOverlay] = string(raw)
	before := inst.DeepCopy()
	for _, opts := range []UpgradeOptions{{}, {Options: Options{ResetValues: true}}, {Options: Options{ValuesOverlay: map[string]interface{}{"replicas": 2}}}} {
		err = applyUpgradeValuesOverlay(&State{Instance: "prod"}, inst, opts)
		require.ErrorContains(t, err, "v1.4")
		require.ErrorContains(t, err, AnnotationValuesOverlay)
		require.Equal(t, before, inst, "refusal must not rewrite the stored overlay")
	}
	// Drive the actual hydration preflight too, using the upgrade harness and a
	// Kubernetes fake that records every operation. No mutation may reach it.
	fakeHome(t)
	stubOperatorCheck(t, nil)
	inst.Spec.TrustedProxies = []string{"10.0.0.0/8"}
	inst.Spec.TrustedProxiesExplicit = true
	stubDeclaration(t, inst)
	stubDeployedInstance(t, nil)
	provider, err := Get("local")
	require.NoError(t, err)
	client := fake.NewSimpleClientset()
	_, err = hydrateUpgradeState(t.Context(), client, provider,
		ClusterBinding{KubeContext: "kind-c", Cluster: "c"}, UpgradeOptions{Options: Options{Instance: "prod"}})
	require.ErrorContains(t, err, "v1.4")
	for _, action := range client.Actions() {
		require.Contains(t, []string{"get", "list", "watch"}, action.GetVerb(), "preflight must not mutate Kubernetes")
	}
}

func TestV14UpgradePreflightRefusesPreviousReleaseObserveWithoutAnOverlay(t *testing.T) {
	assertObservePreflightBeforeUpgradeMutations(t)
	rel := deviceChainRelease(helmReleaseNameFor("prod"), "prod")
	rel.Config = legacyObserveValues()
	cfg, store := inMemoryHelm(t, rel)
	previous := helmActionConfigFor
	t.Cleanup(func() { helmActionConfigFor = previous })
	helmActionConfigFor = func(string) (*action.Configuration, error) { return cfg, nil }
	err := precheckValuesOverlay(t.Context(), &State{Instance: "prod", Evolving: true})
	require.ErrorContains(t, err, "v1.4")
	require.ErrorContains(t, err, "previously installed release")
	require.ErrorContains(t, err, "Nothing has been changed")
	unchanged, err := store.Last(helmReleaseNameFor("prod"))
	require.NoError(t, err)
	require.Equal(t, rel, unchanged)
	// Counterweight: an old release without the opt-out can pass this gate.
	rel.Config = map[string]interface{}{}
	require.NoError(t, store.Update(rel))
	require.NoError(t, precheckValuesOverlay(t.Context(), &State{Instance: "prod", Evolving: true}))
}

// The same source-order harness used by the infrastructure gates pins the preflight
// before every first-write boundary, including phase recording and the claim.
func assertObservePreflightBeforeUpgradeMutations(t *testing.T) {
	t.Helper()
	fn, _ := upgradeFuncDecl(t)
	positions := map[string][]int{}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				positions[id.Name] = append(positions[id.Name], int(call.Pos()))
			}
		}
		return true
	})
	require.Len(t, positions["precheckValuesOverlay"], 1)
	gate := positions["precheckValuesOverlay"][0]
	for _, name := range []string{"planUpgradeInfra", "recordUpgradedVersion", "beginUpgradeClaim", "renewBrokerCertificate", "applyUpgradeInfra", "helmInstall"} {
		require.NotEmpty(t, positions[name], "missing mutation boundary %s", name)
		for _, pos := range positions[name] {
			require.Less(t, gate, pos, "preflight must precede %s", name)
		}
	}
}

func TestV14ChartRefusesObserveThroughValuesSchema(t *testing.T) {
	ch := chartForTest(t)
	_, err := renderChartClientSide(t.Context(), ch, tokenAudienceValues(tlsIngress("iot.example.com"), "full", "", map[string]interface{}{"enforcement": "observe"}))
	require.ErrorContains(t, err, "schema")
	require.ErrorContains(t, err, "enforcement")
}

func TestV14ExistingSecretDocumentRefusesObserve(t *testing.T) {
	ch := chartForTest(t)
	raw, err := renderInstanceConfigDocument(t.Context(), ch, tokenAudienceValues(tlsIngress("iot.example.com"), "", "", nil))
	require.NoError(t, err)
	var doc map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &doc))
	infra := doc["infrastructure"].(map[string]interface{})
	infra["tokenAudience"].(map[string]interface{})["enforcement"] = "observe"
	raw, err = json.Marshal(doc)
	require.NoError(t, err)
	require.ErrorContains(t, validateRenderedInstanceConfig(t.Context(), ch, externalValues(digest("a")), raw), "removed in v1.4")
	_, err = parseDeployedConfig(raw, "prod", "dci-prod-config")
	require.ErrorContains(t, err, "existingSecret")
	require.ErrorContains(t, err, "v1.4")
}
