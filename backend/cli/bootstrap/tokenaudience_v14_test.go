// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"github.com/devicechain-io/dc-microservice/config"
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
	err = applyUpgradeValuesOverlay(&State{Instance: "prod"}, inst, UpgradeOptions{})
	require.ErrorContains(t, err, "v1.4")
	require.ErrorContains(t, err, AnnotationValuesOverlay)
	require.ErrorContains(t, err, "Nothing has been changed")
	require.ErrorContains(t, err, "re-run `dcctl upgrade` with `--values <file without the key>` or `--reset-values`")
	require.Equal(t, before, inst, "refusal must not rewrite the stored overlay")
	for _, opts := range []UpgradeOptions{{Options: Options{ResetValues: true}}, {Options: Options{ValuesOverlay: map[string]interface{}{"replicas": 2}}}} {
		st := compactState(false)
		require.NoError(t, applyUpgradeValuesOverlay(st, inst, opts))
		require.True(t, st.ValuesOverlayIntent)
		require.Equal(t, opts.ValuesOverlay, st.ValuesOverlay)
		require.NoError(t, refuseObserveValues(st.ValuesOverlay, "the new overlay"))
		require.Equal(t, before, inst, "settling must not rewrite the stored overlay")
		if opts.ResetValues {
			require.Empty(t, st.ValuesOverlay)
			raw, err := renderInstanceConfigDocument(t.Context(), chartForTest(t), renderOnlyAuthoring(st))
			require.NoError(t, err)
			require.NotEmpty(t, raw)
			cfg, err := loadInstanceConfigDocument(raw)
			require.NoError(t, err)
			require.Equal(t, "enforce", string(cfg.Infrastructure.TokenAudience.EffectiveEnforcement()))
			require.NotContains(t, string(raw), `"enforcement":"observe"`)
		}
	}
	// A replacement file must still pass validation before being applied.
	require.ErrorContains(t, ValidateValuesOverlay(legacyObserveValues()), "v1.4")
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
	require.ErrorContains(t, err, "re-run `dcctl upgrade` with `--values <file without the key>` or `--reset-values`")
	require.NoError(t, precheckValuesOverlay(t.Context(), &State{Instance: "prod", Evolving: true, ValuesOverlayIntent: true}))
	unchanged, err := store.Last(helmReleaseNameFor("prod"))
	require.NoError(t, err)
	require.Equal(t, rel, unchanged)
	// Counterweight: an old release without the opt-out can pass this gate.
	rel.Config = map[string]interface{}{}
	require.NoError(t, store.Update(rel))
	require.NoError(t, precheckValuesOverlay(t.Context(), &State{Instance: "prod", Evolving: true}))
}

func TestV14BootstrapReplacesStoredObserveWithExplicitOverlay(t *testing.T) {
	inst := atVersion(t, "ghcr.io/devicechain-io", "v1.3.0")
	raw, err := json.Marshal(legacyObserveValues())
	require.NoError(t, err)
	if inst.Annotations == nil {
		inst.Annotations = map[string]string{}
	}
	inst.Annotations[AnnotationValuesOverlay] = string(raw)
	before := inst.DeepCopy()
	err = adoptRecordedValuesOverlay(&State{Instance: "prod"}, inst)
	require.ErrorContains(t, err, "v1.4")
	require.ErrorContains(t, err, AnnotationValuesOverlay)
	require.ErrorContains(t, err, "Nothing has been changed")
	require.ErrorContains(t, err, "re-run `dcctl bootstrap` with `--values <file without the key>`")
	require.NotContains(t, err.Error(), "--reset-values")
	overlay := map[string]interface{}{"replicas": 2}
	st := &State{Instance: "prod", ValuesOverlay: overlay}
	require.NoError(t, adoptRecordedValuesOverlay(st, inst))
	require.Equal(t, overlay, st.ValuesOverlay)
	require.Equal(t, legacyObserveValues(), st.ValuesOverlayRecorded)
	require.Equal(t, before, inst)
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
	// Reading a deployed document that still says observe is not itself a refusal; the upgrade
	// decides (TestV14DeployedObserveIsRefusedFlaglessAndOverriddenByAnExplicitReset).
	_, err = parseDeployedConfig(raw, "prod", "dci-prod-config")
	require.NoError(t, err)
}

func TestV14DeployedObserveIsRefusedFlaglessAndOverriddenByAnExplicitReset(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    UpgradeOptions
		refused bool
	}{
		{"flagless", UpgradeOptions{Options: Options{Instance: "prod"}}, true},
		{"reset-values", UpgradeOptions{Options: Options{Instance: "prod", ResetValues: true}}, false},
		{"replacing values", UpgradeOptions{Options: Options{Instance: "prod", ValuesOverlay: map[string]interface{}{"replicas": 2}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := atVersion(t, "ghcr.io/devicechain-io", "v1.3.0")
			inst.Spec.TrustedProxies = []string{"10.0.0.0/8"}
			inst.Spec.TrustedProxiesExplicit = true
			fakeHome(t)
			stubOperatorCheck(t, nil)
			stubDeclaration(t, inst)
			deployed := &config.InstanceConfiguration{}
			deployed.Infrastructure.TokenAudience.Enforcement = "observe"
			stubDeployedInstance(t, deployed)
			provider, err := Get("local")
			require.NoError(t, err)
			client := fake.NewSimpleClientset()
			_, err = hydrateUpgradeState(t.Context(), client, provider,
				ClusterBinding{KubeContext: "kind-c", Cluster: "c"}, tc.opts)
			if tc.refused {
				require.ErrorContains(t, err, "--reset-values")
				require.ErrorContains(t, err, "Nothing has been changed")
			} else if err != nil {
				require.NotContains(t, err.Error(), "which v1.4 refuses")
			}
			for _, action := range client.Actions() {
				require.Contains(t, []string{"get", "list", "watch"}, action.GetVerb())
			}
		})
	}
}
