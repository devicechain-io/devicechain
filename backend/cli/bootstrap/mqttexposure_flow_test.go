// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"slices"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/action"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// --- the authority is kept across a re-run (H1) ---------------------------------------------

func caFingerprint(t *testing.T, m *natsTLSMaterial) [32]byte {
	t.Helper()
	cert, err := parseFirstCertificate(m.CACertPEM)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(cert.Raw)
}

// 🔴 A BOOTSTRAP RE-RUN KEEPS THE AUTHORITY. Devices pin it; a rotated CA breaks every one of them
// while the run reports success. The leaf is new each time, the CA is not.
func TestARerunKeepsTheBrokerAuthorityAndIssuesANewLeaf(t *testing.T) {
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-time.Hour), 1)
	first := st.NATSTLS

	again, err := settleNATSTLS(context.Background(), c, st, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if caFingerprint(t, again) != caFingerprint(t, first) {
		t.Fatal("a re-run rotated the broker's certificate authority: every device that pins it is now broken")
	}
	if again.LeafCertPEM == first.LeafCertPEM {
		t.Error("the leaf was not re-issued")
	}
	// The new leaf verifies under the kept authority.
	leaf, err := parseFirstCertificate(again.LeafCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := parseFirstCertificate(first.CACertPEM)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: natsReleaseName, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Errorf("the re-issued leaf does not verify under the authority devices trust: %v", err)
	}
}

// The negative control: with no authority in the cluster (a fresh namespace) a new one IS minted, so
// the equality above is a real measurement and not two identical mints.
func TestAFreshInstanceGetsANewAuthorityAndTheControlCanTellTheDifference(t *testing.T) {
	st := aWritableState()
	empty := fake.NewSimpleClientset()
	a, err := settleNATSTLS(context.Background(), empty, st, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	b, err := settleNATSTLS(context.Background(), empty, st, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if caFingerprint(t, a) == caFingerprint(t, b) {
		t.Fatal("two mints share an authority: the fingerprint comparison cannot detect a rotation")
	}
}

// An authority that exists but cannot be read is an error, never a mint.
func TestAnUnreadableExistingAuthorityFailsInsteadOfBeingReplaced(t *testing.T) {
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-time.Hour), 1)
	secrets := c.CoreV1().Secrets(InstanceNamespace(testInstance))
	s, err := secrets.Get(context.Background(), natsAuthoritySecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.Data["tls.crt"] = nil // the key is there, the certificate is not
	if _, err := secrets.Update(context.Background(), s, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := settleNATSTLS(context.Background(), c, st, time.Now().UTC()); err == nil ||
		!strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("err = %v, want a refusal to mint over an authority that exists", err)
	}
}

// --- adding only the MQTT host is a hot renewal; route names still roll the broker (M4) -------

func leafNames(t *testing.T, c *fake.Clientset) []string {
	t.Helper()
	settleStringDataLikeAnAPIServer(t, c)
	return brokerLeaf(t, c).DNSNames
}

func TestAddingOnlyTheMQTTHostRenewsTheCertificateWithoutRestartingTheBroker(t *testing.T) {
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-2*time.Hour), 1)
	podsServeTheSecret(t, c)
	st.MQTTExposure, st.MQTTHost = MQTTExposureGateway, "mqtt.example.com"

	restarted, err := renewBrokerCertificate(context.Background(), c, st)
	if err != nil {
		t.Fatal(err)
	}
	if restarted || statefulSetPatched(c) {
		t.Fatal("the broker was rolled for a certificate that only gained the MQTT host")
	}
	// M8(b): the Secret the renewal WROTE carries the host.
	if !slices.Contains(leafNames(t, c), "mqtt.example.com") {
		t.Fatalf("the certificate written by the renewal does not carry the MQTT host: %v", leafNames(t, c))
	}
}

func TestARouteNameStillRollsTheBrokerEvenWhenTheHostIsAddedToo(t *testing.T) {
	c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-2*time.Hour), 1)
	podsServeTheSecret(t, c)
	st.MQTTExposure, st.MQTTHost = MQTTExposureGateway, "mqtt.example.com"
	st.HA = true // the instance is now scaled to three servers: route names are missing too

	restarted, err := renewBrokerCertificate(context.Background(), c, st)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted || !statefulSetPatched(c) {
		t.Fatal("a topology change was served as a hot swap")
	}
}

// --- a host the certificate cannot carry fails before anything is written (M5) -----------------

