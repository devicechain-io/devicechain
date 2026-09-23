// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/devicechain-io/dc-microservice/config"
)

// 🔴 THE CHART AND dcctl SPELL THE SECRET INDEPENDENTLY, and this is what holds them
// together: dcctl writes `dci-<id>-superuser` (superuserSecretName) and the chart reads
// devicechain.superuserSecret. Rendered through helmValues — the map a real bootstrap
// installs with — so the chart is judged on the values dcctl actually sends, not on a
// restatement of them. The expected strings are LITERALS for the reason the namespace
// agreement test gives: a constant would move both sides of the comparison at once.
func TestTheChartReadsTheSuperuserSecretDcctlWrites(t *testing.T) {
	enabled, err := ResolveEnabledAreas("default", nil)
	if err != nil {
		t.Fatalf("resolving areas: %v", err)
	}
	st := aWritableState()
	st.Profile = "default"
	st.EnabledAreas = enabled
	st.ImageRegistry, st.ImageVersion = DefaultImageRegistry, "v0.0.0-test"
	st.Values["ingressHost"] = "localhost"
	st.Values["secretsRootKey"] = base64.StdEncoding.EncodeToString(make([]byte, 32))

	// What dcctl writes.
	written := superuserSecret(st, st.Credentials)
	if written.Namespace != "dci-acme" || written.Name != "dci-acme-superuser" {
		t.Fatalf("dcctl writes the superuser Secret at %s/%s, want dci-acme/dci-acme-superuser",
			written.Namespace, written.Name)
	}
	if written.Data["password"] != "superuser-pw" {
		t.Fatalf("the superuser Secret does not carry the settled password under key password")
	}

	vals := helmValues(st)
	raw, err := json.Marshal(vals)
	if err != nil {
		t.Fatal(err)
	}
	// The value travels in the Secret and ONLY there. The Helm release's values are
	// readable by anyone with `helm get values`, and a ConfigMap by anyone who can read
	// ConfigMaps.
	if strings.Contains(string(raw), "superuser-pw") {
		t.Error("the superuser's seed password reached the Helm values")
	}

	docs := renderDocs(t, vals)
	var found int
	for _, d := range docs {
		if kind, _ := d["kind"].(string); kind != "Deployment" {
			// It must reach no other object either — not the per-area ConfigMap, not
			// the instance-config Secret.
			b, _ := json.Marshal(d)
			if strings.Contains(string(b), "superuser-pw") {
				t.Errorf("the seed password was rendered into a %v", d["kind"])
			}
			continue
		}
		meta, _ := d["metadata"].(map[string]interface{})
		name, _ := meta["name"].(string)
		spec := d["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
		for _, c := range spec["containers"].([]interface{}) {
			env, _ := c.(map[string]interface{})["env"].([]interface{})
			for _, e := range env {
				ev := e.(map[string]interface{})
				if ev["name"] != "DC_SUPERUSER_PASSWORD" {
					continue
				}
				if name != "user-management" {
					t.Errorf("Deployment %s is given the superuser's seed password; only user-management reads it", name)
					continue
				}
				found++
				if _, literal := ev["value"]; literal {
					t.Error("DC_SUPERUSER_PASSWORD is rendered as a literal value rather than read from the Secret")
				}
				ref, _ := ev["valueFrom"].(map[string]interface{})["secretKeyRef"].(map[string]interface{})
				if ref["name"] != "dci-acme-superuser" || ref["key"] != "password" {
					t.Errorf("user-management reads the seed password from %v/%v; dcctl writes dci-acme-superuser/password",
						ref["name"], ref["key"])
				}
				// Optional, because an instance whose superuser was seeded before this
				// Secret existed has none and must still start (see the upgrade test below).
				if ref["optional"] != true {
					t.Errorf("the secretKeyRef is not optional (%v): every instance built before dcctl generated "+
						"the password would stall in CreateContainerConfigError on upgrade", ref["optional"])
				}
			}
		}
	}
	if found != 1 {
		t.Fatalf("user-management was given DC_SUPERUSER_PASSWORD %d times, want once", found)
	}
}

// 🔴 AN UPGRADE OVER AN INSTANCE THAT NEVER HAD THE SECRET: NOT A REFUSAL, AND NOT A MINT.
// Its superuser was seeded with the old published literal. Refusing would strand it over
// a value the upgrade does not use; minting would write a Secret claiming a password the
// superuser was never given — and every tool that reads the Secret would trust it. So
// every OTHER credential is recovered, this one stays blank, nothing is written, and the
// run remembers why so the upgrade can say so.
func TestAnUpgradeOverAnInstanceWithNoSuperuserSecretLeavesItAbsent(t *testing.T) {
	st := aWritableState()
	c := fake.NewSimpleClientset()
	writeInstallThenBootstrapSecrets(t, c, st)
	settleStringDataLikeAnAPIServer(t, c)
	ref := superuserSecretRef(st.Instance)
	if err := c.CoreV1().Secrets(ref.Namespace).Delete(context.Background(), ref.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("removing the superuser Secret to model an instance built before it existed: %v", err)
	}

	up := aWritableState()
	got, err := readInstanceCredentials(context.Background(), c, up)
	if err != nil {
		t.Fatalf("an upgrade over an instance with no superuser Secret was refused: %v", err)
	}
	if got.SuperuserPassword != "" {
		t.Error("the upgrade produced a superuser password for an instance that never had one")
	}
	if got.RDBInstancePassword != "rdb-instance-pw" || got.TSDBPassword != "tsdb-pw" {
		t.Errorf("the other credentials were not recovered alongside the absent one: %+v", got)
	}
	if up.SuperuserSeed != superuserSeedAbsent {
		t.Errorf("the absence was recorded as %v, so the upgrade would not warn about it", up.SuperuserSeed)
	}
	if _, err := c.CoreV1().Secrets(ref.Namespace).Get(context.Background(), ref.Name, metav1.GetOptions{}); err == nil {
		t.Error("reading the credentials wrote a superuser Secret")
	}

	out := captureStdout(t, func() { warnPreGeneratedSuperuser(up) })
	if !strings.Contains(out, "Neither upgrading nor re-running bootstrap") || !strings.Contains(out, "superuser@devicechain.local") {
		t.Errorf("the upgrade does not say the superuser still has the old password: %q", out)
	}
}

// ...and the ordinary upgrade — the Secret is there — recovers it and warns about nothing.
func TestAnUpgradeRecoversTheSuperuserSecretAndDoesNotWarn(t *testing.T) {
	st := aWritableState()
	c := fake.NewSimpleClientset()
	writeInstallThenBootstrapSecrets(t, c, st)
	settleStringDataLikeAnAPIServer(t, c)

	up := aWritableState()
	got, err := readInstanceCredentials(context.Background(), c, up)
	if err != nil {
		t.Fatal(err)
	}
	if got.SuperuserPassword != "superuser-pw" {
		t.Error("the upgrade did not recover the superuser's seed password from its Secret")
	}
	if out := captureStdout(t, func() { warnPreGeneratedSuperuser(up) }); out != "" {
		t.Errorf("an instance with a generated superuser password was warned about the old one: %q", out)
	}
}

// 🔴 WHERE, ALWAYS; THE VALUE, ONCE. The report shows the password only when it is what
// a not-yet-running instance's superuser is about to be seeded with: generated by this
// run, or read back from an earlier run of this bootstrap that died before its report.
// Over a live instance it may have been shown and changed since, a dry run's is a
// throwaway, and a recovered instance's superuser may have been seeded long before this
// Secret existed — printing the Secret's value there would hand the operator a password
// that signs in as nobody.
func TestTheReportShowsTheSeedPasswordOnlyWhenThisRunGeneratedIt(t *testing.T) {
	const where = "kubectl -n dci-acme get secret dci-acme-superuser -o jsonpath='{.data.password}' | base64 -d"
	for _, tc := range []struct {
		name      string
		mutate    func(*State)
		showValue bool
		says      string
	}{
		{"generated by this run", func(st *State) { st.SuperuserSeed = superuserSeedMinted }, true, where},
		{"read back from an earlier run that died before its report", func(st *State) {
			st.SuperuserSeed = superuserSeedRecovered
		}, true, where},
		{"read back over a live instance", func(st *State) {
			st.SuperuserSeed = superuserSeedRecovered
			st.OverLiveInstance = true
		}, false, where},
		{"generated over a live instance (never settled so, but never shown)", func(st *State) {
			st.SuperuserSeed = superuserSeedMinted
			st.OverLiveInstance = true
		}, false, where},
		{"absent over a live instance", func(st *State) {
			st.SuperuserSeed = superuserSeedAbsent
			st.OverLiveInstance = true
		}, false, "Neither upgrading nor re-running bootstrap"},
		{"a dry run", func(st *State) { st.SuperuserSeed = superuserSeedMinted; st.DryRun = true }, false, "shown once"},
		{"a dry run over a live instance", func(st *State) {
			st.DryRun, st.OverLiveInstance = true, true
		}, false, "generates no password"},
		{"a recovery from escrow", func(st *State) {
			st.SuperuserSeed = superuserSeedMinted
			st.Escrow.RestoredFrom = "/tmp/rootkey.escrow"
		}, false, where},
		{"a restore of the relational store", func(st *State) {
			st.SuperuserSeed = superuserSeedMinted
			st.Restore = RestorePlan{RdbFrom: "dc-rdb-20260101"}
		}, false, "RESTORED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := aWritableState()
			tc.mutate(st)
			out := captureStdout(t, func() { printSuperuserReport(st) })
			if got := strings.Contains(out, "superuser-pw"); got != tc.showValue {
				t.Errorf("the report shows the password = %t, want %t:\n%s", got, tc.showValue, out)
			}
			if !strings.Contains(out, "superuser@devicechain.local") {
				t.Errorf("the report does not name the superuser:\n%s", out)
			}
			if !strings.Contains(out, tc.says) {
				t.Errorf("the report does not say %q:\n%s", tc.says, out)
			}
			for k, v := range st.Values {
				if v == "superuser-pw" {
					t.Errorf("the password was left in st.Values[%q]", k)
				}
			}
		})
	}
}

