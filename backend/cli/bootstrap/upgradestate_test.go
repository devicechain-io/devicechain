// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	"k8s.io/apimachinery/pkg/types"
)

// The credentials in the broker's auth block that are IDENTITIES rather than secrets,
// and so are re-stated by helmValues from natsauth's own constants instead of being
// carried across from the document.
//
// 🔴 THIS LIST IS THE POINT OF THE TEST BELOW, NOT AN EXEMPTION FROM IT. Everything
// else in that struct must survive the round trip, including a field added tomorrow —
// which is why the test walks the struct and consults this, rather than checking the
// fields somebody remembered. Adding a credential and forgetting to carry it fails
// here; adding one and putting it in this list is a decision somebody had to write
// down.
var natsAuthIdentityFields = map[string]bool{"User": true, "SysUser": true}

// 🔴 A CREDENTIAL THE HYDRATION DROPS DOES NOT FAIL — IT RENDERS A VALID DOCUMENT
// WITH SOMETHING MISSING FROM IT. helmValues simply omits the block, the services
// roll onto a configuration that cannot reach the broker, cannot call another
// service, or cannot decrypt a single stored secret, and nothing anywhere errors.
//
// The system-account password is the sharpest: its ABSENCE is the off switch for the
// broker-presence tap, so losing it does not break, it silently turns a feature off.
func TestEveryCredentialTheServicesRunOnSurvivesTheHydration(t *testing.T) {
	deployed := &config.InstanceConfiguration{}
	deployed.Infrastructure.Nats.Tls.Enabled = true
	deployed.Infrastructure.Nats.Tls.Ca = "sentinel-broker-ca"
	deployed.Infrastructure.ServiceAuth.Secret = "sentinel-service-auth"
	deployed.Infrastructure.Secrets.RootKey = "sentinel-root-key"

	// Every field of the broker's auth block, filled by walking it so a field added
	// later is covered without anybody remembering to come back here.
	auth := reflect.ValueOf(&deployed.Infrastructure.Nats.Auth).Elem()
	authType := auth.Type()
	for i := 0; i < auth.NumField(); i++ {
		if auth.Field(i).Kind() == reflect.String && auth.Field(i).CanSet() {
			auth.Field(i).SetString("sentinel-nats-" + authType.Field(i).Name)
		}
	}

	st := &State{Instance: testInstance, Values: map[string]string{}}
	applyDeployedInfrastructure(st, deployed)

	// Rendered the way the release actually renders it, rather than checked against
	// st.Values: a value carried into State and never read by helmValues has not
	// survived anything, and asserting on the intermediate map would not know that.
	rendered, err := json.Marshal(helmValues(st))
	if err != nil {
		t.Fatalf("rendering the values the document is composed from: %v", err)
	}
	got := string(rendered)

	for _, want := range []string{"sentinel-broker-ca", "sentinel-service-auth", "sentinel-root-key"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s did not survive the hydration, so an upgrade would compose this "+
				"instance's configuration without it", want)
		}
	}
	for i := 0; i < auth.NumField(); i++ {
		name := authType.Field(i).Name
		if auth.Field(i).Kind() != reflect.String || natsAuthIdentityFields[name] {
			continue
		}
		if !strings.Contains(got, "sentinel-nats-"+name) {
			t.Errorf("the broker's %s did not survive the hydration: an upgrade would roll "+
				"every service onto a configuration missing it, and nothing would report an "+
				"error", name)
		}
	}
}

// TLS off is a state, not a gap. An instance serving the broker without TLS has no CA
// to carry, and inventing the enabled flag would hand every service a TLS client
// pointed at a listener that does not speak it.
func TestAnInstanceWithoutBrokerTLSIsNotGivenSome(t *testing.T) {
	deployed := &config.InstanceConfiguration{}
	deployed.Infrastructure.Nats.Auth.CalloutIssuerSeed = "seed"

	st := &State{Instance: testInstance, Values: map[string]string{}}
	applyDeployedInfrastructure(st, deployed)

	if st.Values["natsTlsEnabled"] == "true" {
		t.Error("TLS was switched on for an instance whose configuration has it off")
	}
	if st.Values["natsCA"] != "" {
		t.Errorf("a CA was invented for an instance that has none: %q", st.Values["natsCA"])
	}
}

