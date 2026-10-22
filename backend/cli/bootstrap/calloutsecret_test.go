// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/natsauth"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The callout's seeds are in a Secret of their own that only device-management mounts. These tests
// hold the three places that has to be true: what dcctl writes, what the chart projects, and
// what dcctl reads back from a running instance.

func calloutState(t *testing.T) (*State, natsauth.Credentials) {
	t.Helper()
	// The shared set only. The tests that exercise the per-area logins ask for them (loginState).
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	st := aWritableState()
	setBrokerCredentialValues(st, creds)
	return st, creds
}

func TestTheCalloutSecretCarriesBothSeedsAndNothingElse(t *testing.T) {
	st, creds := calloutState(t)
	spec, ok := calloutSecret(st)
	if !ok {
		t.Fatal("an instance with a device callout writes no callout Secret")
	}
	if spec.Namespace != "dci-"+testInstance || spec.Name != "dci-"+testInstance+"-callout" {
		t.Errorf("the Secret is written at %s/%s", spec.Namespace, spec.Name)
	}
	if len(spec.Data) != 2 || spec.Data["issuerSeed"] != creds.IssuerSeed || spec.Data["xkeySeed"] != creds.XKeySeed {
		t.Errorf("the Secret holds %v, want exactly the issuer seed and the xkey seed", spec.Data)
	}
	// An instance with no callout has nothing to hold, and must not be given an empty Secret
	// the chart would then project as an empty seed.
	if _, ok := calloutSecret(&State{Instance: "x", Values: map[string]string{}}); ok {
		t.Error("a callout Secret is written for an instance with no device callout")
	}
}

