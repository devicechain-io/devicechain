// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"testing"

	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// The chart's self-signed TLS is a CHAIN: a bootstrap Issuer mints a CA certificate, and a CA
// Issuer named ingress.tls.issuerName (the name every cert-manager.io/issuer annotation uses)
// signs the host certificate with it. A leaf that signs itself has an empty Subject and Issuer
// and CA:FALSE, which Go accepts as a trust anchor and OpenSSL does not, so a device could
// verify an instance only by pinning its key. These render the real embedded chart and read
// the cert-manager objects back.

type certManagerObject struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		SelfSigned *struct{} `json:"selfSigned"`
		CA         *struct {
			SecretName string `json:"secretName"`
		} `json:"ca"`
		IsCA       bool   `json:"isCA"`
		CommonName string `json:"commonName"`
		SecretName string `json:"secretName"`
		PrivateKey struct {
			RotationPolicy string `json:"rotationPolicy"`
		} `json:"privateKey"`
		IssuerRef struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"issuerRef"`
	} `json:"spec"`
}

func renderIssuerChain(t *testing.T, controller string, tls map[string]interface{}) map[string]certManagerObject {
	t.Helper()
	vals := map[string]interface{}{
		"instance": map[string]interface{}{
			"id": "dctest",
			"config": map[string]interface{}{
				"infrastructure": map[string]interface{}{
					"secrets": map[string]interface{}{
						"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
					},
				},
			},
		},
		"ingress": map[string]interface{}{
			"enabled":    true,
			"controller": controller,
			"host":       "dctest.example.io",
			"tls":        tls,
		},
	}
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	manifest, err := renderChartClientSide(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	out := map[string]certManagerObject{}
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var obj certManagerObject
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			continue
		}
		if obj.Kind == "Issuer" || obj.Kind == "Certificate" {
			out[obj.Kind+"/"+obj.Metadata.Name] = obj
		}
	}
	return out
}

// Both front ends take their certificate from the one Issuer, so both must get the chain.
var frontEnds = []string{"nginx", "gateway"}

func TestSelfSignedIngressIsACAChain(t *testing.T) {
	t.Parallel()
	for _, fe := range frontEnds {
		t.Run(fe, func(t *testing.T) { t.Parallel(); checkSelfSignedChain(t, fe) })
	}
}

func checkSelfSignedChain(t *testing.T, controller string) {
	t.Helper()
	objs := renderIssuerChain(t, controller, map[string]interface{}{"enabled": true, "selfSigned": true})

	if len(objs) != 3 {
		t.Fatalf("want exactly a bootstrap Issuer, a CA Certificate and a CA Issuer, got %d objects: %v", len(objs), objs)
	}

	boot, ok := objs["Issuer/devicechain-selfsigned-bootstrap"]
	if !ok || boot.Spec.SelfSigned == nil {
		t.Fatalf("the bootstrap Issuer must be self-signed, got %+v (present=%v)", boot, ok)
	}

	ca, ok := objs["Certificate/devicechain-selfsigned-ca"]
	if !ok {
		t.Fatal("no CA Certificate rendered")
	}
	if !ca.Spec.IsCA {
		t.Error("the CA Certificate must be isCA: an OpenSSL client needs CA:TRUE to use it as a trust anchor")
	}
	// cert-manager's default regenerates the key on every re-issue, which would orphan every
	// client that holds the old ca.crt at the next host-certificate renewal.
	if ca.Spec.PrivateKey.RotationPolicy != "Never" {
		t.Errorf("the CA key rotationPolicy = %q, want Never", ca.Spec.PrivateKey.RotationPolicy)
	}
	if ca.Spec.CommonName != "dctest DeviceChain CA" {
		t.Errorf("CA commonName = %q, want a non-empty subject naming the instance", ca.Spec.CommonName)
	}
	if ca.Spec.IssuerRef.Name != boot.Metadata.Name || ca.Spec.IssuerRef.Kind != "Issuer" {
		t.Errorf("the CA must be signed by the bootstrap Issuer, got %+v", ca.Spec.IssuerRef)
	}

	// The Issuer every cert-manager.io/issuer annotation names must be the CA Issuer, backed by
	// the Secret the CA certificate is written to, or the host certificate stays self-signed.
	leaf, ok := objs["Issuer/devicechain-selfsigned"]
	if !ok || leaf.Spec.CA == nil {
		t.Fatalf("the Issuer named by ingress.tls.issuerName must be a CA Issuer, got %+v (present=%v)", leaf, ok)
	}
	if leaf.Spec.SelfSigned != nil {
		t.Error("the signing Issuer must not also be self-signed")
	}
	if leaf.Spec.CA.SecretName != ca.Spec.SecretName || ca.Spec.SecretName == "" {
		t.Errorf("CA Issuer reads Secret %q but the CA certificate is written to %q", leaf.Spec.CA.SecretName, ca.Spec.SecretName)
	}
	if ca.Spec.SecretName == "devicechain-tls" {
		t.Error("the CA must not share the host certificate's Secret")
	}
}

func TestSelfSignedChainFollowsTheValuesAndOnlyThem(t *testing.T) {
	t.Parallel()
	for _, fe := range frontEnds {
		t.Run(fe, func(t *testing.T) { t.Parallel(); checkChainFollowsValues(t, fe) })
	}
}

func checkChainFollowsValues(t *testing.T, controller string) {
	t.Helper()
	renamed := renderIssuerChain(t, controller, map[string]interface{}{
		"enabled": true, "selfSigned": true, "issuerName": "mine", "secretName": "mine-tls",
	})
	if _, ok := renamed["Issuer/mine"]; !ok {
		t.Errorf("issuerName must name the CA Issuer, got %v", renamed)
	}
	if ca, ok := renamed["Certificate/mine-ca"]; !ok || ca.Spec.SecretName != "mine-tls-ca" {
		t.Errorf("the CA certificate must follow issuerName and secretName, got %+v", ca)
	}

	// A real ClusterIssuer, or TLS off, must render none of it.
	for name, tls := range map[string]map[string]interface{}{
		"clusterIssuer": {"enabled": true, "selfSigned": false, "clusterIssuer": "letsencrypt"},
		"tls off":       {"enabled": false, "selfSigned": true},
	} {
		if got := renderIssuerChain(t, controller, tls); len(got) != 0 {
			t.Errorf("%s: rendered cert-manager objects it must not: %v", name, got)
		}
	}
}
