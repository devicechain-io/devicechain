// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// planOwnedSecrets is every Secret a run of this State plans: the cluster half and the
// instance half together.
//
// 🔴 THE INSTANCE CONFIG DOCUMENT IS NOT HERE. The credentials services read travel
// inside one JSON document rather than one Secret each, and that document cannot be
// composed until the values that are derived from an apply are known. It is written
// by the composition step, through the same writer.
func planOwnedSecrets(st *State, set *credentialSet) []ownedSecret {
	cluster, archive := planClusterSecrets(st, set)
	return append(cluster, planInstanceSecrets(st, set, archive)...)
}

// 🔴 THE VARIABLES MUST SAY WHAT THE CLUSTER WAS INSTALLED WITH. infraVars emits
// `enable_database_backups` and `enable_cert_manager` from the predicates, so comparing
// the emission with the predicates proves nothing; this compares both with an answer
// stated here instead. On an install that answer is the flags. On a bootstrap it is
// the install record — and every record below DISAGREES with the State's own flags, so
// an emission that read the flags instead of the record fails here rather than
// switching an instance's archiving off on a cluster whose store is archiving.
func TestTheBackupPredicateMatchesTheVariablesEmitted(t *testing.T) {
	check := func(t *testing.T, label string, st *State, backupsOn, certManagerOn bool) {
		t.Helper()
		vars := infraVars(st)
		if got := databaseBackupsEnabled(st); got != backupsOn {
			t.Errorf("%s: the backup predicate says on=%v, want %v", label, got, backupsOn)
		}
		if off := slices.Contains(vars, "enable_database_backups=false"); off == backupsOn {
			t.Errorf("%s: the variables say backups off=%v, want on=%v", label, off, backupsOn)
		}
		if got := certManagerEnabled(st); got != certManagerOn {
			t.Errorf("%s: the cert-manager predicate says on=%v, want %v", label, got, certManagerOn)
		}
		if off := slices.Contains(vars, "enable_cert_manager=false"); off == certManagerOn {
			t.Errorf("%s: the variables say cert-manager off=%v, want on=%v", label, off, certManagerOn)
		}
	}

	compared := 0
	for _, noCNPG := range []bool{false, true} {
		for _, compact := range []bool{false, true} {
			for _, noTLS := range []bool{false, true} {
				for _, noMonitoring := range []bool{false, true} {
					flags := func() *State {
						return &State{
							Instance: "acme", NoCNPG: noCNPG, Compact: compact,
							NoTLS: noTLS, NoMonitoring: noMonitoring,
						}
					}
					label := fmt.Sprintf("no-cnpg=%v compact=%v no-tls=%v no-monitoring=%v",
						noCNPG, compact, noTLS, noMonitoring)

					install := flags()
					flagBackups := !noCNPG && !(compact && noTLS)
					flagCertManager := !(compact && noTLS)
					check(t, "install "+label, install, flagBackups, flagCertManager)
					if off := slices.Contains(infraVars(install), "enable_monitoring=false"); off != noMonitoring {
						t.Errorf("install %s: the variables say monitoring off=%v", label, off)
					}
					if got := DatabaseBackupsEnabled(noCNPG, compact, noTLS); got != flagBackups {
						t.Errorf("%s: DatabaseBackupsEnabled says %v, want %v", label, got, flagBackups)
					}

					boot := flags()
					boot.Install = &InstallRecord{Settings: InstallSettings{
						DatabaseBackups: !flagBackups, CertManager: !flagCertManager,
					}}
					check(t, "bootstrap "+label, boot, !flagBackups, !flagCertManager)
					compared++
				}
			}
		}
	}
	if compared != 16 {
		t.Fatalf("the matrix compared %d of 16 combinations", compared)
	}
}