// 🔴 THE CHART HAS TO READ WHAT DCCTL WRITES, AND ONLY DEVICE-MANAGEMENT MAY READ IT. The seeds
// are the reason the Secret exists: any other pod that mounted it could sign a grant for any
// device, or open every device's CONNECT credentials. Rendered from the values dcctl sends, the
// literals below being the contract, not a constant that would move both sides at once.
func TestTheChartReadsTheCalloutSecretDcctlWrites(t *testing.T) {
	enabled, err := ResolveEnabledAreas("default", nil)
	if err != nil {
		t.Fatal(err)
	}
	st, creds := calloutState(t)
	st.Instance = "acme"
	st.Profile = "default"
	st.EnabledAreas = enabled
	st.ImageRegistry, st.ImageVersion = DefaultImageRegistry, "v0.0.0-test"
	st.Values["ingressHost"] = "localhost"
	st.Values["secretsRootKey"] = base64.StdEncoding.EncodeToString(make([]byte, 32))

	written, _ := calloutSecret(st)
	if written.Namespace != "dci-acme" || written.Name != "dci-acme-callout" {
		t.Fatalf("dcctl writes the callout Secret at %s/%s, want dci-acme/dci-acme-callout", written.Namespace, written.Name)
	}

	vals := helmValues(st)
	raw, err := json.Marshal(vals)
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []string{creds.IssuerSeed, creds.XKeySeed} {
		if strings.Contains(string(raw), seed) {
			t.Fatal("a callout seed reached the Helm values, which are readable by anyone who can `helm get values`")
		}
	}

	want := map[string]string{"DC_CALLOUT_ISSUER_SEED": "issuerSeed", "DC_CALLOUT_XKEY_SEED": "xkeySeed"}
	mounters := map[string]int{}
	for _, d := range renderDocs(t, vals) {
		b, _ := json.Marshal(d)
		kind, _ := d["kind"].(string)
		for _, seed := range []string{creds.IssuerSeed, creds.XKeySeed} {
			if strings.Contains(string(b), seed) {
				t.Errorf("a callout seed was rendered into a %s", kind)
			}
		}
		meta, _ := d["metadata"].(map[string]interface{})
		name, _ := meta["name"].(string)
		// ANY reference to the Secret outside device-management, by whatever route (an env var
		// under another name, envFrom, a volume), hands another workload the seeds.
		if strings.Contains(string(b), `"dci-acme-callout"`) && (kind != "Deployment" || name != "device-management") {
			t.Errorf("%s %s references the callout Secret; only device-management may", kind, name)
		}
		if kind != "Deployment" {
			continue
		}
		spec := d["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
		for _, c := range spec["containers"].([]interface{}) {
			env, _ := c.(map[string]interface{})["env"].([]interface{})
			got := map[string]bool{}
			for _, e := range env {
				ev := e.(map[string]interface{})
				envName, _ := ev["name"].(string)
				key, isSeed := want[envName]
				if !isSeed {
					continue
				}
				if name != "device-management" {
					t.Errorf("Deployment %s is given %s; only device-management reads the callout seeds", name, envName)
					continue
				}
				got[envName] = true
				if _, literal := ev["value"]; literal {
					t.Errorf("%s is rendered as a literal rather than read from the Secret", envName)
				}
				ref, _ := ev["valueFrom"].(map[string]interface{})["secretKeyRef"].(map[string]interface{})
				if ref["name"] != "dci-acme-callout" || ref["key"] != key {
					t.Errorf("%s reads %v/%v; dcctl writes dci-acme-callout/%s", envName, ref["name"], ref["key"], key)
				}
				// Optional so an instance with no callout starts; the service refuses to start
				// itself when its configuration says the callout runs and a seed is missing.
				if ref["optional"] != true {
					t.Errorf("%s is not optional, so an instance with no callout could not start device-management", envName)
				}
			}
			if name == "device-management" {
				mounters[name] += len(got)
			}
		}
	}
	if mounters["device-management"] != 2 {
		t.Fatalf("device-management was given %d of the 2 callout seed variables", mounters["device-management"])
	}
}

func TestOnlyThePublicHalvesOfTheCalloutKeysReachTheDocument(t *testing.T) {
	st, creds := calloutState(t)
	doc, _ := composedForTest(t, withComposeBasics(st))
	var parsed config.InstanceConfiguration
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatal(err)
	}
	auth := parsed.Infrastructure.Nats.Auth
	if auth.CalloutIssuerPublic != creds.IssuerPublic || auth.CalloutXKeyPublic != creds.XKeyPublic {
		t.Errorf("the document records issuer %q xkey %q, want %q and %q",
			auth.CalloutIssuerPublic, auth.CalloutXKeyPublic, creds.IssuerPublic, creds.XKeyPublic)
	}
	if auth.CalloutIssuerSeed != "" || auth.CalloutXKeySeed != "" || strings.Contains(string(doc), creds.XKeySeed) {
		t.Error("a callout SEED is in the document every service mounts")
	}
	if !auth.CalloutEnabled() {
		t.Error("the services cannot tell the broker runs the device callout")
	}
}

func withComposeBasics(st *State) *State {
	st.Profile = "default"
	st.ImageRegistry, st.ImageVersion = DefaultImageRegistry, "v0.0.0-test"
	st.Values["ingressHost"] = "localhost"
	st.Values["secretsRootKey"] = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	st.Values["natsServicePassword"] = "pw"
	return st
}

