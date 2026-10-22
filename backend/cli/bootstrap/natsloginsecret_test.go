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
	"github.com/devicechain-io/dc-microservice/streams"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Every area that uses the broker has a login of its own, in a Secret only that area mounts. These
// tests hold the places that has to be true: what dcctl writes, what the chart projects into whom,
// what the document carries, what dcctl reads back, and what an upgrade does to an instance that
// has none, some, or all.

func loginState(t *testing.T) (*State, natsauth.Credentials) {
	t.Helper()
	st, creds := calloutState(t)
	if len(creds.Logins) != len(config.NatsLoginPrincipals()) {
		t.Fatalf("GenerateCredentials minted %d logins, want one per principal (%d)",
			len(creds.Logins), len(config.NatsLoginPrincipals()))
	}
	return st, creds
}

func TestEachNatsLoginSecretCarriesItsOwnUserAndPasswordAndNothingElse(t *testing.T) {
	st, creds := loginState(t)
	specs := natsLoginSecrets(st)
	if len(specs) != len(config.NatsLoginPrincipals()) {
		t.Fatalf("%d Secrets, want one per principal", len(specs))
	}
	seen := map[string]bool{}
	for _, spec := range specs {
		principal := strings.TrimPrefix(spec.Name, "dci-"+testInstance+"-nats-")
		l, ok := creds.Logins[principal]
		if !ok || spec.Namespace != "dci-"+testInstance {
			t.Errorf("Secret %s/%s does not belong to a principal", spec.Namespace, spec.Name)
			continue
		}
		seen[principal] = true
		if len(spec.Data) != 2 || spec.Data["user"] != "dc-"+principal+"-g1" || spec.Data["password"] != l.Password {
			t.Errorf("Secret %s holds %v, want exactly the user and the password of %s", spec.Name, spec.Data, principal)
		}
		for other, ol := range creds.Logins {
			if other != principal && strings.Contains(spec.Data["password"]+spec.Data["user"], ol.Password) {
				t.Errorf("Secret %s carries the password of %s", spec.Name, other)
			}
		}
	}
	if len(seen) != len(creds.Logins) {
		t.Errorf("Secrets for %v, want %d principals", seen, len(creds.Logins))
	}
	if got := natsLoginSecrets(&State{Instance: "x", Values: map[string]string{}}); len(got) != 0 {
		t.Error("Secrets are written for an instance with no logins to put in them")
	}
}