// A first bootstrap generates the seed and records that it did — which is the one state
// in which the report shows it. A dry run records nothing, since its value is a throwaway.
func TestAFirstBootstrapGeneratesTheSuperuserSeed(t *testing.T) {
	rec := aCompleteInstall()
	st := &State{Instance: "acme", InstanceUID: testUID, ClusterUID: testClusterUID, Values: map[string]string{}}
	FollowInstall(st, &rec)
	set, err := resolveCredentials(context.Background(), fake.NewSimpleClientset(), st, liveArchiveState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.SuperuserPassword) < 40 {
		t.Errorf("the generated seed password is %d characters, want a full minted credential", len(set.SuperuserPassword))
	}
	if st.SuperuserSeed != superuserSeedMinted {
		t.Errorf("a first bootstrap recorded its seed as %v, want minted", st.SuperuserSeed)
	}

	dry := &State{Instance: "acme", InstanceUID: testUID, ClusterUID: testClusterUID, Values: map[string]string{}, DryRun: true}
	FollowInstall(dry, &rec)
	if _, err := resolveCredentials(context.Background(), nil, dry, liveArchiveState{}); err != nil {
		t.Fatal(err)
	}
	if dry.SuperuserSeed != superuserSeedUnsettled {
		t.Errorf("a dry run recorded its throwaway seed as %v", dry.SuperuserSeed)
	}
}

