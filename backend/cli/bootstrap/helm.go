// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"time"

	assets "github.com/devicechain-io/dc-deploy"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// helmReleaseName is the per-instance chart release name.
const helmReleaseName = "dc"

// helmTimeout bounds how long an install/upgrade waits for the rendered
// workloads to become ready.
const helmTimeout = 10 * time.Minute

// helmInstall installs (or upgrades) the embedded per-instance chart via the
// Helm Go SDK, blocking until the rendered workloads are ready. The chart ships
// inside the binary, so no chart repo, no `helm` CLI and no source are needed.
func helmInstall(ctx context.Context, st *State) error {
	ch, err := loadEmbeddedChart()
	if err != nil {
		return fmt.Errorf("loading embedded chart: %w", err)
	}

	// The release record lives in the "default" namespace (matching the manual
	// recipe); the chart templates place workloads in dc-<instance> themselves.
	const releaseNamespace = "default"

	settings := cli.New()
	settings.KubeContext = st.KubeContext
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), releaseNamespace, "secret",
		func(string, ...interface{}) {}); err != nil {
		return err
	}

	// When the broker terminates TLS (ADR-025), merge the CA + enable flag into the
	// instance config so services dial NATS/MQTT over TLS. Deep-merges over the
	// chart's instance.config defaults (hostname/port/persistence are preserved).
	instanceVals := map[string]interface{}{"id": st.Instance}
	if st.Values["natsTlsEnabled"] == "true" {
		instanceVals["config"] = map[string]interface{}{
			"infrastructure": map[string]interface{}{
				"nats": map[string]interface{}{
					"tls": map[string]interface{}{
						"enabled": true,
						"ca":      st.Values["natsCA"],
					},
				},
			},
		}
	}

	vals := map[string]interface{}{
		"instance": instanceVals,
		"profile":  st.Profile,
		// Set the host explicitly (matching the chart default) so the deployed
		// ingress and the access report agree on one value. --no-tls turns off the
		// self-signed cert for a plain-HTTP (zero-warning) local URL.
		"ingress": map[string]interface{}{
			"enabled": true,
			"host":    st.Values["ingressHost"],
			"tls":     map[string]interface{}{"enabled": !st.NoTLS},
		},
		"image": map[string]interface{}{"registry": st.ImageRegistry, "tag": st.ImageVersion},
		// A bare cluster has no Prometheus Operator, so the ServiceMonitors the
		// metrics path renders would fail to apply.
		"metrics": map[string]interface{}{"enabled": false},
	}

	// Upgrade if the release already exists, otherwise install — so a re-run is
	// idempotent.
	hist := action.NewHistory(actionConfig)
	hist.Max = 1
	if _, err := hist.Run(helmReleaseName); err == driver.ErrReleaseNotFound {
		inst := action.NewInstall(actionConfig)
		inst.ReleaseName = helmReleaseName
		inst.Namespace = releaseNamespace
		inst.Wait = true
		inst.Timeout = helmTimeout
		_, err := inst.RunWithContext(ctx, ch, vals)
		return err
	} else if err != nil {
		return err
	}

	upg := action.NewUpgrade(actionConfig)
	upg.Namespace = releaseNamespace
	upg.Wait = true
	upg.Timeout = helmTimeout
	_, err = upg.RunWithContext(ctx, helmReleaseName, ch, vals)
	return err
}

// helmUninstall removes the per-instance chart release, deleting every resource
// the chart created (workloads, services, ingress, and the instance namespace).
// A missing release is treated as success so destroy is idempotent.
func helmUninstall(ctx context.Context, kubeContext string) error {
	const releaseNamespace = "default"

	settings := cli.New()
	settings.KubeContext = kubeContext
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), releaseNamespace, "secret",
		func(string, ...interface{}) {}); err != nil {
		return err
	}

	un := action.NewUninstall(actionConfig)
	un.Wait = true
	un.Timeout = helmTimeout
	_, err := un.Run(helmReleaseName)
	if err != nil && strings.Contains(err.Error(), "not found") {
		return nil
	}
	return err
}

// loadEmbeddedChart materializes the embedded chart files into an in-memory
// Helm chart (no disk extraction needed — the Helm loader accepts buffered
// files keyed by their chart-relative path).
func loadEmbeddedChart() (*chart.Chart, error) {
	src := assets.HelmChart()
	var files []*loader.BufferedFile
	err := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, rerr := fs.ReadFile(src, path)
		if rerr != nil {
			return rerr
		}
		files = append(files, &loader.BufferedFile{Name: path, Data: b})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return loader.LoadFiles(files)
}
