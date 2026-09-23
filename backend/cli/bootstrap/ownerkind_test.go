// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// A Secret belongs to one instance, or to the cluster every instance on it shares. The
// shared credentials the cluster prerequisites are built from have to be the cluster's
// before `dcctl install` can write them — install has no instance — and every reader
// that assumed an instance owner has to be able to tell the two apart.

// 🔴🔴 WHICH SECRETS ARE THE CLUSTER'S, BY LITERAL NAME. A fixture built from the
// constants would agree with itself through any rename; these are the contract.
func TestTheSharedCredentialsBelongToTheCluster(t *testing.T) {
	for _, tc := range []struct {
		name         string
		st           *State
		wantCluster  []string
		wantInstance []string
	}{
		{
			"in-cluster backups and monitoring",
			aWritableState(),
			[]string{
				"dc-system/dc-object-store-credentials",
				"dc-system/dc-rdb-app-credentials",
				"dc-system/dc-rdb-provisioner-credentials",
				"monitoring/dc-grafana-admin",
			},
			instanceOwnedAt("dc-object-store-credentials", "dc-tsdb-app-credentials", "dci-acme-rdb-credentials", "dci-acme-superuser"),
		},
		{
			"backups to an object store the operator owns",
			func() *State {
				s := aWritableState()
				s.BackupDestination = &BackupDestination{
					EndpointURL: "https://example.invalid", BucketRdb: "a", BucketTsdb: "b",
					AccessKeyID: "k", SecretAccessKey: "s",
				}
				return s
			}(),
			[]string{
				"dc-system/dc-backup-credentials",
				"dc-system/dc-rdb-app-credentials",
				"dc-system/dc-rdb-provisioner-credentials",
				"monitoring/dc-grafana-admin",
			},
			instanceOwnedAt("dc-backup-credentials", "dc-tsdb-app-credentials", "dci-acme-rdb-credentials", "dci-acme-superuser"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cluster, instance []string
			for _, s := range planOwnedSecrets(tc.st, tc.st.Credentials) {
				at := s.Namespace + "/" + s.Name
				switch s.Scope {
				case ownerCluster:
					cluster = append(cluster, at)
				case "", ownerInstance:
					instance = append(instance, at)
				default:
					t.Errorf("%s has scope %q, which is neither", at, s.Scope)
				}
			}
			sort.Strings(cluster)
			sort.Strings(instance)
			if got, want := strings.Join(cluster, ","), strings.Join(tc.wantCluster, ","); got != want {
				t.Errorf("cluster-owned = %s\n                want %s", got, want)
			}
			if got, want := strings.Join(instance, ","), strings.Join(tc.wantInstance, ","); got != want {
				t.Errorf("instance-owned = %s\n                 want %s", got, want)
			}
		})
	}
}

