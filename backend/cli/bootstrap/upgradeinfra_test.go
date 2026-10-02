// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

// --- fixtures ---------------------------------------------------------------

func aBroker(replicas int32, jsSize string) *appsv1.StatefulSet {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: natsStatefulSetName, Namespace: InstanceNamespace("prod"), Generation: 4},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "nats"}},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 4, Replicas: replicas, ReadyReplicas: replicas, UpdatedReplicas: replicas,
			CurrentRevision: "dc-nats-abc", UpdateRevision: "dc-nats-abc",
		},
	}
	if jsSize != "" {
		sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: natsStatefulSetName + "-js"},
			Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(jsSize)}}},
		}}
	}
	return sts
}

func anEventStore(instances int64, size, image string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]interface{}{"name": TsdbClusterName, "generation": int64(7)},
		"spec": map[string]interface{}{
			"instances": instances, "imageName": image,
			"storage": map[string]interface{}{"size": size},
		},
		"status": map[string]interface{}{
			"phase": healthyClusterPhase, "readyInstances": instances,
			"currentPrimary": "dc-tsdb-1", "targetPrimary": "dc-tsdb-1",
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True", "observedGeneration": int64(7)}},
		},
	}}
	return u
}

func imagesOf(n int, image string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = image
	}
	return out
}

// stubUpgradeReads replaces every cluster read settleUpgradeInfraInputs makes.
func stubUpgradeReads(t *testing.T, live clusterArchiveState, infra liveInfra, hashes natsauth.DeployedHashes) {
	t.Helper()
	prevArchive, prevSingle, prevInfra, prevHashes := readLiveArchiveState, readClusterSingletons, readLiveInfra, lookupDeployedBrokerHashes
	t.Cleanup(func() {
		readLiveArchiveState, readClusterSingletons, readLiveInfra, lookupDeployedBrokerHashes = prevArchive, prevSingle, prevInfra, prevHashes
	})
	readLiveArchiveState = func(context.Context, string, string) (clusterArchiveState, error) { return live, nil }
	readClusterSingletons = func(context.Context, string, string, string) (clusterSingletons, error) {
		return clusterSingletons{}, nil
	}
	readLiveInfra = func(context.Context, string, string) (liveInfra, error) { return infra, nil }
	lookupDeployedBrokerHashes = func(context.Context, string, string, string) natsauth.DeployedHashes { return hashes }
}

func healthyInfra(jsSize, storeSize string) liveInfra {
	return liveInfra{Broker: aBroker(3, jsSize), EventStore: anEventStore(3, storeSize, "tsdb:1"),
		StoreImages: imagesOf(3, "tsdb:1")}
}

func valuesAttr(t *testing.T, docs ...map[string]interface{}) map[string]interface{} {
	t.Helper()
	var list []interface{}
	for _, d := range docs {
		b, err := yaml.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, string(b))
	}
	return map[string]interface{}{"values": list}
}

func releaseChange(t *testing.T, address, typ string, actions tfjson.Actions, before, after map[string]interface{}) *tfjson.ResourceChange {
	t.Helper()
	rc := &tfjson.ResourceChange{Address: address, Type: typ, Change: &tfjson.Change{Actions: actions}}
	if before != nil {
		rc.Change.Before = valuesAttr(t, before)
	}
	if after != nil {
		rc.Change.After = valuesAttr(t, after)
	}
	return rc
}

func notStated(string) bool { return false }

// nobody is a plan judged with no value declared by the operator and none passed by dcctl.
var nobody = operatorInputs{declared: notStated, passed: notStated}

// --- bootstrap is unchanged ---------------------------------------------------

// 🔴 THE REFACTOR GUARD. Bootstrap's instance-root vars now come through
// instanceRootVars, and its CA through natsCA rather than the mint. Both must leave
// what a bootstrap passes EXACTLY as it was. The literals below were not typed: they
// were printed by running the base commit's assembly — splitVars(infraVars(st)) plus
// the archive vars, as applyInfra built them — over these same two States. One var
// has been added since, on purpose: event_management_replicas (persistenceTopology),
// 2 for the --ha State and 1 for the compact one.
func TestBootstrapInstanceRootVarsAreUnchanged(t *testing.T) {
	full := func() *State {
		rec := aCompleteInstall()
		rec.Settings.HA = true
		return &State{Instance: "prod", KubeContext: "gke_project_zone_cluster", HA: true, Install: &rec,
			NATSTLS: &natsTLSMaterial{CACertPEM: "-----CA-----"},
			Values: map[string]string{"natsCA": "-----CA-----", "natsCalloutIssuerPublic": "AISSUERPUBLIC",
				"natsServicePasswordBcrypt": "$2a$10$service", "natsSysPasswordBcrypt": "$2a$10$sys",
				"backupServerNameTsdb": "dc-tsdb-prod-1a2b3c4d"}}
	}
	compactLocal := func() *State {
		rec := aCompleteInstall()
		rec.Settings.Compact = true
		return &State{Instance: "dev", KubeContext: "kind-devicechain", Compact: true, Install: &rec,
			NATSTLS: &natsTLSMaterial{CACertPEM: "-----CA-----"},
			Values: map[string]string{"natsCA": "-----CA-----", "natsCalloutIssuerPublic": "AISSUERPUBLIC",
				"natsServicePasswordBcrypt": "$2a$10$service", "backupServerNameTsdb": "dc-tsdb-dev-1a2b3c4d"}}
	}
	for name, c := range map[string]struct {
		st   *State
		want []string
	}{
		"full":         {full(), []string{"kubeconfig_context=gke_project_zone_cluster", "timescale_database=prod", "instance_namespace=dci-prod", "nats_ca_cert_pem=-----CA-----", "ha=true", "nats_cluster_replicas=3", "event_management_replicas=2", "database_node_selector={}", "database_tolerations=[]", "backup_server_name_tsdb=dc-tsdb-prod-1a2b3c4d", "nats_enable_auth=true", "nats_callout_issuer_public=AISSUERPUBLIC", "nats_service_password_bcrypt=$2a$10$service", "nats_sys_password_bcrypt=$2a$10$sys", "backup_endpoint_url=http://dc-object-store.dc-system:9000", "backup_credentials_secret=dc-object-store-credentials", "backup_access_key_id_key=MINIO_ROOT_USER", "backup_secret_access_key_key=MINIO_ROOT_PASSWORD", "backup_bucket_tsdb=devicechain-tsdb"}},
		"compactLocal": {compactLocal(), []string{"kubeconfig_context=kind-devicechain", "timescale_database=dev", "instance_namespace=dci-dev", "nats_ca_cert_pem=-----CA-----", "ha=false", "nats_cluster_replicas=1", "event_management_replicas=1", "nats_mqtt_node_port=31883", "database_node_selector={}", "database_tolerations=[]", "nats_jetstream_storage=3Gi", "timescale_storage=4Gi", "nats_prom_exporter=false", "nats_cpu_request=25m", "nats_memory_request=64Mi", "backup_server_name_tsdb=dc-tsdb-dev-1a2b3c4d", "nats_enable_auth=true", "nats_callout_issuer_public=AISSUERPUBLIC", "nats_service_password_bcrypt=$2a$10$service", "backup_endpoint_url=http://dc-object-store.dc-system:9000", "backup_credentials_secret=dc-object-store-credentials", "backup_access_key_id_key=MINIO_ROOT_USER", "backup_secret_access_key_key=MINIO_ROOT_PASSWORD", "backup_bucket_tsdb=devicechain-tsdb"}},
	} {
		got, err := instanceRootVars(c.st)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: a bootstrap's instance-root vars moved:\n got %q\nwant %q", name, got, c.want)
		}
	}
}

