// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"slices"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"sigs.k8s.io/yaml"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

func TestSettleMQTTExposure(t *testing.T) {
	cases := []struct {
		name                 string
		recMode, recHost     string
		givenMode, givenHost string
		wantMode, wantHost   string
		wantChanged          bool
		wantErr              string
	}{
		{name: "nothing recorded or given stays off", wantMode: MQTTExposureNone},
		{name: "gateway with a host", givenMode: "gateway", givenHost: "mqtt.example.com",
			wantMode: "gateway", wantHost: "mqtt.example.com", wantChanged: true},
		{name: "loadbalancer with a host", givenMode: "loadbalancer", givenHost: "mqtt.example.com",
			wantMode: "loadbalancer", wantHost: "mqtt.example.com", wantChanged: true},
		{name: "a flagless run keeps what is recorded", recMode: "gateway", recHost: "mqtt.example.com",
			wantMode: "gateway", wantHost: "mqtt.example.com"},
		{name: "none turns it off and drops the host", recMode: "gateway", recHost: "mqtt.example.com", givenMode: "none",
			wantMode: "none", wantChanged: true},
		{name: "a new host alone moves the recorded exposure", recMode: "gateway", recHost: "a.example.com", givenHost: "b.example.com",
			wantMode: "gateway", wantHost: "b.example.com", wantChanged: true},
		{name: "switching mode keeps the recorded host", recMode: "gateway", recHost: "mqtt.example.com", givenMode: "loadbalancer",
			wantMode: "loadbalancer", wantHost: "mqtt.example.com", wantChanged: true},

		// fail closed
		{name: "exposure needs a host", givenMode: "gateway", wantErr: "needs --mqtt-host"},
		{name: "a bad mode", givenMode: "public", wantErr: "want gateway, loadbalancer or none"},
		{name: "a bare label is not a host", givenMode: "gateway", givenHost: "broker", wantErr: "fully-qualified"},
		{name: "an IP is not a host", givenMode: "gateway", givenHost: "203.0.113.7", wantErr: "fully-qualified"},
		{name: "upper case is refused", givenMode: "gateway", givenHost: "MQTT.example.com", wantErr: "fully-qualified"},
		{name: "a host without an exposure", givenHost: "mqtt.example.com", wantErr: "without --mqtt-exposure"},
		{name: "none with a host contradicts itself", givenMode: "none", givenHost: "mqtt.example.com", wantErr: "give one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := settleMQTTExposure(tc.recMode, tc.recHost, tc.givenMode, tc.givenHost)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Mode != tc.wantMode || got.Host != tc.wantHost || got.Changed != tc.wantChanged {
				t.Fatalf("got %+v, want mode %q host %q changed %v", got, tc.wantMode, tc.wantHost, tc.wantChanged)
			}
		})
	}
}

func TestMQTTExposureIsAbsentFromTheReleaseUnlessAsked(t *testing.T) {
	for _, mode := range []string{"", MQTTExposureNone} {
		if v := mqttExposureValues(&State{MQTTExposure: mode, MQTTHost: "mqtt.example.com"}); v != nil {
			t.Errorf("mode %q rendered chart values %v; an instance that never asked must render as before", mode, v)
		}
		if n := mqttBrokerNames(&State{MQTTExposure: mode, MQTTHost: "mqtt.example.com"}); len(n) != 0 {
			t.Errorf("mode %q added certificate names %v", mode, n)
		}
	}
	v := mqttExposureValues(&State{MQTTExposure: "gateway", MQTTHost: "mqtt.example.com"})
	if v["mode"] != "gateway" || v["host"] != "mqtt.example.com" {
		t.Errorf("values = %v", v)
	}
}