// The broker's TLS and the instance config document are the instance's, and are
// written through paths that do not go through planOwnedSecrets — so they are checked
// where they land.
func TestTheBrokerMaterialIsWrittenAsTheInstances(t *testing.T) {
	st := aWritableState()
	st.NATSTLS = &natsTLSMaterial{CACertPEM: "ca", CAKeyPEM: "cakey", LeafCertPEM: "leaf", LeafKeyPEM: "leafkey"}
	c := fake.NewSimpleClientset()
	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []ownedSecret{natsTLSSecret(natsReleaseName, st.NATSTLS), natsAuthoritySecret(natsReleaseName, st.NATSTLS)} {
		s, err := c.CoreV1().Secrets(spec.Namespace).Get(context.Background(), spec.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := readOwnership(s).owner; got != instanceOwner(testInstance, testUID) {
			t.Errorf("%s was written as %s's; the broker belongs to the instance", spec.Name, got)
		}
	}
}

// 🔴 EXACTLY ONE OWNER'S FIELDS. A Secret carrying an instance name AND a cluster UID
// is read correctly by readOwnership and wrongly by anything that looks at one
// annotation alone.
func TestAStampNamesExactlyOneOwner(t *testing.T) {
	s := &corev1.Secret{}
	setAnnotations(s, instanceOwner("prod", testUID), "2026-09-16T00:00:00Z")
	setAnnotations(s, clusterOwner(testClusterUID), "")
	a := s.GetAnnotations()
	if a["devicechain.io/owner-kind"] != "cluster" || a["devicechain.io/cluster-uid"] != testClusterUID {
		t.Errorf("the cluster stamp was not written by its literal names: %v", a)
	}
	for _, gone := range []string{"devicechain.io/instance", "devicechain.io/instance-uid"} {
		if _, ok := a[gone]; ok {
			t.Errorf("%s survived restamping the Secret as the cluster's", gone)
		}
	}

	setAnnotations(s, instanceOwner("prod", testUID), "")
	a = s.GetAnnotations()
	if _, ok := a["devicechain.io/cluster-uid"]; ok {
		t.Error("devicechain.io/cluster-uid survived restamping the Secret as an instance's")
	}
	if a["devicechain.io/owner-kind"] != "instance" || a["devicechain.io/instance"] != "prod" {
		t.Errorf("the instance stamp was not written by its literal names: %v", a)
	}
}

// A Secret written before the kind existed is an instance's, because its instance
// annotations say so — and an unknown kind is kept as written, so it is never mistaken
// for either.
func TestAStampWithNoKindIsAnInstancesAndAnUnknownKindIsNeither(t *testing.T) {
	legacy := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		"devicechain.io/managed-by": "dcctl", "devicechain.io/instance": "prod", "devicechain.io/instance-uid": testUID,
	}}}
	if got := readOwnership(legacy).owner; got != instanceOwner("prod", testUID) {
		t.Errorf("a pre-kind stamp read as %+v", got)
	}
	odd := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		"devicechain.io/managed-by": "dcctl", "devicechain.io/owner-kind": "tenant", "devicechain.io/cluster-uid": testClusterUID,
	}}}
	got := readOwnership(odd).owner
	if got == clusterOwner(testClusterUID) || got.Kind == ownerInstance {
		t.Errorf("an unrecognised kind was read as a known owner: %+v", got)
	}
}

func clusterOwnedSecret(name, uid string, data map[string][]byte) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: infraNamespace}, Data: data}
	setAnnotations(s, clusterOwner(uid), "2026-09-16T00:00:00Z")
	return s
}

// 🔴 THE WRITER REFUSES EVERY CROSSING: an instance over the cluster's, the cluster
// over an instance's, and one cluster over another's.
func TestTheWriterRefusesAnyOtherOwnersSecret(t *testing.T) {
	spec := ownedSecret{Name: "dc-rdb-app-credentials", Namespace: infraNamespace, Data: map[string]string{"password": "x"}}
	for _, tc := range []struct {
		name     string
		existing *corev1.Secret
		writer   secretOwner
		// The refusal is read by an operator, and each crossing has a different
		// remedy — so what it SAYS is asserted, not just that it refused. A generic
		// "previous instance, run destroy" about another cluster's Secret sends them
		// to destroy an instance that has nothing to do with it.
		says string
	}{
		{"an instance over the cluster's", clusterOwnedSecret(spec.Name, testClusterUID, nil), instanceOwner("prod", testUID),
			"does not change hands"},
		{"another cluster's", clusterOwnedSecret(spec.Name, "446b60a1-5c0e-4a8e-9d8f-2b4a3e6f7c10", nil), clusterOwner(testClusterUID),
			"different cluster"},
		{"the cluster over an instance's", func() *corev1.Secret {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: spec.Name, Namespace: infraNamespace}}
			setAnnotations(s, instanceOwner("prod", testUID), "2026-09-16T00:00:00Z")
			return s
		}(), clusterOwner(testClusterUID), "does not change hands"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewSimpleClientset(tc.existing)
			err := writeOwnedSecret(context.Background(), c, tc.writer, spec, fixedClock("2026-09-16T01:00:00Z"))
			var foreign *ErrForeignSecret
			if !errors.As(err, &foreign) {
				t.Fatalf("the write was not refused as foreign: %v", err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}

	// The counterweight: the cluster rewriting its own Secret succeeds.
	c := fake.NewSimpleClientset(clusterOwnedSecret(spec.Name, testClusterUID, nil))
	if err := writeOwnedSecret(context.Background(), c, clusterOwner(testClusterUID), spec, fixedClock("2026-09-16T01:00:00Z")); err != nil {
		t.Errorf("the cluster could not rewrite its own Secret: %v", err)
	}
}

func TestTheClusterCannotMintWithoutItsIdentity(t *testing.T) {
	spec := ownedSecret{Name: "dc-rdb-app-credentials", Namespace: infraNamespace}
	err := writeOwnedSecret(context.Background(), fake.NewSimpleClientset(), clusterOwner(""), spec, fixedClock("2026-09-16T01:00:00Z"))
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Errorf("a cluster-owned Secret was minted with no cluster identity: %v", err)
	}
}

