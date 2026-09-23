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
	if !strings.Contains(out, "Upgrading does not change it") || !strings.Contains(out, "superuser@devicechain.local") {
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

// 🔴 WHERE, ALWAYS; THE VALUE, ONCE. The report shows the password only when THIS run
// generated it for a fresh instance. A reused value may already have been shown, a dry
// run's is a throwaway, and a recovered instance's superuser was seeded long before this
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
		{"read back from an earlier run", func(st *State) { st.SuperuserSeed = superuserSeedRecovered }, false, where},
		{"a dry run", func(st *State) { st.SuperuserSeed = superuserSeedMinted; st.DryRun = true }, false, "shown once"},
		{"a recovery from escrow", func(st *State) {
			st.SuperuserSeed = superuserSeedMinted
			st.Escrow.RestoredFrom = "/tmp/rootkey.escrow"
		}, false, "RECOVERED"},
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