// 🔴 THE CHART HAS TO PROJECT EACH AREA ITS OWN LOGIN AND NO OTHER, AND mcp AND ai-inference NONE.
// The literals below are the contract with the chart, not constants that would move both sides at
// once. The control inside is the reason this can fail: the exempt areas are looked for as well as
// asserted absent, so a render that deployed none of them would not pass.
func TestTheChartMountsEachAreaItsOwnBrokerLogin(t *testing.T) {
	enabled, err := ResolveEnabledAreas("full", nil)
	if err != nil {
		t.Fatal(err)
	}
	st, creds := loginState(t)
	st.Instance = "acme"
	st.Profile = "full"
	st.EnabledAreas = enabled
	st.ImageRegistry, st.ImageVersion = DefaultImageRegistry, "v0.0.0-test"
	st.Values["ingressHost"] = "localhost"
	st.Values["secretsRootKey"] = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

	vals := helmValues(st)
	raw, err := json.Marshal(vals)
	if err != nil {
		t.Fatal(err)
	}
	for p, l := range creds.Logins {
		if strings.Contains(string(raw), l.Password) {
			t.Fatalf("the broker login of %s reached the Helm values, readable by anyone who can `helm get values`", p)
		}
	}

	exempt := map[string]bool{"mcp": true, "ai-inference": true}
	deployed := map[string]bool{}
	for _, d := range renderDocs(t, vals) {
		b, _ := json.Marshal(d)
		kind, _ := d["kind"].(string)
		meta, _ := d["metadata"].(map[string]interface{})
		name, _ := meta["name"].(string)
		for p, l := range creds.Logins {
			if strings.Contains(string(b), l.Password) {
				t.Errorf("the broker login of %s was rendered into a %s %s", p, kind, name)
			}
		}
		// A reference to ANY login Secret outside the Deployment of its own area hands that login to a
		// second workload, by whatever route (an env var under another name, envFrom, a volume).
		for _, p := range config.NatsLoginPrincipals() {
			if strings.Contains(string(b), `"dci-acme-nats-`+p+`"`) && (kind != "Deployment" || name != p) {
				t.Errorf("%s %s references the broker login Secret of %s; only the %s Deployment may", kind, name, p, p)
			}
		}
		if kind != "Deployment" {
			continue
		}
		if !streams.IsPlatformArea(name) {
			continue // the console's frontend is a Deployment too; it is not an area
		}
		deployed[name] = true
		spec := d["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
		got := map[string]map[string]interface{}{}
		for _, c := range spec["containers"].([]interface{}) {
			env, _ := c.(map[string]interface{})["env"].([]interface{})
			for _, e := range env {
				ev := e.(map[string]interface{})
				if n, _ := ev["name"].(string); n == config.EnvNatsUser || n == config.EnvNatsPassword {
					got[n] = ev
				}
			}
		}
		if exempt[name] {
			if len(got) != 0 {
				t.Errorf("Deployment %s never opens a broker connection and is given %v", name, got)
			}
			continue
		}
		if len(got) != 2 {
			t.Errorf("Deployment %s is given %d of DC_NATS_USER/DC_NATS_PASSWORD, want both", name, len(got))
			continue
		}
		for env, key := range map[string]string{config.EnvNatsUser: "user", config.EnvNatsPassword: "password"} {
			ev := got[env]
			if _, literal := ev["value"]; literal {
				t.Errorf("%s of %s is rendered as a literal rather than read from the Secret", env, name)
			}
			ref, _ := ev["valueFrom"].(map[string]interface{})["secretKeyRef"].(map[string]interface{})
			if ref["name"] != "dci-acme-nats-"+name || ref["key"] != key {
				t.Errorf("%s of %s reads %v/%v; dcctl writes dci-acme-nats-%s/%s", env, name, ref["name"], ref["key"], name, key)
			}
			// Optional so an instance whose broker has no per-area logins starts; the service itself
			// refuses to start when its configuration says they exist and this one is missing.
			if ref["optional"] != true {
				t.Errorf("%s of %s is not optional, so an instance with no per-area logins could not start it", env, name)
			}
		}
	}
	for area := range exempt {
		if !deployed[area] {
			t.Errorf("the control failed: %s was not deployed, so its absence of a login proves nothing", area)
		}
	}
	for _, a := range config.NatsLoginAreas() {
		if !deployed[a] {
			t.Errorf("the full profile did not deploy %s, so the chart's mount of its login was not examined", a)
		}
	}
	// ...and the set the chart exempts is the set Go does, read off what the render produced.
	for area := range deployed {
		if exempt[area] == config.NeedsNatsLogin(area) {
			t.Errorf("the chart and config.NeedsNatsLogin disagree about %s", area)
		}
	}
}

// The administrator's Secret is written and mounted by nobody yet: it exists so the step that gives
// stream administration one credential finds it already recorded.
func TestTheAdministratorsLoginIsMintedAndMountedNowhere(t *testing.T) {
	st, creds := loginState(t)
	if creds.Logins[config.NatsAdminPrincipal].User != "dc-broker-admin-g1" {
		t.Fatalf("the administrator's login is %+v", creds.Logins[config.NatsAdminPrincipal])
	}
	st.Instance = "acme"
	st.Profile = "default"
	enabled, _ := ResolveEnabledAreas("default", nil)
	st.EnabledAreas = enabled
	st.ImageRegistry, st.ImageVersion = DefaultImageRegistry, "v0.0.0-test"
	st.Values["ingressHost"] = "localhost"
	st.Values["secretsRootKey"] = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	for _, d := range renderDocs(t, helmValues(st)) {
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), "nats-"+config.NatsAdminPrincipal) {
			t.Errorf("%v references the administrator's Secret; nothing may mount it yet", d["kind"])
		}
	}
}