// Reuse follows the same owner, and an owner with no identity is an error rather than
// a verdict of "foreign" about a Secret that may well be ours.
func TestReuseRecognisesOnlyTheClustersOwnCredential(t *testing.T) {
	ref := mintedCredentialRef{infraNamespace, "dc-rdb-app-credentials", "password"}
	c := fake.NewSimpleClientset(clusterOwnedSecret(ref.Name, testClusterUID, map[string][]byte{"password": []byte("pw")}))

	found, got, err := reuseMintedCredential(context.Background(), c, clusterOwner(testClusterUID), ref)
	if err != nil || found != reuseRecovered || got != "pw" {
		t.Errorf("the cluster's own credential was not recovered: %v %v %q", found, err, got)
	}
	if found, _, _ := reuseMintedCredential(context.Background(), c, clusterOwner("446b60a1-5c0e-4a8e-9d8f-2b4a3e6f7c10"), ref); found != reuseForeign {
		t.Errorf("another cluster's credential read as %v, want foreign", found)
	}
	if found, _, _ := reuseMintedCredential(context.Background(), c, instanceOwner("prod", testUID), ref); found != reuseForeign {
		t.Errorf("the cluster's credential read as an instance's own (%v)", found)
	}
	if _, _, err := reuseMintedCredential(context.Background(), c, clusterOwner(""), ref); err == nil {
		t.Error("an identity-less read answered instead of refusing")
	}
}

// 🔴🔴 THE ONE THAT WOULD BLOCK EVERY BOOTSTRAP. The second-instance guard reads every
// dcctl-stamped Secret in the infrastructure namespace; a cluster-owned one must be
// neither an instance nor an unattributable stamp.
func TestAClusterOwnedSecretIsNotAnInstance(t *testing.T) {
	instanceSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "dc-tsdb-app-credentials", Namespace: infraNamespace}}
	setAnnotations(instanceSecret, instanceOwner("prod", testUID), "2026-09-16T00:00:00Z")

	c := fake.NewSimpleClientset(kubeSystem(testClusterUID), clusterOwnedSecret("dc-rdb-app-credentials", testClusterUID, nil), instanceSecret)
	ids, err := ownedSecretInstances(context.Background(), c)
	if err != nil {
		t.Fatalf("a cluster-owned Secret made the guard refuse: %v", err)
	}
	if strings.Join(ids, ",") != "prod" {
		t.Errorf("the guard reports instances %v, want exactly [prod]", ids)
	}

	// ...but a cluster stamp that names no cluster is as unattributable as an instance
	// stamp that names no instance.
	c = fake.NewSimpleClientset(clusterOwnedSecret("dc-rdb-app-credentials", "", nil))
	if _, err := ownedSecretInstances(context.Background(), c); err == nil {
		t.Error("a cluster stamp with no UID was skipped rather than refused")
	}
}

// A cluster-owned Secret is not evidence that any one instance is still here.
func TestAClusterOwnedSecretIsNotAnInstancesFootprint(t *testing.T) {
	dyn := declarationClient()
	typed := fake.NewSimpleClientset(clusterOwnedSecret("dc-rdb-app-credentials", testClusterUID, nil))
	found, err := instanceFootprint(context.Background(), dyn, typed, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("the cluster's credential was counted as instance prod's footprint: %v", found)
	}
}

