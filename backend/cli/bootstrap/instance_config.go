// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"slices"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// renderedInstanceConfigSecretName is the name the chart gives the instance
// config Secret it renders itself: devicechain.instanceConfigSecret with
// instance.existingSecret empty. Read from the supplied values, falling back to
// the chart's own default for instance.id — the same two-layer merge Helm does
// for this one key, and the reason this is not a hard-coded literal.
func renderedInstanceConfigSecretName(ch *chart.Chart, vals map[string]interface{}) string {
	id := ""
	for _, src := range []map[string]interface{}{vals, ch.Values} {
		inst, ok := src["instance"].(map[string]interface{})
		if !ok {
			continue
		}
		if v, ok := inst["id"].(string); ok && v != "" {
			id = v
			break
		}
	}
	return fmt.Sprintf("dci-%s-config", id)
}

// renderInstanceConfigDocument renders the chart client-side — no cluster, no release —
// and returns the raw instance configuration document out of the Secret it produces.
//
// That Secret is mounted at /etc/dci-config/instance on every pod and is the
// literal document each service loads at startup, so what comes back here is byte for
// byte what the services are about to read. The render is a pure function of
// the chart and the values, so doing it twice (once here, once for real) cannot
// disagree with itself.
//
// It returns the BYTES rather than a decoded struct because its two callers want
// different things from them: the pre-flight below runs the services' own strict loader
// over them, and the render tests decode them permissively to observe what the chart
// actually delivered. A nil document means the chart rendered no Secret of its own.
// renderChartClientSide renders the chart with no cluster and no release, and
// returns the whole manifest stream.
//
// One definition, because two callers that must render the SAME chart state have
// no way to notice when they drift: a difference in APIVersions alone would make
// one see the metrics templates and the other not, silently, and each would still
// look correct on its own.
//
// ClientOnly renders without contacting a cluster. APIVersions is ADDITIVE to the
// default capability set, so declaring the Prometheus Operator's group lets the
// metrics templates render here regardless of what the target cluster has — which
// is right, because these checks are about the instance config, not about which
// CRDs exist.
func renderChartClientSide(ctx context.Context, ch *chart.Chart, vals map[string]interface{}) (string, error) {
	inst := action.NewInstall(&action.Configuration{})
	// 🔑 A PLACEHOLDER, AND SAFE ONLY BECAUSE THE CHART READS NOTHING FROM IT. No
	// template in deploy/helm/devicechain references .Release.Name or .Release.Namespace
	// — every resource name, label, selector and namespace is derived from
	// .Values.instance.id — so what is rendered here is identical to what the real
	// install renders under the instance's own release name. If a template ever DOES
	// reach for the release, this render stops matching the one that is installed, and
	// validateRenderedInstanceConfig's whole claim (that it checks the bytes about to be
	// written) goes with it. Hence a name that reads as a placeholder rather than one
	// that reads as the truth.
	inst.ReleaseName = "render"
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = []string{"monitoring.coreos.com/v1"}

	rel, err := inst.RunWithContext(ctx, ch, vals)
	if err != nil {
		return "", fmt.Errorf("rendering chart: %w", err)
	}
	return rel.Manifest, nil
}

// renderedFunctionalAreas reports which areas the chart actually deployed, read
// off the Deployments it rendered rather than re-derived from the profile and
// --enable-area flags. Re-deriving would be a second implementation of
// devicechain.enabledAreas, and the whole value of asking is that it answers for
// THIS render.
func renderedFunctionalAreas(manifest string) []string {
	seen := map[string]bool{}
	var out []string
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj.Kind != "Deployment" {
			continue
		}
		area := obj.Metadata.Labels["devicechain.io/functional-area"]
		if area != "" && !seen[area] {
			seen[area] = true
			out = append(out, area)
		}
	}
	return out
}

func renderInstanceConfigDocument(ctx context.Context, ch *chart.Chart, vals map[string]interface{}) ([]byte, error) {
	manifest, err := renderChartClientSide(ctx, ch, vals)
	if err != nil {
		return nil, err
	}
	return instanceConfigFromManifest(ch, vals, manifest)
}