func TestTheCalloutSecretIsWrittenOwnedAndReadBack(t *testing.T) {
	ctx := context.Background()
	st, creds := calloutState(t)
	c := fake.NewSimpleClientset()

	if err := writeCalloutSecret(ctx, c, st); err != nil {
		t.Fatal(err)
	}
	settleStringDataLikeAnAPIServer(t, c)
	issuer, xkey, err := readCalloutSeeds(ctx, c, InstanceNamespace(st.Instance), st.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if issuer != creds.IssuerSeed || xkey != creds.XKeySeed {
		t.Errorf("read back %q/%q", issuer, xkey)
	}
	s, _ := c.CoreV1().Secrets(InstanceNamespace(st.Instance)).Get(ctx, calloutSecretName(st.Instance), metav1.GetOptions{})
	if own := readOwnership(s); !own.managed || own.owner.UID != testUID {
		t.Errorf("the callout Secret is not stamped as this instance's: %+v", own)
	}

	// A second run rewrites it in place, and a SECRET another generation of the instance left is refused,
	// not adopted: it holds a dead instance's seeds.
	if err := writeCalloutSecret(ctx, c, st); err != nil {
		t.Fatalf("an idempotent re-run could not rewrite its own Secret: %v", err)
	}
	other := aWritableState()
	other.InstanceUID = "a-different-generation"
	other.Values = st.Values
	var foreign *ErrForeignSecret
	if err := writeCalloutSecret(ctx, c, other); !errors.As(err, &foreign) {
		t.Fatalf("a Secret from another generation of the instance was adopted: %v", err)
	}
}

func TestReadingTheCalloutSeedsFailsClosed(t *testing.T) {
	ctx := context.Background()
	ns := InstanceNamespace(testInstance)

	// Absent is not an error: an instance built before the split has none, and its seed is in the document.
	issuer, xkey, err := readCalloutSeeds(ctx, fake.NewSimpleClientset(), ns, testInstance)
	if err != nil || issuer != "" || xkey != "" {
		t.Fatalf("an absent Secret read as %q/%q/%v", issuer, xkey, err)
	}

	// Present but empty IS an error: reading it as absent would mint a seed over a broker that trusts
	// the real one.
	empty := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: calloutSecretName(testInstance), Namespace: ns},
		Data: map[string][]byte{"xkeySeed": []byte("x")}}
	if _, _, err := readCalloutSeeds(ctx, fake.NewSimpleClientset(empty), ns, testInstance); err == nil {
		t.Fatal("a callout Secret with no issuer seed read as absent")
	}
}

// 🔑 EVERYONE WHO ASKS "WHAT IS THIS INSTANCE RUNNING ON" GETS THE SEEDS FROM WHERE THEY ARE.
// DeployedInstanceConfig is that question, and it is also what makes an upgrade from a release that
// wrote the seed into the document work: the document's copy is the fallback, the Secret wins.
func TestTheDeployedViewOverlaysTheCalloutSecret(t *testing.T) {
	ctx := context.Background()
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	ns := InstanceNamespace(testInstance)
	docWith := func(seed string) *corev1.Secret {
		cfg := &config.InstanceConfiguration{}
		cfg.Infrastructure.Nats.Auth.CalloutIssuerSeed = seed
		raw, _ := json.Marshal(cfg)
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "dci-" + testInstance + "-config", Namespace: ns},
			Data: map[string][]byte{"instance": raw}}
	}
	callout := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: calloutSecretName(testInstance), Namespace: ns},
		Data: map[string][]byte{"issuerSeed": []byte(creds.IssuerSeed), "xkeySeed": []byte(creds.XKeySeed)}}

	t.Run("an instance built before the split keeps its document seed and has no xkey", func(t *testing.T) {
		got, err := deployedInstanceConfigVia(ctx, fake.NewSimpleClientset(docWith(creds.IssuerSeed)), testInstance)
		if err != nil {
			t.Fatal(err)
		}
		a := got.Infrastructure.Nats.Auth
		if a.CalloutIssuerSeed != creds.IssuerSeed || a.CalloutXKeySeed != "" {
			t.Errorf("legacy view = %q / %q", a.CalloutIssuerSeed, a.CalloutXKeySeed)
		}
	})
	t.Run("the Secret wins and supplies the xkey", func(t *testing.T) {
		got, err := deployedInstanceConfigVia(ctx, fake.NewSimpleClientset(docWith(""), callout), testInstance)
		if err != nil {
			t.Fatal(err)
		}
		a := got.Infrastructure.Nats.Auth
		if a.CalloutIssuerSeed != creds.IssuerSeed || a.CalloutXKeySeed != creds.XKeySeed {
			t.Errorf("view = %q / %q", a.CalloutIssuerSeed, a.CalloutXKeySeed)
		}
	})
	t.Run("a Secret that cannot be recovered fails the lookup rather than reading as a fresh install", func(t *testing.T) {
		bad := callout.DeepCopy()
		bad.Data = map[string][]byte{"xkeySeed": []byte("x")}
		if _, err := deployedInstanceConfigVia(ctx, fake.NewSimpleClientset(docWith(creds.IssuerSeed), bad), testInstance); err == nil {
			t.Fatal("an unreadable callout Secret was ignored")
		}
	})
}

