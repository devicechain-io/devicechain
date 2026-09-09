// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

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
func renderInstanceConfigDocument(ctx context.Context, ch *chart.Chart, vals map[string]interface{}) ([]byte, error) {
	// ClientOnly renders without contacting a cluster. APIVersions is ADDITIVE to
	// the default capability set, so declaring the Prometheus Operator's group lets
	// the metrics templates render here regardless of what the target cluster has —
	// which is right, because this check is about the instance config, not about
	// which CRDs exist.
	inst := action.NewInstall(&action.Configuration{})
	inst.ReleaseName = helmReleaseName
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = []string{"monitoring.coreos.com/v1"}

	rel, err := inst.RunWithContext(ctx, ch, vals)
	if err != nil {
		return nil, fmt.Errorf("rendering chart: %w", err)
	}

	for _, doc := range releaseutil.SplitManifests(rel.Manifest) {
		var obj struct {
			Kind       string            `json:"kind"`
			StringData map[string]string `json:"stringData"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			continue
		}
		if obj.Kind != "Secret" {
			continue
		}
		raw, ok := obj.StringData["instance"]
		if !ok {
			continue
		}
		return []byte(raw), nil
	}
	// The chart deliberately renders no Secret when instance.existingSecret points
	// at one the operator manages. There is nothing to check in that case, and
	// nothing wrong either.
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
func validateRenderedInstanceConfig(ctx context.Context, ch *chart.Chart, vals map[string]interface{}) error {
	raw, err := renderInstanceConfigDocument(ctx, ch, vals)
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	if err := core.LoadConfiguration(raw, &config.InstanceConfiguration{}); err != nil {
		return fmt.Errorf("the instance configuration this deploy would render is one "+
			"the services will refuse to start on: %w", err)
	}
	return nil
}