// 🔴 THE UPGRADE WIRING, END TO END. readInstanceCredentials is exercised directly
// elsewhere, which cannot see whether hydrateUpgradeState ever gives it the cluster's
// identity — and without it every cluster-owned credential is unreadable, so every
// upgrade refuses.
func TestAnUpgradeReadsTheClusterOwnedCredentials(t *testing.T) {
	provider, err := Get("local")
	if err != nil {
		t.Fatal(err)
	}
	prevDecl, prevDeployed := readInstanceDeclaration, lookupDeployedInstance
	t.Cleanup(func() { readInstanceDeclaration, lookupDeployedInstance = prevDecl, prevDeployed })
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return atVersion(t, "ghcr.io/devicechain-io", "v0.17.0"), nil
	}
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		return &config.InstanceConfiguration{}, nil
	}

	// What an install and a bootstrap wrote, into a cluster with a real identity.
	written := aWritableState()
	written.Instance = "prod"
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "kube-system", UID: types.UID(testClusterUID)}})
	writeInstallThenBootstrapSecrets(t, c, written)
	settleStringDataLikeAnAPIServer(t, c)
	// ...on a cluster `dcctl install` prepared, which an upgrade refuses without.
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}

	stubOperatorCheck(t, nil)
	st, err := hydrateUpgradeState(context.Background(), c, provider,
		ClusterBinding{KubeContext: "kind-devicechain", Cluster: "devicechain"},
		UpgradeOptions{Options: Options{Instance: "prod"}})
	if err != nil {
		t.Fatalf("the upgrade could not read back what bootstrap wrote: %v", err)
	}
	if st.ClusterUID != testClusterUID {
		t.Errorf("the upgrade ran with cluster identity %q, want the cluster's", st.ClusterUID)
	}
	if st.Credentials.RDBPassword != "rdb-pw" || st.Credentials.ObjectStoreSecret != "os-secret" {
		t.Errorf("the cluster-owned credentials were not recovered: %+v", st.Credentials)
	}
}

// 🔴 WHICH SHARED CREDENTIALS EXIST IS THE INSTALL'S ANSWER. The declaration does not
// record an off-site archive, so an upgrade deciding from it demands the in-cluster
// object store's root credential — which a cluster archiving off-site never had.
func TestAnUpgradeOnAnOffSiteArchivedClusterDemandsNoObjectStoreCredential(t *testing.T) {
	provider, err := Get("local")
	if err != nil {
		t.Fatal(err)
	}
	prevDecl, prevDeployed := readInstanceDeclaration, lookupDeployedInstance
	t.Cleanup(func() { readInstanceDeclaration, lookupDeployedInstance = prevDecl, prevDeployed })
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return atVersion(t, "ghcr.io/devicechain-io", "v0.17.0"), nil
	}
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		return &config.InstanceConfiguration{}, nil
	}

	written := aWritableState()
	written.Instance = "prod"
	c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "kube-system", UID: types.UID(testClusterUID)}})
	writeInstallThenBootstrapSecrets(t, c, written)
	settleStringDataLikeAnAPIServer(t, c)
	// An off-site cluster never had the in-cluster store's credential.
	if err := c.CoreV1().Secrets(infraNamespace).Delete(context.Background(), "dc-object-store-credentials",
		metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	rec := aCompleteInstall()
	rec.Settings.BackupsExternal = true
	rec.Outputs.Archive = ClusterArchive{EndpointURL: "https://s3.example.invalid", CredentialsSecret: "dc-backup-credentials",
		AccessKeyIDKey: "ACCESS_KEY_ID", SecretAccessKey: "ACCESS_SECRET_KEY", BucketTsdb: "tsdb-archive"}
	if err := writeInstalled(context.Background(), c, rec, installClock); err != nil {
		t.Fatal(err)
	}

	stubOperatorCheck(t, nil)
	st, err := hydrateUpgradeState(context.Background(), c, provider,
		ClusterBinding{KubeContext: "kind-devicechain", Cluster: "devicechain"},
		UpgradeOptions{Options: Options{Instance: "prod"}})
	if err != nil {
		t.Fatalf("an upgrade on an off-site archived cluster was refused: %v", err)
	}
	if st.Install == nil || !st.Install.Settings.BackupsExternal {
		t.Errorf("the upgrade state does not follow the install: %+v", st.Install)
	}
	if st.Credentials.ObjectStoreSecret != "" || st.Credentials.RDBPassword != "rdb-pw" {
		t.Errorf("the recovered credentials are not the off-site cluster's: %+v", st.Credentials)
	}
}