func TestMQTTExposureIsRecordedAndReadBack(t *testing.T) {
	var spec dcv1beta1.InstanceSpec
	st := &State{MQTTExposure: "loadbalancer", MQTTHost: "mqtt.example.com"}
	recordMQTTExposure(&spec, st)
	if spec.MQTTExposure != "loadbalancer" || spec.MQTTHost != "mqtt.example.com" || !mqttExposureRecorded(spec, st) {
		t.Fatalf("not recorded: %+v", spec)
	}
	recordMQTTExposure(&spec, &State{MQTTExposure: MQTTExposureNone, MQTTHost: "ignored.example.com"})
	if spec.MQTTExposure != "" || spec.MQTTHost != "" {
		t.Fatalf("none must be recorded as absent, got %+v", spec)
	}
}

func TestInstanceSpecRecordsTheMQTTExposure(t *testing.T) {
	st := &State{Profile: "default", MQTTExposure: "gateway", MQTTHost: "mqtt.example.com"}
	spec := InstanceSpecFrom(st, ClusterBinding{}, "local")
	if spec.MQTTExposure != "gateway" || spec.MQTTHost != "mqtt.example.com" {
		t.Fatalf("declaration = %+v", spec)
	}
	spec.MQTTHost = "nope"
	if err := ValidateInstanceSpec(spec); err == nil || !strings.Contains(err.Error(), "mqttHost") {
		t.Fatalf("ValidateInstanceSpec = %v, want the host refusal", err)
	}
}

func TestRefuseMQTTHostTaken(t *testing.T) {
	other := dcv1beta1.Instance{}
	other.Name = "acme"
	other.Spec.MQTTExposure, other.Spec.MQTTHost = "gateway", "mqtt.example.com"
	off := dcv1beta1.Instance{}
	off.Name = "idle"
	off.Spec.MQTTHost = "mqtt.example.com" // a leftover host with exposure off holds nothing
	others := []dcv1beta1.Instance{other, off}

	if err := refuseMQTTHostTaken("MQTT.example.com", "beta", others); err == nil || !strings.Contains(err.Error(), `"acme"`) {
		t.Fatalf("a host another instance exposes must be refused, got %v", err)
	}
	if err := refuseMQTTHostTaken("mqtt.example.com", "acme", others); err != nil {
		t.Fatalf("an instance's own host is not a clash: %v", err)
	}
	if err := refuseMQTTHostTaken("mqtt.beta.example.com", "beta", others); err != nil {
		t.Fatalf("a distinct host is fine: %v", err)
	}
}

