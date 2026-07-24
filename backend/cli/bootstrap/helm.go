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

	vals := helmValues(st)

	// Check the instance config the chart is about to render BEFORE handing it to
	// the cluster. Everything below waits on workload readiness, so a config a
	// service refuses to load does not surface as a config error at all — it
	// surfaces as a ten-minute wait that ends in a generic timeout, with the actual
	// reason only in the logs of pods that are already gone. Rendering costs
	// milliseconds and turns that into a sentence.
	if err := validateRenderedInstanceConfig(ctx, ch, vals); err != nil {
		return err
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

// helmValues builds the value map the chart is installed with.
//
// Split out of helmInstall so it can be rendered and inspected without a cluster:
// the sizing this map carries is checked against the volume the infra step
// provisions (TestCompactReservationFitsItsSmallerVolume), and that check is only
// worth anything if it reads the map the install actually uses rather than a
// restatement of it.
func helmValues(st *State) map[string]interface{} {
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
	// The compact preset's JetStream/KV ceilings (compactSizing). Merged into the
	// SAME nats map as the broker-auth material above rather than assigned over it:
	// assigning would drop whichever block was written first, and the loser would be
	// either the ceilings (a silently full-size instance) or the credentials (a
	// service that cannot reach the broker at all).
	if st.Compact {
		for k, v := range compact.natsValues() {
			natsVals[k] = v
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

	// Deployment selection: normally the named profile (or "" → the chart's default).
	// When --enable-area added extra areas, ResolveEnabledAreas already expanded
	// profile ∪ extras into one validated explicit set; emit THAT as
	// enabledFunctionalAreas and NOT profile, because the chart rejects both being set
	// at once (_helpers.tpl). The expansion preserves the profile's areas, so this is
	// purely additive.
	if len(st.EnabledAreas) > 0 {
		// The chart's values schema validates enabledFunctionalAreas as a JSON array;
		// a Go []string is not the jsonType its validator accepts, so hand it
		// []interface{} of strings (the shape a YAML/JSON values file would produce).
		areas := make([]interface{}, len(st.EnabledAreas))
		for i, a := range st.EnabledAreas {
			areas[i] = a
		}
		vals["enabledFunctionalAreas"] = areas
	} else {
		vals["profile"] = st.Profile
	}

	// Compact lowers the SCHEDULING requests so the pods fit a small node. It does
	// not touch limits — see compactSizing.CPURequest.
	if st.Compact {
		vals["resources"] = compact.resourceValues()
	}

	// Grafana SSO (ADR-047): turn on user-management's OAuth AS (the issuer) and seed
	// the confidential Grafana client. The bcrypt hash is the SAME secret whose
	// cleartext went to Grafana's config in the tofu step (one mint, both sides). The
	// redirect URI matches the /grafana ingress path. Deep-merges into the chart's
	// functionalAreas.user-management.config, preserving the other areas' config.
	if grafanaSSOEnabled(st) {
		u := grafanaSSOURLsFor(st)
		mergeFunctionalArea(vals, "user-management", map[string]interface{}{
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
		})
	}

	// LwM2M PSK provisioning (--lwm2m-identities): render the device PSKs into a
	// chart-owned Secret (extraSecrets) and bind each into lwm2m-ingest's config
	// (security.identities[]) + an extraEnv secretKeyRef that projects it. The area is
	// turned on separately via EnabledAreas (the flag implies --enable-area
	// lwm2m-ingest); this only supplies its config, merged so it coexists with any
	// other functionalAreas block (e.g. Grafana SSO) rather than overwriting it.
	if len(st.Lwm2mIdentities) > 0 {
		secret, areaConfig := lwm2mProvisioning(st.Instance, st.Lwm2mIdentities)
		// Append rather than assign, so a future second writer of extraSecrets doesn't
		// silently drop this one (the same clobber class mergeFunctionalArea guards).
		existing, _ := vals["extraSecrets"].([]interface{})
		vals["extraSecrets"] = append(existing, secret)
		mergeFunctionalArea(vals, "lwm2m-ingest", areaConfig)
	}

	return vals
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