// --- the wiring, end to end ---------------------------------------------------

// 🔴 TESTING settleUpgradeInfraInputs ALONE CANNOT SEE WHETHER AN UPGRADE'S STATE EVER
// REACHES IT WITH WHAT IT NEEDS. This runs the real hydration over a cluster holding
// what an install and a bootstrap wrote, then the real settle, then the real var
// assembly, and asserts the inputs an upgrade's apply runs with, by value: broker auth on
// with the running issuer and the broker's OWN hashes, the running CA, the live archive
// path, and the live volume sizes.
func TestTheUpgradeHandsTheApplyTheRunningBrokerArchiveAndVolumes(t *testing.T) {
	provider, err := Get("local")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	deployed := &config.InstanceConfiguration{}
	deployed.Infrastructure.Nats.Tls.Enabled = true
	deployed.Infrastructure.Nats.Tls.Ca = "-----RUNNING CA-----"
	deployed.Infrastructure.Nats.Auth.CalloutIssuerSeed = creds.IssuerSeed
	deployed.Infrastructure.Nats.Auth.Password = creds.ServicePassword
	deployed.Infrastructure.Nats.Auth.SysPassword = creds.SysPassword

	prevDecl, prevDeployed := readInstanceDeclaration, lookupDeployedInstance
	t.Cleanup(func() { readInstanceDeclaration, lookupDeployedInstance = prevDecl, prevDeployed })
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return atVersion(t, "ghcr.io/devicechain-io", "v0.17.0"), nil
	}
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		return deployed, nil
	}
	written := aWritableState()
	written.Instance = "prod"
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "kube-system", UID: types.UID(testClusterUID)}})
	writeInstallThenBootstrapSecrets(t, c, written)
	settleStringDataLikeAnAPIServer(t, c)
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	stubOperatorCheck(t, nil)
	st, err := hydrateUpgradeState(context.Background(), c, provider,
		ClusterBinding{KubeContext: "gke_p_z_c", Cluster: "devicechain"},
		UpgradeOptions{Options: Options{Instance: "prod"}})
	if err != nil {
		t.Fatal(err)
	}

	stubUpgradeReads(t, clusterArchiveState{Exists: true, Path: "dc-tsdb-prod-restored-20260101T000000Z"},
		healthyInfra("12Gi", "8Gi"),
		natsauth.DeployedHashes{Service: creds.ServicePasswordBcrypt, Sys: creds.SysPasswordBcrypt})
	if err := settleUpgradeInfraInputs(context.Background(), st); err != nil {
		t.Fatalf("settling the upgrade's infrastructure inputs: %v", err)
	}
	vars, err := instanceRootVars(st)
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, v := range vars {
		has[v] = true
	}
	for _, want := range []string{
		"nats_enable_auth=true",
		"nats_callout_issuer_public=" + creds.IssuerPublic,
		// The broker's OWN strings: a fresh hash of the same password would change its
		// ConfigMap and restart every server for nothing.
		"nats_service_password_bcrypt=" + creds.ServicePasswordBcrypt,
		"nats_sys_password_bcrypt=" + creds.SysPasswordBcrypt,
		"nats_ca_cert_pem=-----RUNNING CA-----",
		"backup_server_name_tsdb=dc-tsdb-prod-restored-20260101T000000Z",
		"nats_jetstream_storage=12Gi",
		"timescale_storage=8Gi",
	} {
		if !has[want] {
			t.Errorf("an upgrade's apply would run without %q:\n%s", want, strings.Join(vars, "\n"))
		}
	}
}

