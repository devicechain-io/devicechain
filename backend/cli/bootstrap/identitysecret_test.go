// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
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

// The identity account's login is in a Secret of its own that only user-management mounts. These
// tests hold the places that has to be true: what dcctl writes, what the chart projects, what the
// document carries, what dcctl reads back, and what an upgrade does to an instance that has none.

func identityState(t *testing.T) (*State, natsauth.Credentials) {
	t.Helper()
	st, creds := calloutState(t)
	if creds.IdentityPassword == "" || creds.IdentityPasswordBcrypt == "" {
		t.Fatal("GenerateCredentials minted no identity login")
	}
	return st, creds
}

func TestTheIdentitySecretCarriesTheLoginAndNothingElse(t *testing.T) {
	st, creds := identityState(t)
	spec, ok := identitySecret(st)
	if !ok {
		t.Fatal("an instance with broker authentication writes no identity Secret")
	}
	if spec.Namespace != "dci-"+testInstance || spec.Name != "dci-"+testInstance+"-identity" {
		t.Errorf("the Secret is written at %s/%s", spec.Namespace, spec.Name)
	}
	if len(spec.Data) != 1 || spec.Data["password"] != creds.IdentityPassword {
		t.Errorf("the Secret holds %v, want exactly the password", spec.Data)
	}
	if _, ok := identitySecret(&State{Instance: "x", Values: map[string]string{}}); ok {
		t.Error("an identity Secret is written for an instance with no login to put in it")
	}
}

// 🔴 THE CHART HAS TO READ WHAT DCCTL WRITES, AND ONLY USER-MANAGEMENT MAY READ IT. The password is
// the only way into the account that holds the refresh tokens, so any other workload that mounted the
// Secret would undo the account. The literals below are the contract, not a constant that would
// move both sides at once.
func TestTheChartReadsTheIdentitySecretDcctlWrites(t *testing.T) {
	enabled, err := ResolveEnabledAreas("default", nil)
	if err != nil {
		t.Fatal(err)
	}
	st, creds := identityState(t)
	st.Instance = "acme"
	st.Profile = "default"
	st.EnabledAreas = enabled
	st.ImageRegistry, st.ImageVersion = DefaultImageRegistry, "v0.0.0-test"
	st.Values["ingressHost"] = "localhost"
	st.Values["secretsRootKey"] = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

	written, _ := identitySecret(st)
	if written.Namespace != "dci-acme" || written.Name != "dci-acme-identity" {
		t.Fatalf("dcctl writes the identity Secret at %s/%s, want dci-acme/dci-acme-identity", written.Namespace, written.Name)
	}

	vals := helmValues(st)
	raw, err := json.Marshal(vals)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), creds.IdentityPassword) {
		t.Fatal("the identity password reached the Helm values, which are readable by anyone who can `helm get values`")
	}

	givenTo := ""
	for _, d := range renderDocs(t, vals) {
		b, _ := json.Marshal(d)
		kind, _ := d["kind"].(string)
		meta, _ := d["metadata"].(map[string]interface{})
		name, _ := meta["name"].(string)
		if strings.Contains(string(b), creds.IdentityPassword) {
			t.Errorf("the identity password was rendered into a %s %s", kind, name)
		}
		// ANY reference to the Secret outside user-management, by whatever route (an env var under
		// another name, envFrom, a volume, any other kind of workload), hands it the account.
		if strings.Contains(string(b), `"dci-acme-identity"`) && (kind != "Deployment" || name != "user-management") {
			t.Errorf("%s %s references the identity Secret; only user-management may", kind, name)
		}
		if kind != "Deployment" {
			continue
		}
		spec := d["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
		for _, c := range spec["containers"].([]interface{}) {
			env, _ := c.(map[string]interface{})["env"].([]interface{})
			for _, e := range env {
				ev := e.(map[string]interface{})
				if ev["name"] != "DC_NATS_IDENTITY_PASSWORD" {
					continue
				}
				if name != "user-management" {
					t.Errorf("Deployment %s is given DC_NATS_IDENTITY_PASSWORD; only user-management reads it", name)
					continue
				}
				givenTo = name
				if _, literal := ev["value"]; literal {
					t.Error("DC_NATS_IDENTITY_PASSWORD is rendered as a literal rather than read from the Secret")
				}
				ref, _ := ev["valueFrom"].(map[string]interface{})["secretKeyRef"].(map[string]interface{})
				if ref["name"] != "dci-acme-identity" || ref["key"] != "password" {
					t.Errorf("DC_NATS_IDENTITY_PASSWORD reads %v/%v; dcctl writes dci-acme-identity/password", ref["name"], ref["key"])
				}
				// Optional so an instance whose broker has no account starts; the service itself refuses
				// to start when its configuration says the account exists and this is empty.
				if ref["optional"] != true {
					t.Error("DC_NATS_IDENTITY_PASSWORD is not optional, so an instance with no identity account could not start user-management")
				}
			}
		}
	}
	if givenTo != "user-management" {
		t.Fatalf("the password was given to %q, want user-management", givenTo)
	}
}