func TestAnMQTTHostTheCertificateCannotCarryFailsLoudly(t *testing.T) {
	exposed := func(st *State) { st.MQTTExposure, st.MQTTHost = MQTTExposureGateway, "mqtt.example.com" }

	t.Run("the authority is not kept", func(t *testing.T) {
		c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-2*time.Hour), 1)
		if err := c.CoreV1().Secrets(InstanceNamespace(testInstance)).Delete(context.Background(), natsAuthoritySecretName, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		exposed(st)
		if err := precheckMQTTHostCertificate(context.Background(), c, st); err == nil || !strings.Contains(err.Error(), "Nothing was exposed") {
			t.Fatalf("precheck = %v, want a refusal", err)
		}
		if _, err := renewBrokerCertificate(context.Background(), c, st); err == nil {
			t.Fatal("the renewal warned and carried on although it cannot issue a certificate for the host")
		}
	})
	t.Run("the certificate Secret is absent", func(t *testing.T) {
		c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-2*time.Hour), 1)
		if err := c.CoreV1().Secrets(InstanceNamespace(testInstance)).Delete(context.Background(), natsReleaseName+"-tls", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		exposed(st)
		if err := precheckMQTTHostCertificate(context.Background(), c, st); err == nil {
			t.Fatal("precheck passed with no certificate Secret")
		}
		if _, err := renewBrokerCertificate(context.Background(), c, st); err == nil {
			t.Fatal("the renewal carried on with no certificate Secret")
		}
	})
	// The negative control: the very same broken instance is fine when no exposure is asked for,
	// so the failures above are about the host and not about the fixture.
	t.Run("without an exposure the same instance is left alone", func(t *testing.T) {
		c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-2*time.Hour), 1)
		if err := c.CoreV1().Secrets(InstanceNamespace(testInstance)).Delete(context.Background(), natsAuthoritySecretName, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := precheckMQTTHostCertificate(context.Background(), c, st); err != nil {
			t.Fatalf("precheck refused an instance that asked for nothing: %v", err)
		}
		if _, err := renewBrokerCertificate(context.Background(), c, st); err != nil {
			t.Fatalf("renewal refused an instance that asked for nothing: %v", err)
		}
	})
	t.Run("a leaf that already carries the host needs no authority", func(t *testing.T) {
		c, st := anInstanceWithBrokerMaterialMintedAt(t, time.Now().UTC().Add(-2*time.Hour), 1)
		exposed(st)
		withHost, err := mintNATSTLS(natsReleaseName, InstanceNamespace(testInstance), 1, time.Now().UTC(), mqttBrokerNames(st)...)
		if err != nil {
			t.Fatal(err)
		}
		st.NATSTLS = withHost
		if err := writeMintedSecrets(context.Background(), c, st); err != nil {
			t.Fatal(err)
		}
		settleStringDataLikeAnAPIServer(t, c)
		if err := c.CoreV1().Secrets(InstanceNamespace(testInstance)).Delete(context.Background(), natsAuthoritySecretName, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := precheckMQTTHostCertificate(context.Background(), c, st); err != nil {
			t.Fatalf("precheck = %v", err)
		}
	})
}

// --- closing the Traefik port is refused while an instance routes through it (M6) --------------

func instancesDyn(t *testing.T, specs map[string]dcv1beta1.InstanceSpec) *dynamicfake.FakeDynamicClient {
	t.Helper()
	var objs []runtime.Object
	for name, spec := range specs {
		inst := &dcv1beta1.Instance{Spec: spec}
		inst.Name = name
		u, err := instanceToUnstructured(inst)
		if err != nil {
			t.Fatal(err)
		}
		objs = append(objs, u)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{instanceGVR: "InstanceList"}, objs...)
}

func TestClosingTheMQTTGatewayIsRefusedWhileAnInstanceUsesIt(t *testing.T) {
	ctx := context.Background()
	using := instancesDyn(t, map[string]dcv1beta1.InstanceSpec{
		"acme": {Provider: "p", MQTTExposure: "gateway", MQTTHost: "mqtt.acme.example.com"},
		"beta": {Provider: "p"},
	})
	err := refuseClosingMQTTGateway(ctx, using)
	if err == nil || !strings.Contains(err.Error(), "acme") || strings.Contains(err.Error(), "beta") {
		t.Fatalf("err = %v, want a refusal naming acme only", err)
	}
	// Negative control: the same cluster with the exposure moved to a load balancer, or off, may close it.
	for _, mode := range []string{"loadbalancer", "none", ""} {
		free := instancesDyn(t, map[string]dcv1beta1.InstanceSpec{
			"acme": {Provider: "p", MQTTExposure: mode, MQTTHost: "mqtt.acme.example.com"},
		})
		if err := refuseClosingMQTTGateway(ctx, free); err != nil {
			t.Errorf("mode %q: closing was refused although nothing routes through the port: %v", mode, err)
		}
	}
	if err := refuseClosingMQTTGateway(ctx, nil); err == nil {
		t.Error("closing the port was allowed with no way to read the declarations")
	}
}