// The broker certificate must carry the host devices dial, from the first mint and from a renewal.
func TestBrokerCertificateCarriesTheMQTTHost(t *testing.T) {
	const host = "mqtt.example.com"
	now := time.Now().UTC()
	has := func(m *natsTLSMaterial, name string) bool {
		block, _ := pem.Decode([]byte(m.LeafCertPEM))
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(leaf.DNSNames, name)
	}

	off, err := mintNATSTLS(natsReleaseName, "dci-x", 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if has(off, host) {
		t.Fatal("a certificate minted without an exposure must not carry the host")
	}
	on, err := mintNATSTLS(natsReleaseName, "dci-x", 3, now, mqttBrokerNames(&State{MQTTExposure: "gateway", MQTTHost: host})...)
	if err != nil {
		t.Fatal(err)
	}
	if !has(on, host) {
		t.Fatal("the certificate does not carry the MQTT host")
	}
	if !has(on, natsReleaseName+"-0."+natsReleaseName+"-headless") {
		t.Fatal("the route names must survive the extra name")
	}

	// A leaf issued without the host is stale for a State that now wants it, so the renewal
	// path re-issues it (and restarts the broker) without waiting for expiry.
	wanted := natsServerDNSNames(natsReleaseName, "dci-x", 1, host)
	reason, err := leafReissueReason(off.LeafCertPEM, wanted, now)
	if err != nil {
		t.Fatal(err)
	}
	if reason == "" {
		t.Fatal("a leaf missing the MQTT host must be re-issued")
	}
	reason, err = leafReissueReason(on.LeafCertPEM, natsServerDNSNames(natsReleaseName, "dci-x", 3, host), now)
	if err != nil || reason != "" {
		t.Fatalf("a leaf that carries the host needs nothing: %q %v", reason, err)
	}
}

// renderWithMQTT renders the embedded chart for an instance with the broker's TLS on.
func renderWithMQTT(t *testing.T, mqtt map[string]interface{}, brokerTLS, gateway bool) (string, error) {
	t.Helper()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	ingress := map[string]interface{}{"enabled": gateway, "controller": "gateway", "host": "dc.example.com"}
	vals := map[string]interface{}{
		"instance": map[string]interface{}{
			"id": "dctest",
			"config": map[string]interface{}{
				"infrastructure": map[string]interface{}{
					"secrets": map[string]interface{}{"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32))},
					"nats":    map[string]interface{}{"hostname": "dc-nats.dci-dctest", "tls": map[string]interface{}{"enabled": brokerTLS}},
				},
			},
		},
		"ingress": ingress,
	}
	if mqtt != nil {
		vals["mqttExposure"] = mqtt
	}
	inst := action.NewInstall(&action.Configuration{})
	inst.ReleaseName = "dc-mqtt"
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = []string{"monitoring.coreos.com/v1"}
	rel, err := inst.RunWithContext(t.Context(), ch, vals)
	if err != nil {
		return "", err
	}
	return rel.Manifest, nil
}

type renderedObject struct {
	Kind     string                 `json:"kind"`
	Metadata struct{ Name string }  `json:"metadata"`
	Spec     map[string]interface{} `json:"spec"`
}

func objectsOf(t *testing.T, manifest string) []renderedObject {
	t.Helper()
	var out []renderedObject
	for _, doc := range splitManifests(manifest) {
		var o renderedObject
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	return out
}

func find(objs []renderedObject, kind, name string) *renderedObject {
	for i := range objs {
		if objs[i].Kind == kind && objs[i].Metadata.Name == name {
			return &objs[i]
		}
	}
	return nil
}

func mqttListener(gw *renderedObject) map[string]interface{} {
	ls, _ := gw.Spec["listeners"].([]interface{})
	for _, l := range ls {
		if m, ok := l.(map[string]interface{}); ok && m["name"] == "mqtt-tls" {
			return m
		}
	}
	return nil
}

func TestChartRendersNothingForMQTTWhenOff(t *testing.T) {
	t.Parallel()
	for name, mqtt := range map[string]map[string]interface{}{"absent": nil, "none": {"mode": "none"}} {
		manifest, err := renderWithMQTT(t, mqtt, true, true)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		objs := objectsOf(t, manifest)
		for _, o := range objs {
			if o.Kind == "TLSRoute" || strings.HasSuffix(o.Metadata.Name, "-mqtt-lb") {
				t.Errorf("%s: rendered %s %s with MQTT exposure off", name, o.Kind, o.Metadata.Name)
			}
		}
		gw := find(objs, "Gateway", "devicechain")
		if gw == nil || mqttListener(gw) != nil {
			t.Errorf("%s: the Gateway must carry no mqtt-tls listener (gateway=%v)", name, gw)
		}
	}
}

func TestChartRendersTheGatewayMode(t *testing.T) {
	t.Parallel()
	manifest, err := renderWithMQTT(t, map[string]interface{}{"mode": "gateway", "host": "mqtt.example.com"}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	objs := objectsOf(t, manifest)

	gw := find(objs, "Gateway", "devicechain")
	l := mqttListener(gw)
	if l == nil {
		t.Fatal("no mqtt-tls listener on the Gateway")
	}
	tls, _ := l["tls"].(map[string]interface{})
	allowed, _ := l["allowedRoutes"].(map[string]interface{})
	ns, _ := allowed["namespaces"].(map[string]interface{})
	if l["protocol"] != "TLS" || l["port"] != float64(8883) || l["hostname"] != "mqtt.example.com" ||
		tls["mode"] != "Passthrough" || ns["from"] != "Same" {
		t.Errorf("listener = %v; want TLS/8883 bound to the host, Passthrough, routes from the same namespace only", l)
	}
	if _, ok := tls["certificateRefs"]; ok {
		t.Error("a Passthrough listener must not hold a certificate: the broker terminates TLS")
	}

	route := find(objs, "TLSRoute", "devicechain-mqtt")
	if route == nil {
		t.Fatal("no TLSRoute")
	}
	hn, _ := route.Spec["hostnames"].([]interface{})
	if len(hn) != 1 || hn[0] != "mqtt.example.com" {
		t.Errorf("hostnames = %v", hn)
	}
	if !strings.Contains(manifest, "sectionName: mqtt-tls") || !strings.Contains(manifest, "name: dc-nats\n          port: 1883") {
		t.Error("the route must attach to the mqtt-tls listener and back onto the broker's TLS-terminating MQTT port")
	}
	if find(objs, "Service", "dc-nats-mqtt-lb") != nil {
		t.Error("gateway mode must not also render a LoadBalancer")
	}
}

func TestChartRendersTheLoadBalancerMode(t *testing.T) {
	t.Parallel()
	manifest, err := renderWithMQTT(t, map[string]interface{}{
		"mode": "loadbalancer", "host": "mqtt.example.com",
		"loadBalancer": map[string]interface{}{
			"annotations":  map[string]interface{}{"service.beta.kubernetes.io/aws-load-balancer-scheme": "internal"},
			"sourceRanges": []interface{}{"198.51.100.0/24"},
		},
	}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	objs := objectsOf(t, manifest)
	svc := find(objs, "Service", "dc-nats-mqtt-lb")
	if svc == nil {
		t.Fatal("no LoadBalancer Service")
	}
	if svc.Spec["type"] != "LoadBalancer" || svc.Spec["externalTrafficPolicy"] != "Local" {
		t.Errorf("spec = %v", svc.Spec)
	}
	ports, _ := svc.Spec["ports"].([]interface{})
	if len(ports) != 1 {
		t.Fatalf("ports = %v; exactly the TLS port may be published", ports)
	}
	p := ports[0].(map[string]interface{})
	if p["port"] != float64(8883) || p["targetPort"] != "mqtt" {
		t.Errorf("port = %v; must publish 8883 to the broker's mqtt port and never 1883 itself", p)
	}
	if !strings.Contains(manifest, "198.51.100.0/24") || !strings.Contains(manifest, "aws-load-balancer-scheme") {
		t.Error("source ranges and annotations must reach the Service")
	}
	if find(objs, "TLSRoute", "devicechain-mqtt") != nil {
		t.Error("loadbalancer mode must not render a TLSRoute")
	}
}

func TestChartRefusesUnsafeMQTTExposure(t *testing.T) {
	t.Parallel()
	good := map[string]interface{}{"mode": "gateway", "host": "mqtt.example.com"}
	cases := []struct {
		name      string
		mqtt      map[string]interface{}
		brokerTLS bool
		gateway   bool
		want      string
	}{
		{"broker TLS off", good, false, true, "TLS is off"},
		{"no host", map[string]interface{}{"mode": "gateway"}, true, true, "mqttExposure.host"},
		{"bad host", map[string]interface{}{"mode": "loadbalancer", "host": "Bad_Host"}, true, false, "mqttExposure.host"},
		{"gateway without the Gateway front end", good, true, false, "ingress.enabled"},
		{"unknown mode", map[string]interface{}{"mode": "public", "host": "mqtt.example.com"}, true, true, "mode"},
	}
	for _, tc := range cases {
		_, err := renderWithMQTT(t, tc.mqtt, tc.brokerTLS, tc.gateway)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "ADR-") {
			t.Errorf("%s: a refusal printed to the operator must not cite an ADR: %v", tc.name, err)
		}
	}
}