// 🔴 THE TEST THAT WOULD HAVE CAUGHT IT, AND THE DEFECT IT IS BUILT FROM WAS FOUND ON
// A LIVE CLUSTER RATHER THAN HERE.
//
// `dcctl upgrade` composed a value map with no `ingress.host` in it — bootstrap
// defaulted that inline, and the upgrade path, assembling the same values from the
// declaration, simply never set the key. The chart's own schema requires it, so this
// did not produce a slightly different instance: it failed to render at all, minutes
// into an upgrade, with a schema error naming a value the operator had never typed.
//
// 🔑 THE SHAPE IS ONE THIS ARC HAS SEEN BEFORE. The credentials were held by a test
// that WALKS the struct; the value keys were held by a list of the ones I remembered.
// A list cannot see the entry nobody wrote. So the judge here is not a list at all —
// it is the chart, rendered, with its schema doing the asserting.
func TestTheValuesAnUpgradeComposesAreAcceptedByTheChart(t *testing.T) {
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading the embedded chart: %v", err)
	}

	for _, tc := range []struct {
		name string
		spec dcv1beta1.InstanceSpec
	}{
		{"an ordinary instance", dcv1beta1.InstanceSpec{
			Provider: "local", Cluster: "dcupg", Monitoring: true, CNPG: true, TLS: true,
			ImageRegistry: "ghcr.io/devicechain-io", ImageVersion: "v1.2.3",
		}},
		{"one exposed on a host of its own", dcv1beta1.InstanceSpec{
			Provider: "local", Cluster: "dcupg", Host: "dc.example.com",
			Monitoring: true, CNPG: true, TLS: true,
			ImageRegistry: "ghcr.io/devicechain-io", ImageVersion: "v1.2.3",
		}},
		{"a compact instance serving plain http", dcv1beta1.InstanceSpec{
			Provider: "local", Cluster: "dcupg", Host: "localhost",
			Compact: true, Monitoring: true, CNPG: true,
			ImageRegistry: "localhost:5000", ImageVersion: "my-build",
		}},
		{"a replicated one with an extra area", dcv1beta1.InstanceSpec{
			Provider: "local", Cluster: "dcupg", HA: true, Monitoring: true, CNPG: true, TLS: true,
			ExtraFunctionalAreas: []string{"lwm2m-ingest"},
			ImageRegistry:        "ghcr.io/devicechain-io", ImageVersion: "v1.2.3",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &State{
				Instance: testInstance, Provider: "local", Values: map[string]string{},
			}
			inst := &dcv1beta1.Instance{Spec: tc.spec}
			inst.SetUID(types.UID(testUID))
			if err := applyUpgradeDeclaration(st, inst, UpgradeOptions{}); err != nil {
				t.Fatalf("settling what the declaration says: %v", err)
			}

			// The credentials an upgrade would have read back off the cluster.
			deployed := &config.InstanceConfiguration{}
			deployed.Infrastructure.Nats.Tls.Enabled = true
			deployed.Infrastructure.Nats.Tls.Ca = "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n"
			deployed.Infrastructure.Nats.Auth.CalloutIssuerSeed = "SA-seed"
			deployed.Infrastructure.Nats.Auth.Password = "svc"
			deployed.Infrastructure.Secrets.RootKey = "cm9vdC1rZXk="
			applyDeployedInfrastructure(st, deployed)
			st.Credentials = &credentialSet{RDBPassword: "r", TSDBPassword: "t"}

			// The chart is the judge. Rendering is what an upgrade does next, and its
			// schema is what rejected the value this test exists for.
			if _, err := composeInstanceConfig(context.Background(), ch, helmValues(st)); err != nil {
				t.Errorf("the values an upgrade composes are not renderable: %v", err)
			}
		})
	}
}
