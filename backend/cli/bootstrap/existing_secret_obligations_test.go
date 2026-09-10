// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
)

// 🔴 WHAT THIS FILE IS FOR. instance.existingSecret hands the instance config to
// a supplier outside the chart. Every check the chart performs on that document
// is a check it performs while AUTHORING it, so switching the source switches
// them all off at once — silently, because a template that is not rendered
// raises nothing and a pre-flight with no document to load returns "valid".
//
// Nothing sets that value yet. These tests exist so that the day something does,
// the guards are already closed rather than being closed afterwards against a
// path that already shipped without them.

// renderChartManifests renders the embedded chart client-side and returns the
// whole manifest stream, so a test can assert on templates other than the
// instance-config Secret — the pod annotations, in particular, which are where
// the config-change rollout lives.
func renderChartManifests(ctx context.Context, ch *chart.Chart, vals map[string]interface{}) (string, error) {
	inst := action.NewInstall(&action.Configuration{})
	inst.ReleaseName = helmReleaseName
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = []string{"monitoring.coreos.com/v1"}
	rel, err := inst.RunWithContext(ctx, ch, vals)
	if err != nil {
		return "", err
	}
	return rel.Manifest, nil
}

// externalValues builds a values map whose instance config comes from a Secret
// the chart cannot see. checksum is passed through verbatim, including empty.
func externalValues(checksum string) map[string]interface{} {
	inst := map[string]interface{}{
		"id":             "dctest",
		"existingSecret": "dci-dctest-config",
	}
	if checksum != "" {
		inst["existingSecretChecksum"] = checksum
	}
	return map[string]interface{}{"instance": inst}
}

// inlineValues is the same instance with the config supplied in values, which is
// the path every other test in this package uses.
func inlineValues() map[string]interface{} {
	return map[string]interface{}{
		"instance": map[string]interface{}{
			"id": "dctest",
			"config": map[string]interface{}{
				"infrastructure": map[string]interface{}{
					"secrets": map[string]interface{}{
						"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
					},
				},
			},
		},
	}
}

func chartForTest(t *testing.T) *chart.Chart {
	t.Helper()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	return ch
}

// OBLIGATION: the pod annotation that rolls workloads on a config change must
// still change when the config changes.
//
// It hashes the document the chart renders, and under an external Secret the
// chart renders none — so the hash is a constant. A credential rotation would
// update the Secret, apply cleanly, restart nothing, and report success, with
// every pod still holding the old value: secretKeyRef and mounted-Secret content
// are read at container start. The chart cannot compute the digest itself, so it
// requires the supplier to declare one.
func TestExternalInstanceConfigRequiresADeclaredChecksum(t *testing.T) {
	ch := chartForTest(t)

	_, err := renderChartManifests(t.Context(), ch, externalValues(""))
	if err == nil {
		t.Fatal("the chart rendered with instance.existingSecret and no checksum: pods would " +
			"carry a constant annotation, and a config change would roll nothing")
	}
	if !strings.Contains(err.Error(), "existingSecretChecksum") {
		t.Errorf("the refusal does not name the value that fixes it: %v", err)
	}

	// The counterweight: supplying it must actually work, or this is a check that
	// bans the feature rather than one that guards it.
	out, err := renderChartManifests(t.Context(), ch, externalValues("deadbeef"))
	if err != nil {
		t.Fatalf("a declared checksum was rejected: %v", err)
	}
	if !strings.Contains(out, "checksum/instance-secret: deadbeef") {
		t.Error("the declared checksum did not reach the pod annotation, so the value is " +
			"accepted and then ignored — which is the failure it was added to prevent")
	}
}