// The tools that sign in as the superuser read the generated password from the Secret —
// and fail, naming it, rather than returning an empty string they would send as a
// password.
func TestReadingTheSuperuserPassword(t *testing.T) {
	ref := superuserSecretRef("acme")
	ctx := context.Background()

	if _, err := readSuperuserPassword(ctx, fake.NewSimpleClientset(), "acme"); err == nil ||
		!strings.Contains(err.Error(), "dci-acme/dci-acme-superuser") {
		t.Errorf("a missing Secret did not fail naming it: %v", err)
	}

	empty := mintedSecret(ref.Namespace, ref.Name, testUID, map[string]string{"password": ""})
	if _, err := readSuperuserPassword(ctx, fake.NewSimpleClientset(empty), "acme"); err == nil {
		t.Error("an empty password was returned rather than refused")
	}

	full := mintedSecret(ref.Namespace, ref.Name, testUID, map[string]string{"password": "the-seed"})
	got, err := readSuperuserPassword(ctx, fake.NewSimpleClientset(full), "acme")
	if err != nil || got != "the-seed" {
		t.Errorf("reading the Secret returned %q, %v", got, err)
	}
}

// 🔴 A BOOTSTRAP RE-RUN OVER A LIVE INSTANCE MINTS NO SEED. stepRefuseRebuild lets two
// kinds of re-run through over an instance that is already running — a restore (of any
// store) and --allow-legacy-db-removal — and every legitimate use of the second targets
// an instance built before dcctl generated the superuser's password. Its identity table
// was seeded with the old literal, so a value minted now seeds nothing: writing it to the
// Secret and printing it as the superuser's password would both be false, and `dcctl sim`
// would trust the Secret. The upgrade's answer applies: nothing minted, nothing written,
// and the report says what the absence means.
func TestABootstrapRerunOverALiveInstanceWithNoSecretMintsNoSeed(t *testing.T) {
	restore := lookupDeployedInstance
	t.Cleanup(func() { lookupDeployedInstance = restore })
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		return aLiveInstance(), nil
	}

	for _, tc := range []struct {
		name   string
		mutate func(*State)
	}{
		{"--allow-legacy-db-removal", func(st *State) { st.AllowLegacyDbRemoval = true }},
		{"a restore of the event store only", func(st *State) { st.Restore = RestorePlan{TsdbFrom: "dc-tsdb-20260101"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := aCompleteInstall()
			st := &State{Instance: testInstance, InstanceUID: testUID, ClusterUID: testClusterUID, Values: map[string]string{}}
			FollowInstall(st, &rec)
			tc.mutate(st)

			if err := stepRefuseRebuild(context.Background(), st); err != nil {
				t.Fatalf("the carve-out re-run was refused: %v", err)
			}
			if !st.OverLiveInstance {
				t.Fatal("the refusal step let a run through over a live instance without recording that it did")
			}
			set, err := resolveCredentials(context.Background(), fake.NewSimpleClientset(), st, liveArchiveState{})
			if err != nil {
				t.Fatal(err)
			}
			if set.SuperuserPassword != "" {
				t.Error("a seed password was generated for a live instance whose superuser was seeded long ago")
			}
			if st.SuperuserSeed != superuserSeedAbsent {
				t.Errorf("the seed was settled as %v, want absent", st.SuperuserSeed)
			}
			for _, s := range planInstanceSecrets(st, set, nil) {
				if s.Name == superuserSecretName(st.Instance) {
					t.Error("the superuser Secret is still planned for writing over a live instance that never had one")
				}
			}

			st.Credentials = set
			out := captureStdout(t, func() { printSuperuserReport(st) })
			if strings.Contains(out, "password:") || strings.Contains(out, "shown only this once") {
				t.Errorf("the report presents a password over a live instance:\n%s", out)
			}
			if !strings.Contains(out, "Neither upgrading nor re-running bootstrap") {
				t.Errorf("the report does not say the superuser still has the old password:\n%s", out)
			}
		})
	}
}