// --- "not found" is not "no 8883" (L10) ---------------------------------------------------------

func traefikService(ports ...int32) *corev1.Service {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name: "traefik", Namespace: traefikNamespace,
		Labels: map[string]string{"app.kubernetes.io/name": "traefik", "app.kubernetes.io/instance": "traefik-" + traefikNamespace},
	}}
	for _, p := range ports {
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Port: p, Protocol: corev1.ProtocolTCP})
	}
	return svc
}

func TestAMissingTraefikServiceIsNotReadAsAClosedPort(t *testing.T) {
	ctx := context.Background()
	open, found, err := traefikServesMQTT(ctx, fake.NewSimpleClientset())
	if err != nil || open || found {
		t.Fatalf("an empty cluster read as open=%v found=%v err=%v; found must be false so a caller that expects Traefik can refuse", open, found, err)
	}
	open, found, err = traefikServesMQTT(ctx, fake.NewSimpleClientset(traefikService(80, 443)))
	if err != nil || open || !found {
		t.Fatalf("Traefik without 8883 read as open=%v found=%v err=%v", open, found, err)
	}
	open, found, err = traefikServesMQTT(ctx, fake.NewSimpleClientset(traefikService(80, 443, 8883)))
	if err != nil || !open || !found {
		t.Fatalf("Traefik with 8883 read as open=%v found=%v err=%v", open, found, err)
	}
}

// --- declaration -> State -> helm values -> rendered manifest (M8a) ------------------------------

func renderManifestFor(t *testing.T, st *State) string {
	t.Helper()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	inst := action.NewInstall(&action.Configuration{})
	inst.ReleaseName = "dc-flow"
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = []string{"monitoring.coreos.com/v1"}
	rel, err := inst.RunWithContext(t.Context(), ch, helmValues(st))
	if err != nil {
		t.Fatal(err)
	}
	return rel.Manifest
}

func flowState(spec dcv1beta1.InstanceSpec) *State {
	st := haState(false)
	st.Instance = "dctest"
	spec.Provider, spec.TLS = "local", true
	applyDeclaration(st, spec)
	return st
}

// 🔴 The path an upgrade takes: the declaration is read into a State that is given NO MQTT flags,
// and that State must still render the TLSRoute AND put the host on the certificate. Both come
// from the same State, so a flagless upgrade cannot keep one and drop the other.
func TestAFlaglessUpgradeKeepsTheExposureInTheRenderAndOnTheCertificate(t *testing.T) {
	const host = "mqtt.example.com"
	recorded := dcv1beta1.InstanceSpec{MQTTExposure: MQTTExposureGateway, MQTTHost: host}
	st := flowState(recorded)

	manifest := renderManifestFor(t, st)
	if !strings.Contains(manifest, "kind: TLSRoute") || !strings.Contains(manifest, `- "`+host+`"`) || !strings.Contains(manifest, "name: mqtt-tls") {
		t.Fatalf("the recorded exposure did not reach the rendered manifest")
	}
	leaf, err := settleNATSTLS(context.Background(), nil, st, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := parseFirstCertificate(leaf.LeafCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cert.DNSNames, host) {
		t.Fatalf("the recorded exposure did not reach the certificate: %v", cert.DNSNames)
	}
}

// Negative control: the same flow with nothing recorded renders no route and names no host.
func TestWithNothingRecordedTheRenderAndTheCertificateCarryNoMQTTHost(t *testing.T) {
	st := flowState(dcv1beta1.InstanceSpec{})
	manifest := renderManifestFor(t, st)
	if strings.Contains(manifest, "kind: TLSRoute") || strings.Contains(manifest, "mqtt-tls") || strings.Contains(manifest, "mqtt-lb") {
		t.Fatal("an instance that never asked rendered MQTT exposure")
	}
	leaf, err := settleNATSTLS(context.Background(), nil, st, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := parseFirstCertificate(leaf.LeafCertPEM)
	for _, n := range cert.DNSNames {
		if strings.Contains(n, "mqtt.") {
			t.Fatalf("an MQTT name leaked onto the certificate: %v", cert.DNSNames)
		}
	}
}