// 🔴 NEITHER DATABASE IS GATED ON THE FLAG THAT SKIPS THE OPERATOR INSTALL. That flag
// means "an operator is already running here", so the Clusters are created either
// way — and a run that skipped minting their credentials on that flag would produce
// two databases nothing can log in to.
func TestBothDatabaseCredentialsAreMintedEvenWithoutTheOperator(t *testing.T) {
	st := &State{Instance: "acme", NoCNPG: true}
	set, err := mintNewCredentials(st)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if set.RDBPassword == "" || set.TSDBPassword == "" {
		t.Fatal("a database password was left empty because the operator install was skipped")
	}
	names := secretNames(planOwnedSecrets(st, set))
	for _, want := range []string{"dc-rdb-app-credentials", "dc-tsdb-app-credentials"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s is not in the plan: %v", want, names)
		}
	}
}

// The two stores must never share a password. They are separate roles on separate
// clusters, and one value reaching both is the shape the shared default already had.
func TestTheTwoDatabasesGetDifferentPasswords(t *testing.T) {
	set, err := mintNewCredentials(&State{Instance: "acme"})
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if set.RDBPassword == set.TSDBPassword {
		t.Fatal("both stores were given the same password")
	}
	if set.ObjectStoreSecret == set.RDBPassword || set.GrafanaAdminPassword == set.RDBPassword {
		t.Fatal("a credential was reused across two different systems")
	}
}

// 🔴 THE TEST ABOVE MEASURES THE GENERATOR, AND THE GENERATOR IS NOT WHERE THIS GOES
// WRONG. Two distinct passwords can still be placed into one Secret and the other
// left with a copy — a mutation that does exactly that survived every other case
// here, because nothing compared the values that actually reach the two readers. The
// assertion has to be made on the PLAN, against the set it was built from, or it is
// checking that two random strings differ.
func TestEachDatabaseSecretCarriesItsOwnPassword(t *testing.T) {
	st := &State{Instance: "acme"}
	set, err := mintNewCredentials(st)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	byName := map[string]ownedSecret{}
	for _, s := range planOwnedSecrets(st, set) {
		byName[s.Name] = s
	}

	rdb := byName["dc-rdb-app-credentials"].Data[secretKeyPassword]
	tsdb := byName["dc-tsdb-app-credentials"].Data[secretKeyPassword]

	if rdb == tsdb {
		t.Fatal("both database Secrets carry the same password; one store's credential was " +
			"placed into the other, which is the shape the shared default already had")
	}
	if rdb != set.RDBPassword {
		t.Errorf("the relational store's Secret does not carry the relational store's password")
	}
	if tsdb != set.TSDBPassword {
		t.Errorf("the telemetry store's Secret does not carry the telemetry store's password")
	}
	if set.ObjectStoreSecret != "" {
		obj := byName["dc-object-store-credentials"].Data[keyMinioPassword]
		if obj != set.ObjectStoreSecret {
			t.Errorf("the object store's Secret does not carry the object store's credential")
		}
		if obj == rdb || obj == tsdb {
			t.Errorf("the object store shares a credential with a database")
		}
	}
}