func TestOnlyTheFlagForTheAreaLoginsReachesTheDocument(t *testing.T) {
	st, creds := loginState(t)
	doc, _ := composedForTest(t, withComposeBasics(st))
	var parsed config.InstanceConfiguration
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatal(err)
	}
	auth := parsed.Infrastructure.Nats.Auth
	if !auth.AreaLogins {
		t.Error("the services are not told the per-area logins exist")
	}
	if len(auth.AreaPasswords) != 0 {
		t.Error("the document carries per-area passwords")
	}
	for p, l := range creds.Logins {
		if strings.Contains(string(doc), l.Password) {
			t.Errorf("the broker login of %s is in the document every service mounts", p)
		}
	}
}

func TestTheNatsLoginSecretsAreWrittenOwnedAndReadBack(t *testing.T) {
	ctx := context.Background()
	st, creds := loginState(t)
	c := fake.NewSimpleClientset()

	if err := writeNatsLoginSecrets(ctx, c, st); err != nil {
		t.Fatal(err)
	}
	settleStringDataLikeAnAPIServer(t, c)
	got, err := readNatsLoginPasswords(ctx, c, InstanceNamespace(st.Instance), st.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(creds.Logins) {
		t.Fatalf("read back %d logins, want %d", len(got), len(creds.Logins))
	}
	for p, l := range creds.Logins {
		if got[p] != l.Password {
			t.Errorf("%s read back %q", p, got[p])
		}
	}
	s, _ := c.CoreV1().Secrets(InstanceNamespace(st.Instance)).Get(ctx, natsLoginSecretName(st.Instance, "device-management"), metav1.GetOptions{})
	if own := readOwnership(s); !own.managed || own.owner.UID != testUID {
		t.Errorf("a login Secret is not stamped as this instance's: %+v", own)
	}
	if err := writeNatsLoginSecrets(ctx, c, st); err != nil {
		t.Fatalf("an idempotent re-run could not rewrite its own Secrets: %v", err)
	}
	other := aWritableState()
	other.InstanceUID = "a-different-generation"
	other.Values = st.Values
	var foreign *ErrForeignSecret
	if err := writeNatsLoginSecrets(ctx, c, other); !errors.As(err, &foreign) {
		t.Fatalf("a Secret from another generation of the instance was adopted: %v", err)
	}
}

func TestReadingTheNatsLoginsFailsClosed(t *testing.T) {
	ctx := context.Background()
	ns := InstanceNamespace(testInstance)
	got, err := readNatsLoginPasswords(ctx, fake.NewSimpleClientset(), ns, testInstance)
	if err != nil || len(got) != 0 {
		t.Fatalf("absent Secrets read as %v / %v; an instance built before the logins has none", got, err)
	}
	noPassword := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: natsLoginSecretName(testInstance, "device-state"), Namespace: ns},
		Data:       map[string][]byte{"user": []byte("dc-device-state-g1")},
	}
	if _, err := readNatsLoginPasswords(ctx, fake.NewSimpleClientset(noPassword), ns, testInstance); err == nil {
		t.Fatal("a login Secret with no password read as absent, which would mint a login over a broker that trusts the real one")
	}
}

func TestTheDeployedViewOverlaysTheNatsLoginSecrets(t *testing.T) {
	ctx := context.Background()
	ns := InstanceNamespace(testInstance)
	cfg := &config.InstanceConfiguration{}
	cfg.Infrastructure.Nats.Auth.AreaLogins = true
	raw, _ := json.Marshal(cfg)
	doc := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "dci-" + testInstance + "-config", Namespace: ns},
		Data: map[string][]byte{"instance": raw}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: natsLoginSecretName(testInstance, "device-state"), Namespace: ns},
		Data: map[string][]byte{"password": []byte("the-login")}}

	got, err := deployedInstanceConfigVia(ctx, fake.NewSimpleClientset(doc, secret), testInstance)
	if err != nil {
		t.Fatal(err)
	}
	auth := got.Infrastructure.Nats.Auth
	if auth.AreaPasswords["device-state"] != "the-login" || !auth.AreaLogins || len(auth.AreaPasswords) != 1 {
		t.Errorf("view = %+v", auth)
	}
}

