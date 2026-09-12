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

// helmReleaseName and helmReleaseNamespace address the one DeviceChain chart release
// a cluster holds.
//
// 🔴 NEITHER CARRIES THE INSTANCE, AND THE NAME USED TO CLAIM OTHERWISE. This constant
// was commented "the per-instance chart release name", which it is not: it is a package
// constant, installed into a constant namespace, so a cluster has exactly one
// DeviceChain release no matter how many instances are declared. Everything that has to
// tell one instance's objects from another's must therefore do so by some OTHER means —
// see releaseInstance, which reads the id back out of the release's own values.
//
// They are hoisted here because install and uninstall MUST agree on both, and they used
// to declare the namespace separately, one local const each. Two literals that must be
// equal and are written down twice are the same shape of defect as a name that does not
// carry what it claims to.
const (
	helmReleaseName      = "dc"
	helmReleaseNamespace = "default"
)

// helmTimeout bounds how long an install/upgrade waits for the rendered
// workloads to become ready.
const helmTimeout = 10 * time.Minute

// helmMaxHistory bounds the release revisions Helm keeps for this instance.
//
// 🔴 THE SDK DEFAULT IS UNLIMITED, WHICH IS NOT THE `helm` COMMAND'S DEFAULT. The
// CLI passes --history-max 10; a program driving action.Upgrade gets 0, and 0 means
// keep everything (storage.go: the prune is guarded by `MaxHistory > 0`). So every
// bootstrap this command has ever run is still recorded, and each revision holds the
// values it was rendered with.
//
// That matters because those values have carried credentials. Rotating one does not
// retract it: the previous revision still holds the previous value, readable by
// exactly whoever could read the new one. Bounding the history does not make that
// property go away — only dcctl owning the Secrets does, so the chart renders a NAME
// instead of a plaintext — but it stops the record growing without limit in the
// meantime, and it prunes what is already there. Helm's removeLeastRecent trims down
// to the bound on the next write rather than only capping growth from now on, so one
// upgrade collapses a long history.
//
// Ten matches the `helm` command, so `helm history dc` shows an operator what they
// would expect from a chart installed any other way. Nothing in dcctl reads the
// history for anything but an existence check (hist.Max = 1, below), so the number
// is chosen for that familiarity rather than for a rollback depth we rely on.
const helmMaxHistory = 10