// 🔴 AN UPGRADE MINTS NOTHING. An instance running with no system-account password must
// come out of the settle with none — not with one generated for it.
func TestSettleMintsNoSystemPasswordForAnInstanceWithout(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Instance: "prod", KubeContext: "gke_p_z_c", Values: map[string]string{
		"natsCA": "ca", "natsCalloutIssuerSeed": creds.IssuerSeed, "natsServicePassword": creds.ServicePassword}}
	stubUpgradeReads(t, clusterArchiveState{Exists: true}, healthyInfra("16Gi", "32Gi"),
		natsauth.DeployedHashes{Service: creds.ServicePasswordBcrypt})
	if err := settleUpgradeInfraInputs(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if st.Values["natsSysPassword"] != "" || st.Values["natsSysPasswordBcrypt"] != "" {
		t.Errorf("the upgrade minted a system-account password (%q) for an instance that had none",
			st.Values["natsSysPassword"])
	}
	for _, v := range infraVars(st) {
		if strings.HasPrefix(v, "nats_sys_password_bcrypt=") {
			t.Errorf("the apply would be handed %s", v)
		}
	}
}

// A release whose values the plan cannot know is not judged as an empty release.
func TestThePlanGateRefusesValuesItCannotSee(t *testing.T) {
	rc := releaseChange(t, tsdbReleaseAddress, "helm_release", tfjson.Actions{tfjson.ActionUpdate},
		map[string]interface{}{"backup": map[string]interface{}{"enabled": true, "retentionPolicy": "7d"}}, nil)
	rc.Change.AfterUnknown = map[string]interface{}{"values": []interface{}{false, true}}
	_, err := reviewUpgradePlan("prod", &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{rc}}, nobody)
	if err == nil || !strings.Contains(err.Error(), "until it applies them") {
		t.Errorf("unknown values were judged rather than refused: %v", err)
	}
}

func TestSettleRefusesWhatCannotBeAppliedSafely(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	base := func() *State {
		return &State{Instance: "prod", KubeContext: "gke_p_z_c", Values: map[string]string{
			"natsCA": "ca", "natsCalloutIssuerSeed": creds.IssuerSeed, "natsServicePassword": creds.ServicePassword}}
	}
	// Short a server on the template every server already runs: a lost node, a crash
	// nobody's change caused. An apply would add a restart and fix nothing.
	unhealthyBroker := healthyInfra("16Gi", "32Gi")
	unhealthyBroker.Broker.Status.ReadyReplicas = 2
	unhealthyBroker.BrokerPods = []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "dc-nats-2"},
		Status: corev1.PodStatus{Phase: corev1.PodPending}}}
	degradedStore := healthyInfra("16Gi", "32Gi")
	_ = unstructured.SetNestedField(degradedStore.EventStore.Object, "Failing over", "status", "phase")
	noTemplate := healthyInfra("", "32Gi")

	for _, c := range []struct {
		name  string
		st    func() *State
		live  clusterArchiveState
		infra liveInfra
		want  []string
	}{
		{"no broker authority", func() *State { s := base(); delete(s.Values, "natsCA"); return s },
			clusterArchiveState{Exists: true}, healthyInfra("16Gi", "32Gi"),
			[]string{"no broker certificate authority", "--skip-infrastructure", "Nothing has been changed"}},
		{"no event store", base, clusterArchiveState{}, healthyInfra("16Gi", "32Gi"),
			[]string{"has no event store", "Nothing has been changed"}},
		{"broker short a server on its current template", base, clusterArchiveState{Exists: true}, unhealthyBroker,
			[]string{"broker is not healthy", "dc-nats-2 Pending", "Nothing has been changed"}},
		{"store failing over", base, clusterArchiveState{Exists: true}, degradedStore,
			[]string{"event store is not healthy", "Failing over", "Nothing has been changed"}},
		{"no JetStream template", base, clusterArchiveState{Exists: true}, noTemplate,
			[]string{"no volume claim template \"dc-nats-js\""}},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubUpgradeReads(t, c.live, c.infra, natsauth.DeployedHashes{})
			err := settleUpgradeInfraInputs(context.Background(), c.st())
			if err == nil {
				t.Fatal("settled, want a refusal")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal does not say %q:\n%v", w, err)
				}
			}
		})
	}
}

// --- volumes ------------------------------------------------------------------

func TestUpgradeKeepsTheLiveVolumeSizes(t *testing.T) {
	got, err := liveVolumesFrom(aBroker(3, "10Gi"), anEventStore(3, "8Gi", "img"))
	if err != nil {
		t.Fatal(err)
	}
	if got != (liveVolumes{JetStream: "10Gi", EventStore: "8Gi"}) {
		t.Fatalf("liveVolumesFrom = %+v", got)
	}
	render := func(st *State) map[string]string {
		out := map[string]string{}
		for _, v := range infraVars(st) {
			if k, val, ok := strings.Cut(v, "="); ok && (k == "nats_jetstream_storage" || k == "timescale_storage") {
				out[k] = val
			}
		}
		return out
	}
	for name, c := range map[string]struct {
		st   *State
		want map[string]string
	}{
		"full size, grown volumes": {&State{Instance: "p", LiveVolumes: got, Values: map[string]string{}},
			map[string]string{"nats_jetstream_storage": "10Gi", "timescale_storage": "8Gi"}},
		"compact, live beats compact": {&State{Instance: "p", Compact: true, LiveVolumes: got, Values: map[string]string{}},
			map[string]string{"nats_jetstream_storage": "10Gi", "timescale_storage": "8Gi"}},
		// 🔑 The canonical read-back of the default is the default's own spelling, so a
		// volume that did not change does not change the broker's file-store ceiling.
		"full size, at the defaults": {&State{Instance: "p", LiveVolumes: liveVolumes{JetStream: "16384Mi", EventStore: "32Gi"},
			Values: map[string]string{}}, map[string]string{}},
		"compact, at compact's sizes": {&State{Instance: "p", Compact: true, LiveVolumes: liveVolumes{JetStream: "3072Mi", EventStore: "4Gi"},
			Values: map[string]string{}}, map[string]string{"nats_jetstream_storage": "3Gi", "timescale_storage": "4Gi"}},
	} {
		if g := render(c.st); !reflect.DeepEqual(g, c.want) {
			t.Errorf("%s: volume vars = %v, want %v", name, g, c.want)
		}
	}

	if _, err := liveVolumesFrom(aBroker(3, "10Gi"), anEventStore(3, "", "img")); err == nil ||
		!strings.Contains(err.Error(), "spec.storage.size") {
		t.Errorf("a Cluster with no size was not refused by name: %v", err)
	}
}

