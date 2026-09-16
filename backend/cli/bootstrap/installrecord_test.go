// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func installClock() time.Time { return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC) }

// aCompleteInstall is a record for a default install: backups in-cluster, the operator
// and monitoring on.
func aCompleteInstall() InstallRecord {
	return InstallRecord{
		ClusterUID:   testClusterUID,
		DcctlVersion: "v0.17.0",
		Settings: InstallSettings{
			Monitoring: true, CNPG: true, CertManager: true, DatabaseBackups: true,
		},
		Outputs: InstallOutputs{
			Archive: InstallArchive{
				EndpointURL:       "http://dc-object-store.dc-system:9000",
				CredentialsSecret: "dc-object-store-credentials",
				AccessKeyIDKey:    "MINIO_ROOT_USER",
				SecretAccessKey:   "MINIO_ROOT_PASSWORD",
				BucketTsdb:        "devicechain-tsdb",
			},
			CNPGNamespace:    "cnpg-system",
			GrafanaService:   "kube-prometheus-stack-grafana",
			GrafanaNamespace: "monitoring",
		},
	}
}

// The contract, by literal: where a bootstrap on any machine looks.
func TestTheInstallRecordLivesWhereEveryMachineCanFindIt(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	cm, err := c.CoreV1().ConfigMaps("dc-system").Get(context.Background(), "dc-install", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the record is not at dc-system/dc-install: %v", err)
	}
	if _, ok := cm.Data["install.json"]; !ok {
		t.Errorf("the record has no install.json entry: %v", cm.Data)
	}
	if cm.Annotations["devicechain.io/managed-by"] != "dcctl" {
		t.Errorf("the record does not say dcctl wrote it: %v", cm.Annotations)
	}
}

func TestACompletedInstallReadsBackWhole(t *testing.T) {
	c := fake.NewSimpleClientset()
	want := aCompleteInstall()
	if err := writeInstalled(context.Background(), c, want, installClock); err != nil {
		t.Fatal(err)
	}
	got, err := readInstallRecord(context.Background(), c, testClusterUID)
	if err != nil {
		t.Fatalf("a completed install could not be read: %v", err)
	}
	if got.Settings != want.Settings || got.Outputs != want.Outputs || got.Phase != "installed" {
		t.Errorf("round trip lost something:\n got %+v\nwant %+v", *got, want)
	}
}

func TestAClusterWithNoRecordIsNotInstalled(t *testing.T) {
	_, err := readInstallRecord(context.Background(), fake.NewSimpleClientset(), testClusterUID)
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("a cluster with no record read as %v, want ErrNotInstalled", err)
	}
}

// 🔴🔴 THE CASE THE PHASE EXISTS FOR. A cluster installed once and then re-installed
// with a failure part-way must not keep reading as installed off the first record.
func TestAFailedReinstallIsNotReadAsInstalled(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	if err := markInstallApplying(context.Background(), c, testClusterUID, "v0.17.1", installClock); err != nil {
		t.Fatal(err)
	}
	// ...and the apply fails here, so nothing writes "installed" again.
	_, err := readInstallRecord(context.Background(), c, testClusterUID)
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("a half-applied re-install read as usable: %v", err)
	}
	if !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("the refusal does not say the install was interrupted: %v", err)
	}
}

// ...and the ordinary path through the same two calls: marking then completing leaves
// a usable record, so the refusal above is about the missing second write and not
// about the first one poisoning the record.
func TestAnInstallThatFinishesIsUsableAfterBeingMarked(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := markInstallApplying(context.Background(), c, testClusterUID, "v0.17.0", installClock); err != nil {
		t.Fatal(err)
	}
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	if _, err := readInstallRecord(context.Background(), c, testClusterUID); err != nil {
		t.Errorf("a finished install is not usable: %v", err)
	}
}

func TestARecordFromAnotherClusterIsRefused(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	_, err := readInstallRecord(context.Background(), c, "446b60a1-5c0e-4a8e-9d8f-2b4a3e6f7c10")
	if err == nil || !strings.Contains(err.Error(), "carried here from elsewhere") {
		t.Errorf("a record naming another cluster was accepted: %v", err)
	}
	if _, err := readInstallRecord(context.Background(), c, ""); err == nil {
		t.Error("a record was accepted without knowing which cluster is asking")
	}
}