// helmInstall installs (or upgrades) the embedded per-instance chart via the
// Helm Go SDK, blocking until the rendered workloads are ready. The chart ships
// inside the binary, so no chart repo, no `helm` CLI and no source are needed.
func helmInstall(ctx context.Context, st *State) error {
	ch, err := loadEmbeddedChart()
	if err != nil {
		return fmt.Errorf("loading embedded chart: %w", err)
	}

	// The release record lives in helmReleaseNamespace (matching the manual recipe);
	// the chart templates place workloads in the instance's own namespace themselves.
	releaseNamespace := helmReleaseNamespace

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
	if err := validateRenderedInstanceConfig(ctx, ch, vals, nil); err != nil {
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

	_, err = newHelmUpgrade(actionConfig, releaseNamespace).RunWithContext(ctx, helmReleaseName, ch, vals)
	return err
}

// newHelmUpgrade builds the upgrade action every re-run of the chart goes through.
//
// Separated from helmInstall so the history bound has somewhere to be checked. The
// bound is one assignment, and a silently missing one assignment is precisely the
// failure it guards against: MaxHistory's zero value is not "a small default", it is
// "keep every revision forever".
func newHelmUpgrade(cfg *action.Configuration, namespace string) *action.Upgrade {
	upg := action.NewUpgrade(cfg)
	upg.Namespace = namespace
	upg.Wait = true
	upg.Timeout = helmTimeout
	upg.MaxHistory = helmMaxHistory
	return upg
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
		auth := map[string]interface{}{
			"user":              natsauth.ServiceUser,
			"password":          st.Values["natsServicePassword"],
			"calloutIssuerSeed": seed,
		}
		// The system-account login, written only when one was minted. Its ABSENCE is
		// the off switch for the broker-presence tap (see NatsAuthConfiguration), so
		// writing an empty user here would be writing a credential that cannot
		// authenticate rather than leaving the feature off.
		if sysPw := st.Values["natsSysPassword"]; sysPw != "" {
			auth["sysUser"] = natsauth.SysUser
			auth["sysPassword"] = sysPw
		}
		natsVals["auth"] = auth
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
	// The Helm half of the HA topology, merged for the same reason. Unconditional,
	// like its OpenTofu counterpart in infraVars: rendering streamReplicas even at 1
	// keeps both halves sourced from the one haTopology on every path, so they
	// cannot drift when HA is turned back OFF either. See haTopology.
	for k, v := range haFor(st.HA).natsValues() {
		natsVals[k] = v
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
		//
		// databaseBackups gates the WAL-archiving alerts (ADR-028, ADR-020 A2.5) and
		// comes from what the infrastructure REPORTED, not from a dcctl flag: with
		// archiving off the cnpg_pg_stat_archiver_* series do not exist, so rendering
		// those rules anyway yields four alerts that load, evaluate nothing, and never
		// fire. databaseNamespace is where the database Clusters run — deliberately
		// NOT instance.id, because an alert scoped to the instance's own namespace
		// selects none of those series at all.
		//
		// cnpgNamespace is a THIRD namespace and not a typo for either of the
		// above: dc-system holds the database Clusters, the instance namespace
		// holds this release, and cnpg-system holds the OPERATOR that drives the
		// Clusters. It gates the operator's PodMonitor and the control-plane
		// alerting rules as one unit (ADR-020 A1.5). Absent means OFF here, with
		// no fallback to the chart's default — the same asymmetry the backup
		// alerts use, and for the same reason: rules that cannot fire look like a
		// monitored instance and are not, while no rules at all is visibly
		// nothing. An install whose outputs could not be read should get the
		// visible kind of gap.
		"metrics": map[string]interface{}{
			"enabled":           !st.NoMonitoring,
			"databaseBackups":   st.Values[databaseBackupsKey] == "true",
			"databaseNamespace": databaseNamespaceFor(st),
			"cnpgNamespace":     st.Values[cnpgNamespaceKey],
		},
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

// helmUninstall removes the named instance's chart release, deleting every resource
// the chart created (workloads, services, ingress, and the instance namespace).
// A missing release is treated as success so destroy is idempotent.
//
// 🔴 IT TAKES THE INSTANCE BECAUSE THE RELEASE NAME DOES NOT CARRY ONE, AND THIS
// FUNCTION DESTROYS DATA. Without the check below it uninstalls WHICHEVER release it
// finds — and because templates/namespace.yaml renders the instance namespace INSIDE
// the release, that uninstall cascade-deletes the namespace and everything in it.
//
// Measured on a live cluster, 2026-09-12: `dcctl destroy local b --keep-cluster`, for
// an instance "b" that had never been installed, removed instance "a" in its entirety
// — namespace, all ten deployments, the release — and closed with
// `Instance "b" uninstalled; cluster kind-a left running.`
//
// 🔴 AND THE WAY IN IS THE DOCUMENTED RECOVERY ACTION, WHICH IS WHY A GUARD AND NOT A
// DOC NOTE. A second bootstrap into a cluster that already holds an instance is
// correctly refused — writeOwnedSecret, at the infrastructure step, before anything is
// applied — but it writes ~/.devicechain/<instance>/instance.json BEFORE it fails, and
// that is the binding this command reads. So the operator does the right thing, is
// told no, cleans up the failed attempt, and loses the healthy instance.
//
// No multi-instance work is needed to reach it: any instance record pointing at a
// cluster running a differently-named instance will do, which is exactly what both
// validation rigs produce by passing --kube-context.
func helmUninstall(ctx context.Context, kubeContext, instance string) error {
	settings := cli.New()
	settings.KubeContext = kubeContext
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), helmReleaseNamespace, "secret",
		func(string, ...interface{}) {}); err != nil {
		return err
	}

	owner, present, err := releaseInstance(actionConfig)
	if err != nil {
		return err
	}
	if !present {
		// Nothing installed. Idempotent success, as before: a destroy re-run, or an
		// instance whose release was removed by hand, is not a failure.
		return nil
	}
	if err := uninstallRefusalReason(owner, instance); err != nil {
		return err
	}

	un := action.NewUninstall(actionConfig)
	un.Wait = true
	un.Timeout = helmTimeout
	_, err = un.Run(helmReleaseName)
	if err != nil && strings.Contains(err.Error(), "not found") {
		return nil
	}
	return err
}

// uninstallRefusalReason returns the refusal, or nil when this uninstall may proceed.
//
// Separated from helmUninstall so the policy can be exercised without a cluster — the
// same reason rebuildRefusalReason is separate from its step. A guard whose refusal has
// only ever been produced by hand on a kind cluster is a guard the next edit removes.
func uninstallRefusalReason(owner, instance string) error {
	if owner == instance {
		return nil
	}
	return fmt.Errorf(
		"the DeviceChain release in this cluster belongs to instance %q, not %q, so uninstalling "+
			"it would destroy an instance this command did not name. A cluster holds one release "+
			"(%q in namespace %q) and its name carries no instance, so this is checked rather "+
			"than assumed.\n\n"+
			"  To remove the instance that is actually installed here:\n"+
			"      dcctl destroy <provider> %s --keep-cluster\n\n"+
			"  If %q is a stale local record — a bootstrap that failed part-way leaves one —\n"+
			"  `dcctl instances list` shows what dcctl believes it has.",
		owner, instance, helmReleaseName, helmReleaseNamespace, owner, instance)
}

// releaseInstance reports which instance the installed release belongs to.
//
// Three answers rather than two, for the same reason reuseMintedCredential has three:
// "there is no release" and "there is a release I cannot attribute" call for OPPOSITE
// handling, and collapsing them into an empty string makes the second read as the
// first — which is the branch that deletes.
//
//   - (_, false, nil) — no release here. The caller returns success.
//   - (id, true, nil) — a release, and this is whose it is.
//   - (_, _, err)     — a release that could not be attributed. The caller must NOT
//     uninstall it. "We could not tell" never resolves to the destructive answer, the
//     same rule clusterArchivePath and reuseMintedCredential are written to.
func releaseInstance(cfg *action.Configuration) (string, bool, error) {
	get := action.NewGetValues(cfg)
	// 🔴 THE COMPUTED VALUES, NOT THE SUPPLIED ONES — the opposite of what
	// previousReleaseValues wants, deliberately. That function reads the supplied half
	// because an upgrade may only carry forward what was passed IN. The question here
	// is which namespace the release actually RENDERED into, and instance.id has a
	// chart default ("devicechain", deploy/helm/devicechain/values.yaml). A release
	// installed by a plain `helm install` — still a documented path — therefore
	// supplies no id at all while deploying into a real namespace, and reading the
	// supplied half would attribute it to nobody and take the destructive branch.
	get.AllValues = true
	vals, err := get.Run(helmReleaseName)
	if err == driver.ErrReleaseNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading the values of the release in this cluster, to check which "+
			"instance it belongs to before uninstalling it: %w", err)
	}
	return instanceIDFromValues(vals)
}

// instanceIDFromValues digs instance.id out of a rendered value map.
//
// Split from the Helm call so every way it can be wrong is exercisable without a
// cluster, and because the failure that matters is a SHAPE change. If the chart ever
// moves or renames this key, a version returning "" would make every release
// unattributable — and depending on which way the caller read that, either refuse every
// destroy or guard none. It errors, so the caller cannot read a missing key as an
// answer.
func instanceIDFromValues(vals map[string]interface{}) (string, bool, error) {
	unattributable := func() (string, bool, error) {
		return "", false, fmt.Errorf(
			"the DeviceChain release in this cluster does not say which instance it belongs to "+
				"(no instance.id in its values), so it cannot be told from another instance's. "+
				"Refusing to uninstall it rather than guess — inspect it with "+
				"`helm get values %s -n %s`",
			helmReleaseName, helmReleaseNamespace)
	}
	inst, ok := vals["instance"].(map[string]interface{})
	if !ok {
		return unattributable()
	}
	id, ok := inst["id"].(string)
	if !ok || id == "" {
		return unattributable()
	}
	return id, true, nil
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