func TestSayKeptVolumesNamesTheSizeThisReleaseWouldCreate(t *testing.T) {
	lines := sayKeptVolumes(&State{LiveVolumes: liveVolumes{JetStream: "16Gi", EventStore: "8Gi"}})
	if len(lines) != 1 || !strings.Contains(lines[0], "event store volume kept at 8Gi") ||
		!strings.Contains(lines[0], "32Gi") {
		t.Errorf("the kept event store volume was not reported against the release's 32Gi: %q", lines)
	}
}

// --- a restored store's recovery source ---------------------------------------

func TestUpgradeCarriesARestoredStoresRecoverySource(t *testing.T) {
	stateWith := func(restore map[string]interface{}) *tfjson.State {
		docs := []map[string]interface{}{{"name": TsdbClusterName}}
		if restore != nil {
			docs = append(docs, map[string]interface{}{"restore": restore})
		}
		attrs := valuesAttr(t, docs...)
		return &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{
			ChildModules: []*tfjson.StateModule{{Resources: []*tfjson.StateResource{{
				Address: tsdbReleaseAddress, AttributeValues: attrs}}}}}}}
	}
	got, err := carriedRestoreVars(stateWith(map[string]interface{}{"enabled": true, "sourceServerName": "dc-tsdb-old",
		"recoveryTarget": map[string]interface{}{"targetTime": "2026-09-01 10:00:00+00"}}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"restore_tsdb_from=dc-tsdb-old", "restore_tsdb_target_time=2026-09-01 10:00:00+00"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("carried = %q, want %q", got, want)
	}
	if got, err := carriedRestoreVars(stateWith(nil)); err != nil || got != nil {
		t.Errorf("a store that was never restored carries %q (%v), want nothing", got, err)
	}
	_, err = carriedRestoreVars(stateWith(map[string]interface{}{"enabled": true, "sourceServerName": "x",
		"recoveryTarget": map[string]interface{}{"targetLSN": "0/3000000"}}))
	if err == nil || !strings.Contains(err.Error(), "targetLSN") {
		t.Errorf("a recovery target dcctl cannot re-pass was not refused by name: %v", err)
	}
	bad := &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{Resources: []*tfjson.StateResource{{
		Address: tsdbReleaseAddress, AttributeValues: map[string]interface{}{"values": []interface{}{"{not: [yaml"}}}}}}}
	if _, err := carriedRestoreVars(bad); err == nil {
		t.Error("undecodable release values read as no restore")
	}
}

// --- the plan gate ------------------------------------------------------------

func TestThePlanGateRefusesDeletesReplacementsAndAMissingState(t *testing.T) {
	upd := tfjson.Actions{tfjson.ActionUpdate}
	for _, c := range []struct {
		name    string
		rc      *tfjson.ResourceChange
		refused string
	}{
		{"update", &tfjson.ResourceChange{Address: "module.nats.kubernetes_config_map_v1.nats_ca[0]", Type: "kubernetes_config_map_v1", Change: &tfjson.Change{Actions: upd}}, ""},
		{"create", &tfjson.ResourceChange{Address: "module.nats.kubernetes_service_v1.mqtt_nodeport[0]", Type: "kubernetes_service_v1", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionCreate}}}, ""},
		{"no-op", &tfjson.ResourceChange{Address: natsReleaseAddress, Type: "helm_release", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}}}, ""},
		{"read", &tfjson.ResourceChange{Address: "data.kubernetes_resources.x", Type: "kubernetes_resources", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionRead}}}, ""},
		{"guard replaced", &tfjson.ResourceChange{Address: "terraform_data.restore_guard[0]", Type: "terraform_data", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionDelete, tfjson.ActionCreate}}}, ""},
		{"delete", &tfjson.ResourceChange{Address: natsReleaseAddress, Type: "helm_release", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionDelete}}}, "would delete or replace"},
		{"delete then create", &tfjson.ResourceChange{Address: tsdbReleaseAddress, Type: "helm_release", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionDelete, tfjson.ActionCreate}}}, "would delete or replace"},
		{"create then delete", &tfjson.ResourceChange{Address: "module.nats.kubernetes_config_map_v1.nats_ca[0]", Type: "kubernetes_config_map_v1", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionCreate, tfjson.ActionDelete}}}, "would delete or replace"},
		{"no change recorded", &tfjson.ResourceChange{Address: natsReleaseAddress, Type: "helm_release"}, "cannot read"},
		{"create the broker", &tfjson.ResourceChange{Address: natsReleaseAddress, Type: "helm_release", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionCreate}}}, "machine that bootstrapped"},
		{"create the store", &tfjson.ResourceChange{Address: tsdbReleaseAddress, Type: "helm_release", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionCreate}}}, "holds no infrastructure state"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := reviewUpgradePlan("prod", &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{c.rc}}, nobody)
			switch {
			case c.refused == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.refused != "" && err == nil:
				t.Fatalf("passed, want a refusal saying %q", c.refused)
			case c.refused != "":
				for _, w := range []string{c.refused, c.rc.Address, "--skip-infrastructure"} {
					if c.name == "no change recorded" && w == "--skip-infrastructure" {
						continue
					}
					if !strings.Contains(err.Error(), w) {
						t.Errorf("the refusal does not say %q:\n%v", w, err)
					}
				}
			}
		})
	}
}

