// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/fatih/color"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// helmChartName is the name the embedded chart declares, and the only way a release in
// this cluster can be recognised as one of ours.
//
// 🔴 THE RECOGNITION KEY, NOW THAT THE RELEASE NAME IS NOT ONE. While every DeviceChain
// release was called "dc" the name answered both questions at once — is this ours, and
// whose is it. An instance-derived name answers neither on its own: "dc-a" is a string
// anybody may install, and a cluster's other releases are not ours to reason about. So
// the chart's own metadata says which releases to look at, and the values inside them
// say which instance each belongs to.
const helmChartName = "devicechain"

// legacyHelmReleaseName is the single constant name every release before v0.17.0 was
// installed under.
//
// 🔴 IT EXISTS TO BE FOUND, NOT TO BE WRITTEN. Nothing installs under this name any
// more. It is read in two places — the cluster reader, so a legacy instance still
// answers "this cluster is taken", and destroy, so the recreate the upgrade refusal
// PRESCRIBES actually removes what is here. Without the second, `dcctl destroy` on a
// pre-v0.17.0 instance finds no release under the new name, reports success, and leaves
// the release record behind.
//
// 🔑 PRE-GA, AND DELETED AT GA. An instance may be recreated before v1.0.0 and may not
// after, so the oldest release any supported instance can have been built by rises to
// v1.0.0 and this stops being reachable. See hack/upgrade-baseline-policy.
const legacyHelmReleaseName = "dc"

// helmReleaseNamespace is where the release RECORD lives — not where the workloads go.
//
// 🔴 IT IS STILL A CONSTANT, AND THAT IS DELIBERATE RATHER THAN UNFINISHED. Moving the
// record into the instance's own namespace would mean Helm uninstalling a release whose
// record lives in the namespace that uninstall is deleting, and whether that is safe is
// a live-cluster question nobody has asked yet. The NAME is the half that has to move
// for two instances to coexist; the record's home does not, because a release name is
// unique per namespace and the names are now distinct.
const helmReleaseNamespace = "default"