func TestOnlyTheFlagForTheIdentityAccountReachesTheDocument(t *testing.T) {
	st, creds := identityState(t)
	doc, _ := composedForTest(t, withComposeBasics(st))
	var parsed config.InstanceConfiguration
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatal(err)
	}
	auth := parsed.Infrastructure.Nats.Auth
	if !auth.IdentityAccount {
		t.Error("the services are not told the identity account exists")
	}
	if auth.IdentityPassword != "" || strings.Contains(string(doc), creds.IdentityPassword) {
		t.Error("the identity PASSWORD is in the document every service mounts")
	}
}

func TestTheIdentitySecretIsWrittenOwnedAndReadBack(t *testing.T) {
	ctx := context.Background()
	st, creds := identityState(t)
	c := fake.NewSimpleClientset()

	if err := writeIdentitySecret(ctx, c, st); err != nil {
		t.Fatal(err)
	}
	settleStringDataLikeAnAPIServer(t, c)
	got, err := readIdentityPassword(ctx, c, InstanceNamespace(st.Instance), st.Instance)
	if err != nil || got != creds.IdentityPassword {
		t.Fatalf("read back %q / %v", got, err)
	}
	s, _ := c.CoreV1().Secrets(InstanceNamespace(st.Instance)).Get(ctx, identitySecretName(st.Instance), metav1.GetOptions{})
	if own := readOwnership(s); !own.managed || own.owner.UID != testUID {
		t.Errorf("the identity Secret is not stamped as this instance's: %+v", own)
	}
	if err := writeIdentitySecret(ctx, c, st); err != nil {
		t.Fatalf("an idempotent re-run could not rewrite its own Secret: %v", err)
	}
	other := aWritableState()
	other.InstanceUID = "a-different-generation"
	other.Values = st.Values
	var foreign *ErrForeignSecret
	if err := writeIdentitySecret(ctx, c, other); !errors.As(err, &foreign) {
		t.Fatalf("a Secret from another generation of the instance was adopted: %v", err)
	}
}

func TestReadingTheIdentityPasswordFailsClosed(t *testing.T) {
	ctx := context.Background()
	ns := InstanceNamespace(testInstance)
	got, err := readIdentityPassword(ctx, fake.NewSimpleClientset(), ns, testInstance)
	if err != nil || got != "" {
		t.Fatalf("an absent Secret read as %q / %v; an instance built before the account has none", got, err)
	}
	empty := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: identitySecretName(testInstance), Namespace: ns},
		Data: map[string][]byte{"other": []byte("x")}}
	if _, err := readIdentityPassword(ctx, fake.NewSimpleClientset(empty), ns, testInstance); err == nil {
		t.Fatal("an identity Secret with no password read as absent, which would mint a login over a broker that trusts the real one")
	}
}

func TestTheDeployedViewOverlaysTheIdentitySecret(t *testing.T) {
	ctx := context.Background()
	ns := InstanceNamespace(testInstance)
	cfg := &config.InstanceConfiguration{}
	cfg.Infrastructure.Nats.Auth.IdentityAccount = true
	raw, _ := json.Marshal(cfg)
	doc := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "dci-" + testInstance + "-config", Namespace: ns},
		Data: map[string][]byte{"instance": raw}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: identitySecretName(testInstance), Namespace: ns},
		Data: map[string][]byte{"password": []byte("the-login")}}

	got, err := deployedInstanceConfigVia(ctx, fake.NewSimpleClientset(doc, secret), testInstance)
	if err != nil {
		t.Fatal(err)
	}
	if got.Infrastructure.Nats.Auth.IdentityPassword != "the-login" || !got.Infrastructure.Nats.Auth.IdentityAccount {
		t.Errorf("view = %+v", got.Infrastructure.Nats.Auth)
	}
}