// instanceConfigFromManifest picks the instance config document out of an
// already-rendered stream.
func instanceConfigFromManifest(ch *chart.Chart, vals map[string]interface{}, manifest string) ([]byte, error) {

	// 🔴 MATCHED BY NAME, NOT BY THE SHAPE OF ITS CONTENT. The first version of
	// this took the first Secret in the stream carrying a `stringData.instance`
	// key, and the chart has a template — extraSecrets — that renders operator-
	// supplied Secrets with arbitrary key names. One entry keyed `instance` is
	// therefore indistinguishable from the instance config, and SplitManifests
	// returns a MAP, so which of the two came back varied between runs of the same
	// values: measured at 11 of 20 renders picking the impostor. That is the
	// pre-flight validating a document nobody mounts, nondeterministically, and
	// the strict loader would report on the wrong bytes without saying so.
	//
	// The name is the chart's own (devicechain.instanceConfigSecret), and under
	// instance.existingSecret the chart renders no Secret under that name at all —
	// which is exactly the answer this function must give there.
	want := renderedInstanceConfigSecretName(ch, vals)
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			StringData map[string]string `json:"stringData"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			continue
		}
		if obj.Kind != "Secret" || obj.Metadata.Name != want {
			continue
		}
		raw, ok := obj.StringData["instance"]
		if !ok {
			// The right Secret with no `instance` key is a chart defect, not an
			// absent document: saying "nothing authored one" here would send the
			// caller down the external-Secret branch for an inline deploy.
			return nil, fmt.Errorf("the chart rendered Secret %q without an `instance` key, "+
				"so no service would find its configuration", want)
		}
		return []byte(raw), nil
	}
	// The chart deliberately renders no Secret when instance.existingSecret points
	// at one supplied out of band. Whether that is fine is not this function's
	// call — see validateRenderedInstanceConfig, which is the one that has to know
	// whether anybody authored a document at all.
	return nil, nil
}

// validateRenderedInstanceConfig runs the services' own startup load against the config
// the chart is about to hand them.
//
// The services already fail closed on a bad instance config — that is the point of
// config.Validate. What they cannot do is fail closed EARLY: by the time a pod
// reads the config it is a workload the installer is waiting on, so a rejected
// config reaches the operator as a readiness timeout rather than as the specific
// sentence config.Validate wrote. Running the same check here, against the same
// bytes, keeps one definition of validity and moves the report to where the
// operator can act on it.
//
// It goes through core.LoadConfiguration — the whole loader, not just Validate — for
// exactly that reason. The load decodes strictly, so a key that is not a field is
// refused; and since the services refuse it too, doing anything less here would let an
// install proceed to a whole instance of pods that cannot start, which is the failure
// this pre-flight exists to convert into a sentence. The loader also applies defaults
// before validating, as the services do, so an omitted key is judged the way it will
// actually be judged and not as a zero.
// 🔴 authored IS THE SECOND CONFIG SOURCE, AND IT IS A PARAMETER SO THAT "NOBODY
// AUTHORED ONE" CANNOT PASS AS "NOTHING TO CHECK".
//
// The chart renders no Secret when instance.existingSecret names one supplied out
// of band, and this function used to read that as success and return nil. Nothing
// sets that value today, so the branch was unreachable — which is the only reason
// it was harmless. The moment dcctl writes the Secret itself, an unreachable
// "return nil" becomes a live one: the pre-flight would answer "valid" for every
// document, including the ones it exists to catch, and it would do so on the
// exact path where the chart's own guards are also switched off.
//
// So the check is now about SOURCES rather than about bytes. Exactly one side
// must have authored the document; whichever did, the same strict load runs over
// it. Passing bytes here when the chart also rendered a Secret is a contradiction
// — two documents, and the pods mount only one — and is refused rather than
// resolved by precedence, because a silent precedence rule is how the two drift.
func validateRenderedInstanceConfig(ctx context.Context, ch *chart.Chart, vals map[string]interface{}, authored []byte) error {
	manifest, err := renderChartClientSide(ctx, ch, vals)
	if err != nil {
		return err
	}
	rendered, err := instanceConfigFromManifest(ch, vals, manifest)
	if err != nil {
		return err
	}

	var raw []byte
	switch {
	case rendered != nil && authored != nil:
		return fmt.Errorf("two instance configuration documents were produced for one deploy: " +
			"the chart rendered its own Secret AND a document was supplied for instance.existingSecret. " +
			"The pods mount exactly one, so this is not a preference to resolve")
	case rendered != nil:
		raw = rendered
	case authored != nil:
		raw = authored
	default:
		return fmt.Errorf("no instance configuration document was produced for this deploy: " +
			"the chart rendered no Secret (instance.existingSecret is set) and nothing was supplied " +
			"in its place, so the services would mount a Secret this run never validated")
	}

	loaded, err := loadInstanceConfigDocument(raw)
	if err != nil {
		return err
	}
	if authored != nil {
		return checkAuthoredTransforms(ch, vals, manifest, loaded)
	}
	return nil
}

// loadInstanceConfigDocument runs the services' own startup load over a document and
// says so in the verdict.
//
// 🔴 ONE SENTENCE FOR ONE VERDICT. Two callers now strict-load the same bytes — the
// pre-flight above, and the composition that reads the coordinates the chart has to
// be told — and a second phrasing of "the services will not start on this" is a
// second thing an operator has to recognise as the same problem. The pre-flight's
// wording is the one that has been read in anger, so it is the one that stays.
func loadInstanceConfigDocument(raw []byte) (*config.InstanceConfiguration, error) {
	loaded := &config.InstanceConfiguration{}
	if err := core.LoadConfiguration(raw, loaded); err != nil {
		return nil, fmt.Errorf("the instance configuration this deploy would render is one "+
			"the services will refuse to start on: %w", err)
	}
	return loaded, nil
}

// 🔴 THE TWO THINGS THE CHART DOES TO THE DOCUMENT THAT VALIDITY CANNOT SEE.
//
// devicechain.instanceConfig is not a passthrough. Before writing the document it
// injects the shutdown budget from the top-level values, and it unsets the
// ai-inference coordinates when that area is not deployed. Both are the chart
// acting as AUTHOR, so under instance.existingSecret neither happens — the
// supplier's document is mounted as written.
//
// Neither omission is invalid. core.LoadConfiguration accepts a document with a
// mismatched grace period and one naming an undeployed inference service, which
// is exactly why the strict load alone is not enough here, and why an earlier
// version of this file that stopped at the strict load was closing half a hole.
//
// What each costs:
//
//   - The shutdown budget is ONE budget checked against itself. A service refuses
//     to start unless its drain window leaves room, inside the pod's grace period,
//     for the teardown that closes in-flight requests, broker consumers and the
//     database pool. The pod's grace period comes from the chart values; the
//     document's copy comes from the supplier. The service validates the document
//     against ITSELF, so two consistent-looking numbers that disagree with the pod
//     spec pass every check and surface as a SIGKILL mid-drain.
//   - The ai-inference coordinates were filtered because values.yaml ships a
//     hostname non-empty by default. A service that sees one builds its rule
//     drafter, fails at DNS, and reports that the tenant has not enabled external
//     AI routing — blaming consent for a service the operator never deployed.
//
// Enabled areas are read off the rendered Deployments rather than re-derived from
// the profile, so this answers for the render in hand.
func checkAuthoredTransforms(ch *chart.Chart, vals map[string]interface{}, manifest string, cfg *config.InstanceConfiguration) error {
	podGrace := chartIntValue(ch, vals, "terminationGracePeriodSeconds")
	if podGrace > 0 && cfg.Infrastructure.Shutdown.TerminationGracePeriodSeconds != podGrace {
		return fmt.Errorf("the supplied instance configuration says the pod's termination grace "+
			"period is %ds and the chart gives the pods %ds. The drain window is validated against "+
			"the document's own copy, so both numbers look consistent to the services and the pods "+
			"are still SIGKILLed mid-drain. Write infrastructure.shutdown.terminationGracePeriodSeconds "+
			"to match terminationGracePeriodSeconds, as the chart does when it authors the document",
			cfg.Infrastructure.Shutdown.TerminationGracePeriodSeconds, podGrace)
	}

	areas := renderedFunctionalAreas(manifest)
	if len(areas) == 0 {
		return fmt.Errorf("this deploy renders no functional-area Deployments, so the supplied " +
			"instance configuration cannot be checked against what it actually runs")
	}
	if !slices.Contains(areas, "ai-inference") && cfg.Infrastructure.AiInference.Hostname != "" {
		return fmt.Errorf("the supplied instance configuration names an ai-inference host (%q) "+
			"and this deploy does not run that area. Services that see a hostname build their "+
			"natural-language rule drafter and then report that the TENANT has not enabled external "+
			"AI routing — blaming consent for a service nobody deployed. Remove the "+
			"infrastructure.aiInference block, as the chart does when it authors the document",
			cfg.Infrastructure.AiInference.Hostname)
	}
	return nil
}

// chartIntValue reads one top-level integer value the way Helm would: the
// supplied values first, the chart's own defaults behind them. Returns 0 when
// neither carries it, which every caller reads as "do not judge".
func chartIntValue(ch *chart.Chart, vals map[string]interface{}, key string) int {
	for _, src := range []map[string]interface{}{vals, ch.Values} {
		switch v := src[key].(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		}
	}
	return 0
}