// helmReleaseNameFor is the release name this instance's chart is installed under.
//
// 🔴 THE NAME CARRIES THE INSTANCE NOW, AND IT USED TO CLAIM TO WITHOUT DOING IT. This
// was the package constant "dc", commented "the per-instance chart release name", which
// it was not: one constant name in one constant namespace means a cluster has exactly
// one DeviceChain release however many instances are declared. Everything that had to
// tell one instance's release from another's did so by reading instance.id back out of
// the values — a correct workaround for a name that carried nothing.
//
// 🔑 THE ATTRIBUTION READ DOES NOT GO AWAY, AND EXPECTING IT TO WOULD BE THE DEFECT.
// A name is a label this binary chose; the values are what the release actually
// RENDERED with. A release named "dc-a" whose values say instance.id is "b" is a
// contradiction, and the only safe reading of it is to refuse — see
// uninstallRefusalReason, which is now a consistency check between the two rather than
// the sole source of truth it was.
func helmReleaseNameFor(instance string) string {
	return legacyHelmReleaseName + "-" + instance
}

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
	// The NAME carries the instance, so two instances in one cluster are two releases
	// rather than one overwriting the other — see helmReleaseNameFor.
	releaseNamespace := helmReleaseNamespace
	releaseName := helmReleaseNameFor(st.Instance)

	settings := cli.New()
	settings.KubeContext = st.KubeContext
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), releaseNamespace, "secret",
		func(string, ...interface{}) {}); err != nil {
		return err
	}

	// WHAT THE RELEASE ALREADY HOLDS, when this run is moving an instance rather than
	// building one. Read before the values are computed, because some of what it
	// carries is an INPUT to that computation — see carryForwardFromRelease.
	var previous map[string]interface{}
	if st.Evolving {
		if previous, err = previousReleaseValues(actionConfig, releaseName); err != nil {
			return err
		}
		carryForwardFromRelease(st, previous)
	}

	// 🔴 TWO VALUE MAPS, AND THE DIFFERENCE BETWEEN THEM IS THE SLICE. The authoring
	// values carry the instance's credentials inside instance.config; the install
	// values carry the NAME of a Secret dcctl wrote instead. Helm records the values
	// of every revision it keeps, so anything that stays in this map stays readable
	// for as long as that revision does — which is what makes rotating a credential
	// something other than retracting it.
	authoring := helmValues(st)
	doc, err := composeInstanceConfig(ctx, ch, authoring)
	if err != nil {
		return err
	}
	vals, err := installValuesFor(authoring, st.Instance, doc)
	if err != nil {
		return err
	}
	// Before the check below rather than after it, so what is validated is what is
	// installed. A carried block that the chart refuses should fail here, named, and
	// not ten minutes later as a rollout that never became ready.
	if st.Evolving {
		carryReleaseValues(vals, previous)
	}

	// Check the instance config the chart is about to be handed BEFORE handing it to
	// the cluster. Everything below waits on workload readiness, so a config a
	// service refuses to load does not surface as a config error at all — it
	// surfaces as a ten-minute wait that ends in a generic timeout, with the actual
	// reason only in the logs of pods that are already gone. Rendering costs
	// milliseconds and turns that into a sentence.
	//
	// 🔑 IT IS NOW THE AUTHORED PATH, which is the one it was written for. The
	// document is passed in because the chart no longer renders one: with nothing
	// supplied here this returns "no instance configuration document was produced",
	// and the strict load that turns an unloadable config into a sentence would
	// simply not run. Handing it the exact bytes about to be written also makes this
	// a check on the COMPOSITION — a document the chart transformed one way and
	// dcctl wrote another is refused here rather than mounted.
	if err := validateRenderedInstanceConfig(ctx, ch, vals, doc); err != nil {
		return err
	}

	// The namespace, then the document, then the release. The pods mount the Secret
	// at container start, so it has to be there before the workloads Helm waits on;
	// and the namespace has to be there before the Secret. See
	// ensureNamespaceForRelease for why creating it here does not move its ownership.
	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to write the instance configuration: %w", err)
	}
	if err := ensureNamespaceForRelease(ctx, typed, st.Instance, releaseName, releaseNamespace); err != nil {
		return err
	}
	// On an instance built before dcctl owned this document, the Secret is there and
	// the chart wrote it — so the writer below would refuse it as somebody else's.
	// See adoptChartWrittenInstanceConfig for why that is a takeover and not a guess.
	if err := adoptChartWrittenInstanceConfig(ctx, typed, st.Instance, st.InstanceUID,
		releaseName, releaseNamespace); err != nil {
		return err
	}
	if err := writeOwnedSecret(ctx, typed, st.Instance, st.InstanceUID,
		instanceConfigSecret(st.Instance, doc), time.Now); err != nil {
		return err
	}

	// Upgrade if the release already exists, otherwise install — so a re-run is
	// idempotent.
	hist := action.NewHistory(actionConfig)
	hist.Max = 1
	if _, err := hist.Run(releaseName); err == driver.ErrReleaseNotFound {
		inst := action.NewInstall(actionConfig)
		inst.ReleaseName = releaseName
		inst.Namespace = releaseNamespace
		inst.Wait = true
		inst.Timeout = helmTimeout
		_, err := inst.RunWithContext(ctx, ch, vals)
		return err
	} else if err != nil {
		return err
	}

	_, err = newHelmUpgrade(actionConfig, releaseNamespace).RunWithContext(ctx, releaseName, ch, vals)
	return err
}