// Each Secret's keys and type are a contract with a reader that is not this code:
// the database operator reads username/password from a basic-auth Secret, and the
// object store's key names are read both by its own container and by the backup
// plugin's reference. A wrong key name is an empty value, not an error.
func TestEachSecretCarriesTheKeysItsReaderExpects(t *testing.T) {
	st := &State{Instance: "acme"}
	set, err := mintNewCredentials(st)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	byName := map[string]ownedSecret{}
	for _, s := range planOwnedSecrets(st, set) {
		byName[s.Name] = s
	}

	for name, wantUser := range map[string]string{
		"dc-rdb-app-credentials":         rdbOwnerUsername,
		"dc-rdb-provisioner-credentials": rdbProvisionerUsername,
		"dc-tsdb-app-credentials":        dbRoleUsername,
	} {
		s, ok := byName[name]
		if !ok {
			t.Fatalf("%s missing from the plan", name)
		}
		if s.Type != corev1.SecretTypeBasicAuth {
			t.Errorf("%s has type %q, want basic-auth", name, s.Type)
		}
		if s.Data[secretKeyUsername] != wantUser {
			t.Errorf("%s username is %q, want %q — the role name is not a secret and a "+
				"value that disagrees with the rest of the install locks the services out",
				name, s.Data[secretKeyUsername], wantUser)
		}
		if s.Data[secretKeyPassword] == "" {
			t.Errorf("%s carries no password", name)
		}
		// The provisioner's is read by dcctl alone, which sets the role's password itself.
		if name != rdbProvisionerSecretName && s.Labels[cnpgReloadLabel] != "true" {
			t.Errorf("%s lacks the reload label, so a credential change would land in the "+
				"Secret while the database kept the old password", name)
		}
		// The shared store's credentials are the cluster's; the event store's, the instance's.
		wantNS := infraNamespace
		if name == "dc-tsdb-app-credentials" {
			wantNS = "acme"
		}
		if s.Namespace != wantNS {
			t.Errorf("%s is planned for namespace %q, want %q", name, s.Namespace, wantNS)
		}
	}

	// The instance's own login: named after it, and read by nothing but dcctl, so no
	// reload label.
	login, ok := byName["dci-acme-rdb-credentials"]
	if !ok {
		t.Fatal("the instance's own relational login is missing from the plan")
	}
	if login.Namespace != "acme" {
		t.Errorf("the instance's login Secret is planned for namespace %q, want the instance's own", login.Namespace)
	}
	if login.Data[secretKeyUsername] != "acme" || login.Data[secretKeyPassword] == "" {
		t.Errorf("the instance login Secret must carry username %q and a password; got username %q",
			"acme", login.Data[secretKeyUsername])
	}

	obj, ok := byName["dc-object-store-credentials"]
	if !ok {
		t.Fatal("the object-store credential is missing from a run that has backups on")
	}
	for _, k := range []string{keyMinioUser, keyMinioPassword} {
		if obj.Data[k] == "" {
			t.Errorf("object-store Secret has no %s; the store reads that exact key and an "+
				"absent one reads as empty rather than as an error", k)
		}
	}
	if err := validateObjectStoreSecret(obj.Data[keyMinioPassword]); err != nil {
		t.Errorf("the planned object-store credential would crash-loop the store: %v", err)
	}
}

// A run with no backup destination must not leave a credential for a store it never
// stood up.
func TestNoObjectStoreCredentialWithoutABackupDestination(t *testing.T) {
	st := &State{Instance: "acme", NoCNPG: true}
	set, err := mintNewCredentials(st)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if set.ObjectStoreSecret != "" {
		t.Error("minted an object-store credential for a run that provisions no object store")
	}
	if names := secretNames(planOwnedSecrets(st, set)); slices.ContainsFunc(names, func(n string) bool {
		return strings.Contains(n, "object-store")
	}) {
		t.Errorf("planned an object-store Secret with backups off: %v", names)
	}
}

func TestNoDashboardCredentialWithoutMonitoring(t *testing.T) {
	set, err := mintNewCredentials(&State{Instance: "acme", NoMonitoring: true})
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if set.GrafanaAdminPassword != "" {
		t.Error("minted a dashboard credential for a run that installs no dashboard")
	}
}

func secretNames(ss []ownedSecret) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Name)
	}
	return out
}