func TestARecordDcctlDidNotWriteIsNeitherReadNorOverwritten(t *testing.T) {
	body, _ := json.Marshal(func() InstallRecord {
		r := aCompleteInstall()
		r.Schema, r.Phase = installRecordSchema, installPhaseInstalled
		return r
	}())
	c := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "dc-install", Namespace: "dc-system"},
		Data:       map[string]string{"install.json": string(body)},
	})
	if _, err := readInstallRecord(context.Background(), c, testClusterUID); err == nil {
		t.Error("an unmanaged ConfigMap was trusted as the install record")
	}
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err == nil {
		t.Error("an unmanaged ConfigMap was overwritten")
	}
}

func TestARecordFromAnotherSchemaIsRefused(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	cm, _ := c.CoreV1().ConfigMaps("dc-system").Get(context.Background(), "dc-install", metav1.GetOptions{})
	var rec InstallRecord
	_ = json.Unmarshal([]byte(cm.Data["install.json"]), &rec)
	rec.Schema = installRecordSchema + 1
	body, _ := json.Marshal(rec)
	cm.Data["install.json"] = string(body)
	if _, err := c.CoreV1().ConfigMaps("dc-system").Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := readInstallRecord(context.Background(), c, testClusterUID); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Errorf("a record from a schema this dcctl does not know was read: %v", err)
	}
}

// 🔴 WHAT THE SETTINGS PROMISE, THE OUTPUTS MUST DELIVER — each missing value on its
// own, since every one is a thing an instance would silently build without.
func TestARecordMissingWhatItsSettingsPromiseIsRefused(t *testing.T) {
	for name, mutate := range map[string]func(*InstallRecord){
		"archive endpoint":           func(r *InstallRecord) { r.Outputs.Archive.EndpointURL = "" },
		"archive credentials Secret": func(r *InstallRecord) { r.Outputs.Archive.CredentialsSecret = "" },
		"archive access key name":    func(r *InstallRecord) { r.Outputs.Archive.AccessKeyIDKey = "" },
		"archive secret key name":    func(r *InstallRecord) { r.Outputs.Archive.SecretAccessKey = "" },
		"archive event bucket":       func(r *InstallRecord) { r.Outputs.Archive.BucketTsdb = "" },
		"operator namespace":         func(r *InstallRecord) { r.Outputs.CNPGNamespace = "" },
		"dashboard service":          func(r *InstallRecord) { r.Outputs.GrafanaService = "" },
		"dashboard namespace":        func(r *InstallRecord) { r.Outputs.GrafanaNamespace = "" },
	} {
		t.Run(name, func(t *testing.T) {
			rec := aCompleteInstall()
			mutate(&rec)
			if err := writeInstalled(context.Background(), fake.NewSimpleClientset(), rec, installClock); err == nil {
				t.Errorf("a record missing its %s was written as installed", name)
			}
		})
	}
}

// The counterweight: with those settings OFF, the same values are legitimately absent,
// so the checks above are about the promise and not about the fields.
func TestAMinimalInstallNeedsNoneOfThoseOutputs(t *testing.T) {
	rec := InstallRecord{ClusterUID: testClusterUID, Settings: InstallSettings{}}
	c := fake.NewSimpleClientset()
	if err := writeInstalled(context.Background(), c, rec, installClock); err != nil {
		t.Fatalf("an install with no backups, operator or monitoring was refused: %v", err)
	}
	if _, err := readInstallRecord(context.Background(), c, testClusterUID); err != nil {
		t.Errorf("it could not be read back: %v", err)
	}
}

// "Could not tell" is never "not installed" — that answer sends an operator to
// re-install a cluster that may be serving instances.
func TestAnUnreadableRecordIsNotReportedAsAbsent(t *testing.T) {
	c := fake.NewSimpleClientset()
	c.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the server is unavailable")
	})
	_, err := readInstallRecord(context.Background(), c, testClusterUID)
	if err == nil || errors.Is(err, ErrNotInstalled) {
		t.Errorf("an unreadable record read as %v", err)
	}
	// ...and it says WHY. A mutation round found this passing with the read error thrown
	// away, because the empty object the client returned was then refused for a
	// different reason — "not written by dcctl" — which sends an operator to inspect a
	// ConfigMap that is fine.
	if err != nil && !strings.Contains(err.Error(), "the server is unavailable") {
		t.Errorf("the refusal does not carry the read failure: %v", err)
	}
}

