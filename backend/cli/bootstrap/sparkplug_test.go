// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
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
`))
	if err != nil {
		t.Fatal(err)
	}
	secret, area := sparkplugProvisioning("dctest", sources)

	if secret["name"] != "dci-dctest-sparkplug-broker" {
		t.Fatalf("secret name = %v", secret["name"])
	}
	sd := secret["stringData"].(map[string]interface{})
	if len(sd) != 1 || sd["DC_SPARKPLUG_BROKER_PW_1"] != brokerPassword {
		t.Fatalf("only the source with a password has a key: %v", sd)
	}
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
	env := area["extraEnv"].([]interface{})
	if len(env) != 1 {
		t.Fatalf("extraEnv = %v", env)
	}
	ref := env[0].(map[string]interface{})["valueFrom"].(map[string]interface{})["secretKeyRef"].(map[string]interface{})
	if env[0].(map[string]interface{})["name"] != "DC_SPARKPLUG_BROKER_PW_1" || ref["name"] != "dci-dctest-sparkplug-broker" || ref["key"] != "DC_SPARKPLUG_BROKER_PW_1" {
		t.Fatalf("the projection does not tie to the secret: %v", env[0])
	}

	// No password anywhere: no Secret at all (an empty one would be a dangling manifest).
	secret, area = sparkplugProvisioning("dctest", sources[1:])
	if secret != nil {
		t.Fatalf("a Secret was rendered for sources with no password: %v", secret)
	}
	if _, has := area["extraEnv"]; has {
		t.Fatalf("env rendered for sources with no password: %v", area["extraEnv"])
	}
}

func sparkplugState(sources []SparkplugSource) *State {
	enabled, err := ResolveEnabledAreas("default", []string{"sparkplug-ingest"})
	if err != nil {
		panic(err)
	}
	return &State{
		Instance:         "dctest",
		Profile:          "default",
		ImageRegistry:    DefaultImageRegistry,
		ImageVersion:     "v0.0.0-test",
		EnabledAreas:     enabled,
		SparkplugSources: sources,
		Values: map[string]string{
			"ingressHost":    "localhost",
			"secretsRootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
	}
}

// what the rendered chart holds for the Sparkplug sources.
type renderedSparkplug struct {
	secret    map[string]interface{}
	configRaw string
	env       []interface{}
}

func renderSparkplug(t *testing.T, vals map[string]interface{}) renderedSparkplug {
	t.Helper()
	var out renderedSparkplug
	for _, d := range renderDocs(t, vals) {
		kind, _ := d["kind"].(string)
		meta, _ := d["metadata"].(map[string]interface{})
		name, _ := meta["name"].(string)
		switch {
		case kind == "Secret" && name == "dci-dctest-sparkplug-broker":
			out.secret = d
		case kind == "ConfigMap" && name == "dct-dctest-config":
			if data, ok := d["data"].(map[string]interface{}); ok {
				out.configRaw, _ = data["sparkplug-ingest"].(string)
			}
		case kind == "Deployment" && name == "sparkplug-ingest":
			spec := d["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
			out.env, _ = spec["containers"].([]interface{})[0].(map[string]interface{})["env"].([]interface{})
		}
	}
	return out
}

// The end-to-end proof, through the chart a real bootstrap installs: the password Secret, the
// source in the area's mounted configuration (naming a variable, not holding a password), and the
// Deployment env tying the two.
func TestSparkplugSourcesRenderEndToEnd(t *testing.T) {
	t.Parallel()
	sources, err := ParseSparkplugSources(writeSourcesFile(t, goodSources))
	if err != nil {
		t.Fatal(err)
	}
	got := renderSparkplug(t, helmValues(sparkplugState(sources)))

	if got.secret == nil {
		t.Fatal("the broker password Secret was not rendered")
	}
	if sd, _ := got.secret["stringData"].(map[string]interface{}); sd["DC_SPARKPLUG_BROKER_PW_1"] != brokerPassword {
		t.Fatalf("the Secret does not hold the password: %v", got.secret["stringData"])
	}
	if got.configRaw == "" {
		t.Fatal("sparkplug-ingest has no key in the microservice ConfigMap")
	}
	if strings.Contains(got.configRaw, brokerPassword) {
		t.Fatal("the mounted configuration carries the broker password")
	}
	var cfg struct {
		Sources []struct {
			Tenant          string
			HostId          string
			DeviceTypeToken string
			AutoRegister    bool
			Groups          []string
			Broker          struct{ URL, Username, PasswordEnv string }
			Commands        struct {
				Enabled         bool
				HostRole        string
				WritableMetrics []string
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
		!s.AutoRegister || s.DeviceTypeToken != "sparkplug-pump" || !s.Commands.Enabled || s.Commands.HostRole != "primary" ||
		len(s.Groups) != 1 || s.Groups[0] != "plant-a" {
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

// 🔴 The reported defect: an upgrade composed the release's values again without the source.
// An upgrade given NO file composes values with none; carrying must put back the sources and the
// Secret, including onto an instance whose operator tuned the same area (which makes the area's
// block exist in the fresh values), and must not displace another feature's Secret.
func TestAnUpgradeKeepsTheSparkplugSources(t *testing.T) {
	t.Parallel()
	sources, err := ParseSparkplugSources(writeSourcesFile(t, goodSources))
	if err != nil {
		t.Fatal(err)
	}
	// The release: sources, plus a LwM2M Secret that another feature put in extraSecrets.
	previous := helmValues(sparkplugState(sources))
	previous["extraSecrets"] = append(previous["extraSecrets"].([]interface{}),
		map[string]interface{}{"name": "dci-dctest-lwm2m-psk", "stringData": map[string]interface{}{"DC_LWM2M_PSK_1": "x"}})

	// The upgrade: no file, and the operator's overlay tunes the same area.
	st := sparkplugState(nil)
	st.Evolving = true
	st.ValuesOverlay = map[string]interface{}{
		"functionalAreas": map[string]interface{}{
			"sparkplug-ingest": map[string]interface{}{"resources": map[string]interface{}{"limits": map[string]interface{}{"memory": "512Mi"}}},
		},
	}
	fresh := authoringValues(st)
	if _, has := fresh["extraSecrets"]; has {
		t.Fatal("the fresh values already hold extraSecrets, so this proves nothing about the carry")
	}
	carryReleaseValues(fresh, previous)

	got := renderSparkplug(t, fresh)
	if got.secret == nil || got.configRaw == "" {
		t.Fatalf("an upgrade dropped the source: secret=%v config=%q", got.secret != nil, got.configRaw)
	}
	if !strings.Contains(got.configRaw, `"tcp://spb-mosquitto:1883"`) || !strings.Contains(got.configRaw, "DC_SPARKPLUG_BROKER_PW_1") {
		t.Fatalf("the carried configuration is not the release's: %s", got.configRaw)
	}
	names := []string{}
	for _, s := range fresh["extraSecrets"].([]interface{}) {
		names = append(names, s.(map[string]interface{})["name"].(string))
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"dci-dctest-lwm2m-psk", "dci-dctest-sparkplug-broker"}) {
		t.Fatalf("extraSecrets after the carry = %v", names)
	}
	// The tuning survived beside the carried sources.
	area := fresh["functionalAreas"].(map[string]interface{})["sparkplug-ingest"].(map[string]interface{})
	if _, ok := area["resources"]; !ok {
		t.Fatalf("the overlay's tuning was lost by the carry: %v", area)
	}
}

// A run given the file again replaces what the release holds, and still keeps another feature's
// Secret.
func TestAFileGivenAgainReplacesTheCarriedSources(t *testing.T) {
	t.Parallel()
	old, _ := ParseSparkplugSources(writeSourcesFile(t, goodSources))
	previous := helmValues(sparkplugState(old))
	previous["extraSecrets"] = append(previous["extraSecrets"].([]interface{}),
		map[string]interface{}{"name": "dci-dctest-lwm2m-psk", "stringData": map[string]interface{}{"k": "v"}})

	replacement, err := ParseSparkplugSources(writeSourcesFile(t, strings.ReplaceAll(strings.ReplaceAll(goodSources, "spb-mosquitto", "new-broker"), brokerPassword, "rotated-pw")))
	if err != nil {
		t.Fatal(err)
	}
	st := sparkplugState(replacement)
	st.Evolving = true
	fresh := authoringValues(st)
	carryReleaseValues(fresh, previous)

	got := renderSparkplug(t, fresh)
	if !strings.Contains(got.configRaw, "new-broker") || strings.Contains(got.configRaw, "spb-mosquitto") {
		t.Fatalf("the carry overwrote the new sources: %s", got.configRaw)
	}
	if sd, _ := got.secret["stringData"].(map[string]interface{}); sd["DC_SPARKPLUG_BROKER_PW_1"] != "rotated-pw" {
		t.Fatalf("the carry overwrote the rotated password: %v", got.secret["stringData"])
	}
	if len(fresh["extraSecrets"].([]interface{})) != 2 {
		t.Fatalf("extraSecrets = %v", fresh["extraSecrets"])
	}
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
	// dcctl's broker block swaps passwordEnv (which it writes) for password / passwordFile.
	want := []string{}
	for _, f := range fields("SourceBroker") {
		if f != "passwordenv" {
			want = append(want, f)
		}
	}
	want = append(want, "password", "passwordfile")
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