// The xkey is INTRODUCED: minted when the starting state has none, reused when it has one, and
// an unusable one is an error rather than an invitation to mint over it.
func TestAnUpgradeIntroducesTheXKeyOnceAndNeverReplacesIt(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	hydrate := func(seed, xkey string) (*State, error) {
		st := &State{Instance: testInstance, Values: map[string]string{}}
		d := &config.InstanceConfiguration{}
		d.Infrastructure.Nats.Auth.CalloutIssuerSeed = seed
		d.Infrastructure.Nats.Auth.CalloutXKeySeed = xkey
		return st, applyDeployedInfrastructure(st, d)
	}

	t.Run("an instance with no xkey is given one, and its issuer public is derived", func(t *testing.T) {
		st, err := hydrate(creds.IssuerSeed, "")
		if err != nil {
			t.Fatal(err)
		}
		if st.Values["natsCalloutXKeySeed"] == "" || st.Values["natsCalloutXKeyPublic"] == "" {
			t.Fatalf("no xkey was introduced: %v", st.Values)
		}
		if st.Values["natsCalloutIssuerPublic"] != creds.IssuerPublic {
			t.Errorf("issuer public = %q, want %q", st.Values["natsCalloutIssuerPublic"], creds.IssuerPublic)
		}
	})
	t.Run("an instance that has one keeps it", func(t *testing.T) {
		st, err := hydrate(creds.IssuerSeed, creds.XKeySeed)
		if err != nil {
			t.Fatal(err)
		}
		if st.Values["natsCalloutXKeySeed"] != creds.XKeySeed || st.Values["natsCalloutXKeyPublic"] != creds.XKeyPublic {
			t.Error("an existing xkey was replaced")
		}
	})
	t.Run("an unusable one is an error", func(t *testing.T) {
		if _, err := hydrate(creds.IssuerSeed, creds.IssuerSeed); err == nil {
			t.Fatal("an account seed in the xkey's place was accepted, or silently replaced")
		}
	})
	t.Run("an instance with no callout has nothing to introduce", func(t *testing.T) {
		st, err := hydrate("", "")
		if err != nil {
			t.Fatal(err)
		}
		if st.Values["natsCalloutXKeySeed"] != "" {
			t.Error("an xkey was minted for an instance that runs no device callout")
		}
	})
}

func TestTheBrokerRecordCarriesTheXKeyAndToleratesItsAbsence(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	prev := brokerRecordPathFor
	t.Cleanup(func() { brokerRecordPathFor = prev })
	brokerRecordPathFor = func(string) (string, error) { return dir + "/" + brokerRecordFile, nil }

	// A record written by this release round-trips the xkey.
	raw, _ := json.Marshal(brokerRecord{Instance: "prod", IssuerSeed: creds.IssuerSeed, XKeySeed: creds.XKeySeed,
		ServicePassword: "a", SysPassword: "b"})
	if err := os.WriteFile(dir+"/"+brokerRecordFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rec := readBrokerRecord("prod")
	if rec == nil || rec.XKeySeed != creds.XKeySeed {
		t.Fatalf("the xkey did not survive the record: %+v", rec)
	}

	// A record from before the xkey is still a record: its absence reads as "introduce one".
	raw, _ = json.Marshal(brokerRecord{Instance: "prod", IssuerSeed: creds.IssuerSeed, ServicePassword: "a", SysPassword: "b"})
	if err := os.WriteFile(dir+"/"+brokerRecordFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rec = readBrokerRecord("prod")
	if rec == nil || rec.XKeySeed != "" {
		t.Fatalf("a record without an xkey was rejected: %+v", rec)
	}
}