// The logins are INTRODUCED: minted when the starting state has none, kept when it has them, and
// completed one principal at a time when it has some.
func TestAnUpgradeIntroducesTheNatsLoginsOnceAndNeverReplacesOne(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	hydrate := func(existing map[string]string, deployedFlag, skipInfra bool) *State {
		st := &State{Instance: testInstance, SkipInfrastructure: skipInfra, Values: map[string]string{}}
		d := &config.InstanceConfiguration{}
		d.Infrastructure.Nats.Auth.CalloutIssuerSeed = creds.IssuerSeed
		d.Infrastructure.Nats.Auth.AreaPasswords = existing
		d.Infrastructure.Nats.Auth.AreaLogins = deployedFlag
		if err := applyDeployedInfrastructure(st, d); err != nil {
			t.Fatal(err)
		}
		return st
	}
	allPlaintexts := func() map[string]string {
		out := map[string]string{}
		for p, l := range creds.Logins {
			out[p] = l.Password
		}
		return out
	}

	t.Run("an instance with none is given every one, and the logins are turned on", func(t *testing.T) {
		st := hydrate(nil, false, false)
		for _, p := range config.NatsLoginPrincipals() {
			if loginPassword(st, p) == "" || loginBcrypt(st, p) == "" {
				t.Fatalf("no login was introduced for %s: %v", p, st.Values)
			}
		}
		if !areaLoginsIntroduced(st) {
			t.Error("the first upgrade does not know it introduced the logins")
		}
		if st.Values[areaLoginsEnabledValue] != "true" {
			t.Error("the document would not tell the areas their logins exist")
		}
	})
	t.Run("an instance that has them keeps every one", func(t *testing.T) {
		st := hydrate(allPlaintexts(), true, false)
		for p, l := range creds.Logins {
			if loginPassword(st, p) != l.Password {
				t.Errorf("the login of %s was replaced", p)
			}
		}
		if areaLoginsIntroduced(st) {
			t.Error("a later upgrade claims to have introduced the logins")
		}
	})
	t.Run("an instance with only some keeps those and is given the rest", func(t *testing.T) {
		some := map[string]string{
			"device-management": creds.Logins["device-management"].Password,
			"user-management":   creds.Logins["user-management"].Password,
		}
		st := hydrate(some, false, false)
		for p, pw := range some {
			if loginPassword(st, p) != pw {
				t.Errorf("the existing login of %s was replaced", p)
			}
		}
		for _, p := range config.NatsLoginPrincipals() {
			if loginPassword(st, p) == "" {
				t.Errorf("the missing login of %s was not introduced", p)
			}
		}
		if loginPassword(st, "device-state") == creds.Logins["device-state"].Password {
			t.Error("a missing login was derived from something rather than minted")
		}
	})
	t.Run("a run that skips the infrastructure does not claim logins the broker lacks", func(t *testing.T) {
		st := hydrate(nil, false, true)
		if loginPassword(st, "device-state") == "" {
			t.Error("the logins are still introduced, so the next full run has them")
		}
		if st.Values[areaLoginsEnabledValue] == "true" {
			t.Fatal("the document would tell every area it has a login the broker was never given; none would start")
		}
		if v := strings.Join(infraVars(withIssuer(st, creds)), "\n"); strings.Contains(v, "nats_login_password_bcrypts") {
			t.Error("the broker is handed login hashes by a run that does not apply the broker")
		}
	})
	t.Run("a run that skips the infrastructure on an instance that has them keeps them", func(t *testing.T) {
		st := hydrate(allPlaintexts(), true, true)
		if st.Values[areaLoginsEnabledValue] != "true" {
			t.Error("the logins were turned off by a skipped apply")
		}
	})
	t.Run("an instance with no broker authentication has nothing to introduce", func(t *testing.T) {
		st := &State{Instance: testInstance, Values: map[string]string{}}
		if err := applyDeployedInfrastructure(st, &config.InstanceConfiguration{}); err != nil {
			t.Fatal(err)
		}
		if loginPassword(st, "device-state") != "" {
			t.Error("a login was minted for a broker that has no authentication")
		}
	})
}