// The login is INTRODUCED: minted when the starting state has none, reused when it has one.
func TestAnUpgradeIntroducesTheIdentityLoginOnceAndNeverReplacesIt(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	hydrate := func(password string, deployedFlag, skipInfra bool) *State {
		st := &State{Instance: testInstance, SkipInfrastructure: skipInfra, Values: map[string]string{}}
		d := &config.InstanceConfiguration{}
		d.Infrastructure.Nats.Auth.CalloutIssuerSeed = creds.IssuerSeed
		d.Infrastructure.Nats.Auth.IdentityPassword = password
		d.Infrastructure.Nats.Auth.IdentityAccount = deployedFlag
		if err := applyDeployedInfrastructure(st, d); err != nil {
			t.Fatal(err)
		}
		return st
	}

	t.Run("an instance with no login is given one, and the account is turned on", func(t *testing.T) {
		st := hydrate("", false, false)
		if st.Values["natsIdentityPassword"] == "" || st.Values["natsIdentityPasswordBcrypt"] == "" {
			t.Fatalf("no login was introduced: %v", st.Values)
		}
		if !identityIntroduced(st) {
			t.Error("the first upgrade does not know it ends every session")
		}
	})
	t.Run("an instance that has one keeps it, and is not told it ended sessions again", func(t *testing.T) {
		st := hydrate(creds.IdentityPassword, true, false)
		if st.Values["natsIdentityPassword"] != creds.IdentityPassword {
			t.Error("an existing identity login was replaced")
		}
		if identityIntroduced(st) {
			t.Error("a later upgrade claims to have ended every session")
		}
	})
	t.Run("a run that skips the infrastructure does not claim an account the broker lacks", func(t *testing.T) {
		st := hydrate("", false, true)
		if st.Values["natsIdentityPassword"] == "" {
			t.Error("the login is still introduced, so the next full run has it")
		}
		if st.Values[identityEnabledValue] == "true" {
			t.Fatal("the document would tell user-management an account exists that the broker was never given; " +
				"it would refuse to start")
		}
		if v := strings.Join(infraVars(withIssuer(st, creds)), "\n"); strings.Contains(v, "nats_identity_password_bcrypt") {
			t.Error("the broker is handed an identity hash by a run that does not apply the broker")
		}
	})
	t.Run("a run that skips the infrastructure on an instance that has the account keeps it", func(t *testing.T) {
		st := hydrate(creds.IdentityPassword, true, true)
		if st.Values[identityEnabledValue] != "true" {
			t.Error("the account was turned off by a skipped apply")
		}
	})
	t.Run("an instance with no broker authentication has nothing to introduce", func(t *testing.T) {
		st := &State{Instance: testInstance, Values: map[string]string{}}
		if err := applyDeployedInfrastructure(st, &config.InstanceConfiguration{}); err != nil {
			t.Fatal(err)
		}
		if st.Values["natsIdentityPassword"] != "" {
			t.Error("a login was minted for a broker that has no authentication")
		}
	})
}

func withIssuer(st *State, creds natsauth.Credentials) *State {
	st.Values["natsCalloutIssuerPublic"] = creds.IssuerPublic
	st.Values["natsServicePasswordBcrypt"] = creds.ServicePasswordBcrypt
	return st
}

func TestTheBrokerIsHandedOnlyTheIdentityHash(t *testing.T) {
	st, creds := identityState(t)
	joined := strings.Join(infraVars(st), "\n")
	if !strings.Contains(joined, "nats_identity_password_bcrypt="+creds.IdentityPasswordBcrypt) {
		t.Error("the broker is not given the identity login's hash, so no account is rendered")
	}
	if strings.Contains(joined, creds.IdentityPassword) {
		t.Error("OpenTofu received the identity PLAINTEXT")
	}
}

func TestTheBrokerRecordCarriesTheIdentityLoginAndToleratesItsAbsence(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	prev := brokerRecordPathFor
	t.Cleanup(func() { brokerRecordPathFor = prev })
	brokerRecordPathFor = func(string) (string, error) { return dir + "/" + brokerRecordFile, nil }

	raw, _ := json.Marshal(brokerRecord{Instance: "prod", IssuerSeed: creds.IssuerSeed, XKeySeed: creds.XKeySeed,
		IdentityPassword: creds.IdentityPassword, ServicePassword: "a", SysPassword: "b"})
	if err := os.WriteFile(dir+"/"+brokerRecordFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := readBrokerRecord("prod"); rec == nil || rec.IdentityPassword != creds.IdentityPassword {
		t.Fatalf("the identity login did not survive the record: %+v", rec)
	}
	raw, _ = json.Marshal(brokerRecord{Instance: "prod", IssuerSeed: creds.IssuerSeed, ServicePassword: "a", SysPassword: "b"})
	if err := os.WriteFile(dir+"/"+brokerRecordFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := readBrokerRecord("prod"); rec == nil || rec.IdentityPassword != "" {
		t.Fatalf("a record from before the account was rejected: %+v", rec)
	}
}

// A hash the broker already holds is reused, so a re-run does not re-salt it and roll the broker.
func TestTheDeployedIdentityHashIsReadAndReused(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	rendered := `{"accounts": {"IDENTITY": {"jetstream": "enabled", "users": [` +
		`{"password": "` + creds.IdentityPasswordBcrypt + `", "user": "dc_identity"}]}}}`
	c := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: InstanceNamespace("acme"), Name: natsStatefulSetName + "-config"},
		Data:       map[string]string{"nats.conf": rendered},
	})
	got := deployedBrokerHashesIn(context.Background(), c, InstanceNamespace("acme"), natsStatefulSetName)
	if got.Identity != creds.IdentityPasswordBcrypt {
		t.Fatalf("read %+v, want the identity hash", got)
	}
	again, err := natsauth.Credentials{}.WithIdentity(creds.IdentityPassword, got.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if again.IdentityPasswordBcrypt != creds.IdentityPasswordBcrypt {
		t.Error("a hash that verifies was re-salted: that rolls the broker on every re-run")
	}
	wrong, err := natsauth.Credentials{}.WithIdentity("another-password", got.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if wrong.IdentityPasswordBcrypt == got.Identity {
		t.Error("a hash of a DIFFERENT password was shipped to the broker")
	}
}