// previousReleaseValues returns the values the release is currently installed with,
// or nil when there is no release yet.
//
// 🔴 THE USER-SUPPLIED VALUES, NOT THE COMPUTED ONES. action.GetValues with AllValues
// left off returns what was passed IN, which is the only half an upgrade may carry
// forward: the computed half is the chart's own defaults merged underneath, and
// carrying those would pin this instance to the defaults of the chart version it was
// installed with — freezing exactly the thing an upgrade exists to move.
//
// 🔑 AND THEY HOLD NO CREDENTIALS, which is what makes reading them safe. The
// instance's secrets were taken out of the release values when dcctl became the
// document's author — installValuesFor strips instance.config and leaves a NAME. This
// reads the map that change created.
func previousReleaseValues(cfg *action.Configuration, releaseName string) (map[string]interface{}, error) {
	vals, err := action.NewGetValues(cfg).Run(releaseName)
	if err == driver.ErrReleaseNotFound {
		// Not an error here. An instance whose release is gone but whose declaration
		// and configuration document survive is a real state — a release uninstalled
		// by hand — and the install branch below rebuilds it. There is simply nothing
		// to carry.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the values instance's release is currently installed "+
			"with, which this upgrade has to carry forward: %w", err)
	}
	return vals, nil
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
	configVals := map[string]interface{}{}
	if len(infraVals) > 0 {
		configVals["infrastructure"] = infraVals
	}
	// THE DATABASE PASSWORDS THE SERVICES CONNECT WITH.
	//
	// 🔴 UNTIL THIS LINE THEY CAME FROM THE CHART'S OWN DEFAULTS — the literal
	// `password: devicechain` in values.yaml, identical on every instance anyone has
	// ever built, and identical to the OpenTofu variable default that created the
	// role. Stating them here is what makes the value an instance's own.
	//
	// 🔑 IT IS THE SAME VALUE THAT WENT INTO THE CREDENTIALS SECRET, by construction:
	// both come from the one credentialSet this run settled. That is the entire
	// reason the credentials are resolved in one place before anything is applied —
	// the role and the connection string cannot be given different passwords if
	// there is only one to give.
	if creds := st.Credentials; creds != nil {
		configVals["persistence"] = map[string]interface{}{
			"rdb": map[string]interface{}{
				"configuration": map[string]interface{}{
					"username": dbRoleUsername,
					"password": creds.RDBPassword,
				},
			},
			"tsdb": map[string]interface{}{
				"configuration": map[string]interface{}{
					"username": dbRoleUsername,
					"password": creds.TSDBPassword,
				},
			},
		}
	}
	if len(configVals) > 0 {
		instanceVals["config"] = configVals
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
// 🔴 IT STILL CHECKS WHOSE THE RELEASE IS, AND THE INSTANCE-DERIVED NAME IS NOT A
// SUBSTITUTE FOR THAT. This function destroys data, and templates/namespace.yaml renders
// the instance namespace INSIDE the release, so an uninstall cascade-deletes the
// namespace and everything in it. The name is now evidence and no longer merely a
// label — but it is evidence THIS binary wrote, and a release named "dc-a" whose values
// say instance.id is "b" is a contradiction rather than a permission.
//
// Measured on a live cluster, 2026-09-12, back when one constant name served every
// instance: `dcctl destroy local b --keep-cluster`, for an instance "b" that had never
// been installed, removed instance "a" in its entirety — namespace, all ten deployments,
// the release — and closed with `Instance "b" uninstalled; cluster kind-a left running.`
// The rename makes that particular lookup impossible; the check below is what makes the
// claim true rather than merely likely.
//
// 🔴 AND IT SWEEPS THE LEGACY RELEASE, WHICH IS THE HALF THE RENAME WOULD OTHERWISE
// BREAK. Every release before v0.17.0 installed under one constant name, and `dcctl
// upgrade` refuses those instances and PRESCRIBES destroy-then-bootstrap. A destroy that
// looked only under the new name would find nothing, take the idempotent-success branch,
// and report success having left the release record behind — turning the one remedy this
// release offers into a remedy that half-works. See uninstallLegacyRelease.
func helmUninstall(ctx context.Context, kubeContext, instance string) error {
	actionConfig, err := helmActionConfig(kubeContext)
	if err != nil {
		return err
	}
	if err := uninstallRelease(ctx, actionConfig, helmReleaseNameFor(instance), instance); err != nil {
		return err
	}
	return uninstallLegacyRelease(ctx, actionConfig, instance)
}

// uninstallRelease removes one named release, after confirming it belongs to the
// instance this command was told to destroy.
func uninstallRelease(ctx context.Context, cfg *action.Configuration, releaseName, instance string) error {
	owner, present, err := releaseInstanceNamed(cfg, releaseName)
	if err != nil {
		return err
	}
	if !present {
		// Nothing installed. Idempotent success, as before: a destroy re-run, or an
		// instance whose release was removed by hand, is not a failure.
		return nil
	}
	if err := uninstallRefusalReason(owner, instance, releaseName); err != nil {
		return err
	}

	un := action.NewUninstall(cfg)
	un.Wait = true
	un.Timeout = helmTimeout
	_, err = un.Run(releaseName)
	if err != nil && strings.Contains(err.Error(), "not found") {
		return nil
	}
	return err
}

// uninstallLegacyRelease removes the pre-v0.17.0 constant-named release when it belongs
// to the instance being destroyed, and leaves it alone when it does not.
//
// 🔴 A FOREIGN OWNER IS SKIPPED HERE AND REFUSED ONE FUNCTION UP, AND THE ASYMMETRY IS
// THE POINT. Under the instance-derived name, a release called "dc-a" that says it
// belongs to "b" is a CONTRADICTION — two attributions this binary controls disagreeing —
// and the only safe reading of a contradiction is to stop. The legacy name carries no
// instance at all, so a legacy release owned by somebody else is not a contradiction; it
// is the ordinary fact that this cluster belongs to another instance. Refusing there
// would fail the very destroy that exists to clear a stale local record pointing at
// somebody else's cluster — the orphan case #862 and #1065 were about.
//
// 🔑 AN UNATTRIBUTABLE LEGACY RELEASE STILL FAILS CLOSED. releaseInstanceNamed's third
// answer is an error, and it stays an error here: "there is a DeviceChain release and I
// cannot tell whose" must never resolve to the branch that deletes it.
func uninstallLegacyRelease(ctx context.Context, cfg *action.Configuration, instance string) error {
	owner, present, err := releaseInstanceNamed(cfg, legacyHelmReleaseName)
	if err != nil {
		return err
	}
	if !present || owner != instance {
		return nil
	}
	fmt.Println(color.YellowString(
		"  Instance %q was built by a release that installed under the name %q; removing that "+
			"release too.", instance, legacyHelmReleaseName))
	return uninstallRelease(ctx, cfg, legacyHelmReleaseName, instance)
}

// foreignReleaseError is the refusal below, as a value the CALLER can recognise.
//
// 🔴 A TYPE RATHER THAN A MESSAGE, BECAUSE ONE CALLER HAS TO ACT ON THIS PARTICULAR
// REFUSAL AND ON NO OTHER. destroyInstanceOnly answers it by asking whether the instance
// it was told to destroy has anything in this cluster at all, and that question ends in
// removing local state — see resolveForeignRelease. A caller that recognised the refusal
// by matching words in its message would start clearing state the day the wording
// changed, or the day some unrelated failure happened to contain them. The shape is
// ErrForeignSecret's, for the same reason.
type foreignReleaseError struct {
	// Owner is the instance the installed release belongs to; Instance is the one the
	// operator named. They are never equal here — an equal pair is not a refusal.
	Owner, Instance string
	// Release is the release whose values disagree with the instance, so the message
	// names the object an operator can go and look at.
	Release string
}

func (e *foreignReleaseError) Error() string {
	return fmt.Sprintf(
		"the Helm release %q in this cluster says it belongs to instance %q, not %q, so "+
			"uninstalling it would destroy an instance this command did not name. dcctl installs "+
			"each instance under a release named after it, so a release whose NAME and whose "+
			"instance.id disagree has been renamed, re-used, or installed by hand — which is "+
			"exactly when guessing is worst.\n\n"+
			"  To remove the instance that is actually installed here:\n"+
			"      dcctl destroy <provider> %s --keep-cluster\n\n"+
			"  Inspect the release itself with:\n"+
			"      helm get values %s -n %s\n\n"+
			"  If %q is a stale local record — a bootstrap that failed part-way leaves one —\n"+
			"  `dcctl instances list` shows what dcctl believes it has.",
		e.Release, e.Owner, e.Instance, e.Owner, e.Release, helmReleaseNamespace, e.Instance)
}

// uninstallRefusalReason returns the refusal, or nil when this uninstall may proceed.
//
// Separated from helmUninstall so the policy can be exercised without a cluster — the
// same reason rebuildRefusalReason is separate from its step. A guard whose refusal has
// only ever been produced by hand on a kind cluster is a guard the next edit removes.
//
// 🔴 IT RETURNS error, NOT *foreignReleaseError, AND THAT IS NOT A STYLE CHOICE. A
// function returning the concrete pointer hands its caller a TYPED NIL, which is not
// nil — so `if err := uninstallRefusalReason(...); err != nil` would refuse every
// uninstall, including the instance's own, and the negative control below is what would
// catch it.
func uninstallRefusalReason(owner, instance, releaseName string) error {
	if owner == instance {
		return nil
	}
	return &foreignReleaseError{Owner: owner, Instance: instance, Release: releaseName}
}

// helmActionConfig reaches the release records a cluster holds.
//
// One definition rather than one per caller, for the same reason helmReleaseNameFor and
// helmReleaseNamespace live together above: every reader of the release has to look in
// the same namespace as the writer, and a namespace written down once per call site is
// a namespace that eventually disagrees with itself. The logger is discarded because
// Helm's storage driver narrates every read at info level, which would interleave with
// dcctl's own step output.
func helmActionConfig(kubeContext string) (*action.Configuration, error) {
	settings := cli.New()
	settings.KubeContext = kubeContext
	cfg := new(action.Configuration)
	if err := cfg.Init(settings.RESTClientGetter(), helmReleaseNamespace, "secret",
		func(string, ...interface{}) {}); err != nil {
		return nil, err
	}
	return cfg, nil
}

// releaseInstanceNamed reports which instance the named release belongs to.
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
func releaseInstanceNamed(cfg *action.Configuration, releaseName string) (string, bool, error) {
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
	vals, err := get.Run(releaseName)
	if err == driver.ErrReleaseNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading the values of release %q in this cluster, to check which "+
			"instance it belongs to before uninstalling it: %w", releaseName, err)
	}
	return instanceIDFromValues(vals, releaseName)
}

// instanceIDFromValues digs instance.id out of a rendered value map.
//
// Split from the Helm call so every way it can be wrong is exercisable without a
// cluster, and because the failure that matters is a SHAPE change. If the chart ever
// moves or renames this key, a version returning "" would make every release
// unattributable — and depending on which way the caller read that, either refuse every
// destroy or guard none. It errors, so the caller cannot read a missing key as an
// answer.
func instanceIDFromValues(vals map[string]interface{}, releaseName string) (string, bool, error) {
	unattributable := func() (string, bool, error) {
		return "", false, fmt.Errorf(
			"the Helm release %q in this cluster does not say which instance it belongs to "+
				"(no instance.id in its values), so it cannot be told from another instance's. "+
				"Refusing to uninstall it rather than guess — inspect it with "+
				"`helm get values %s -n %s`",
			releaseName, releaseName, helmReleaseNamespace)
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