// ...and it must be the DECLARED value that lands, not a constant that happens to
// differ from the inline one. Two renders that declare different digests must
// produce different annotations; a hard-coded or empty-document hash would make
// them equal.
func TestExternalChecksumIsNotConstant(t *testing.T) {
	ch := chartForTest(t)

	first, err := renderChartManifests(t.Context(), ch, externalValues("1111111111"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderChartManifests(t.Context(), ch, externalValues("2222222222"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "checksum/instance-secret: 1111111111") ||
		!strings.Contains(second, "checksum/instance-secret: 2222222222") {
		t.Fatal("the annotation does not track the declared checksum")
	}
}

// OBLIGATION: the root-key guard turns a CrashLoopBackOff into an apply-time
// refusal, and it used to live INSIDE the Secret template — the one thing an
// external config source stops rendering. It is invoked from the unconditional
// gate now; this pins that the move did not drop it on the path where it still
// works.
func TestRootKeyGuardStillFiresOnTheInlinePath(t *testing.T) {
	ch := chartForTest(t)

	_, err := renderChartManifests(t.Context(), ch, map[string]interface{}{
		"instance": map[string]interface{}{"id": "dctest"},
	})
	if err == nil {
		t.Fatal("a deployment with a secret-store area and no root key rendered cleanly: the " +
			"operator would learn about it as a crash-loop")
	}
	if !strings.Contains(err.Error(), "rootKey is required") {
		t.Errorf("the render failed for some other reason, so this proves nothing: %v", err)
	}

	if _, err := renderChartManifests(t.Context(), ch, inlineValues()); err != nil {
		t.Errorf("a root key was supplied and the render still failed: %v", err)
	}
}

// OBLIGATION: the hand-set shutdown refusal must survive the checksum change.
//
// It lived inside devicechain.instanceConfig, which the pod annotation called
// unconditionally — so it ran even under an external Secret, by accident. Making
// the annotation stop hashing an unrendered document removes that accident, and
// would have taken the refusal with it. The two numbers it protects are one
// budget checked against itself: a service refuses to start if its drain window
// does not fit inside the pod's grace period.
func TestHandSetShutdownIsRefusedOnBothConfigSources(t *testing.T) {
	ch := chartForTest(t)

	for _, tc := range []struct {
		name string
		vals map[string]interface{}
	}{
		{"external Secret", externalValues("deadbeef")},
		{"inline config", inlineValues()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := tc.vals["instance"].(map[string]interface{})
			inline, ok := cfg["config"].(map[string]interface{})
			if !ok {
				inline = map[string]interface{}{}
				cfg["config"] = inline
			}
			infra, ok := inline["infrastructure"].(map[string]interface{})
			if !ok {
				infra = map[string]interface{}{}
				inline["infrastructure"] = infra
			}
			infra["shutdown"] = map[string]interface{}{"drainSeconds": 9}

			_, err := renderChartManifests(t.Context(), ch, tc.vals)
			if err == nil {
				t.Fatal("a hand-set shutdown block was accepted: the document's drain window " +
					"and the pod's grace period would come from two places")
			}
			if !strings.Contains(err.Error(), "set by the chart, not by hand") {
				t.Errorf("the render failed for some other reason: %v", err)
			}
		})
	}
}

// OBLIGATION: the bootstrap pre-flight must not read "no document" as "valid".
//
// It runs the services' own strict loader over the bytes the chart is about to
// hand them, which is the only thing that turns a rejected config into a sentence
// instead of a readiness timeout. Under an external Secret the chart renders
// nothing, and the pre-flight returned nil — answering "valid" for every document
// including the ones it exists to catch, on the exact path where the chart's own
// guards are off too.
func TestPreflightRefusesADeployNobodyAuthoredAConfigFor(t *testing.T) {
	ch := chartForTest(t)
	external := externalValues("deadbeef")

	err := validateRenderedInstanceConfig(t.Context(), ch, external, nil)
	if err == nil {
		t.Fatal("the pre-flight passed a deploy whose instance config it never saw")
	}
	if !strings.Contains(err.Error(), "no instance configuration document") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}

	// An authored document goes through the SAME strict loader — the whole point of
	// the parameter. A bad one must be refused with the loader's own sentence...
	bad := []byte(`{"infrastructure":{"thisIsNotAField":true}}`)
	err = validateRenderedInstanceConfig(t.Context(), ch, external, bad)
	if err == nil {
		t.Fatal("an authored document with an unknown key passed the strict load")
	}
	if !strings.Contains(err.Error(), "refuse to start") {
		t.Errorf("the refusal did not come from the services' own loader: %v", err)
	}

	// ...and a good one must pass, or the parameter bans the path it enables.
	good, err := renderInstanceConfigDocument(t.Context(), ch, inlineValues())
	if err != nil {
		t.Fatal(err)
	}
	if good == nil {
		t.Fatal("the inline path rendered no document, so this test has no known-good bytes")
	}
	if err := validateRenderedInstanceConfig(t.Context(), ch, external, good); err != nil {
		t.Errorf("a valid authored document was rejected: %v", err)
	}
}

// Two documents for one deploy is a contradiction, not a preference: the pods
// mount exactly one. Resolving it by precedence is how the two drift apart.
func TestPreflightRefusesTwoConfigSources(t *testing.T) {
	ch := chartForTest(t)
	err := validateRenderedInstanceConfig(t.Context(), ch, inlineValues(), []byte(`{}`))
	if err == nil {
		t.Fatal("a deploy carrying both a chart-rendered Secret and an authored document was accepted")
	}
	if !strings.Contains(err.Error(), "two instance configuration documents") {
		t.Errorf("the refusal names something else: %v", err)
	}
}