func TestThePlanGateRefusesLosingARecoveryWindowOrAReader(t *testing.T) {
	store := func(retention string, readers ...string) map[string]interface{} {
		roles := []interface{}{map[string]interface{}{"name": "analytics_reader", "login": false}}
		for _, r := range readers {
			roles = append(roles, map[string]interface{}{"name": r, "login": true})
		}
		return map[string]interface{}{"imageName": "tsdb:1", "instances": 3, "extraRoles": roles,
			"backup": map[string]interface{}{"enabled": true, "retentionPolicy": retention}}
	}
	noBackups := map[string]interface{}{"imageName": "tsdb:1", "instances": 3,
		"extraRoles": []interface{}{map[string]interface{}{"name": "analytics_reader", "login": false}}}
	is := func(name string) func(string) bool { return func(n string) bool { return n == name } }
	stated := func(name string) operatorInputs { return operatorInputs{declared: is(name), passed: notStated} }
	// 🔴 DECLARED AND ALSO PASSED BY dcctl: the -var wins over the file, so the operator's
	// value never reaches the apply and must not be counted as stated.
	overridden := func(name string) operatorInputs { return operatorInputs{declared: is(name), passed: is(name)} }
	upd := tfjson.Actions{tfjson.ActionUpdate}

	for _, c := range []struct {
		name          string
		before, after map[string]interface{}
		stated        operatorInputs
		refused       string
	}{
		{"30d to 7d, set by hand", store("30d"), store("7d"), nobody, "backup_retention_tsdb"},
		{"30d to 7d, stated", store("30d"), store("7d"), stated("backup_retention_tsdb"), ""},
		{"30d to 7d, stated but overridden by dcctl", store("30d"), store("7d"), overridden("backup_retention_tsdb"), "backup_retention_tsdb"},
		{"keep-all to 7d", store(""), store("7d"), nobody, "backup_retention_tsdb"},
		{"7d to 30d", store("7d"), store("30d"), nobody, ""},
		{"backups off", store("7d"), noBackups, nobody, "switched off"},
		// No declaration gets this through: dcctl decides enable_database_backups.
		{"backups off, stated", store("7d"), noBackups, stated("enable_database_backups"), "no setting in terraform.tfvars"},
		{"a reader dropped", store("7d", "analytics_acme"), store("7d"), nobody, "timescale_analytics_readers"},
		{"a reader dropped, stated", store("7d", "analytics_acme"), store("7d"), stated("timescale_analytics_readers"), ""},
		{"a reader dropped, stated but overridden by dcctl", store("7d", "analytics_acme"), store("7d"), overridden("timescale_analytics_readers"), "timescale_analytics_readers"},
		{"a reader kept", store("7d", "analytics_acme"), store("7d", "analytics_acme"), nobody, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
				releaseChange(t, tsdbReleaseAddress, "helm_release", upd, c.before, c.after)}}
			_, err := reviewUpgradePlan("prod", plan, c.stated)
			switch {
			case c.refused == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.refused != "" && err == nil:
				t.Fatalf("passed, want a refusal naming %q", c.refused)
			case c.refused != "" && !strings.Contains(err.Error(), c.refused):
				t.Errorf("the refusal does not name %q:\n%v", c.refused, err)
			}
		})
	}
}

func TestThePlanGateRefusesAnImageAndSettingsChangeTogether(t *testing.T) {
	store := func(image, wal string, instances int) map[string]interface{} {
		return map[string]interface{}{"imageName": image, "instances": instances,
			"parameters": map[string]interface{}{"wal_compression": wal}}
	}
	upd := tfjson.Actions{tfjson.ActionUpdate}
	plan := func(b, a map[string]interface{}) *tfjson.Plan {
		return &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{releaseChange(t, tsdbReleaseAddress, "helm_release", upd, b, a)}}
	}
	_, err := reviewUpgradePlan("prod", plan(store("tsdb:1", "", 3), store("tsdb:2", "lz4", 3)), nobody)
	if err == nil || !strings.Contains(err.Error(), `timescale_image = "tsdb:1"`) {
		t.Errorf("an image and settings change on a three-instance store was not refused with the order out: %v", err)
	}
	for name, p := range map[string]*tfjson.Plan{
		"one instance":  plan(store("tsdb:1", "", 1), store("tsdb:2", "lz4", 1)),
		"settings only": plan(store("tsdb:1", "", 3), store("tsdb:1", "lz4", 3)),
		"image only":    plan(store("tsdb:1", "lz4", 3), store("tsdb:2", "lz4", 3)),
	} {
		if _, err := reviewUpgradePlan("prod", p, nobody); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

// 🔴 THE PLAN IS PRINTED, AND THE BROKER'S VALUES CARRY ITS HASHES. What moves is named;
// a value is shown only for the settings on the allowlist.
func TestThePlanSaysWhatMovesAndNeverPrintsASecret(t *testing.T) {
	before := map[string]interface{}{"config": map[string]interface{}{"merge": map[string]interface{}{
		"authorization": "$2a$10$OLDHASHOLDHASH"}}}
	after := map[string]interface{}{
		"config": map[string]interface{}{"merge": map[string]interface{}{"authorization": "$2a$10$NEWHASHNEWHASH"}},
		"container": map[string]interface{}{"resources": map[string]interface{}{
			"limits": map[string]interface{}{"memory": "2Gi"}}},
	}
	review, err := reviewUpgradePlan("prod", &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		releaseChange(t, natsReleaseAddress, "helm_release", tfjson.Actions{tfjson.ActionUpdate}, before, after)}}, nobody)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(review.Lines, "\n")
	if !strings.Contains(all, "the message broker: container.resources.limits.memory (unset) -> 2Gi") {
		t.Errorf("the broker's new memory limit is not named with its value:\n%s", all)
	}
	if !strings.Contains(all, "config.merge.authorization (changed)") {
		t.Errorf("the changed auth block is not named:\n%s", all)
	}
	if strings.Contains(all, "HASH") {
		t.Errorf("the plan summary prints a credential:\n%s", all)
	}
}

