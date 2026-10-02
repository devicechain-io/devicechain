// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// What `dcctl upgrade` applies below the chart: the instance's OpenTofu root — its
// message broker and its event store.
//
// 🔴 THIS IS WHERE A RELEASE'S BROKER AND EVENT STORE CHANGES REACH A RUNNING INSTANCE,
// AND BEFORE IT NOTHING DID. Only `dcctl bootstrap` applied the instance root, and it
// refuses an instance that is already running, so a release that changed the NATS
// servers' resources or the event store's settings changed only instances built after
// it. An upgraded instance kept its old broker and store configuration and nothing said
// so — drift the operator could not see.
//
// 🔑 IT REUSES BOOTSTRAP'S APPLY AND FEEDS IT WHAT THE INSTANCE IS RUNNING ON. The root,
// its fences and its variables are bootstrap's (openInstanceRoot, instanceRootVars); what
// differs is where each input comes from. A bootstrap mints or derives them; an upgrade
// reads them back from the running instance — the broker's authority and logins, the
// path its event store archives under, the sizes of its volumes, the recovery source of
// a restored store — because an input this verb got wrong would not fail. It would
// reconfigure the instance: a broker with no auth, an archiver retargeted at a prefix
// holding no base backup, a volume asked to shrink.
//
// 🔴 IT PLANS FIRST AND REFUSES WHAT AN UPGRADE MUST NOT DO, BEFORE ANYTHING IS WRITTEN.
// The plan is saved, judged (reviewUpgradePlan) and only then applied — the saved plan,
// not a second computation of it — so what was judged is what runs.

const (
	// natsReleaseAddress is the broker in the instance root's state, as
	// tsdbReleaseAddress (destroy_infra.go) is the event store. A plan that would CREATE
	// either is a plan made against a state that does not describe the running instance.
	natsReleaseAddress = "module.nats.helm_release.nats"

	// upgradePlanFile is the saved plan, beside the state in the instance's own
	// directory (mode 0700). It carries bcrypt hashes and the CA, as the state does, and
	// is removed when the upgrade ends.
	upgradePlanFile = "upgrade.tfplan"

	// brokerRolloutTimeout matches the nats release's own timeout
	// (modules/nats/main.tf), which is sized for a rolling restart of every server.
	brokerRolloutTimeout = 15 * time.Minute
	// eventStoreReadyTimeout matches the event store release's own timeout
	// (modules/cnpg-cluster/main.tf).
	eventStoreReadyTimeout = 15 * time.Minute

	// healthyClusterPhase is CloudNativePG's phase for a Cluster with nothing to do.
	healthyClusterPhase = "Cluster in healthy state"
)

// liveVolumes are the sizes an existing instance's volumes have.
type liveVolumes struct{ JetStream, EventStore string }

// volumeSize is the size an apply passes for one volume: the size the volume already
// has, when it has one, or what this run would otherwise pass.
//
// 🔴 AN UPGRADE NEVER RESIZES A VOLUME. Growing one is a disk and cost decision some
// StorageClasses refuse, and a JetStream volume cannot change at all — a StatefulSet's
// volumeClaimTemplates are immutable, so the broker's release would fail outright. And
// an event store an operator grew by hand would be asked to SHRINK back to the default.
//
// 🔑 THE LIVE SIZE IS PASSED ONLY WHEN IT DIFFERS BY VALUE. The API server returns a
// canonical quantity — a volume created at 16384Mi reads back as 16Gi — and the broker's
// file-store ceiling is derived from the size STRING (90% of its magnitude, in its unit).
// Passing the read-back spelling would turn 14745Mi into 14Gi: a config change that rolls
// the broker and lowers its ceiling, over a volume that did not change. So a live size
// equal to what would otherwise be passed yields that, spelled as it always was.
func volumeSize(live, otherwise string) string {
	if live == "" {
		return otherwise
	}
	if otherwise != "" {
		l, lerr := resource.ParseQuantity(live)
		o, oerr := resource.ParseQuantity(otherwise)
		if lerr == nil && oerr == nil && l.Cmp(o) == 0 {
			return otherwise
		}
	}
	return live
}

// liveInfra is what the instance's broker and event store are running as.
type liveInfra struct {
	Broker     *appsv1.StatefulSet
	BrokerPods []corev1.Pod
	EventStore *unstructured.Unstructured
	// StoreImages are the postgres container images of the event store's pods.
	StoreImages []string
}

// readLiveInfra reads the instance's broker and event store. Indirected like
// readLiveArchiveState, so the decisions made from it can be exercised without a cluster.
var readLiveInfra = func(ctx context.Context, kubeContext, instance string) (liveInfra, error) {
	dyn, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return liveInfra{}, fmt.Errorf("connecting to the cluster to read the instance's broker and event store: %w", err)
	}
	return readInfraFrom(ctx, typed, dyn, InstanceNamespace(instance))
}

// readInfraFrom is readLiveInfra against given clients. A missing broker or event store
// is an ERROR, not an absence: an instance without them is not one this verb can keep
// the volumes of, and treating it as "nothing to keep" would hand the apply the roots'
// defaults.
func readInfraFrom(ctx context.Context, typed kubernetes.Interface, dyn dynamic.Interface, ns string) (liveInfra, error) {
	var out liveInfra
	sts, err := typed.AppsV1().StatefulSets(ns).Get(ctx, natsStatefulSetName, metav1.GetOptions{})
	if err != nil {
		return liveInfra{}, fmt.Errorf("reading the broker (StatefulSet %s/%s): %w", ns, natsStatefulSetName, err)
	}
	out.Broker = sts
	if sel := sts.Spec.Selector; sel != nil {
		pods, err := typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: metav1.FormatLabelSelector(sel)})
		if err != nil {
			return liveInfra{}, fmt.Errorf("listing the broker's pods in %s: %w", ns, err)
		}
		out.BrokerPods = pods.Items
	}
	cl, err := dyn.Resource(clusterGVR).Namespace(ns).Get(ctx, TsdbClusterName, metav1.GetOptions{})
	if err != nil {
		return liveInfra{}, fmt.Errorf("reading the event store (Cluster %s/%s): %w", ns, TsdbClusterName, err)
	}
	out.EventStore = cl
	if out.StoreImages, err = eventStorePodImages(ctx, typed, ns); err != nil {
		return liveInfra{}, err
	}
	return out, nil
}

