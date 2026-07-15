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
	"github.com/devicechain-io/dc-microservice/natsauth"
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

	// Thread the broker-auth material into the instance config (ADR-025): the CA +
	// TLS flag, plus the shared service credential every service presents and the
	// callout issuer seed device-management signs with. Built as one nats map and
	// deep-merged over the chart's instance.config defaults (hostname/port/
	// persistence are preserved).
	natsVals := map[string]interface{}{}
	if st.Values["natsTlsEnabled"] == "true" {
		natsVals["tls"] = map[string]interface{}{
			"enabled": true,
			"ca":      st.Values["natsCA"],
		}
	}
	if seed := st.Values["natsCalloutIssuerSeed"]; seed != "" {
		natsVals["auth"] = map[string]interface{}{
			"user":              natsauth.ServiceUser,
			"password":          st.Values["natsServicePassword"],
			"calloutIssuerSeed": seed,
		}
	}
	infraVals := map[string]interface{}{}
	if len(natsVals) > 0 {
		infraVals["nats"] = natsVals
	}
	// The shared service secret (ADR-044 amendment) backing the sync cross-service
	// call primitive, threaded into every service's instance config.
	if secret := st.Values["serviceAuthSecret"]; secret != "" {
		infraVals["serviceAuth"] = map[string]interface{}{"secret": secret}
	}
	// The instance secret-store root key (ADR-059): the base64 256-bit KEK that
	// wraps every per-secret DEK, threaded into every service's instance config so
	// each seals with the same instance KEK.
	if rootKey := st.Values["secretsRootKey"]; rootKey != "" {
		infraVals["secrets"] = map[string]interface{}{"rootKey": rootKey}
	}
	instanceVals := map[string]interface{}{"id": st.Instance}
	if len(infraVals) > 0 {
		instanceVals["config"] = map[string]interface{}{"infrastructure": infraVals}
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
		// Metrics rendering (ServiceMonitors / PrometheusRule / dashboards) needs the
		// Prometheus Operator CRDs. The infra step installs kube-prometheus-stack by
		// default (BEFORE this Helm step), so enable it — UNLESS --no-monitoring, where
		// we install no operator and must not render CRs against absent CRDs.
		"metrics": map[string]interface{}{"enabled": !st.NoMonitoring},
	}

	// Grafana SSO (ADR-047): turn on user-management's OAuth AS (the issuer) and seed
	// the confidential Grafana client. The bcrypt hash is the SAME secret whose
	// cleartext went to Grafana's config in the tofu step (one mint, both sides). The
	// redirect URI matches the /grafana ingress path. Deep-merges into the chart's
	// functionalAreas.user-management.config, preserving the other areas' config.
	if grafanaSSOEnabled(st) {
		u := grafanaSSOURLsFor(st)
		vals["functionalAreas"] = map[string]interface{}{
			"user-management": map[string]interface{}{
				"config": map[string]interface{}{
					"auth": map[string]interface{}{
						"issuerUrl": u.Issuer,
						"seedClients": []map[string]interface{}{{
							"clientId":     "grafana",
							"redirectUris": []string{u.Redirect},
							"scopes":       []string{"read-only"},
							"secretHash":   st.Values["grafanaOAuthSecretBcrypt"],
						}},
					},
				},
			},
		}
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