func TestALoweredBrokerLimitIsReportedLoudly(t *testing.T) {
	res := func(limit string) map[string]interface{} {
		return map[string]interface{}{"container": map[string]interface{}{"resources": map[string]interface{}{
			"limits": map[string]interface{}{"memory": limit}}}}
	}
	review, err := reviewUpgradePlan("prod", &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		releaseChange(t, natsReleaseAddress, "helm_release", tfjson.Actions{tfjson.ActionUpdate}, res("4Gi"), res("2Gi"))}}, nobody)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Warnings) != 1 || !strings.Contains(review.Warnings[0], "limits.memory is LOWERED from 4Gi to 2Gi") {
		t.Errorf("a lowered broker limit was not reported: %q", review.Warnings)
	}
	if !strings.Contains(review.Warnings[0], "set nats_memory_limit in the terraform.tfvars") {
		t.Errorf("the warning does not name the variable to set: %q", review.Warnings)
	}
}

// 🔴 THE ADVICE MUST BE ONE THE APPLY HONOURS. On a compact instance dcctl passes the
// broker's requests itself with -var, which overrides terraform.tfvars, so telling the
// operator to set them there is advice that is read and then silently ignored. The plan
// is judged through planGated, so the "passed" half is the vars this apply really runs
// with, not a stub.
func TestALoweredCompactBrokerRequestDoesNotSendTheOperatorToTfvars(t *testing.T) {
	req := func(cpu string) map[string]interface{} {
		return map[string]interface{}{"container": map[string]interface{}{"resources": map[string]interface{}{
			"requests": map[string]interface{}{"cpu": cpu}}}}
	}
	plan := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		releaseChange(t, natsReleaseAddress, "helm_release", tfjson.Actions{tfjson.ActionUpdate}, req("100m"), req("25m"))}}
	rec := aCompleteInstall()
	compactVars, err := instanceRootVars(&State{Instance: "prod", KubeContext: "gke_p_z_c", Compact: true,
		Install: &rec, Values: map[string]string{"natsCA": "ca"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		vars []string
		want string
		not  string
	}{
		"compact":   {compactVars, "dcctl sets nats_cpu_request itself", "set nats_cpu_request in the terraform.tfvars"},
		"full size": {nil, "set nats_cpu_request in the terraform.tfvars", "dcctl sets"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := planGated(context.Background(), &fakeUpgradeTofu{plans: []*tfjson.Plan{plan}}, t.TempDir(), c.vars, "prod")
			if err != nil {
				t.Fatal(err)
			}
			w := strings.Join(p.review.Warnings, "\n")
			if !strings.Contains(w, "requests.cpu is LOWERED from 100m to 25m") || !strings.Contains(w, c.want) || strings.Contains(w, c.not) {
				t.Errorf("want a warning saying %q and not %q, got:\n%s", c.want, c.not, w)
			}
		})
	}
}

func TestStatedByOperatorReadsWhatTofuLoadsOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	if statedByOperator(dir, "backup_retention_tsdb") {
		t.Fatal("an empty directory states a value")
	}
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars"), []byte("backup_retention_tsdb = \"30d\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !statedByOperator(dir, "backup_retention_tsdb") {
		t.Error("a value in terraform.tfvars was not seen")
	}
	if statedByOperator(dir, "backup_retention") {
		t.Error("a different variable's prefix was read as this one")
	}
	if err := os.WriteFile(filepath.Join(dir, "x.auto.tfvars.json"), []byte("{\n  \"timescale_analytics_readers\": []\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !statedByOperator(dir, "timescale_analytics_readers") {
		t.Error("a value in an .auto.tfvars.json was not seen")
	}
	// A .json form on one line is still a declaration: OpenTofu reads it as JSON.
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars.json"), []byte(`{"backup_retention_tsdb_x":"1d","nats_cpu_request":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !statedByOperator(dir, "nats_cpu_request") {
		t.Error("a value in a single-line terraform.tfvars.json was not seen")
	}
	if statedByOperator(dir, "backup_retention") {
		t.Error("a different variable's prefix was read as this one in a .json file")
	}
	t.Setenv("TF_VAR_nats_memory_limit", "4Gi")
	if !statedByOperator(dir, "nats_memory_limit") {
		t.Error("TF_VAR_ was not seen")
	}
}

// --- health -------------------------------------------------------------------

func TestStatefulSetRolledOutRejectsStatesTheNaiveCheckAccepts(t *testing.T) {
	for name, mutate := range map[string]func(*appsv1.StatefulSet){
		"revision not yet current":   func(s *appsv1.StatefulSet) { s.Status.UpdateRevision = "dc-nats-new" },
		"controller has not seen it": func(s *appsv1.StatefulSet) { s.Generation = 5 },
		"not every server updated":   func(s *appsv1.StatefulSet) { s.Status.UpdatedReplicas = 2 },
		"a server not ready":         func(s *appsv1.StatefulSet) { s.Status.ReadyReplicas = 2 },
	} {
		s := aBroker(3, "16Gi")
		mutate(s)
		if statefulSetRolledOut(s) {
			t.Errorf("%s: read as rolled out", name)
		}
	}
	if !statefulSetRolledOut(aBroker(3, "16Gi")) {
		t.Error("a finished roll reads as unfinished")
	}
}