// ...and over a live instance that HAS the Secret, the value is kept and not shown again:
// it may have been shown by the bootstrap that built the instance, and changed since.
func TestABootstrapRerunOverALiveInstanceKeepsItsSeedAndDoesNotShowIt(t *testing.T) {
	restore := lookupDeployedInstance
	t.Cleanup(func() { lookupDeployedInstance = restore })
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		return aLiveInstance(), nil
	}
	rec := aCompleteInstall()
	st := &State{Instance: testInstance, InstanceUID: testUID, ClusterUID: testClusterUID, Values: map[string]string{},
		AllowLegacyDbRemoval: true}
	FollowInstall(st, &rec)
	if err := stepRefuseRebuild(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	ref := superuserSecretRef(st.Instance)
	c := fake.NewSimpleClientset(mintedSecret(ref.Namespace, ref.Name, testUID, map[string]string{ref.Key: "the-seed"}))
	set, err := resolveCredentials(context.Background(), c, st, liveArchiveState{})
	if err != nil {
		t.Fatal(err)
	}
	if set.SuperuserPassword != "the-seed" || st.SuperuserSeed != superuserSeedRecovered {
		t.Fatalf("the live instance's seed was not kept: settled %v", st.SuperuserSeed)
	}
	st.Credentials = set
	if out := captureStdout(t, func() { printSuperuserReport(st) }); strings.Contains(out, "the-seed") {
		t.Errorf("the report shows a live instance's seed again:\n%s", out)
	}
}