// 🔴🔴 A RE-RUN REUSES THE CLUSTER'S CREDENTIALS, and the mutation round is why this
// exists: reading the shared database password or the object-store identity under the
// wrong owner SURVIVED every other test. The wrong owner reads the real Secret as
// foreign, so the run keeps the fresh values it just minted — a password the live
// database was never told about, which is the rotate-every-credential defect reported
// as success.
func TestARerunReusesTheClusterOwnedCredentials(t *testing.T) {
	st := aWritableState()
	st.Credentials = nil
	c := fake.NewSimpleClientset(
		clusterOwnedSecret("dc-rdb-app-credentials", testClusterUID, map[string][]byte{
			"username": []byte("devicechain"), "password": []byte("rdb-in-use")}),
		clusterOwnedSecret("dc-object-store-credentials", testClusterUID, map[string][]byte{
			"MINIO_ROOT_USER": []byte("os-user-in-use"), "MINIO_ROOT_PASSWORD": []byte("os-secret-in-use")}),
		mintedSecret(InstanceNamespace("acme"), "dc-tsdb-app-credentials", testUID, map[string]string{
			"username": "devicechain", "password": "tsdb-in-use"}),
		clusterOwnedSecret("dc-rdb-provisioner-credentials", testClusterUID, map[string][]byte{
			"username": []byte("dc_provisioner"), "password": []byte("provisioner-in-use")}),
		mintedSecret(InstanceNamespace("acme"), "dci-acme-rdb-credentials", testUID, map[string]string{
			"username": "acme", "password": "login-in-use"}),
		// The superuser's seed: a re-run that minted over it would leave the Secret
		// naming a password the superuser was never seeded with.
		mintedSecret(InstanceNamespace("acme"), "dci-acme-superuser", testUID, map[string]string{
			"password": "superuser-in-use"}),
		// 🔴 THE DASHBOARD PASSWORD WAS RE-MINTED ON EVERY INSTALL RE-RUN, on the premise
		// that the same run rolls Grafana onto it. It does not: Grafana reads the Secret as
		// an environment variable, nothing restarts it when the Secret changes, and a
		// persistent Grafana database would ignore the change regardless. Every re-run
		// left the Secret naming a password the running Grafana had never been given.
		inMonitoring(clusterOwnedSecret("dc-grafana-admin", testClusterUID, map[string][]byte{
			"admin-user": []byte("admin"), "admin-password": []byte("grafana-in-use")})),
	)
	live := liveArchiveState{Rdb: clusterArchiveState{Exists: true}, Tsdb: clusterArchiveState{Exists: true}}

	set, err := resolveCredentials(context.Background(), c, st, live)
	if err != nil {
		t.Fatalf("a re-run over the credentials it wrote was refused: %v", err)
	}
	for field, got := range map[string]string{
		"RDBPassword":            set.RDBPassword,
		"RDBProvisionerPassword": set.RDBProvisionerPassword,
		"RDBInstancePassword":    set.RDBInstancePassword,
		"TSDBPassword":           set.TSDBPassword,
		"ObjectStoreUser":        set.ObjectStoreUser,
		"ObjectStoreSecret":      set.ObjectStoreSecret,
		"GrafanaAdminPassword":   set.GrafanaAdminPassword,
		"SuperuserPassword":      set.SuperuserPassword,
	} {
		if !strings.HasSuffix(got, "-in-use") {
			t.Errorf("%s was re-minted rather than reused; the live store still holds the old value", field)
		}
	}
	// ...and the report is told it was READ, so it does not print it as newly generated.
	if st.SuperuserSeed != superuserSeedRecovered {
		t.Errorf("a reused superuser seed was recorded as %v, want recovered", st.SuperuserSeed)
	}
}

// inMonitoring moves a fixture into the monitoring namespace, where the dashboard
// login's Secret lives.
func inMonitoring(s *corev1.Secret) *corev1.Secret {
	s.Namespace = monitoringNamespace
	return s
}