func TestEventStoreReadyPredicate(t *testing.T) {
	if ok, why := eventStoreReady(anEventStore(3, "32Gi", "tsdb:1"), imagesOf(3, "tsdb:1")); !ok {
		t.Fatalf("a healthy store reads unhealthy: %s", why)
	}
	for name, c := range map[string]struct {
		mutate func(*unstructured.Unstructured)
		images []string
		reason string
	}{
		"phase": {func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "Upgrading cluster", "status", "phase")
		}, imagesOf(3, "tsdb:1"), "phase"},
		"ready count": {func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, int64(2), "status", "readyInstances")
		}, imagesOf(3, "tsdb:1"), "2 of 3"},
		"switchover": {func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "dc-tsdb-2", "status", "targetPrimary")
		}, imagesOf(3, "tsdb:1"), "primary is moving"},
		"old image":   {func(*unstructured.Unstructured) {}, []string{"tsdb:1", "tsdb:1", "tsdb:0"}, "still runs tsdb:0"},
		"pod missing": {func(*unstructured.Unstructured) {}, imagesOf(2, "tsdb:1"), "2 instance pods"},
		"not ready":   {func(u *unstructured.Unstructured) { setReadyCondition(u, "False", 7) }, imagesOf(3, "tsdb:1"), "Ready condition"},
		"stale judge": {func(u *unstructured.Unstructured) { setReadyCondition(u, "True", 6) }, imagesOf(3, "tsdb:1"), "latest spec"},
	} {
		u := anEventStore(3, "32Gi", "tsdb:1")
		c.mutate(u)
		ok, why := eventStoreReady(u, c.images)
		if ok || !strings.Contains(why, c.reason) {
			t.Errorf("%s: ready=%v reason=%q, want unready naming %q", name, ok, why, c.reason)
		}
	}
	// A Ready condition that never says which spec it judged is not held against it.
	u := anEventStore(3, "32Gi", "tsdb:1")
	setReadyCondition(u, "True", 0)
	if ok, why := eventStoreReady(u, imagesOf(3, "tsdb:1")); !ok {
		t.Errorf("a Ready condition with no observedGeneration failed the store: %s", why)
	}
}

func setReadyCondition(u *unstructured.Unstructured, status string, observed int64) {
	cond := map[string]interface{}{"type": "Ready", "status": status}
	if observed != 0 {
		cond["observedGeneration"] = observed
	}
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{cond}, "status", "conditions")
}

// 🔴 ONE SERVER IS NOT A DEGRADED BROKER. A timed-out wait over a single server that is
// Pending must say the broker is DOWN, and name the pod.
func TestTheBrokerWaitNamesThePodAndSaysWhenTheBrokerIsDown(t *testing.T) {
	s := aBroker(1, "3Gi")
	s.Status.ReadyReplicas, s.Status.UpdateRevision = 0, "dc-nats-new"
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "dc-nats-0", Namespace: s.Namespace, Labels: map[string]string{"app": "nats"}},
		Status: corev1.PodStatus{Phase: corev1.PodPending}}
	c := fake.NewSimpleClientset(s, pod)
	err := waitForBrokerRollout(context.Background(), c, s.Namespace, 50*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("the wait passed over a broker with no ready server")
	}
	for _, w := range []string{"dc-nats-0 Pending", "DOWN, not degraded"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the timeout does not say %q:\n%v", w, err)
		}
	}
}

func TestEventStoreWaitRequiresReadinessToHold(t *testing.T) {
	calls := 0
	_, err := pollUntil(context.Background(), time.Second, time.Millisecond, 3, func() (bool, string, error) {
		calls++
		// healthy, then a blip, then healthy for good
		return calls != 2, "blip", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 5 {
		t.Errorf("readiness was accepted after %d polls; a blip must restart the count of three", calls)
	}
}

// --- the apply ----------------------------------------------------------------

type fakeUpgradeTofu struct {
	state      *tfjson.State
	plans      []*tfjson.Plan
	planCalls  int
	applyErrs  []error
	applyCalls int
	outputs    map[string]tfexec.OutputMeta
}

func (f *fakeUpgradeTofu) Show(context.Context, ...tfexec.ShowOption) (*tfjson.State, error) {
	return f.state, nil
}
func (f *fakeUpgradeTofu) Plan(context.Context, ...tfexec.PlanOption) (bool, error) {
	f.planCalls++
	return true, nil
}
func (f *fakeUpgradeTofu) ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.Plan, error) {
	i := f.planCalls - 1
	if i >= len(f.plans) {
		i = len(f.plans) - 1
	}
	return f.plans[i], nil
}
func (f *fakeUpgradeTofu) Apply(context.Context, ...tfexec.ApplyOption) error {
	f.applyCalls++
	if f.applyCalls <= len(f.applyErrs) {
		return f.applyErrs[f.applyCalls-1]
	}
	return nil
}
func (f *fakeUpgradeTofu) Output(context.Context, ...tfexec.OutputOption) (map[string]tfexec.OutputMeta, error) {
	return f.outputs, nil
}

func noopWaits(broker, store *int) infraWaits {
	return infraWaits{
		admission: func(context.Context) error { return nil },
		broker:    func(context.Context) error { *broker++; return nil },
		store:     func(context.Context) error { *store++; return nil },
	}
}

const webhookDown = `Internal error occurred: failed calling webhook "mcluster.cnpg.io": failed to call webhook: connection refused`

func TestARehearsalPlansAndNeverApplies(t *testing.T) {
	tf := &fakeUpgradeTofu{plans: []*tfjson.Plan{{}}}
	p, err := planGated(context.Background(), tf, t.TempDir(), nil, "prod")
	if err != nil {
		t.Fatal(err)
	}
	var b, s int
	if err := applyUpgradeInfra(context.Background(), &State{DryRun: true, Values: map[string]string{}}, p, noopWaits(&b, &s)); !errors.Is(err, errRehearsalApplies) {
		t.Errorf("a rehearsal's apply returned %v, want the refusal", err)
	}
	if tf.applyCalls != 0 {
		t.Errorf("a rehearsal applied %d times", tf.applyCalls)
	}
}

func TestTheAdmissionRaceIsReplannedAndRetriedOnce(t *testing.T) {
	tf := &fakeUpgradeTofu{plans: []*tfjson.Plan{{}}, applyErrs: []error{errors.New(webhookDown)}}
	p, err := planGated(context.Background(), tf, t.TempDir(), nil, "prod")
	if err != nil {
		t.Fatal(err)
	}
	var b, s int
	if err := applyUpgradeInfra(context.Background(), &State{Values: map[string]string{}}, p, noopWaits(&b, &s)); err != nil {
		t.Fatal(err)
	}
	if tf.planCalls != 2 || tf.applyCalls != 2 {
		t.Errorf("planned %d and applied %d times, want a fresh judged plan and one retry (2, 2)", tf.planCalls, tf.applyCalls)
	}

	tf = &fakeUpgradeTofu{plans: []*tfjson.Plan{{}}, applyErrs: []error{errors.New(webhookDown), errors.New(webhookDown)}}
	p, _ = planGated(context.Background(), tf, t.TempDir(), nil, "prod")
	if err := applyUpgradeInfra(context.Background(), &State{Values: map[string]string{}}, p, noopWaits(&b, &s)); err == nil {
		t.Error("a second admission failure was retried around")
	}
	if tf.applyCalls != 2 {
		t.Errorf("applied %d times, want the retry once and no more", tf.applyCalls)
	}
}

// 🔴 A RE-RUN AFTER A FAILED ROLL PLANS NOTHING, AND MUST STILL WAIT. The releases
// already carry the new values, so a wait keyed on the plan would move the services
// onto a broker that is still short a server.
func TestAnEmptyPlanStillWaitsForTheBrokerAndTheStore(t *testing.T) {
	tf := &fakeUpgradeTofu{plans: []*tfjson.Plan{{}}, outputs: map[string]tfexec.OutputMeta{
		"database_backups_enabled": {Value: json.RawMessage(`true`)}}}
	p, err := planGated(context.Background(), tf, t.TempDir(), nil, "prod")
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Values: map[string]string{}}
	brokerDown := errors.New("dc-nats-2 Pending")
	waits := infraWaits{
		admission: func(context.Context) error { return nil },
		broker:    func(context.Context) error { return brokerDown },
		store:     func(context.Context) error { return nil },
	}
	if err := applyUpgradeInfra(context.Background(), st, p, waits); !errors.Is(err, brokerDown) {
		t.Errorf("an empty plan over an unready broker returned %v, want the broker wait's failure", err)
	}
	if !st.InfraApplied || st.Values[databaseBackupsKey] != "true" {
		t.Errorf("the apply's outputs were not recorded: applied=%v backups=%q", st.InfraApplied, st.Values[databaseBackupsKey])
	}
	var b, s int
	p, _ = planGated(context.Background(), tf, t.TempDir(), nil, "prod")
	if err := applyUpgradeInfra(context.Background(), &State{Values: map[string]string{}}, p, noopWaits(&b, &s)); err != nil || b != 1 || s != 1 {
		t.Errorf("waits ran broker=%d store=%d (%v), want each exactly once", b, s, err)
	}
}

