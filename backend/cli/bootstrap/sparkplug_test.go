// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const brokerPassword = "s3cret-broker-pw"

func writeSourcesFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sources.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("writing sources file: %v", err)
	}
	return p
}

const goodSources = `
- tenant: plant
  hostId: devicechain
  broker:
    url: tcp://spb-mosquitto:1883
    username: devicechain
    password: ` + brokerPassword + `
  groups: [plant-a]
  autoRegister: true
  deviceTypeToken: sparkplug-pump
  commands:
    enabled: true
    hostRole: primary
    writableMetrics: ["Outputs/*"]
`

func TestParseSparkplugSources(t *testing.T) {
	t.Run("the flag off is no sources", func(t *testing.T) {
		got, err := ParseSparkplugSources("")
		if err != nil || got != nil {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("a good file parses, as YAML or JSON", func(t *testing.T) {
		for name, body := range map[string]string{
			"yaml": goodSources,
			"json": `[{"tenant":"plant","hostId":"devicechain","broker":{"url":"tcp://spb-mosquitto:1883","username":"devicechain","password":"` + brokerPassword + `"},"groups":["plant-a"],"autoRegister":true,"deviceTypeToken":"sparkplug-pump","commands":{"enabled":true,"hostRole":"primary","writableMetrics":["Outputs/*"]}}]`,
		} {
			got, err := ParseSparkplugSources(writeSourcesFile(t, body))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(got) != 1 || got[0].Tenant != "plant" || got[0].Broker.Password != brokerPassword || !got[0].AutoRegister {
				t.Fatalf("%s: parsed %+v", name, got)
			}
		}
	})
	t.Run("the password may come from a file", func(t *testing.T) {
		pw := filepath.Join(t.TempDir(), "pw")
		if err := os.WriteFile(pw, []byte(brokerPassword+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ParseSparkplugSources(writeSourcesFile(t, "- tenant: t\n  broker: {url: 'tcp://b:1883', username: u, passwordFile: "+pw+"}\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Broker.Password != brokerPassword || got[0].Broker.PasswordFile != "" {
			t.Fatalf("the password file was not resolved: %+v", got[0].Broker)
		}
	})

	for name, tc := range map[string]struct{ body, want string }{
		"no sources":                {"[]", "holds no sources"},
		"an unknown key":            {"- tenant: t\n  broker: {url: 'tcp://b:1883'}\n  tentant: x\n", "tentant"},
		"no tenant":                 {"- broker: {url: 'tcp://b:1883'}\n", "tenant is required"},
		"no url":                    {"- tenant: t\n", "broker.url is required"},
		"no host":                   {"- tenant: t\n  broker: {url: 'tcp://'}\n", "must include a host"},
		"a scheme the pod refuses":  {"- tenant: t\n  broker: {url: 'http://b:1883'}\n", "unsupported scheme"},
		"autoRegister with no type": {"- tenant: t\n  broker: {url: 'tcp://b:1883'}\n  autoRegister: true\n", "deviceTypeToken is required"},
		"a wildcard group":          {"- tenant: t\n  broker: {url: 'tcp://b:1883'}\n  groups: ['a/#']\n", "single MQTT topic level"},
		"a password with no login":  {"- tenant: t\n  broker: {url: 'tcp://b:1883', password: p}\n", "without broker.username"},
		"a misspelled command":      {"- tenant: t\n  broker: {url: 'tcp://b:1883'}\n  commands: {enable: true}\n", "commands.enable is not a command setting"},
		"two tenants on a broker":   {"- tenant: a\n  hostId: h1\n  broker: {url: 'tcp://b:1883'}\n- tenant: b\n  hostId: h2\n  broker: {url: 'TCP://B:1883'}\n", "trust boundary"},
		"one STATE identity twice":  {"- tenant: a\n  hostId: h\n  broker: {url: 'tcp://b:1883'}\n- tenant: a\n  hostId: h\n  broker: {url: 'tcp://b:1883'}\n", "STATE identity"},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			_, err := ParseSparkplugSources(writeSourcesFile(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one naming %q", err, tc.want)
			}
		})
	}
}

// The password is a credential: whatever an error or a log line formats from a source must not
// carry it.
func TestASourceNeverFormatsItsPassword(t *testing.T) {
	sources, err := ParseSparkplugSources(writeSourcesFile(t, goodSources))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
		if out := fmt.Sprintf(f, sources[0]); strings.Contains(out, brokerPassword) {
			t.Errorf("%s of a source printed the password: %s", f, out)
		}
		if out := fmt.Sprintf(f, sources); strings.Contains(out, brokerPassword) {
			t.Errorf("%s of the sources printed the password: %s", f, out)
		}
	}
	// A refusal names the source by position and tenant, never by its contents.
	_, err = ParseSparkplugSources(writeSourcesFile(t, "- tenant: t\n  broker: {url: 'http://b', username: u, password: "+brokerPassword+"}\n"))
	if err == nil || strings.Contains(err.Error(), brokerPassword) {
		t.Fatalf("a refusal carried the password: %v", err)
	}
}

func TestSparkplugProvisioningComposition(t *testing.T) {
	sources, err := ParseSparkplugSources(writeSourcesFile(t, goodSources+`
- tenant: other
  hostId: second
  broker: {url: 'tcp://other:1883'}
- tenant: third
  hostId: third
  broker:
    url: 'tcp://third:1883'
    username: u
    passwordSecretRef: {name: my-broker-secret, key: pw}
`))
	if err != nil {
		t.Fatal(err)
	}
	area := sparkplugProvisioning("dctest", sources)

	cfg := area["config"].(map[string]interface{})["sources"].([]interface{})
	b0 := cfg[0].(map[string]interface{})["broker"].(map[string]interface{})
	if b0["passwordEnv"] != "DC_SPARKPLUG_BROKER_PW_1" || b0["url"] != "tcp://spb-mosquitto:1883" || b0["username"] != "devicechain" {
		t.Fatalf("source 0 broker = %v", b0)
	}
	if _, leaked := b0["password"]; leaked {
		t.Fatal("the password was written into the source's configuration")
	}
	if raw, _ := json.Marshal(area); strings.Contains(string(raw), brokerPassword) {
		t.Fatal("the area's configuration or env carries the password")
	}
	b1 := cfg[1].(map[string]interface{})["broker"].(map[string]interface{})
	if _, has := b1["passwordEnv"]; has {
		t.Fatalf("a source with no password names a variable: %v", b1)
	}
	b2 := cfg[2].(map[string]interface{})["broker"].(map[string]interface{})
	if b2["passwordEnv"] != "DC_SPARKPLUG_BROKER_PW_3" {
		t.Fatalf("source 2 broker = %v", b2)
	}

	env := area["extraEnv"].([]interface{})
	if len(env) != 2 {
		t.Fatalf("extraEnv = %v", env)
	}
	ref := func(i int) (string, map[string]interface{}) {
		e := env[i].(map[string]interface{})
		return e["name"].(string), e["valueFrom"].(map[string]interface{})["secretKeyRef"].(map[string]interface{})
	}
	if name, r := ref(0); name != "DC_SPARKPLUG_BROKER_PW_1" || r["name"] != "dci-dctest-sparkplug-broker" || r["key"] != "DC_SPARKPLUG_BROKER_PW_1" {
		t.Fatalf("the dcctl-written projection does not tie to the Secret: %v", r)
	}
	// An external reference projects the operator's Secret, under the variable the source names.
	if name, r := ref(1); name != "DC_SPARKPLUG_BROKER_PW_3" || r["name"] != "my-broker-secret" || r["key"] != "pw" {
		t.Fatalf("the external projection is wrong: %v", r)
	}

	// Only a password dcctl holds goes in the Secret dcctl writes.
	data := sparkplugBrokerSecretData(sources)
	if len(data) != 1 || data["DC_SPARKPLUG_BROKER_PW_1"] != brokerPassword {
		t.Fatalf("the Secret data = %v", data)
	}
	if got := sparkplugBrokerSecretData(sources[1:]); len(got) != 0 {
		t.Fatalf("a Secret would be written for sources with no password of their own: %v", got)
	}
}

func TestPasswordSecretRefIsValidated(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"with a password":    {"- tenant: t\n  broker: {url: 'tcp://b:1883', username: u, password: p, passwordSecretRef: {name: s, key: k}}\n", "not both"},
		"without a key":      {"- tenant: t\n  broker: {url: 'tcp://b:1883', username: u, passwordSecretRef: {name: s}}\n", "needs the name of a Secret"},
		"a bad name":         {"- tenant: t\n  broker: {url: 'tcp://b:1883', username: u, passwordSecretRef: {name: 'Not_A_Name', key: k}}\n", "needs the name of a Secret"},
		"without a username": {"- tenant: t\n  broker: {url: 'tcp://b:1883', passwordSecretRef: {name: s, key: k}}\n", "without broker.username"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSparkplugSources(writeSourcesFile(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one naming %q", err, tc.want)
			}
		})
	}
}

func sparkplugState(sources []SparkplugSource) *State {
	enabled, err := ResolveEnabledAreas("default", []string{"sparkplug-ingest"})
	if err != nil {
		panic(err)
	}
	return &State{
		Instance:              "dctest",
		InstanceUID:           "uid-1",
		Profile:               "default",
		ImageRegistry:         DefaultImageRegistry,
		ImageVersion:          "v0.0.0-test",
		EnabledAreas:          enabled,
		SparkplugSources:      sources,
		SparkplugSourcesGiven: len(sources) > 0,
		Values: map[string]string{
			"ingressHost":    "localhost",
			"secretsRootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
	}
}

// what the rendered chart holds for the Sparkplug sources.
type renderedSparkplug struct {
	docs      []map[string]interface{}
	configRaw string
	env       []interface{}
	podAnno   map[string]interface{}
}

func renderSparkplug(t *testing.T, vals map[string]interface{}) renderedSparkplug {
	t.Helper()
	out := renderedSparkplug{docs: renderDocs(t, vals)}
	for _, d := range out.docs {
		kind, _ := d["kind"].(string)
		meta, _ := d["metadata"].(map[string]interface{})
		name, _ := meta["name"].(string)
		switch {
		case kind == "ConfigMap" && name == "dct-dctest-config":
			if data, ok := d["data"].(map[string]interface{}); ok {
				out.configRaw, _ = data["sparkplug-ingest"].(string)
			}
		case kind == "Deployment" && name == "sparkplug-ingest":
			tpl := d["spec"].(map[string]interface{})["template"].(map[string]interface{})
			out.podAnno, _ = tpl["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})
			spec := tpl["spec"].(map[string]interface{})
			out.env, _ = spec["containers"].([]interface{})[0].(map[string]interface{})["env"].([]interface{})
		}
	}
	return out
}

// anywhere says whether s appears in the JSON form of v.
func anywhere(v interface{}, s string) bool {
	raw, _ := json.Marshal(v)
	return strings.Contains(string(raw), s)
}

// 🔴 H1: THE PASSWORD IS NEVER A HELM VALUE, AND NEVER IN THE RENDERED MANIFEST. A release records
// the values of every revision it keeps; the Secret is dcctl's, written outside it.
func TestSparkplugSourcesRenderEndToEnd(t *testing.T) {
	t.Parallel()
	sources, err := ParseSparkplugSources(writeSourcesFile(t, goodSources))
	if err != nil {
		t.Fatal(err)
	}
	st := sparkplugState(sources)
	vals := helmValues(st)
	stamp, err := sparkplugRun(t, versionedCluster(), st)
	if err != nil {
		t.Fatal(err)
	}
	withSparkplugCredentialsChecksum(vals, stamp)
	got := renderSparkplug(t, vals)

	if anywhere(vals, brokerPassword) {
		t.Fatal("the broker password is in the release values")
	}
	if anywhere(got.docs, brokerPassword) {
		t.Fatal("the broker password is in the rendered manifest")
	}
	for _, d := range got.docs {
		if d["kind"] == "Secret" {
			meta, _ := d["metadata"].(map[string]interface{})
			if meta["name"] == "dci-dctest-sparkplug-broker" {
				t.Fatal("the chart renders the broker Secret, so it would be in the release manifest")
			}
		}
	}
	if got.configRaw == "" {
		t.Fatal("sparkplug-ingest has no key in the microservice ConfigMap")
	}
	var cfg struct {
		Sources []struct {
			Tenant          string
			DeviceTypeToken string
			AutoRegister    bool
			Groups          []string
			Broker          struct{ URL, Username, PasswordEnv string }
			Commands        struct {
				Enabled  bool
				HostRole string
			}
		}
	}
	if err := json.Unmarshal([]byte(got.configRaw), &cfg); err != nil {
		t.Fatalf("decoding the area configuration: %v (%q)", err, got.configRaw)
	}
	if len(cfg.Sources) != 1 {
		t.Fatalf("sources in the mounted configuration = %d", len(cfg.Sources))
	}
	s := cfg.Sources[0]
	if s.Tenant != "plant" || s.Broker.URL != "tcp://spb-mosquitto:1883" || s.Broker.PasswordEnv != "DC_SPARKPLUG_BROKER_PW_1" ||
		!s.AutoRegister || s.DeviceTypeToken != "sparkplug-pump" || !s.Commands.Enabled || s.Commands.HostRole != "primary" {
		t.Fatalf("the source did not reach the configuration intact: %+v", s)
	}
	found := false
	for _, e := range got.env {
		ev := e.(map[string]interface{})
		if ev["name"] != "DC_SPARKPLUG_BROKER_PW_1" {
			continue
		}
		ref := ev["valueFrom"].(map[string]interface{})["secretKeyRef"].(map[string]interface{})
		if ref["name"] != "dci-dctest-sparkplug-broker" || ref["key"] != "DC_SPARKPLUG_BROKER_PW_1" {
			t.Fatalf("the env projection points at %v", ref)
		}
		found = true
	}
	if !found {
		t.Fatal("the sparkplug-ingest Deployment has no env projecting the password")
	}
}

// 🔴 M1: THE POD TEMPLATE CARRIES A STAMP DERIVED FROM THE SECRET'S VERSION AND SALTED PER INSTANCE, AND
// NOTHING DERIVED FROM THE PASSWORD: not a bare hash, not a salted one.
func TestTheSparkplugPodsCarryAVersionStampAndNothingDerivedFromThePassword(t *testing.T) {
	t.Parallel()
	secret := func(rv, pw string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "dci-dctest-sparkplug-broker", UID: "su", ResourceVersion: rv},
			Data:       map[string][]byte{"DC_SPARKPLUG_BROKER_PW_1": []byte(pw)},
		}
	}
	stamp := sparkplugCredentialsStamp("uid-1", secret("7", brokerPassword))
	if stamp == "" {
		t.Fatal("no stamp for a Secret")
	}
	if stamp != sparkplugCredentialsStamp("uid-1", secret("7", "a completely different password")) {
		t.Fatal("the stamp depends on the password: it is a digest of a secret, which the release values would then expose to guessing")
	}
	if stamp == sparkplugCredentialsStamp("uid-1", secret("8", brokerPassword)) {
		t.Fatal("a new version of the Secret does not change the stamp, so the pods would not roll")
	}
	if stamp == sparkplugCredentialsStamp("uid-2", secret("7", brokerPassword)) {
		t.Fatal("two instances share a stamp: it is not salted")
	}
	if sparkplugCredentialsStamp("uid-1", nil) != "" {
		t.Fatal("an absent Secret has a stamp")
	}
	bare := sha256.Sum256([]byte(brokerPassword))
	if stamp == fmt.Sprintf("%x", bare) {
		t.Fatal("the stamp is the bare SHA-256 of the password")
	}

	sources, _ := ParseSparkplugSources(writeSourcesFile(t, goodSources))
	st := sparkplugState(sources)
	vals := helmValues(st)
	withSparkplugCredentialsChecksum(vals, stamp)
	got := renderSparkplug(t, vals)
	if got.podAnno["checksum/area-credentials"] != stamp {
		t.Fatalf("the pod template does not carry the stamp: %v", got.podAnno)
	}
	for _, d := range got.docs {
		if anywhere(d, fmt.Sprintf("%x", bare)) {
			t.Fatal("an unsalted hash of the password is in the rendered manifest")
		}
	}
	// And the extra-secrets hash (still over LwM2M's values) no longer covers a Sparkplug password.
	extra := sha256.Sum256([]byte(`[{"name":"dci-dctest-sparkplug-broker","stringData":{"DC_SPARKPLUG_BROKER_PW_1":"` + brokerPassword + `"}}]`))
	if anywhere(got.docs, fmt.Sprintf("%x", extra)) {
		t.Fatal("the Sparkplug password is still inside the extraSecrets hash")
	}
}

// versionedCluster is a fake cluster whose Secrets get a UID and a resourceVersion that moves on
// every write, as an API server's do.
func versionedCluster(objs ...runtime.Object) *fake.Clientset {
	typed := fake.NewSimpleClientset(objs...)
	n := 0
	typed.PrependReactor("*", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		var obj runtime.Object
		switch a := action.(type) {
		case k8stesting.CreateAction:
			obj = a.GetObject()
		case k8stesting.UpdateAction:
			obj = a.GetObject()
		}
		if sec, ok := obj.(*corev1.Secret); ok {
			n++
			sec.ResourceVersion = fmt.Sprint(n)
			if sec.UID == "" {
				sec.UID = "secret-uid"
			}
		}
		return false, nil, nil
	})
	return typed
}

// sparkplugRun is one run's worth of writes against a fake cluster.
func sparkplugRun(t *testing.T, typed kubernetes.Interface, st *State) (string, error) {
	t.Helper()
	stamp, _, err := syncSparkplugBrokerSecret(context.Background(), typed, st)
	return stamp, err
}

// sparkplugRunSteps is sparkplugRun with the second phase handed back.
func sparkplugRunSteps(t *testing.T, typed kubernetes.Interface, st *State) (string, postReleaseStep, error) {
	t.Helper()
	return syncSparkplugBrokerSecret(context.Background(), typed, st)
}

func brokerSecret(t *testing.T, typed kubernetes.Interface) (map[string][]byte, bool) {
	t.Helper()
	s, err := typed.CoreV1().Secrets("dci-dctest").Get(context.Background(), "dci-dctest-sparkplug-broker", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for k, v := range s.Data {
		out[k] = v
	}
	for k, v := range s.StringData {
		out[k] = []byte(v)
	}
	return out, true
}

// 🔴 H1: AN UPGRADE KEEPS THE SOURCE WITHOUT READING ANY SECRET FROM THE RELEASE. The release
// values hold no password; the carry puts the source back from them; the password stays in the
// Secret dcctl wrote, which the upgrade leaves alone and reads only for the pods' digest.
func TestAnUpgradeKeepsTheSparkplugSourcesWithoutReadingASecretFromTheRelease(t *testing.T) {
	t.Parallel()
	sources, err := ParseSparkplugSources(writeSourcesFile(t, goodSources))
	if err != nil {
		t.Fatal(err)
	}
	typed := versionedCluster()

	// The bootstrap that gave the file.
	first := sparkplugState(sources)
	stamp1, err := sparkplugRun(t, typed, first)
	if err != nil {
		t.Fatal(err)
	}
	previous := helmValues(first)
	withSparkplugCredentialsChecksum(previous, stamp1)
	// The release record, as `helm get values` would print it.
	if anywhere(previous, brokerPassword) {
		t.Fatal("the release values hold the broker password")
	}
	if _, ok := brokerSecret(t, typed); !ok {
		t.Fatal("the Secret was not written")
	}

	// The upgrade: no file, and the operator's overlay tunes the same area.
	st := sparkplugState(nil)
	st.Evolving = true
	st.ValuesOverlay = map[string]interface{}{
		"functionalAreas": map[string]interface{}{
			"sparkplug-ingest": map[string]interface{}{"resources": map[string]interface{}{"limits": map[string]interface{}{"memory": "512Mi"}}},
		},
	}
	fresh := authoringValues(st)
	stamp2, err := sparkplugRun(t, typed, st)
	if err != nil {
		t.Fatal(err)
	}
	withSparkplugCredentialsChecksum(fresh, stamp2)
	carryReleaseValues(fresh, previous)

	if stamp2 != stamp1 {
		t.Fatal("an upgrade that was given no file changed the pods' stamp, which would roll them for nothing")
	}
	if data, ok := brokerSecret(t, typed); !ok || string(data["DC_SPARKPLUG_BROKER_PW_1"]) != brokerPassword {
		t.Fatalf("an upgrade disturbed the password Secret: %v", data)
	}
	got := renderSparkplug(t, fresh)
	if got.configRaw == "" || !strings.Contains(got.configRaw, `"tcp://spb-mosquitto:1883"`) || !strings.Contains(got.configRaw, "DC_SPARKPLUG_BROKER_PW_1") {
		t.Fatalf("an upgrade dropped the source: %q", got.configRaw)
	}
	if anywhere(fresh, brokerPassword) || anywhere(got.docs, brokerPassword) {
		t.Fatal("the upgraded release holds the password")
	}
	area := fresh["functionalAreas"].(map[string]interface{})["sparkplug-ingest"].(map[string]interface{})
	if _, ok := area["resources"]; !ok {
		t.Fatalf("the overlay's tuning was lost by the carry: %v", area)
	}
}

// A rotation replaces the dcctl-written Secret in place, changes the digest (so the pods roll), and
// the file given again replaces the carried source rather than being overwritten by it.
func TestAFileGivenAgainRotatesTheSecretAndReplacesTheCarriedSources(t *testing.T) {
	t.Parallel()
	old, _ := ParseSparkplugSources(writeSourcesFile(t, goodSources))
	typed := versionedCluster()
	first := sparkplugState(old)
	stamp1, err := sparkplugRun(t, typed, first)
	if err != nil {
		t.Fatal(err)
	}
	previous := helmValues(first)
	withSparkplugCredentialsChecksum(previous, stamp1)

	replacement, err := ParseSparkplugSources(writeSourcesFile(t, strings.ReplaceAll(strings.ReplaceAll(goodSources, "spb-mosquitto", "new-broker"), brokerPassword, "rotated-pw")))
	if err != nil {
		t.Fatal(err)
	}
	st := sparkplugState(replacement)
	st.Evolving = true
	fresh := authoringValues(st)
	stamp2, err := sparkplugRun(t, typed, st)
	if err != nil {
		t.Fatal(err)
	}
	withSparkplugCredentialsChecksum(fresh, stamp2)
	// What releaseValues does for a run that decides the sources.
	carryReleaseValues(fresh, withoutSparkplugSources(previous))

	data, _ := brokerSecret(t, typed)
	if string(data["DC_SPARKPLUG_BROKER_PW_1"]) != "rotated-pw" {
		t.Fatalf("the Secret still holds the old password: %v", data)
	}
	if stamp1 == stamp2 {
		t.Fatal("a rotation did not change the stamp, so the pods would not roll")
	}
	got := renderSparkplug(t, fresh)
	if !strings.Contains(got.configRaw, "new-broker") || strings.Contains(got.configRaw, "spb-mosquitto") {
		t.Fatalf("the carry overwrote the new sources: %s", got.configRaw)
	}
	if anywhere(fresh, "rotated-pw") || anywhere(fresh, brokerPassword) || anywhere(got.docs, "rotated-pw") {
		t.Fatal("a password is in the release")
	}
}

// 🔴 L1 + M3: WITHDRAWING (an explicit run with no sources) removes the source from the release and the
// password from the cluster, and the carry cannot put either back; so does a run whose sources carry
// no password of their own.
func TestWithdrawingOrRetiringAPasswordRemovesTheSecretAndTheCarriedLeaves(t *testing.T) {
	t.Parallel()
	old, _ := ParseSparkplugSources(writeSourcesFile(t, goodSources))
	for name, next := range map[string][]SparkplugSource{
		"withdrawn":         nil,
		"password-less set": {{Tenant: "plant", Broker: SparkplugBroker{URL: "tcp://spb-mosquitto:1883"}}},
	} {
		t.Run(name, func(t *testing.T) {
			typed := versionedCluster()
			first := sparkplugState(old)
			stamp1, err := sparkplugRun(t, typed, first)
			if err != nil {
				t.Fatal(err)
			}
			previous := helmValues(first)
			withSparkplugCredentialsChecksum(previous, stamp1)

			st := sparkplugState(next)
			st.Evolving = true
			st.SparkplugSourcesGiven = true // a withdrawal gives no sources and still decides
			fresh := authoringValues(st)
			stamp2, post, err := sparkplugRunSteps(t, typed, st)
			if err != nil {
				t.Fatal(err)
			}
			withSparkplugCredentialsChecksum(fresh, stamp2)
			carryReleaseValues(fresh, withoutSparkplugSources(previous))

			if _, ok := brokerSecret(t, typed); !ok {
				t.Fatal("the Secret was removed BEFORE the release")
			}
			if err := finishOwnedSecrets(context.Background(), nil, []postReleaseStep{post}, func(m string) { t.Fatal(m) }); err != nil {
				t.Fatal(err)
			}
			if _, ok := brokerSecret(t, typed); ok {
				t.Fatal("the retired password Secret is still in the cluster")
			}
			if stamp2 != "" {
				t.Fatalf("a stamp is set for a Secret that is gone: %q", stamp2)
			}
			got := renderSparkplug(t, fresh)
			if anywhere(fresh, "DC_SPARKPLUG_BROKER_PW_1") {
				t.Fatal("the retired password's projection was carried back into the release")
			}
			if name == "withdrawn" && strings.Contains(got.configRaw, "spb-mosquitto") {
				t.Fatal("the withdrawn source was carried back")
			}
		})
	}
}

// A Secret named like ours that dcctl did not write is not deleted by a withdrawal.
func TestAWithdrawalLeavesAForeignSecretAlone(t *testing.T) {
	typed := versionedCluster(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "dci-dctest-sparkplug-broker", Namespace: "dci-dctest"}})
	st := sparkplugState(nil)
	st.SparkplugSourcesGiven = true
	if _, err := sparkplugRun(t, typed, st); err == nil {
		t.Fatal("a Secret dcctl did not write was removed")
	}
	if _, ok := brokerSecret(t, typed); !ok {
		t.Fatal("the foreign Secret is gone")
	}
}

// 🔴 M3: a bootstrap that is given no sources over an instance that has some is refused, unless it
// says it means to withdraw them.
func TestABootstrapThatWouldSilentlyDropSourcesIsRefused(t *testing.T) {
	held := map[string]interface{}{"functionalAreas": map[string]interface{}{"sparkplug-ingest": map[string]interface{}{
		"config": map[string]interface{}{"sources": []interface{}{map[string]interface{}{"tenant": "plant"}}}}}}
	old := sparkplugReleaseValues
	t.Cleanup(func() { sparkplugReleaseValues = old })

	for name, tc := range map[string]struct {
		release map[string]interface{}
		st      State
		refused bool
	}{
		"given none, release holds sources":     {held, State{Instance: "i"}, true},
		"given a file":                          {held, State{Instance: "i", SparkplugSourcesGiven: true}, false},
		"withdrawing explicitly":                {held, State{Instance: "i", SparkplugSourcesGiven: true}, false},
		"release holds none":                    {map[string]interface{}{}, State{Instance: "i"}, false},
		"a fresh install (no release)":          {nil, State{Instance: "i"}, false},
		"an upgrade is judged elsewhere":        {held, State{Instance: "i", Evolving: true}, false},
		"a rehearsal does not read the cluster": {held, State{Instance: "i", DryRun: true}, false},
	} {
		t.Run(name, func(t *testing.T) {
			sparkplugReleaseValues = func(*State) (map[string]interface{}, error) { return tc.release, nil }
			err := precheckSparkplugWithdrawal(context.Background(), &tc.st)
			if tc.refused != (err != nil) {
				t.Fatalf("refused=%v, want %v (%v)", err != nil, tc.refused, err)
			}
			if err != nil && !strings.Contains(err.Error(), "--withdraw-sparkplug-sources") {
				t.Fatalf("the refusal does not name the way out: %v", err)
			}
		})
	}
	if err := CheckSparkplugFlags(someParsed(), true); err == nil {
		t.Fatal("--sparkplug-source with --withdraw-sparkplug-sources was accepted")
	}
	if err := CheckSparkplugFlags(nil, true); err != nil {
		t.Fatalf("a withdrawal alone was refused: %v", err)
	}
}

func someParsed() []SparkplugSource {
	return []SparkplugSource{{Tenant: "t", Broker: SparkplugBroker{URL: "tcp://b:1883"}}}
}

// A tuning overlay still may not set a source: that is what the flag is for.
func TestAnOverlayStillCannotSetSparkplugSources(t *testing.T) {
	err := refuseOverlayKeys(map[string]interface{}{
		"functionalAreas": map[string]interface{}{"sparkplug-ingest": map[string]interface{}{
			"config": map[string]interface{}{"sources": []interface{}{}}}},
	}, "values.yaml")
	if err == nil || !strings.Contains(err.Error(), "--sparkplug-source") {
		t.Fatalf("error = %v, want one pointing at --sparkplug-source", err)
	}
}

// The file's keys are the service's own. dcctl cannot import the service (they are separate
// modules), so this reads its declaration from source: a key added there and not here, or one
// spelled differently, fails this instead of being refused by a pod after a ten-minute rollout.
//
// 🔴 THE PARSED FILE LIVES OUTSIDE THIS MODULE AND GO'S TEST CACHE DOES NOT TRACK IT; CI runs
// every module with -count=1.
func TestSparkplugSourceKeysAreTheServicesOwn(t *testing.T) {
	const path = "../../services/sparkplug-ingest/config/configuration.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	fields := func(name string) []string {
		var out []string
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != name {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, f := range st.Fields.List {
				for _, id := range f.Names {
					out = append(out, strings.ToLower(id.Name))
				}
			}
			return false
		})
		sort.Strings(out)
		if len(out) == 0 {
			t.Fatalf("%s not found in %s", name, path)
		}
		return out
	}
	lowerJSON := func(v interface{}) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			out = append(out, strings.ToLower(strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]))
		}
		sort.Strings(out)
		return out
	}

	if got, want := lowerJSON(SparkplugSource{}), fields("SparkplugSource"); !reflect.DeepEqual(got, want) {
		t.Errorf("source keys: dcctl accepts %v, the service declares %v", got, want)
	}
	// dcctl's broker block swaps passwordEnv (which it writes) for the three ways to supply one.
	want := []string{}
	for _, f := range fields("SourceBroker") {
		if f != "passwordenv" {
			want = append(want, f)
		}
	}
	want = append(want, "password", "passwordfile", "passwordsecretref")
	sort.Strings(want)
	if got := lowerJSON(SparkplugBroker{}); !reflect.DeepEqual(got, want) {
		t.Errorf("broker keys: dcctl accepts %v, the service declares %v", got, want)
	}
	cmds := []string{}
	for _, k := range sparkplugCommandKeys {
		cmds = append(cmds, strings.ToLower(k))
	}
	sort.Strings(cmds)
	if want := fields("SourceCommands"); !reflect.DeepEqual(cmds, want) {
		t.Errorf("command keys: dcctl accepts %v, the service declares %v", cmds, want)
	}
}