// The dashboard password is minted only when the cluster has no Secret of its own for it:
// absent, it is minted; present but not dcctl's, it is not read; with monitoring off, it
// is neither settled nor looked for. Reuse of an owned Secret is held by the re-run case
// above.
func TestTheDashboardPasswordIsMintedOnlyWhenTheClusterHasNone(t *testing.T) {
	st := &State{ClusterUID: testClusterUID, Values: map[string]string{}}

	// Absent — a first install, or monitoring switched on after an install without it.
	set, err := resolveCredentials(context.Background(), fake.NewSimpleClientset(), st, liveArchiveState{})
	if err != nil {
		t.Fatalf("an install with no dashboard Secret yet was refused: %v", err)
	}
	if set.GrafanaAdminPassword == "" {
		t.Error("no dashboard password was minted for a cluster that has none, so Grafana gets an empty login")
	}

	// Present but not dcctl's: kept out of reuse, the way every other login is. The writer
	// refuses that Secret by name, so reading a value out of it would only hide the refusal.
	notOurs := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dc-grafana-admin", Namespace: monitoringNamespace},
		Data:       map[string][]byte{"admin-user": []byte("admin"), "admin-password": []byte("grafana-not-ours")},
	}
	set, err = resolveCredentials(context.Background(), fake.NewSimpleClientset(notOurs), st, liveArchiveState{})
	if err != nil {
		t.Fatalf("resolving over a dashboard Secret dcctl did not write: %v", err)
	}
	if set.GrafanaAdminPassword == "" || set.GrafanaAdminPassword == "grafana-not-ours" {
		t.Error("the dashboard password was taken from a Secret dcctl did not write, rather than minted")
	}

	// Monitoring off: there is no dashboard, so there is nothing to look for.
	off := &State{ClusterUID: testClusterUID, Values: map[string]string{}, NoMonitoring: true}
	c := fake.NewSimpleClientset()
	reads := secretReads(c)
	if set, err = resolveCredentials(context.Background(), c, off, liveArchiveState{}); err != nil {
		t.Fatalf("an install without monitoring was refused: %v", err)
	}
	if set.GrafanaAdminPassword != "" {
		t.Error("a dashboard password was settled for a cluster with no dashboard")
	}
	for _, r := range *reads {
		if strings.HasPrefix(r, monitoringNamespace+"/") {
			t.Errorf("an install without monitoring looked for %s", r)
		}
	}
}

// kindlessInstanceSecret is what every instance built before owner kinds holds: a
// dcctl stamp naming the instance, and no kind.
func kindlessInstanceSecret(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: infraNamespace, Annotations: map[string]string{
			"devicechain.io/managed-by": "dcctl", "devicechain.io/instance": testInstance,
			"devicechain.io/instance-uid": testUID,
		}},
		Data: data,
	}
}