// 🔴 THE LITERALS ARE THE POINT OF THIS TEST. Every other case here reads the key
// names through the same constants the code under test uses, which makes them blind
// to the one change that matters: renaming a constant moves the code and the test
// together and nothing fails, while the reader on the other side — a database
// operator, an object store, a backup plugin — goes on looking for the old name and
// finds nothing. An absent key reads as an empty value, not as an error.
//
// So these names are written out. They are not ours to choose; they are what the
// consumers already read, and this is the only test that would notice them changing.
func TestTheSecretNamesAndKeysAreTheOnesTheirReadersUse(t *testing.T) {
	st := &State{Instance: "acme"}
	set, err := mintNewCredentials(st)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	byName := map[string]ownedSecret{}
	for _, s := range planOwnedSecrets(st, set) {
		byName[s.Namespace+"/"+s.Name] = s
	}

	want := map[string]struct {
		keys      []string
		namespace string
		typ       corev1.SecretType
	}{
		// Named by the Cluster resources as "<cluster>-app-credentials"; the operator
		// reads username and password out of a basic-auth Secret.
		// The shared relational store is the cluster's, in the cluster's namespace.
		"dc-system/dc-rdb-app-credentials": {
			keys: []string{"username", "password"}, namespace: "dc-system",
			typ: "kubernetes.io/basic-auth",
		},
		// The event store is the instance's, and CloudNativePG reads a Cluster's
		// credentials from the Cluster's own namespace.
		"acme/dc-tsdb-app-credentials": {
			keys: []string{"username", "password"}, namespace: "acme",
			typ: "kubernetes.io/basic-auth",
		},
		// Read by the object store's own container, and by the relational store's
		// backup plugin, which names these keys explicitly...
		"dc-system/dc-object-store-credentials": {
			keys: []string{"MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD"}, namespace: "dc-system",
			typ: "Opaque",
		},
		// ...and by the event store's backup plugin, which resolves the same name in
		// the event store's namespace.
		"acme/dc-object-store-credentials": {
			keys: []string{"MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD"}, namespace: "acme",
			typ: "Opaque",
		},
	}

	for name, spec := range want {
		s, ok := byName[name]
		if !ok {
			t.Errorf("no Secret named %q is planned; its reader looks it up by that exact "+
				"name. Planned: %v", name, secretNames(planOwnedSecrets(st, set)))
			continue
		}
		if s.Namespace != spec.namespace {
			t.Errorf("%s is planned for %q, but its reader looks in %q", name, s.Namespace, spec.namespace)
		}
		if s.Type != spec.typ {
			t.Errorf("%s has type %q, want %q", name, s.Type, spec.typ)
		}
		for _, k := range spec.keys {
			if s.Data[k] == "" {
				t.Errorf("%s has no %q; that is the key its reader asks for, and an absent "+
					"one reads as empty rather than as an error. Keys present: %v",
					name, k, sortedKeys(s.Data))
			}
		}
	}

	// The reload label is likewise a literal the database operator matches on.
	for _, name := range []string{"dc-system/dc-rdb-app-credentials", "acme/dc-tsdb-app-credentials"} {
		if byName[name].Labels["cnpg.io/reload"] != "true" {
			t.Errorf("%s does not carry cnpg.io/reload=true", name)
		}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// 🔴 EVERY CREDENTIAL THIS RUN MINTS MUST LAND IN A SECRET SOMETHING READS.
//
// A password generated and placed nowhere is not a harmless spare: it is a credential
// the operator believes is protecting something. One shipped that way — the dashboard
// login was minted whenever monitoring was on and carried by no Secret in the plan —
// and nothing could see it. The mutation suite tests this package against the model
// behind it, and the model itself had forgotten the field; every test here enumerated
// the credentials somebody had remembered.
//
// 🔑 SO THIS WALKS THE STRUCT, NOT A LIST. Adding a field to credentialSet without
// placing it fails here, which a list maintained by hand cannot promise — the whole
// failure was a list that was out of date and looked complete.
func TestEveryMintedCredentialIsPlacedSomewhere(t *testing.T) {
	st := &State{} // everything on: backups and monitoring both default to enabled
	set, err := mintNewCredentials(st)
	if err != nil {
		t.Fatal(err)
	}

	placed := map[string]bool{}
	for _, s := range planOwnedSecrets(st, set) {
		for _, v := range s.Data {
			placed[v] = true
		}
	}

	v := reflect.ValueOf(*set)
	typ := v.Type()
	for i := 0; i < v.NumField(); i++ {
		value := v.Field(i).String()
		if value == "" {
			// Not minted for this configuration, so there is nothing to place. The
			// case where that is WRONG — a credential the run needs and did not mint —
			// is what the per-flag tests above cover.
			continue
		}
		if !placed[value] {
			t.Errorf("credentialSet.%s was minted and no Secret in the plan carries it: "+
				"a credential generated on every run and dropped on the floor", typ.Field(i).Name)
		}
	}
}

// ...and the counterweight, because "place everything" is only safe while nothing is
// placed TWICE under a name a different consumer reads. Two Secrets holding one value
// is two things to rotate and one of them to forget.
//
// 🔑 ONE PLACEMENT IS ALLOWED TWICE, BY CONSTRUCTION: the archive credential and the
// instance's copy of it (instanceArchiveCredential) — same name, same keys, the instance's
// namespace, written from the same value on every run. The archiver cannot read across
// namespaces, and a copy derived in the same call cannot be forgotten. Anything else
// carrying a minted value twice is still refused.
func TestNoMintedCredentialIsPlacedInTwoSecrets(t *testing.T) {
	st := &State{Instance: "acme"}
	set, err := mintNewCredentials(st)
	if err != nil {
		t.Fatal(err)
	}

	where := map[string][]string{}
	for _, s := range planOwnedSecrets(st, set) {
		for k, v := range s.Data {
			where[v] = append(where[v], s.Namespace+"/"+s.Name+":"+k)
		}
	}
	for _, field := range []string{
		set.RDBPassword, set.TSDBPassword, set.ObjectStoreUser,
		set.ObjectStoreSecret, set.GrafanaAdminPassword,
	} {
		if field == "" {
			continue
		}
		places := where[field]
		if len(places) == 2 && strings.HasPrefix(places[1], "acme/") &&
			strings.TrimPrefix(places[0], "dc-system/") == strings.TrimPrefix(places[1], "acme/") {
			continue // the cluster's archive credential and the instance's copy of it
		}
		if len(places) > 1 {
			t.Errorf("one minted value is carried by %v: rotating it means finding all of them", places)
		}
	}
}

// The dashboard credential lands where Grafana's own chart looks for it: its release's
// namespace, under the key names admin.existingSecret reads. Literals, because those
// are that chart's contract and nothing here would fail to build if they moved.
func TestTheDashboardCredentialLandsWhereGrafanaReadsIt(t *testing.T) {
	st := &State{}
	set, err := mintNewCredentials(st)
	if err != nil {
		t.Fatal(err)
	}

	var found *ownedSecret
	for i, s := range planOwnedSecrets(st, set) {
		if s.Name == "dc-grafana-admin" {
			found = &planOwnedSecrets(st, set)[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no dashboard credential Secret is planned, so the minted password goes nowhere")
	}
	if found.Namespace != "monitoring" {
		t.Errorf("it is planned for namespace %q; Grafana reads a Secret in its own release's "+
			"namespace, so anywhere else is a Secret nothing mounts", found.Namespace)
	}
	if found.Data["admin-password"] != set.GrafanaAdminPassword {
		t.Error("the minted password is not under the key admin.passwordKey reads")
	}
	if found.Data["admin-user"] == "" {
		t.Error("no user is carried, so admin.userKey resolves to nothing")
	}
}

// secretReads records the namespace/name of every Secret a client is asked for.
func secretReads(c *fake.Clientset) *[]string {
	var reads []string
	c.PrependReactor("get", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		reads = append(reads, a.GetNamespace()+"/"+a.(k8stesting.GetAction).GetName())
		return false, nil, nil
	})
	return &reads
}

// 🔴 A BOOTSTRAP FOLLOWING AN INSTALL MINTS NOTHING THE CLUSTER OWNS, AND READS NONE OF
// IT TO REUSE. The cluster's credentials are what every instance on it already runs on;
// a bootstrap that minted them would be proposing new values for a store it does not
// apply, and one that went on to write them would rotate every other instance's.
//
// The record turns on everything that gates a cluster credential — monitoring, an
// in-cluster archive — so an empty field here is the half-split and not a switched-off
// feature.
func TestABootstrapMintsNoClusterOwnedCredential(t *testing.T) {
	st := &State{Instance: "acme", InstanceUID: testUID, ClusterUID: testClusterUID, Values: map[string]string{}}
	rec := aCompleteInstall()
	FollowInstall(st, &rec)

	set, err := mintNewCredentials(st)
	if err != nil {
		t.Fatal(err)
	}
	for field, got := range map[string]string{
		"RDBPassword": set.RDBPassword, "RDBProvisionerPassword": set.RDBProvisionerPassword,
		"ObjectStoreUser": set.ObjectStoreUser, "ObjectStoreSecret": set.ObjectStoreSecret,
		"GrafanaAdminPassword": set.GrafanaAdminPassword,
	} {
		if got != "" {
			t.Errorf("a bootstrap minted the cluster's %s", field)
		}
	}
	if set.RDBInstancePassword == "" || set.TSDBPassword == "" {
		t.Errorf("a bootstrap minted no credential for its own instance: %+v", set)
	}

	c := fake.NewSimpleClientset()
	reads := secretReads(c)
	if _, err := resolveCredentials(context.Background(), c, st, liveArchiveState{}); err != nil {
		t.Fatalf("settling a bootstrap's credentials: %v", err)
	}
	if len(*reads) == 0 {
		t.Fatal("resolving read no Secret at all, so the check below is vacuous")
	}
	for _, r := range *reads {
		if !strings.HasPrefix(r, instanceNamespace(st.Instance)+"/") {
			t.Errorf("a bootstrap looked for %s to reuse; only its own instance's credentials are its to settle", r)
		}
	}
}

// ...and an install, which has no instance, mints and reads nothing an instance owns.
func TestAnInstallMintsNoInstanceCredential(t *testing.T) {
	st := &State{ClusterUID: testClusterUID, Values: map[string]string{}}

	set, err := mintNewCredentials(st)
	if err != nil {
		t.Fatal(err)
	}
	if set.RDBInstancePassword != "" || set.TSDBPassword != "" {
		t.Errorf("an install minted an instance's credential: instance login set=%t, event store set=%t",
			set.RDBInstancePassword != "", set.TSDBPassword != "")
	}
	if set.RDBPassword == "" || set.RDBProvisionerPassword == "" || set.ObjectStoreSecret == "" || set.GrafanaAdminPassword == "" {
		t.Errorf("an install left a cluster credential unminted: %+v", set)
	}

	c := fake.NewSimpleClientset()
	reads := secretReads(c)
	if _, err := resolveCredentials(context.Background(), c, st, liveArchiveState{}); err != nil {
		t.Fatalf("settling an install's credentials: %v", err)
	}
	if len(*reads) == 0 {
		t.Fatal("resolving read no Secret at all, so the check below is vacuous")
	}
	for _, r := range *reads {
		if !strings.HasPrefix(r, infraNamespace+"/") {
			t.Errorf("an install looked for %s to reuse; it has no instance", r)
		}
	}
}

// 🔴 AN INSTALL OVER A LIVE RELATIONAL STORE WHOSE OWNER SECRET IS GONE IS REFUSED. The
// store's liveness reaches resolveCredentials only through the install, which reads it
// itself; a fresh owner password would be one the running role was never told about.
func TestAnInstallOverALiveRelationalStoreWithNoOwnerSecretIsRefused(t *testing.T) {
	st := &State{ClusterUID: testClusterUID, Values: map[string]string{}}
	live := liveArchiveState{Rdb: clusterArchiveState{Exists: true}}

	_, err := resolveCredentials(context.Background(), fake.NewSimpleClientset(), st, live)
	if err == nil || !strings.Contains(err.Error(), rdbClusterName+"-app-credentials") {
		t.Fatalf("an install minted a fresh owner password over a live relational store: %v", err)
	}
	if _, err := resolveCredentials(context.Background(), fake.NewSimpleClientset(), st, liveArchiveState{}); err != nil {
		t.Fatalf("an install with no relational store yet was refused: %v", err)
	}
}