// eventStorePodImages lists the postgres image of every live instance pod of the event
// store. A pod on its way out, a finished one, and a CloudNativePG job pod (initdb, a
// join) are not instances, and counting them would make a healthy store read as having
// more pods than instances.
func eventStorePodImages(ctx context.Context, typed kubernetes.Interface, ns string) ([]string, error) {
	pods, err := typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "cnpg.io/cluster=" + TsdbClusterName})
	if err != nil {
		return nil, fmt.Errorf("listing the event store's pods in %s: %w", ns, err)
	}
	var images []string
	for _, p := range pods.Items {
		if p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded ||
			p.Status.Phase == corev1.PodFailed || p.Labels["cnpg.io/jobRole"] != "" {
			continue
		}
		for _, c := range p.Spec.Containers {
			if c.Name == "postgres" {
				images = append(images, c.Image)
			}
		}
	}
	return images, nil
}

// liveVolumesFrom reads the two volume sizes off the running objects. A missing
// template or size is an error naming which: emitting nothing would let the root's
// default resize the volume.
func liveVolumesFrom(sts *appsv1.StatefulSet, cluster *unstructured.Unstructured) (liveVolumes, error) {
	var out liveVolumes
	if sts == nil {
		return out, fmt.Errorf("the broker's StatefulSet was not read, so its JetStream volume size is unknown")
	}
	want := natsStatefulSetName + "-js"
	for _, t := range sts.Spec.VolumeClaimTemplates {
		if t.Name != want {
			continue
		}
		q, ok := t.Spec.Resources.Requests[corev1.ResourceStorage]
		if !ok {
			return out, fmt.Errorf("the broker's volume claim template %q states no storage size", want)
		}
		out.JetStream = q.String()
	}
	if out.JetStream == "" {
		return out, fmt.Errorf("the broker's StatefulSet has no volume claim template %q, so its "+
			"JetStream volume size is unknown", want)
	}
	if cluster == nil {
		return out, fmt.Errorf("the event store's Cluster was not read, so its volume size is unknown")
	}
	size, found, err := unstructured.NestedString(cluster.Object, "spec", "storage", "size")
	if err != nil || !found || size == "" {
		return out, fmt.Errorf("the event store's Cluster states no spec.storage.size, so its volume size is unknown")
	}
	out.EventStore = size
	return out, nil
}

// statefulSetRolledOut is deploymentRolledOut for the broker.
//
// 🔴 "READY == REPLICAS" ALONE IS TRUE OF THE OLD PODS, the same trap waitForRollout
// documents for Deployments: before the controller has started the roll every old
// server is ready. The revision check is the one that says the roll FINISHED — the
// StatefulSet controller moves currentRevision to updateRevision only once every pod
// runs the new template.
func statefulSetRolledOut(s *appsv1.StatefulSet) bool {
	desired := int32(1)
	if s.Spec.Replicas != nil {
		desired = *s.Spec.Replicas
	}
	st := s.Status
	return st.ObservedGeneration >= s.Generation &&
		st.UpdatedReplicas == desired &&
		st.ReadyReplicas == desired &&
		st.CurrentRevision == st.UpdateRevision
}

// brokerNotReady says which broker pod is not ready and what it is doing, for a
// refusal or a timeout an operator can act on — "dc-nats-2 Pending" is a node with no
// room for the server.
func brokerNotReady(sts *appsv1.StatefulSet, pods []corev1.Pod) string {
	desired := int32(1)
	if sts.Spec.Replicas != nil {
		desired = *sts.Spec.Replicas
	}
	var stuck []string
	for _, p := range pods {
		ready := false
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready {
			stuck = append(stuck, p.Name+" "+string(p.Status.Phase))
		}
	}
	sort.Strings(stuck)
	s := sts.Status
	out := fmt.Sprintf("%d/%d servers ready, %d/%d on the new revision", s.ReadyReplicas, desired,
		s.UpdatedReplicas, desired)
	if len(stuck) > 0 {
		out += "; not ready: " + strings.Join(stuck, ", ")
	}
	if desired == 1 && s.ReadyReplicas == 0 {
		// 🔴 ONE SERVER IS NOT A DEGRADED BROKER, IT IS NO BROKER. Under --ha two of three
		// keep serving while one waits for room; without it nothing does.
		out += ". This instance runs ONE broker server, so the broker is DOWN, not degraded: " +
			"no device or service can connect until it runs"
	}
	return out
}

// eventStoreReady is the event store's health, with no cluster in it. The reason names
// the first condition that failed.
func eventStoreReady(cluster *unstructured.Unstructured, podImages []string) (bool, string) {
	if cluster == nil {
		return false, "the Cluster was not read"
	}
	phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
	if phase != healthyClusterPhase {
		return false, fmt.Sprintf("its phase is %q, not %q", phase, healthyClusterPhase)
	}
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	ready, _, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")
	if instances == 0 || ready != instances {
		return false, fmt.Sprintf("%d of %d instances are ready", ready, instances)
	}
	current, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	target, _, _ := unstructured.NestedString(cluster.Object, "status", "targetPrimary")
	if current == "" || current != target {
		return false, fmt.Sprintf("its primary is moving (current %q, target %q)", current, target)
	}
	image, _, _ := unstructured.NestedString(cluster.Object, "spec", "imageName")
	if int64(len(podImages)) != instances {
		return false, fmt.Sprintf("%d instance pods are running for %d instances", len(podImages), instances)
	}
	for _, img := range podImages {
		if image != "" && img != image {
			return false, fmt.Sprintf("an instance still runs %s, not %s", img, image)
		}
	}
	conds, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok || m["type"] != "Ready" {
			continue
		}
		if m["status"] != "True" {
			return false, fmt.Sprintf("its Ready condition is %v", m["status"])
		}
		// A condition with no observedGeneration cannot say which spec it judged, so it
		// is not held against the Cluster; one that has it must have seen this spec.
		if og, ok := asInt64(m["observedGeneration"]); ok && og != 0 && og < cluster.GetGeneration() {
			return false, fmt.Sprintf("it has not yet judged its latest spec (generation %d, observed %d)",
				cluster.GetGeneration(), og)
		}
	}
	return true, ""
}

func asInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}

// pollUntil polls check until it has held for `consecutive` polls in a row, or the
// timeout passes. The sleep is clamped to what is left of the deadline, as waitForRollout's
// is, so the limit reported is the limit enforced.
func pollUntil(ctx context.Context, timeout, interval time.Duration, consecutive int,
	check func() (bool, string, error)) (string, error) {
	deadline := time.Now().Add(timeout)
	held := 0
	last := ""
	for {
		ok, why, err := check()
		switch {
		case err != nil:
			held, last = 0, err.Error()
		case ok:
			held++
			if held >= consecutive {
				return "", nil
			}
		default:
			held, last = 0, why
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return last, fmt.Errorf("not within %s: %s", timeout, last)
		}
		wait := interval
		if remaining < wait {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// waitForBrokerRollout blocks until every broker server runs the current template and
// is ready. Helm already waits for a release it changed; this also covers a broker an
// earlier, failed run left part-rolled — whose release now plans no change, so nothing
// else would wait for it at all.
func waitForBrokerRollout(ctx context.Context, typed kubernetes.Interface, ns string, timeout, interval time.Duration) error {
	_, err := pollUntil(ctx, timeout, interval, 1, func() (bool, string, error) {
		sts, err := typed.AppsV1().StatefulSets(ns).Get(ctx, natsStatefulSetName, metav1.GetOptions{})
		if err != nil {
			return false, "", err
		}
		if statefulSetRolledOut(sts) {
			return true, "", nil
		}
		var pods []corev1.Pod
		if sts.Spec.Selector != nil {
			if l, err := typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
				LabelSelector: metav1.FormatLabelSelector(sts.Spec.Selector)}); err == nil {
				pods = l.Items
			}
		}
		return false, brokerNotReady(sts, pods), nil
	})
	if err != nil {
		return fmt.Errorf("waiting for the broker's servers to restart: %w", err)
	}
	return nil
}

// waitForEventStoreReady blocks until the event store is healthy on its current spec.
//
// 🔴 HELM'S WAIT DOES NOT COVER THE DATABASE: the release holds only the Cluster
// resource, which Helm reports ready the moment it is accepted. And a Cluster read in
// the instant after its spec changed, before CloudNativePG has acted on it, still reads
// healthy — so readiness must HOLD for three polls spanning two intervals, not be seen
// once.
func waitForEventStoreReady(ctx context.Context, dyn dynamic.Interface, typed kubernetes.Interface, ns string, timeout, interval time.Duration) error {
	_, err := pollUntil(ctx, timeout, interval, 3, func() (bool, string, error) {
		cl, err := dyn.Resource(clusterGVR).Namespace(ns).Get(ctx, TsdbClusterName, metav1.GetOptions{})
		if err != nil {
			return false, "", err
		}
		images, err := eventStorePodImages(ctx, typed, ns)
		if err != nil {
			return false, "", err
		}
		ok, why := eventStoreReady(cl, images)
		return ok, why, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for the event store to be healthy: %w", err)
	}
	return nil
}

// settleUpgradeInfraInputs reads, from the running instance, every instance-root input
// bootstrap derives for itself, and refuses an instance whose broker or event store is
// not healthy. It reads and decides; it writes nothing.
//
// 🔴 EACH OF THESE IS AN INPUT AN UPGRADE WOULD OTHERWISE GET WRONG WITHOUT AN ERROR.
// The apply takes the broker's authority only when it is given one, turns broker auth on
// only when it is given the issuer, archives under the Cluster's own name unless told
// otherwise, and sizes volumes from the roots' defaults — and every one of those defaults
// is a reconfiguration of a running instance rather than a failure.
func settleUpgradeInfraInputs(ctx context.Context, st *State) error {
	// The broker's authority, as the services hold it (applyDeployedInfrastructure).
	if st.Values["natsCA"] == "" {
		return fmt.Errorf("instance %q runs a configuration that carries no broker certificate "+
			"authority, so its broker cannot be applied without breaking TLS for every client. "+
			"Nothing has been changed. Re-run with --skip-infrastructure to move only the services",
			st.Instance)
	}
	// Its logins, with the hashes the broker already holds.
	creds, err := brokerCredentialsFromRunning(ctx, st, st.Values["natsCalloutIssuerSeed"],
		st.Values["natsServicePassword"], st.Values["natsSysPassword"])
	if err != nil {
		return fmt.Errorf("reusing instance %q's broker logins for its broker's configuration: %w. "+
			"Nothing has been changed", st.Instance, err)
	}
	// 🔴 AN UPGRADE MINTS NOTHING, AND THIS IS THE ONE PLACE IT COULD. An instance whose
	// configuration carries no system-account password would be handed a freshly generated
	// one here (CredentialsFromDeployed fills the gap for a bootstrap). Without it the
	// module renders SYS with no users, which is what such an instance runs today, so it
	// is kept that way rather than given a credential nobody asked this verb for.
	if st.Values["natsSysPassword"] == "" {
		creds.SysPassword, creds.SysPasswordBcrypt = "", ""
	}
	setBrokerCredentialValues(st, creds)

	// The archive path the event store owns: the live one, always. See resolveArchivePath.
	live, err := readLiveArchiveState(ctx, st.KubeContext, st.Instance)
	if err != nil {
		return err
	}
	if !live.Exists {
		return fmt.Errorf("instance %q has no event store (Cluster %s in %s), so there is no "+
			"infrastructure here to upgrade. Nothing has been changed. Destroy the instance and "+
			"bootstrap it again", st.Instance, TsdbClusterName, InstanceNamespace(st.Instance))
	}
	st.Values["backupServerNameTsdb"] = resolveArchivePaths(live, RestorePlan{}, "", time.Now().UTC()).Tsdb

	// The local MQTT node port, which another instance may hold.
	held, err := readClusterSingletons(ctx, st.KubeContext, st.Instance, ingressHostFor(st))
	if err != nil {
		return err
	}
	if held.MQTTNodePortHolder != "" {
		st.Values[mqttNodePortHolderKey] = held.MQTTNodePortHolder
	}

	infra, err := readLiveInfra(ctx, st.KubeContext, st.Instance)
	if err != nil {
		return err
	}
	if st.LiveVolumes, err = liveVolumesFrom(infra.Broker, infra.EventStore); err != nil {
		return err
	}

	// 🔴 HEALTHY BEFORE THE FIRST WRITE. An apply over a broker already short a server,
	// or a store already failing over, is a roll that cannot finish — the upgrade would
	// time out a quarter of an hour later with the infrastructure half moved. Refused
	// here, while a refusal still means nothing has moved.
	if !statefulSetRolledOut(infra.Broker) {
		return fmt.Errorf("instance %q's broker is not healthy (%s), so this upgrade would restart "+
			"servers of a broker that is already short of one. Nothing has been changed. Bring it "+
			"back first, then run the upgrade again", st.Instance, brokerNotReady(infra.Broker, infra.BrokerPods))
	}
	if ok, why := eventStoreReady(infra.EventStore, infra.StoreImages); !ok {
		return fmt.Errorf("instance %q's event store is not healthy (%s), so this upgrade would "+
			"restart instances of a database that is already degraded. Nothing has been changed. "+
			"Bring it back first, then run the upgrade again", st.Instance, why)
	}
	return nil
}

// carriedRestoreVars reads a restored event store's recovery source out of the instance
// root's own state, so an upgrade re-passes it and the Cluster's spec.bootstrap is
// rendered exactly as it was.
//
// 🔴 WITHOUT THEM THE STORE'S CHART RENDERS spec.bootstrap AS initdb, over a store that
// was recovered. What CloudNativePG does with a changed spec.bootstrap on a running
// Cluster is not something this code relies on either way: it re-passes the same values,
// so nothing changes. A recovery target it cannot re-pass is refused by name rather than
// dropped.
func carriedRestoreVars(state *tfjson.State) ([]string, error) {
	vals, found, err := releaseValuesInState(state, tsdbReleaseAddress)
	if err != nil || !found {
		return nil, err
	}
	restore, ok := vals["restore"].(map[string]interface{})
	if !ok || restore["enabled"] != true {
		return nil, nil
	}
	source, _ := restore["sourceServerName"].(string)
	if source == "" {
		return nil, fmt.Errorf("the event store's recorded restore names no source archive, so an " +
			"upgrade cannot re-pass it. Nothing has been changed")
	}
	if imm, _ := restore["recoveryTargetImmediate"].(bool); imm {
		return nil, fmt.Errorf("the event store was restored with recoveryTargetImmediate, which " +
			"dcctl cannot re-pass, and dropping it would re-render the store's spec.bootstrap. " +
			"Nothing has been changed. Re-run with --skip-infrastructure to move only the services")
	}
	out := []string{"restore_tsdb_from=" + source}
	if target, ok := restore["recoveryTarget"].(map[string]interface{}); ok {
		keys := make([]string, 0, len(target))
		for k := range target {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k != "targetTime" {
				return nil, fmt.Errorf("the event store was restored to a recovery target by %s, which "+
					"dcctl cannot re-pass, and dropping it would re-render the store's spec.bootstrap. "+
					"Nothing has been changed. Re-run with --skip-infrastructure to move only the services", k)
			}
			if tt, _ := target[k].(string); tt != "" {
				out = append(out, "restore_tsdb_target_time="+tt)
			}
		}
	}
	return out, nil
}

// releaseValuesInState finds a helm_release in the state and decodes its values.
func releaseValuesInState(state *tfjson.State, address string) (map[string]interface{}, bool, error) {
	if state == nil || state.Values == nil {
		return nil, false, nil
	}
	r := findStateResource(state.Values.RootModule, address)
	if r == nil {
		return nil, false, nil
	}
	vals, err := decodeReleaseValues(r.AttributeValues)
	if err != nil {
		return nil, false, fmt.Errorf("reading %s's values out of the infrastructure state: %w", address, err)
	}
	return vals, true, nil
}

// decodeReleaseValues merges a helm_release's `values` documents the way Helm does:
// later documents over earlier ones, maps merged key by key.
func decodeReleaseValues(attrs interface{}) (map[string]interface{}, error) {
	m, ok := attrs.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("the resource has no attributes")
	}
	docs, ok := m["values"].([]interface{})
	if !ok {
		if m["values"] == nil {
			return map[string]interface{}{}, nil
		}
		return nil, fmt.Errorf("its values are %T, not a list of documents", m["values"])
	}
	out := map[string]interface{}{}
	for i, d := range docs {
		s, ok := d.(string)
		if !ok {
			return nil, fmt.Errorf("values document %d is %T, not a string", i, d)
		}
		var doc map[string]interface{}
		if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
			return nil, fmt.Errorf("values document %d does not decode: %w", i, err)
		}
		mergeValues(out, doc)
	}
	return out, nil
}

