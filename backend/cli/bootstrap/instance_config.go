// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// renderInstanceConfigFromChart renders the chart client-side — no cluster, no
// release — and decodes the instance configuration out of the Secret it produces.
//
// That Secret is mounted at /etc/dci-config/instance on every pod and is the
// literal document each service loads at startup, so decoding it here reads
// exactly what the services are about to read. The render is a pure function of
// the chart and the values, so doing it twice (once here, once for real) cannot
// disagree with itself.
func renderInstanceConfigFromChart(ctx context.Context, ch *chart.Chart, vals map[string]interface{}) (*config.InstanceConfiguration, error) {
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
		cfg := &config.InstanceConfiguration{}
		if err := json.Unmarshal([]byte(raw), cfg); err != nil {
			return nil, fmt.Errorf("decoding rendered instance config: %w", err)
		}
		return cfg, nil
	}
	// The chart deliberately renders no Secret when instance.existingSecret points
	// at one the operator manages. There is nothing to check in that case, and
	// nothing wrong either.
	return nil, nil
}

// validateRenderedInstanceConfig runs the services' own startup validation against
// the config the chart is about to hand them.
//
// The services already fail closed on a bad instance config — that is the point of
// config.Validate. What they cannot do is fail closed EARLY: by the time a pod
// reads the config it is a workload the installer is waiting on, so a rejected
// config reaches the operator as a readiness timeout rather than as the specific
// sentence config.Validate wrote. Running the same check here, against the same
// bytes, keeps one definition of validity and moves the report to where the
// operator can act on it.
//
// It deliberately calls ApplyDefaults first, exactly as the services do
// (core.LoadInstanceConfiguration), so that an omitted key is judged the way it
// will actually be judged and not as a zero.
func validateRenderedInstanceConfig(ctx context.Context, ch *chart.Chart, vals map[string]interface{}) error {
	cfg, err := renderInstanceConfigFromChart(ctx, ch, vals)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("the instance configuration this deploy would render is one "+
			"the services will refuse to start on: %w", err)
	}
	return nil
}