// A run that is NOT over a live instance records nothing of the kind — including a
// half-built one being repaired, which is what the refusal step leaves open.
func TestARunOverNoLiveInstanceIsNotRecordedAsOne(t *testing.T) {
	restore := lookupDeployedInstance
	t.Cleanup(func() { lookupDeployedInstance = restore })
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		return nil, nil
	}
	st := aBootstrapOf(testInstance)
	if err := stepRefuseRebuild(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if st.OverLiveInstance {
		t.Error("a bootstrap with no configuration document in the cluster was recorded as running over a live instance")
	}
}

// 🔴 THE BOOTSTRAP REPORT IS THE ONLY PLACE THE GENERATED PASSWORD IS EVER SHOWN, so this
// drives the step that prints it rather than the helper it calls: a report that stopped
// calling the helper would otherwise leave every other test green while no operator ever
// saw their password. Exactly once when it is to be shown; never otherwise.
func TestTheBootstrapReportShowsTheGeneratedPasswordExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*State)
		times  int
	}{
		{"generated by this run", func(st *State) { st.SuperuserSeed = superuserSeedMinted }, 1},
		{"read back from an earlier run that died before its report", func(st *State) {
			st.SuperuserSeed = superuserSeedRecovered
		}, 1},
		{"read back over a live instance", func(st *State) {
			st.SuperuserSeed, st.OverLiveInstance = superuserSeedRecovered, true
		}, 0},
		{"a dry run", func(st *State) { st.SuperuserSeed, st.DryRun = superuserSeedMinted, true }, 0},
		{"a recovery from escrow", func(st *State) {
			st.SuperuserSeed = superuserSeedMinted
			st.Escrow.RestoredFrom = "/keys/old.escrow"
		}, 0},
		{"a restore of the relational store", func(st *State) {
			st.SuperuserSeed = superuserSeedMinted
			st.Restore = RestorePlan{RdbFrom: "dc-rdb-20260101"}
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := aWritableState()
			tc.mutate(st)
			out := captureStdout(t, func() {
				if err := stepReport(t.Context(), st); err != nil {
					t.Fatalf("stepReport: %v", err)
				}
			})
			if got := strings.Count(out, "superuser-pw"); got != tc.times {
				t.Errorf("the report shows the password %d times, want %d:\n%s", got, tc.times, out)
			}
			if !strings.Contains(out, "dci-acme-superuser") {
				t.Errorf("the report does not say where the seed password is kept:\n%s", out)
			}
		})
	}
}

// 🔴 THE UPGRADE'S CLOSING WORDS ARE THE ONLY THING THAT TELLS AN OLDER INSTANCE'S OPERATOR
// THEIR SUPERUSER STILL HAS THE PUBLISHED PASSWORD — and "every credential was kept" is
// exactly the sentence that would read as good news without it. Both closings are driven,
// the real one and the rehearsal, since Upgrade itself needs a cluster.
func TestTheUpgradeClosingsWarnAboutAPreGeneratedSuperuser(t *testing.T) {
	for _, tc := range []struct {
		name string
		say  func(*State)
	}{
		{"a finished upgrade", func(st *State) { sayUpgradeCredentialsKept(st, UpgradeOptions{}) }},
		{"a dry run", sayUpgradeDryRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			absent := aWritableState()
			absent.SuperuserSeed = superuserSeedAbsent
			if out := captureStdout(t, func() { tc.say(absent) }); !strings.Contains(out, "seeded before dcctl generated") {
				t.Errorf("an instance with no seed Secret was not warned about its superuser:\n%s", out)
			}
			kept := aWritableState()
			kept.SuperuserSeed = superuserSeedRecovered
			if out := captureStdout(t, func() { tc.say(kept) }); strings.Contains(out, "seeded before dcctl generated") {
				t.Errorf("an instance with a generated seed was warned about the old one:\n%s", out)
			}
		})
	}
}