func mergeValues(dst, src map[string]interface{}) {
	for k, v := range src {
		if sm, ok := v.(map[string]interface{}); ok {
			if dm, ok := dst[k].(map[string]interface{}); ok {
				mergeValues(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

// plannedChange is one resource a saved plan would create or update.
type plannedChange struct {
	Address string
	Action  string
}

// planReview is what reviewUpgradePlan found acceptable in a plan, and what it has to say.
type planReview struct {
	Changes []plannedChange
	// Lines are said with the plan: which settings of the broker and the event store
	// move. Warnings are said loudly.
	Lines    []string
	Warnings []string
}

// reviewUpgradePlan judges a saved plan before it is applied, and refuses — by name,
// with nothing changed — what an upgrade must not do.
//
// stated reports whether the operator declared an instance-root variable themselves, in
// a terraform.tfvars beside the state or in TF_VAR_<name>. A change that would cost data
// is accepted when the operator stated the value it comes from, because then it is what
// they asked for; it is refused when it comes from nobody stating anything.
//
// 🔴 IT JUDGES RESOURCES, NOT THE OBJECTS INSIDE A CHART. A helm_release update that
// drops an object from its manifest is an UPDATE here, and Helm deletes the object; what
// this refuses is a resource the configuration manages being deleted or replaced. The
// settings checks below are what look inside the two releases.
func reviewUpgradePlan(instance string, plan *tfjson.Plan, stated func(string) bool) (planReview, error) {
	var out planReview
	if plan == nil {
		return out, fmt.Errorf("the infrastructure plan for instance %q could not be read. Nothing has been changed", instance)
	}
	var destructive, missing, unreadable []string
	for _, rc := range plan.ResourceChanges {
		if rc == nil {
			continue
		}
		if rc.Change == nil {
			unreadable = append(unreadable, rc.Address)
			continue
		}
		a := rc.Change.Actions
		switch {
		case a.NoOp() || a.Read():
			continue
		case actionsDelete(a):
			// terraform_data guards hold nothing: their replacement is a re-evaluation.
			if rc.Type == "terraform_data" {
				out.Changes = append(out.Changes, plannedChange{rc.Address, "re-check"})
				continue
			}
			destructive = append(destructive, rc.Address)
		case a.Create():
			if rc.Address == natsReleaseAddress || rc.Address == tsdbReleaseAddress {
				missing = append(missing, rc.Address)
				continue
			}
			out.Changes = append(out.Changes, plannedChange{rc.Address, "create"})
		case a.Update():
			out.Changes = append(out.Changes, plannedChange{rc.Address, "update"})
			if rc.Address == natsReleaseAddress || rc.Address == tsdbReleaseAddress {
				if err := reviewReleaseChange(&out, rc, stated); err != nil {
					return planReview{}, fmt.Errorf("applying this release's infrastructure to instance %q: %w", instance, err)
				}
			}
		default:
			unreadable = append(unreadable, rc.Address)
		}
	}
	switch {
	case len(unreadable) > 0:
		return planReview{}, fmt.Errorf("the infrastructure plan for instance %q does something this "+
			"upgrade cannot read to %s, so it is not applied. Nothing has been changed",
			instance, strings.Join(unreadable, ", "))
	case len(missing) > 0:
		return planReview{}, fmt.Errorf("this machine holds no infrastructure state for instance %q "+
			"(the plan would create %s, which the instance already runs), so the upgrade cannot apply "+
			"its message broker and event store without trying to create them again. Nothing has been "+
			"changed. Run the upgrade from the machine that bootstrapped the instance, or copy its "+
			"~/.devicechain/instances/%s/ directory here first (it holds credentials: keep it private, "+
			"mode 0700). To move only the services and leave the broker and event store as they are, "+
			"re-run with --skip-infrastructure", instance, strings.Join(missing, " and "), instance)
	case len(destructive) > 0:
		return planReview{}, fmt.Errorf("applying this release's infrastructure to instance %q would "+
			"delete or replace:\n  %s\nAn upgrade does not delete or replace anything the instance's "+
			"OpenTofu configuration manages, so nothing has been changed. The release notes for this "+
			"version say how to move an instance whose infrastructure has to be replaced. To move only "+
			"the services, re-run with --skip-infrastructure", instance, strings.Join(destructive, "\n  "))
	}
	return out, nil
}

func actionsDelete(a tfjson.Actions) bool {
	for _, x := range a {
		if x == tfjson.ActionDelete {
			return true
		}
	}
	return false
}

// reviewReleaseChange looks inside one release's values: it names every setting that
// moves, and refuses the moves that lose something.
func reviewReleaseChange(out *planReview, rc *tfjson.ResourceChange, stated func(string) bool) error {
	// Values the plan cannot know until it applies cannot be judged, and reading them as
	// absent would judge an empty release: refused, not passed.
	if u, ok := rc.Change.AfterUnknown.(map[string]interface{}); ok && containsTrue(u["values"]) {
		return fmt.Errorf("the plan cannot say what %s's values will be until it applies them, so "+
			"this upgrade cannot check them. Nothing has been changed", rc.Address)
	}
	before, err := decodeReleaseValues(rc.Change.Before)
	if err != nil {
		return fmt.Errorf("reading what %s holds now: %w", rc.Address, err)
	}
	after, err := decodeReleaseValues(rc.Change.After)
	if err != nil {
		return fmt.Errorf("reading what %s would hold: %w", rc.Address, err)
	}
	what := "the message broker"
	if rc.Address == tsdbReleaseAddress {
		what = "the event store"
	}
	for _, d := range diffValues(before, after) {
		out.Lines = append(out.Lines, what+": "+d)
	}
	if rc.Address == natsReleaseAddress {
		out.Warnings = append(out.Warnings, brokerResourceDecreases(before, after)...)
		return nil
	}
	return refuseEventStoreLosses(before, after, stated)
}

// refuseEventStoreLosses refuses the event store changes that cost something an
// operator set and nothing here would restore.
//
// 🔴 THESE ARE VALUES OPERATORS ARE TOLD TO SET ON THE INSTANCE ROOT THEMSELVES, AND
// UNTIL AN UPGRADE APPLIED THE ROOT NOTHING EVER REVERTED THEM. Set by hand with -var,
// they are not in anything this apply reads, so the upgrade would put back the default:
//
//   - a shorter recovery window makes the backup plugin PRUNE every base backup and WAL
//     segment older than it — lost recovery range, which no later change gives back;
//   - an analytics reader that disappears from the declared roles stops being managed,
//     so the documented password rotation silently stops working for it.
func refuseEventStoreLosses(before, after map[string]interface{}, stated func(string) bool) error {
	if b, ok := nestedMap(before, "backup"); ok && b["enabled"] == true {
		a, aok := nestedMap(after, "backup")
		if !aok || a["enabled"] != true {
			return fmt.Errorf("the event store's backups would be switched off, and with them every " +
				"archived base backup and WAL segment would stop being kept. Nothing has been changed")
		}
		br, _ := b["retentionPolicy"].(string)
		ar, _ := a["retentionPolicy"].(string)
		if br != ar && !stated("backup_retention_tsdb") {
			shorter, known := retentionShorter(ar, br)
			if !known || shorter {
				return fmt.Errorf("the event store's recovery window would change from %q to %q, and a "+
					"shorter window prunes every backup older than it. That value is not set anywhere this "+
					"upgrade reads, so it was most likely set by hand and would be lost. Nothing has been "+
					"changed. To keep it, set backup_retention_tsdb = %q in the terraform.tfvars beside the "+
					"instance's infrastructure state (~/.devicechain/instances/<instance>/infra/instance/), "+
					"which every dcctl apply reads, then run the upgrade again", displayRetention(br),
					displayRetention(ar), br)
			}
		}
	}
	if lost := lostLoginRoles(before, after); len(lost) > 0 && !stated("timescale_analytics_readers") {
		return fmt.Errorf("the event store would stop declaring the analytics reader role(s) %s. A role "+
			"that is no longer declared is no longer managed, so its password rotation stops working. "+
			"They are not set anywhere this upgrade reads, so they were most likely declared by hand and "+
			"would be lost. Nothing has been changed. Declare them in timescale_analytics_readers in the "+
			"terraform.tfvars beside the instance's infrastructure state "+
			"(~/.devicechain/instances/<instance>/infra/instance/), which every dcctl apply reads, then "+
			"run the upgrade again", strings.Join(lost, ", "))
	}
	// 🔴 CloudNativePG refuses, when a Cluster has more than one instance, an update that
	// changes its image AND any parameter at once (modules/cnpg-cluster/chart/templates/
	// cluster.yaml). The release rolls back atomically and every re-run is refused the
	// same way, so it is refused here, by name, with the order that gets through.
	bi, _ := before["imageName"].(string)
	ai, _ := after["imageName"].(string)
	if bi != "" && bi != ai && !jsonEqual(before["parameters"], after["parameters"]) {
		if n, _ := asInt64(after["instances"]); n > 1 {
			return fmt.Errorf("this release changes the event store's image (%s to %s) and its "+
				"settings in the same apply, which the database operator refuses for a store of more "+
				"than one instance. Nothing has been changed. Set timescale_image = %q in the "+
				"terraform.tfvars beside the instance's infrastructure state "+
				"(~/.devicechain/instances/<instance>/infra/instance/), run the upgrade, then remove that "+
				"line and run it again: the first run moves the settings, the second the image", bi, ai, bi)
		}
	}
	return nil
}

// brokerResourceDecreases names any broker request or limit this apply lowers. A lower
// value is not a loss of data, so it is reported rather than refused — loudly, because
// the most likely cause is a value set by hand that this apply does not know about.
func brokerResourceDecreases(before, after map[string]interface{}) []string {
	var out []string
	for _, path := range [][]string{
		{"container", "resources", "requests", "cpu"},
		{"container", "resources", "requests", "memory"},
		{"container", "resources", "limits", "memory"},
	} {
		b, bok := nestedString(before, path...)
		a, aok := nestedString(after, path...)
		if !bok || !aok {
			continue
		}
		bq, berr := resource.ParseQuantity(b)
		aq, aerr := resource.ParseQuantity(a)
		if berr != nil || aerr != nil || aq.Cmp(bq) >= 0 {
			continue
		}
		out = append(out, fmt.Sprintf("the broker's %s is LOWERED from %s to %s. If you raised it by hand, "+
			"set it in the terraform.tfvars beside the instance's infrastructure state "+
			"(~/.devicechain/instances/<instance>/infra/instance/), which every dcctl apply reads",
			strings.Join(path[1:], "."), b, a))
	}
	return out
}

// containsTrue reports whether an after_unknown fragment marks anything unknown.
func containsTrue(v interface{}) bool {
	switch x := v.(type) {
	case bool:
		return x
	case []interface{}:
		for _, e := range x {
			if containsTrue(e) {
				return true
			}
		}
	case map[string]interface{}:
		for _, e := range x {
			if containsTrue(e) {
				return true
			}
		}
	}
	return false
}

func nestedMap(m map[string]interface{}, key string) (map[string]interface{}, bool) {
	v, ok := m[key].(map[string]interface{})
	return v, ok
}

func nestedString(m map[string]interface{}, path ...string) (string, bool) {
	var cur interface{} = m
	for _, p := range path {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return "", false
		}
		cur = mm[p]
	}
	s, ok := cur.(string)
	return s, ok
}

func lostLoginRoles(before, after map[string]interface{}) []string {
	names := func(m map[string]interface{}, loginOnly bool) map[string]bool {
		out := map[string]bool{}
		roles, _ := m["extraRoles"].([]interface{})
		for _, r := range roles {
			rm, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			if loginOnly && rm["login"] != true {
				continue
			}
			if n, _ := rm["name"].(string); n != "" {
				out[n] = true
			}
		}
		return out
	}
	had, has := names(before, true), names(after, false)
	var lost []string
	for n := range had {
		if !has[n] {
			lost = append(lost, n)
		}
	}
	sort.Strings(lost)
	return lost
}

var retentionRe = regexp.MustCompile(`^([0-9]+)([dwm])$`)

// retentionDays reads a recovery window ("7d", "4w", "3m"); empty keeps everything.
func retentionDays(s string) (days int, forever bool, ok bool) {
	if s == "" {
		return 0, true, true
	}
	m := retentionRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false, false
	}
	switch m[2] {
	case "w":
		n *= 7
	case "m":
		n *= 30
	}
	return n, false, true
}

// retentionShorter reports whether window a keeps less than window b. known is false
// when either cannot be read, which the caller treats as a refusal: it cannot tell.
func retentionShorter(a, b string) (shorter, known bool) {
	ad, af, aok := retentionDays(a)
	bd, bf, bok := retentionDays(b)
	if !aok || !bok {
		return false, false
	}
	switch {
	case af:
		return false, true
	case bf:
		return true, true
	}
	return ad < bd, true
}

func displayRetention(s string) string {
	if s == "" {
		return "keep everything"
	}
	return s
}

func jsonEqual(a, b interface{}) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// shownValuePaths are the settings whose VALUES may be printed with a plan. Everything
// else is named by path only: the broker's values carry bcrypt hashes and its
// authority, and nothing about a plan should put those on a terminal or in a CI log.
var shownValuePaths = []string{
	"imageName", "instances", "storage.size", "walStorage.size", "parameters.",
	"container.resources.", "container.env.GOMEMLIMIT", "backup.retentionPolicy",
	"backup.schedule", "backup.walCompression", "backup.dataCompression",
	"backup.snapshotClass", "backup.objectStoreSchedule", "backup.walMaxParallel",
	"config.jetstream.fileStore.pvc.size",
}

func shownValue(path string) bool {
	for _, p := range shownValuePaths {
		if path == p || (strings.HasSuffix(p, ".") && strings.HasPrefix(path, p)) {
			return true
		}
	}
	return false
}

// diffValues lists the settings that differ between two decoded values maps, one line
// each, sorted: the path, and for a shown setting its old and new values.
func diffValues(before, after map[string]interface{}) []string {
	fb, fa := map[string]interface{}{}, map[string]interface{}{}
	flattenValues("", before, fb)
	flattenValues("", after, fa)
	paths := map[string]bool{}
	for k := range fb {
		paths[k] = true
	}
	for k := range fa {
		paths[k] = true
	}
	var out []string
	for p := range paths {
		b, bok := fb[p]
		a, aok := fa[p]
		if bok && aok && jsonEqual(a, b) {
			continue
		}
		line := p
		if shownValue(p) {
			line += fmt.Sprintf(" %s -> %s", showLeaf(b, bok), showLeaf(a, aok))
		} else {
			switch {
			case !bok:
				line += " (added)"
			case !aok:
				line += " (removed)"
			default:
				line += " (changed)"
			}
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

func showLeaf(v interface{}, ok bool) string {
	if !ok {
		return "(unset)"
	}
	if s, isStr := v.(string); isStr {
		return s
	}
	j, _ := json.Marshal(v)
	return string(j)
}

func flattenValues(prefix string, v interface{}, out map[string]interface{}) {
	m, ok := v.(map[string]interface{})
	if !ok || len(m) == 0 {
		if prefix != "" {
			out[prefix] = v
		}
		return
	}
	for k, child := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		flattenValues(p, child, out)
	}
}

// statedByOperator reports whether the operator declared an instance-root variable
// themselves: in TF_VAR_<name>, or in a variables file OpenTofu loads on its own from
// the root's directory (terraform.tfvars, *.auto.tfvars, and their .json forms). Those
// are the places a dcctl apply honours, because tofu reads them with dcctl's own -var
// on top: a value set there survives every bootstrap and upgrade, one passed by hand
// with -var survives none.
func statedByOperator(rootdir, name string) bool {
	if os.Getenv("TF_VAR_"+name) != "" {
		return true
	}
	entries, err := os.ReadDir(rootdir)
	if err != nil {
		return false
	}
	decl := regexp.MustCompile(`(?m)^\s*"?` + regexp.QuoteMeta(name) + `"?\s*[=:]`)
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !(n == "terraform.tfvars" || n == "terraform.tfvars.json" ||
			strings.HasSuffix(n, ".auto.tfvars") || strings.HasSuffix(n, ".auto.tfvars.json")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(rootdir, n))
		if err == nil && decl.Match(b) {
			return true
		}
	}
	return false
}

// sayKeptVolumes says which volumes keep a size other than the one this release
// creates. Volume sizes are kept on purpose (see volumeSize); saying so is what keeps the
// difference from being a surprise later.
func sayKeptVolumes(st *State) []string {
	var out []string
	for _, v := range []struct {
		what, live, release, name string
	}{
		{"JetStream volume", st.LiveVolumes.JetStream, releaseVolumeSize(st, "nats_jetstream_storage"), "nats_jetstream_storage"},
		{"event store volume", st.LiveVolumes.EventStore, releaseVolumeSize(st, "timescale_storage"), "timescale_storage"},
	} {
		if v.live == "" || v.release == "" || volumeSize(v.live, v.release) == v.release {
			continue
		}
		out = append(out, fmt.Sprintf("%s kept at %s; this release creates new instances with %s "+
			"(an upgrade never resizes a volume)", v.what, v.live, v.release))
	}
	return out
}

// releaseVolumeSize is the size this release gives a NEW instance's volume.
func releaseVolumeSize(st *State, name string) string {
	if st.Compact {
		switch name {
		case "nats_jetstream_storage":
			return compact.JetStreamStorage
		case "timescale_storage":
			return compact.TimescaleStorage
		}
	}
	return embeddedInstanceDefault(name)
}

// upgradeTofu is what the upgrade needs of a tofu runner. *tofuExec satisfies it.
type upgradeTofu interface {
	stateLister
	Plan(ctx context.Context, opts ...tfexec.PlanOption) (bool, error)
	ShowPlanFile(ctx context.Context, planPath string, opts ...tfexec.ShowOption) (*tfjson.Plan, error)
	Apply(ctx context.Context, opts ...tfexec.ApplyOption) error
	infraOutputReader
}

// upgradeInfraPlan is a judged, saved plan of the instance root, ready to apply.
type upgradeInfraPlan struct {
	tf       upgradeTofu
	rootdir  string
	planPath string
	instance string
	vars     []string
	review   planReview
	kept     []string
}

// say prints what the plan changes. It never prints a value outside shownValuePaths.
func (p *upgradeInfraPlan) say() {
	fmt.Println(color.WhiteString("\n  The instance's message broker and event store, from this release's infrastructure configuration:"))
	if len(p.review.Changes) == 0 {
		fmt.Println(color.GreenString("    already at this release; nothing to apply"))
	}
	for _, c := range p.review.Changes {
		gloss := ""
		switch c.Address {
		case natsReleaseAddress:
			gloss = " (the message broker: its servers restart one at a time when their settings change)"
		case tsdbReleaseAddress:
			gloss = " (the event store)"
		}
		fmt.Printf("    %s %s%s\n", c.Action, c.Address, gloss)
	}
	for _, l := range p.review.Lines {
		fmt.Printf("      %s\n", l)
	}
	for _, k := range p.kept {
		fmt.Println(color.YellowString("    %s", k))
	}
	for _, w := range p.review.Warnings {
		fmt.Println(color.YellowString("    ⚠ %s", w))
	}
}

// Close removes the saved plan and tightens the state files the plan and apply wrote.
func (p *upgradeInfraPlan) Close() error {
	if err := os.Remove(p.planPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return hardenStateFiles(p.rootdir)
}

// planUpgradeInfra settles the instance's inputs, opens the instance root (bootstrap's
// extract, init and fences) and returns a judged plan of it. It changes nothing in the
// cluster. The caller closes the plan.
func planUpgradeInfra(ctx context.Context, st *State) (*upgradeInfraPlan, error) {
	if err := settleUpgradeInfraInputs(ctx, st); err != nil {
		return nil, err
	}
	inst, err := openInstanceRoot(ctx, st)
	if err != nil {
		return nil, fmt.Errorf("opening instance %q's infrastructure configuration: %w. Nothing has been "+
			"changed; --skip-infrastructure moves only the services", st.Instance, err)
	}
	p, err := prepareUpgradePlan(ctx, st, inst.tf, inst.rootdir)
	if err != nil {
		_ = hardenStateFiles(inst.rootdir)
		return nil, err
	}
	return p, nil
}

// prepareUpgradePlan reads the recovery source out of the state, composes the inputs
// bootstrap would (instanceRootVars) and plans through the gate.
func prepareUpgradePlan(ctx context.Context, st *State, tf upgradeTofu, rootdir string) (*upgradeInfraPlan, error) {
	state, err := tf.Show(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading instance %q's infrastructure state: %w", st.Instance, err)
	}
	if st.CarriedRestoreVars, err = carriedRestoreVars(state); err != nil {
		return nil, err
	}
	vars, err := instanceRootVars(st)
	if err != nil {
		return nil, err
	}
	p, err := planGated(ctx, tf, rootdir, vars, st.Instance)
	if err != nil {
		return nil, err
	}
	p.kept = sayKeptVolumes(st)
	return p, nil
}

// planGated plans to a saved file and judges it. Its stdout stays quiet: see tofuExec.
func planGated(ctx context.Context, tf upgradeTofu, rootdir string, vars []string, instance string) (*upgradeInfraPlan, error) {
	planPath := filepath.Join(rootdir, upgradePlanFile)
	if err := os.Remove(planPath); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	opts := []tfexec.PlanOption{tfexec.Out(planPath)}
	for _, v := range vars {
		opts = append(opts, tfexec.Var(v))
	}
	if _, err := tf.Plan(ctx, opts...); err != nil {
		return nil, fmt.Errorf("planning instance %q's infrastructure: %w. Nothing has been changed", instance, err)
	}
	// Before it is read, not after: the file holds what the state holds.
	if err := os.Chmod(planPath, stateFileMode); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	plan, err := tf.ShowPlanFile(ctx, planPath)
	if err != nil {
		_ = os.Remove(planPath)
		return nil, fmt.Errorf("reading instance %q's infrastructure plan: %w. Nothing has been changed", instance, err)
	}
	review, err := reviewUpgradePlan(instance, plan, func(name string) bool { return statedByOperator(rootdir, name) })
	if err != nil {
		_ = os.Remove(planPath)
		return nil, err
	}
	return &upgradeInfraPlan{tf: tf, rootdir: rootdir, planPath: planPath, instance: instance, vars: vars, review: review}, nil
}

// infraWaits are the waits applyUpgradeInfra makes, as functions so its decisions can
// be exercised without a cluster.
type infraWaits struct {
	admission func(context.Context) error
	broker    func(context.Context) error
	store     func(context.Context) error
}

func clusterInfraWaits(st *State, typed kubernetes.Interface, dyn dynamic.Interface) infraWaits {
	ns := InstanceNamespace(st.Instance)
	return infraWaits{
		admission: func(ctx context.Context) error {
			return waitForCNPGAdmission(ctx, st.KubeContext, cnpgAdmissionTimeout)
		},
		broker: func(ctx context.Context) error {
			return waitForBrokerRollout(ctx, typed, ns, brokerRolloutTimeout, rolloutPollInterval)
		},
		store: func(ctx context.Context) error {
			return waitForEventStoreReady(ctx, dyn, typed, ns, eventStoreReadyTimeout, rolloutPollInterval)
		},
	}
}

var errRehearsalApplies = errors.New("a rehearsal (--dry-run) applies nothing; refusing to apply the infrastructure plan")

// applyUpgradeInfra applies the SAVED plan, records what the apply reports, and waits
// for the broker and the event store to be healthy.
//
// 🔴 BOTH WAITS RUN WHETHER OR NOT THIS PLAN TOUCHED THEM. A run that failed part-way
// leaves the releases already carrying the new values, so the re-run an operator is
// told to make plans no change at all — and a wait keyed on the plan would then move the
// services onto a broker still short a server. Each costs one poll when healthy.
//
// The CloudNativePG admission race is retried once, as bootstrap's apply does — but by
// planning AGAIN through the gate, not by re-applying: a saved plan is stale after a
// partial apply, and an unjudged one is exactly what the gate exists to prevent.
func applyUpgradeInfra(ctx context.Context, st *State, p *upgradeInfraPlan, waits infraWaits) error {
	if st.DryRun {
		return errRehearsalApplies
	}
	if err := p.tf.Apply(ctx, tfexec.DirOrPlan(p.planPath)); err != nil {
		if !isCNPGWebhookUnavailable(err.Error()) {
			return fmt.Errorf("applying instance %q's infrastructure: %w", st.Instance, err)
		}
		reportCNPGAdmissionWait()
		if werr := waits.admission(ctx); werr != nil {
			return fmt.Errorf("applying instance %q's infrastructure failed because the CloudNativePG "+
				"admission webhook was unreachable, and it did not recover: %w (original apply error: %v)",
				st.Instance, werr, err)
		}
		again, perr := planGated(ctx, p.tf, p.rootdir, p.vars, p.instance)
		if perr != nil {
			return fmt.Errorf("%w (the apply that triggered the re-plan failed with: %v)", perr, err)
		}
		again.kept = p.kept
		*p = *again
		p.say()
		if rerr := p.tf.Apply(ctx, tfexec.DirOrPlan(p.planPath)); rerr != nil {
			return fmt.Errorf("applying instance %q's infrastructure, retried after waiting on the "+
				"CloudNativePG admission webhook: %w (the apply that triggered the wait failed with: %v)",
				st.Instance, rerr, err)
		}
	}
	if err := readInstanceInfraOutputs(ctx, st, p.tf); err != nil {
		return err
	}
	st.InfraApplied = true
	if err := waits.broker(ctx); err != nil {
		return err
	}
	return waits.store(ctx)
}