// The settings come from the predicates the OpenTofu variables are emitted from.
func TestTheRecordedSettingsFollowTheAppliedVariables(t *testing.T) {
	st := &State{Instance: "a", Compact: true, NoTLS: true, HA: true}
	got := installSettingsFor(st)
	want := InstallSettings{HA: true, Compact: true, Monitoring: true, CNPG: true}
	if got != want {
		t.Errorf("compact + no-tls recorded %+v, want %+v (cert-manager and backups go together)", got, want)
	}

	st = aWritableState()
	st.BackupDestination = &BackupDestination{EndpointURL: "https://example.invalid", BucketRdb: "a", BucketTsdb: "b", AccessKeyID: "k", SecretAccessKey: "s"}
	if got := installSettingsFor(st); !got.DatabaseBackups || !got.BackupsExternal {
		t.Errorf("an external destination recorded %+v", got)
	}
}

// 🔴 NO CREDENTIAL IN THE RECORD. It is a ConfigMap, readable by anyone who can list
// them in dc-system; it names where the credentials are and must never hold one.
func TestTheInstallRecordHoldsNoCredential(t *testing.T) {
	st := aWritableState()
	st.ClusterUID = testClusterUID
	recordClusterOutputs(st, nil)
	rec := InstallRecord{ClusterUID: testClusterUID, Settings: installSettingsFor(st),
		Outputs: installOutputsFrom(st, ClusterArchive{})}
	body, _ := json.Marshal(rec)
	for _, secret := range []string{"rdb-pw", "tsdb-pw", "os-user", "os-secret", "grafana-pw"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("the install record carries a credential value (%s)", secret)
		}
	}
}

// A record with no cluster identity is written by nobody who knows where they are.
func TestARecordWithNoClusterIdentityIsNotWritten(t *testing.T) {
	rec := aCompleteInstall()
	rec.ClusterUID = ""
	if err := writeInstalled(context.Background(), fake.NewSimpleClientset(), rec, installClock); err == nil {
		t.Error("an install record naming no cluster was written")
	}
}

// Backups off wins over a supplied destination: a compact plain-HTTP install archives
// nowhere, whatever file was passed, and must not be recorded as archiving off-site.
func TestAnExternalDestinationIsNotRecordedWhenBackupsAreOff(t *testing.T) {
	st := aWritableState()
	st.Compact, st.NoTLS = true, true
	st.BackupDestination = &BackupDestination{EndpointURL: "https://example.invalid", BucketRdb: "a", BucketTsdb: "b", AccessKeyID: "k", SecretAccessKey: "s"}
	if got := installSettingsFor(st); got.DatabaseBackups || got.BackupsExternal {
		t.Errorf("a compact plain-HTTP install recorded %+v; it has no backups at all", got)
	}
}

// The outputs are what the cluster apply RETURNED, carried field for field.
func TestTheRecordedOutputsAreWhatTheClusterApplyReturned(t *testing.T) {
	st := &State{Values: map[string]string{
		cnpgNamespaceKey: "cnpg-system", "grafanaService": "svc", "grafanaNamespace": "monitoring",
		databaseBackupOffsiteKey: "true",
	}}
	got := installOutputsFrom(st, ClusterArchive{
		EndpointURL: "http://e", CredentialsSecret: "s", AccessKeyIDKey: "a", SecretAccessKey: "k", BucketTsdb: "b",
	})
	want := InstallOutputs{
		Archive:                   InstallArchive{EndpointURL: "http://e", CredentialsSecret: "s", AccessKeyIDKey: "a", SecretAccessKey: "k", BucketTsdb: "b"},
		BackupSurvivesClusterLoss: true,
		CNPGNamespace:             "cnpg-system", GrafanaService: "svc", GrafanaNamespace: "monitoring",
	}
	if got != want {
		t.Errorf("recorded outputs\n got %+v\nwant %+v", got, want)
	}
}
