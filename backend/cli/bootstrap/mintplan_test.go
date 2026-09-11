// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// 🔴 THE CROSS-CHECK THAT STOPS TWO COPIES OF ONE DECISION DRIFTING. infraVars decides
// whether backups are on by appending a variable from two separate branches;
// databaseBackupsEnabled decides the same thing directly. If either moves without the
// other, the mint writes an object-store credential nothing reads — or, in the
// direction that breaks an install, does not write one the store needs.
//
// The matrix is every combination of the flags that reach either decision, so a new
// branch in infraVars that this predicate does not know about shows up here rather
// than in an install.
func TestTheBackupPredicateMatchesTheVariablesEmitted(t *testing.T) {
	for _, noCNPG := range []bool{false, true} {
		for _, compact := range []bool{false, true} {
			for _, noTLS := range []bool{false, true} {
				for _, noMonitoring := range []bool{false, true} {
					st := &State{
						Instance: "acme", NoCNPG: noCNPG, Compact: compact,
						NoTLS: noTLS, NoMonitoring: noMonitoring,
					}
					vars := infraVars(st)
					emittedOff := slices.Contains(vars, "enable_database_backups=false")
					if got := databaseBackupsEnabled(st); got == emittedOff {
						t.Errorf("no-cnpg=%v compact=%v no-tls=%v: the predicate says backups on=%v "+
							"while the variables say off=%v — the two have drifted",
							noCNPG, compact, noTLS, got, emittedOff)
					}

					monOff := slices.Contains(vars, "enable_monitoring=false")
					if got := monitoringEnabled(st); got == monOff {
						t.Errorf("no-monitoring=%v: predicate says on=%v, variables say off=%v",
							noMonitoring, got, monOff)
					}
				}
			}
		}
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

	for _, name := range []string{"dc-rdb-app-credentials", "dc-tsdb-app-credentials"} {
		s, ok := byName[name]
		if !ok {
			t.Fatalf("%s missing from the plan", name)
		}
		if s.Type != corev1.SecretTypeBasicAuth {
			t.Errorf("%s has type %q, want basic-auth", name, s.Type)
		}
		if s.Data[secretKeyUsername] != dbRoleUsername {
			t.Errorf("%s username is %q, want %q — the role name is not a secret and a "+
				"value that disagrees with the rest of the install locks the services out",
				name, s.Data[secretKeyUsername], dbRoleUsername)
		}
		if s.Data[secretKeyPassword] == "" {
			t.Errorf("%s carries no password", name)
		}
		if s.Labels[cnpgReloadLabel] != "true" {
			t.Errorf("%s lacks the reload label, so a credential change would land in the "+
				"Secret while the database kept the old password", name)
		}
		if s.Namespace != infraNamespace {
			t.Errorf("%s is planned for namespace %q, want %q", name, s.Namespace, infraNamespace)
		}
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
		byName[s.Name] = s
	}

	want := map[string]struct {
		keys      []string
		namespace string
		typ       corev1.SecretType
	}{
		// Named by the Cluster resources as "<cluster>-app-credentials"; the operator
		// reads username and password out of a basic-auth Secret.
		"dc-rdb-app-credentials": {
			keys: []string{"username", "password"}, namespace: "dc-system",
			typ: "kubernetes.io/basic-auth",
		},
		"dc-tsdb-app-credentials": {
			keys: []string{"username", "password"}, namespace: "dc-system",
			typ: "kubernetes.io/basic-auth",
		},
		// Read twice: by the object store's own container, and by the backup
		// plugin's credential reference, which names these keys explicitly.
		"dc-object-store-credentials": {
			keys: []string{"MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD"}, namespace: "dc-system",
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
	for _, name := range []string{"dc-rdb-app-credentials", "dc-tsdb-app-credentials"} {
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
func TestNoMintedCredentialIsPlacedInTwoSecrets(t *testing.T) {
	st := &State{}
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
		if len(where[field]) > 1 {
			t.Errorf("one minted value is carried by %v: rotating it means finding all of them",
				where[field])
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