// 🔴 A REVIEW FINDING: an instance built before shared credentials were the cluster's was
// refused with "dcctl did not write it" — false, and with no remedy. Both the writer and
// the upgrade read-back must name what it is and what to do.
func TestAPreKindSharedSecretIsNamedWithItsRemedy(t *testing.T) {
	spec := ownedSecret{Name: "dc-rdb-app-credentials", Namespace: infraNamespace, Scope: ownerCluster}
	c := fake.NewSimpleClientset(kindlessInstanceSecret(spec.Name, nil))
	err := writeOwnedSecret(context.Background(), c, clusterOwner(testClusterUID), spec, fixedClock("2026-09-16T01:00:00Z"))
	if err == nil {
		t.Fatal("a pre-kind shared Secret was silently re-attributed to the cluster")
	}
	for _, want := range []string{"from before this credential belonged to the cluster", "dcctl destroy " + testInstance} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the writer's refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "did not write") {
		t.Errorf("the refusal claims dcctl did not write a Secret it did: %v", err)
	}

	st := aWritableState()
	w := fake.NewSimpleClientset()
	writeInstallThenBootstrapSecrets(t, w, st)
	settleStringDataLikeAnAPIServer(t, w)
	if err := w.CoreV1().Secrets(infraNamespace).Delete(context.Background(), spec.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.CoreV1().Secrets(infraNamespace).Create(context.Background(),
		kindlessInstanceSecret(spec.Name, map[string][]byte{"password": []byte("pw")}), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err = readInstanceCredentials(context.Background(), w, st)
	if err == nil || !strings.Contains(err.Error(), "from before this credential belonged to the cluster") {
		t.Errorf("the upgrade does not name a pre-kind shared Secret for what it is: %v", err)
	}
}

// 🔴 A REVIEW FINDING: the second-instance guard skipped a cluster-owned Secret stamped
// with ANOTHER cluster's identity, so a namespace carried in from elsewhere was refused
// only at the credential write, after the operator and declaration were installed.
func TestAnotherClustersSecretStopsTheGuardEarly(t *testing.T) {
	c := fake.NewSimpleClientset(kubeSystem(testClusterUID),
		clusterOwnedSecret("dc-rdb-app-credentials", "446b60a1-5c0e-4a8e-9d8f-2b4a3e6f7c10", nil))
	_, err := ownedSecretInstances(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "carried here from elsewhere") {
		t.Errorf("another cluster's credential was skipped as this cluster's: %v", err)
	}

	// The identity is read only when a cluster-owned Secret is present: a cluster with
	// none is answered without it (no kube-system in this fake at all).
	if _, err := ownedSecretInstances(context.Background(), fake.NewSimpleClientset()); err != nil {
		t.Errorf("an empty namespace needed the cluster's identity to answer: %v", err)
	}
}

func TestAnUnrecognisedOwnerIsNotDescribedAsAnInstance(t *testing.T) {
	got := secretOwner{Kind: "tenant"}.String()
	if strings.Contains(got, "instance") || !strings.Contains(got, "tenant") {
		t.Errorf("an unknown owner kind is described as %q", got)
	}
}

// 🔴 A REVIEW FINDING: marking an install "applying" carried the previous record's
// settings under this run's version, and would rewrite a newer dcctl's record as ours.
//
// 🔴 AND A SECOND ONE: carrying NOTHING forgot what the cluster runs, so a failed
// re-install's re-run decided its budget and its refusals from empty settings. The last
// completed install is carried — under its own name, never as this run's settings.
func TestMarkingAnInstallCarriesNothingAndRespectsANewerRecord(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	if err := markInstallApplying(context.Background(), c, testClusterUID, "v0.17.1", installClock); err != nil {
		t.Fatal(err)
	}
	rec := storedInstallRecord(t, c)
	if rec.Settings != (InstallSettings{}) || rec.Outputs != (InstallOutputs{}) {
		t.Errorf("an applying record shows the previous install's settings as this run's: %+v", rec)
	}
	if rec.DcctlVersion != "v0.17.1" || rec.Phase != installPhaseApplying {
		t.Errorf("the applying record does not describe this run: %+v", rec)
	}

	cm, _ := c.CoreV1().ConfigMaps("dc-system").Get(context.Background(), "dc-install", metav1.GetOptions{})
	rec = aCompleteInstall()
	rec.Schema, rec.Phase = installRecordSchema+1, installPhaseInstalled
	body, _ := json.Marshal(rec)
	cm.Data["install.json"] = string(body)
	if _, err := c.CoreV1().ConfigMaps("dc-system").Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := markInstallApplying(context.Background(), c, testClusterUID, "v0.17.1", installClock); err == nil {
		t.Error("a newer dcctl's install record was rewritten as this build's schema")
	}
}

// storedInstallRecord decodes the record as written, without readInstallRecord's
// refusals — an applying record is exactly what this needs to look at.
func storedInstallRecord(t *testing.T, c *fake.Clientset) InstallRecord {
	t.Helper()
	cm, err := c.CoreV1().ConfigMaps("dc-system").Get(context.Background(), "dc-install", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var rec InstallRecord
	if err := json.Unmarshal([]byte(cm.Data["install.json"]), &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// replaceStoredInstallRecord overwrites the record's body verbatim, keeping the
// ConfigMap dcctl wrote (and so its managed-by stamp).
func replaceStoredInstallRecord(t *testing.T, c *fake.Clientset, rec InstallRecord) {
	t.Helper()
	cm, err := c.CoreV1().ConfigMaps("dc-system").Get(context.Background(), "dc-install", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(rec)
	cm.Data["install.json"] = string(body)
	if _, err := c.CoreV1().ConfigMaps("dc-system").Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// 🔴🔴 THE LAST COMPLETED INSTALL SURVIVES A RE-INSTALL THAT FAILS — AND ONE THAT FAILS
// AGAIN. Marking over an installed record keeps that record; marking over an applying
// record keeps what IT kept, not its own empty settings.
func TestMarkingAnInstallKeepsTheLastCompletedOne(t *testing.T) {
	c := fake.NewSimpleClientset()
	want := aCompleteInstall()
	if err := writeInstalled(context.Background(), c, want, installClock); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"v0.17.1", "v0.17.2"} {
		if err := markInstallApplying(context.Background(), c, testClusterUID, version, installClock); err != nil {
			t.Fatal(err)
		}
		rec := storedInstallRecord(t, c)
		last := rec.LastInstalled
		if last == nil {
			t.Fatalf("after marking %s the last completed install was forgotten: %+v", version, rec)
		}
		if last.Settings != want.Settings || last.Outputs != want.Outputs || last.DcctlVersion != "v0.17.0" {
			t.Errorf("after marking %s the last completed install is not the one that finished:\n got %+v\nwant settings %+v outputs %+v (v0.17.0)",
				version, *last, want.Settings, want.Outputs)
		}
		if rec.Settings != (InstallSettings{}) || rec.Outputs != (InstallOutputs{}) {
			t.Errorf("after marking %s the applying record shows settings as this run's: %+v", version, rec)
		}
	}

	// ...and a completed install is its own last one again: nothing stale rides along.
	if err := writeInstalled(context.Background(), c, want, installClock); err != nil {
		t.Fatal(err)
	}
	if rec := storedInstallRecord(t, c); rec.LastInstalled != nil {
		t.Errorf("an installed record still carries a previous install: %+v", rec.LastInstalled)
	}
}

// An older schema's settings do not mean what this build's do, so nothing is carried
// from one — the re-run decides as a first install would.
func TestMarkingOverAnOlderSchemaCarriesNothing(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	old := aCompleteInstall()
	old.Schema, old.Phase = installRecordSchema-1, installPhaseInstalled
	replaceStoredInstallRecord(t, c, old)

	if err := markInstallApplying(context.Background(), c, testClusterUID, "v0.17.1", installClock); err != nil {
		t.Fatalf("an older dcctl's record could not be marked: %v", err)
	}
	if rec := storedInstallRecord(t, c); rec.LastInstalled != nil || rec.Schema != installRecordSchema {
		t.Errorf("an older schema's install was carried as this schema's last install: %+v", rec)
	}
}

func TestTheLastCompletedInstallIsTheRecordOrWhatItKept(t *testing.T) {
	var none *InstallRecord
	if none.lastCompleted() != nil {
		t.Error("no record has a last completed install")
	}
	rec := aCompleteInstall()
	rec.Phase = installPhaseInstalled
	if got := rec.lastCompleted(); got == nil || got.Settings != rec.Settings || got.Outputs != rec.Outputs {
		t.Errorf("an installed record is not its own last completed install: %+v", got)
	}
	applying := InstallRecord{Phase: installPhaseApplying}
	if applying.lastCompleted() != nil {
		t.Error("an applying record with nothing kept invented a completed install")
	}
	kept := &CompletedInstall{Settings: rec.Settings, Outputs: rec.Outputs}
	applying.LastInstalled = kept
	if applying.lastCompleted() != kept {
		t.Error("an applying record's kept install was not returned")
	}
}

// instanceOwnedAt spells the "<namespace>/<name>" addresses of the test instance's own
// Secrets. The namespace comes through InstanceNamespace; the NAMES stay literal, for the
// reason TestTheSecretNamesAndKeysAreTheOnesTheirReadersUse gives — they are what the
// readers look up, and reading them from our own constants would follow a rename past the
// only thing being checked. (Note `dci-acme-rdb-credentials`: that `dci-` is the Secret
// name's, not the namespace's.)
func instanceOwnedAt(names ...string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, InstanceNamespace("acme")+"/"+n)
	}
	return out
}
