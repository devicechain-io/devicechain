// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// The chart writes the instance configuration as a JSON blob into a Secret, and
// every service decodes it with a plain json.Unmarshal — which IGNORES a key it
// does not recognize. So the chart and the Go struct are joined by nothing but
// spelling: rename a field, or mistype a key in values.yaml, and the service
// starts cleanly on the built-in default while the value the operator wrote is
// silently discarded. Nothing logs it and nothing fails.
//
// That is the same fail-open shape as the graphql-go input-object bug the fork
// exists to fix (CLAUDE.md), and it bites hardest exactly here: these are disk
// ceilings, so a discarded value does not misbehave until a stream fills.
//
// These tests close it by rendering the real embedded chart through the real Helm
// engine and decoding the result with the real config loader — the same three
// pieces dcctl uses at bootstrap, in the same order.

// renderInstanceConfig renders the embedded chart with the given
// infrastructure.nats overrides and returns the decoded instance configuration
// from the rendered Secret. Pass nil to render the chart's own defaults.
//
// It deliberately does NOT call ApplyDefaults: the whole point is to observe what
// the chart actually delivered, and ApplyDefaults would paper over a discarded key
// by filling the field back in with the very default the test is trying to
// distinguish from.
func renderInstanceConfig(t *testing.T, natsOverrides map[string]interface{}) *config.InstanceConfiguration {
	t.Helper()

	infra := map[string]interface{}{
		// The chart refuses to render a profile carrying a secret-store area without
		// a root key (ADR-059). dcctl mints a real one; a throwaway is fine here since
		// nothing decrypts anything.
		"secrets": map[string]interface{}{
			"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
	}
	if natsOverrides != nil {
		infra["nats"] = natsOverrides
	}
	vals := map[string]interface{}{
		"instance": map[string]interface{}{
			"id":     "dctest",
			"config": map[string]interface{}{"infrastructure": infra},
		},
	}

	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}

	// ClientOnly renders without a cluster: no kube API, default capabilities.
	inst := action.NewInstall(&action.Configuration{})
	inst.ReleaseName = helmReleaseName
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = chartutil.VersionSet{"monitoring.coreos.com/v1"}

	rel, err := inst.RunWithContext(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}

	// Find the instance-config Secret among the rendered manifests and pull the
	// `instance` key — the exact bytes a pod mounts at /etc/dci-config/instance.
	var found string
	for _, doc := range releaseutil.SplitManifests(rel.Manifest) {
		var obj struct {
			Kind       string            `json:"kind"`
			StringData map[string]string `json:"stringData"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			continue
		}
		if obj.Kind != "Secret" {
			continue
		}
		if v, ok := obj.StringData["instance"]; ok {
			found = v
			break
		}
	}
	if found == "" {
		t.Fatal("no rendered Secret carried an `instance` key: the instance config " +
			"never reached a pod, so nothing below is measuring the chart")
	}

	cfg := &config.InstanceConfiguration{}
	if err := json.Unmarshal([]byte(found), cfg); err != nil {
		t.Fatalf("decoding rendered instance config: %v", err)
	}
	return cfg
}

// Every JetStream ceiling the chart states must survive the round trip into the
// Go struct, and must land on the field it names.
//
// A zero here is the failure this test exists for: it means the rendered JSON
// carried a key the struct does not have, so the value was dropped on the floor.
// Comparing against the platform default at the same time catches the second half
// — the chart's literal drifting away from the constant it duplicates, which is a
// hand-maintained mirror of exactly the kind core/streams and core/kv exist to
// eliminate.
func TestChartJetStreamCeilingsReachTheStruct(t *testing.T) {
	cfg := renderInstanceConfig(t, nil)
	n := cfg.Infrastructure.Nats

	for _, tc := range []struct {
		key       string
		got, want int64
	}{
		{"streamMaxBytes", n.StreamMaxBytes, config.DefaultStreamMaxBytes},
		{"streamMaxBytesCold", n.StreamMaxBytesCold, config.DefaultStreamMaxBytesCold},
		{"mqttStoreMaxBytes", n.MqttStoreMaxBytes, config.DefaultMqttStoreMaxBytes},
		{"mqttQoS2StoreMaxBytes", n.MqttQoS2StoreMaxBytes, config.DefaultMqttQoS2StoreMaxBytes},
		{"kvCacheMaxBytes", n.KvCacheMaxBytes, config.DefaultKvCacheMaxBytes},
		{"kvStateMaxBytes", n.KvStateMaxBytes, config.DefaultKvStateMaxBytes},
	} {
		if tc.got == 0 {
			t.Errorf("%s decoded as 0: the chart's key does not match the Go field, so "+
				"the rendered value was silently discarded and the service will run on "+
				"its built-in default no matter what an operator writes", tc.key)
			continue
		}
		if tc.got != tc.want {
			t.Errorf("%s = %d, platform default is %d: the chart literal and the Go "+
				"constant have drifted apart", tc.key, tc.got, tc.want)
		}
	}
}

// A value an operator overrides must actually arrive — the property the compact
// preset depends on entirely. The test above would pass even if the chart ignored
// user values completely, since its expected numbers ARE the defaults; this is the
// half that proves the override path works.
func TestChartOverriddenCeilingsReachTheStruct(t *testing.T) {
	const (
		wantHot     int64 = 64 << 20
		wantCold    int64 = 16 << 20
		wantKvCache int64 = 8 << 20
	)
	cfg := renderInstanceConfig(t, map[string]interface{}{
		"streamMaxBytes":     wantHot,
		"streamMaxBytesCold": wantCold,
		"kvCacheMaxBytes":    wantKvCache,
	})
	n := cfg.Infrastructure.Nats

	if n.StreamMaxBytes != wantHot {
		t.Errorf("streamMaxBytes = %d, want %d", n.StreamMaxBytes, wantHot)
	}
	if n.StreamMaxBytesCold != wantCold {
		t.Errorf("streamMaxBytesCold = %d, want %d", n.StreamMaxBytesCold, wantCold)
	}
	if n.KvCacheMaxBytes != wantKvCache {
		t.Errorf("kvCacheMaxBytes = %d, want %d", n.KvCacheMaxBytes, wantKvCache)
	}
	// The keys NOT overridden must survive the deep merge rather than being
	// replaced wholesale by the override map — the failure mode where setting one
	// nats key zeroes the rest of the block.
	if n.KvStateMaxBytes != config.DefaultKvStateMaxBytes {
		t.Errorf("kvStateMaxBytes = %d after overriding its siblings, want the chart "+
			"default %d: the override replaced the nats block instead of merging into it",
			n.KvStateMaxBytes, config.DefaultKvStateMaxBytes)
	}
	if n.Hostname == "" {
		t.Error("nats hostname was lost by the override merge")
	}
}

// The reservation the chart actually delivers must fit the JetStream store the
// chart is deployed against.
//
// core/config already pins this arithmetic, but against values it computes for
// itself. This asserts it against the numbers that reach a real pod, which is the
// only version of the claim a fresh install can falsify: the crashloop this whole
// budget exists to prevent is caused by what the chart rendered, not by what the
// Go defaults say.
func TestRenderedReservationFitsTheJetStreamStore(t *testing.T) {
	cfg := renderInstanceConfig(t, nil)
	n := cfg.Infrastructure.Nats

	var reserved int64
	for _, s := range streams.Suffixes() {
		reserved += n.StreamMaxBytesFor(s)
	}
	reserved += n.MqttStoreReservation()
	reserved += n.KvReservation()

	// The shipped PV is 12Gi and max_file_store floors 90% of the MAGNITUDE — see
	// the derivation in deploy/opentofu/modules/nats/main.tf and the matching
	// constants in core/config's instance_test.go. floor(12 * 0.9) = 10 -> "10Gi".
	const maxFileStore int64 = (12 * 9 / 10) << 30
	if reserved > maxFileStore {
		t.Errorf("the chart's own ceilings reserve %d B against a %d B store: a fresh "+
			"bring-up crashloops with \"insufficient storage resources available\"",
			reserved, maxFileStore)
	}
	if kv.Count(kv.Cache) == 0 {
		t.Fatal("no cache buckets declared — the KV half of this sum is vacuous")
	}
}