func TestPrepareUpgradePlanCarriesTheRestoreAndKeepsVolumes(t *testing.T) {
	rec := aCompleteInstall()
	st := &State{Instance: "prod", KubeContext: "gke_p_z_c", Install: &rec,
		LiveVolumes: liveVolumes{JetStream: "16Gi", EventStore: "8Gi"},
		Values:      map[string]string{"natsCA": "ca"}}
	tf := &fakeUpgradeTofu{plans: []*tfjson.Plan{{}}, state: &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{
		Resources: []*tfjson.StateResource{{Address: tsdbReleaseAddress, AttributeValues: valuesAttr(t,
			map[string]interface{}{"restore": map[string]interface{}{"enabled": true, "sourceServerName": "dc-tsdb-old"}})}}}}}}
	p, err := prepareUpgradePlan(context.Background(), st, tf, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.vars, "\n"), "restore_tsdb_from=dc-tsdb-old") {
		t.Errorf("the plan does not carry the store's recovery source:\n%s", strings.Join(p.vars, "\n"))
	}
	if len(p.kept) != 1 || !strings.Contains(p.kept[0], "8Gi") {
		t.Errorf("the kept event store volume is not reported: %q", p.kept)
	}
}

// --- what the Helm step takes ---------------------------------------------------

func TestCarryForwardDoesNotOverrideWhatTheApplyReported(t *testing.T) {
	previous := map[string]interface{}{"metrics": map[string]interface{}{
		"databaseBackups": true, "databaseBackupSnapshots": true}}
	applied := &State{InfraApplied: true, Values: map[string]string{databaseBackupsKey: "false", databaseBackupSnapshotsKey: "false"}}
	carryForwardFromRelease(applied, previous)
	if applied.Values[databaseBackupsKey] != "false" || applied.Values[databaseBackupSnapshotsKey] != "false" {
		t.Errorf("the previous release overrode what this run's apply reported: %v", applied.Values)
	}
	skipped := &State{Values: map[string]string{}}
	carryForwardFromRelease(skipped, previous)
	if skipped.Values[databaseBackupsKey] != "true" || skipped.Values[databaseBackupSnapshotsKey] != "true" {
		t.Errorf("without an apply the release's values were not carried: %v", skipped.Values)
	}
}

func TestTheRehearsalUnderSkipInfrastructureSaysWhatItLeaves(t *testing.T) {
	out := captureStdout(t, func() { sayUpgradeDryRun(&State{SkipInfrastructure: true, Values: map[string]string{}}) })
	if !strings.Contains(out, "--skip-infrastructure") || strings.Contains(out, "would apply this instance's message broker") {
		t.Errorf("the rehearsal under --skip-infrastructure says:\n%s", out)
	}
	out = captureStdout(t, func() {
		sayUpgradeInfrastructure(&State{SkipInfrastructure: true, Provider: "local"}, UpgradeOptions{Options: Options{Instance: "prod"}})
	})
	if !strings.Contains(out, "NOT applied") || !strings.Contains(out, "dcctl upgrade local prod") {
		t.Errorf("a finished upgrade under --skip-infrastructure does not say what it left:\n%s", out)
	}
}