func TestTheBrokerIsHandedEveryLoginHashAndNoPlaintext(t *testing.T) {
	st, creds := loginState(t)
	var line string
	for _, v := range infraVars(st) {
		if strings.HasPrefix(v, "nats_login_password_bcrypts=") {
			line = v
		}
	}
	if line == "" {
		t.Fatal("the broker is not given the login hashes, so it renders no login")
	}
	for p, l := range creds.Logins {
		if !strings.Contains(line, `"dc-`+p+`-g1"=`+`"`+l.PasswordBcrypt+`"`) {
			t.Errorf("the hash of %s is not handed over under its username", p)
		}
		if strings.Contains(strings.Join(infraVars(st), "\n"), l.Password) {
			t.Errorf("OpenTofu received the PLAINTEXT of %s", p)
		}
	}
	if _, inst, err := splitVars([]string{line}); err != nil || len(inst) != 1 {
		t.Errorf("the instance root does not declare the variable dcctl passes: %v", err)
	}
}

func TestTheBrokerRecordCarriesTheLoginsAndToleratesTheirAbsence(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	prev := brokerRecordPathFor
	t.Cleanup(func() { brokerRecordPathFor = prev })
	brokerRecordPathFor = func(string) (string, error) { return dir + "/" + brokerRecordFile, nil }

	raw, _ := json.Marshal(brokerRecord{Instance: "prod", IssuerSeed: creds.IssuerSeed,
		AreaLogins: loginPlaintexts(creds), ServicePassword: "a", SysPassword: "b"})
	if err := os.WriteFile(dir+"/"+brokerRecordFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rec := readBrokerRecord("prod")
	if rec == nil || len(rec.AreaLogins) != len(creds.Logins) || rec.AreaLogins["user-management"] != creds.Logins["user-management"].Password {
		t.Fatalf("the logins did not survive the record: %+v", rec)
	}
	raw, _ = json.Marshal(brokerRecord{Instance: "prod", IssuerSeed: creds.IssuerSeed, ServicePassword: "a", SysPassword: "b"})
	if err := os.WriteFile(dir+"/"+brokerRecordFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := readBrokerRecord("prod"); rec == nil || len(rec.AreaLogins) != 0 {
		t.Fatalf("a record from before the logins was rejected: %+v", rec)
	}
}

// Hashes the broker already holds are reused, so a re-run does not re-salt them and roll the broker.
func TestTheDeployedLoginHashesAreReadAndReused(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	// The chart renders each user as a JSON object with its keys sorted, so `permissions` sits
	// between `password` and `user`.
	var users []string
	for _, p := range config.NatsLoginPrincipals() {
		l := creds.Logins[p]
		users = append(users, `{"password": "`+l.PasswordBcrypt+`", "permissions": {"publish": {"deny": ["a.*"]}}, "user": "`+l.User+`"}`)
	}
	rendered := `{"accounts": {"APP": {"users": [` + strings.Join(users, ",") + `]}}}`
	c := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: InstanceNamespace("acme"), Name: natsStatefulSetName + "-config"},
		Data:       map[string]string{"nats.conf": rendered},
	})
	got := deployedBrokerHashesIn(context.Background(), c, InstanceNamespace("acme"), natsStatefulSetName)
	if len(got.Logins) != len(creds.Logins) {
		t.Fatalf("read %d login hashes, want %d: %+v", len(got.Logins), len(creds.Logins), got.Logins)
	}
	existing := map[string]string{}
	for p, l := range creds.Logins {
		existing[p] = l.Password
	}
	again, err := natsauth.Credentials{}.WithLogins(existing, got.Logins)
	if err != nil {
		t.Fatal(err)
	}
	for p, l := range creds.Logins {
		if again.Logins[p].PasswordBcrypt != l.PasswordBcrypt {
			t.Errorf("a hash of %s that verifies was re-salted: that rolls the broker on every re-run", p)
		}
	}
	existing["device-state"] = "another-password"
	wrong, err := natsauth.Credentials{}.WithLogins(existing, got.Logins)
	if err != nil {
		t.Fatal(err)
	}
	if wrong.Logins["device-state"].PasswordBcrypt == creds.Logins["device-state"].PasswordBcrypt {
		t.Error("a hash of a DIFFERENT password was shipped to the broker")
	}
}
