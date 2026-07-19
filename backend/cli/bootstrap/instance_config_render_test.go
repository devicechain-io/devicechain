// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
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
	cfg, err := renderInstanceConfigFromChart(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering instance config: %v", err)
	}
	if cfg == nil {
		t.Fatal("no rendered Secret carried an `instance` key: the instance config " +
			"never reached a pod, so nothing below is measuring the chart")
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
// PV the chart is deployed against actually provides.
//
// core/config already pins this arithmetic, but against values it computes for
// itself. This asserts it across the seam between the two halves of the
// deployment — Helm states the ceilings, OpenTofu sizes the volume, and nothing
// but this test reads both. That seam is where the crashloop comes from: the
// budget is only ever violated by what the chart rendered against the volume tofu
// provisioned, never by what the Go defaults say.
//
// Both sides are read from the shipped artifacts rather than restated here. An
// earlier draft of this test summed via StreamMaxBytesFor/KvMaxBytesFor and
// compared against a hardcoded 12Gi, which made it unfalsifiable in both
// directions at once: those accessors fall back to the Go default on a zero, so a
// chart that dropped every key would compute the same total, and a hardcoded
// ceiling cannot notice the PV shrinking underneath it. Both halves have to come
// from the artifact for the comparison to mean anything.
func TestRenderedReservationFitsTheJetStreamStore(t *testing.T) {
	cfg := renderInstanceConfig(t, nil)
	n := cfg.Infrastructure.Nats

	// Every ceiling must have arrived from the chart. Without this the sum below
	// silently measures the Go defaults instead: StreamMaxBytesFor and
	// KvMaxBytesFor both substitute the platform default for a zero, so a chart
	// that delivered nothing at all would produce an identical — and passing —
	// total.
	for _, f := range []struct {
		key string
		got int64
	}{
		{"streamMaxBytes", n.StreamMaxBytes},
		{"streamMaxBytesCold", n.StreamMaxBytesCold},
		{"mqttStoreMaxBytes", n.MqttStoreMaxBytes},
		{"mqttQoS2StoreMaxBytes", n.MqttQoS2StoreMaxBytes},
		{"kvCacheMaxBytes", n.KvCacheMaxBytes},
		{"kvStateMaxBytes", n.KvStateMaxBytes},
	} {
		if f.got <= 0 {
			t.Fatalf("%s did not arrive from the chart: the sum below would fall back "+
				"to the Go default and measure nothing about the deployment", f.key)
		}
	}

	var reserved int64
	for _, s := range streams.Suffixes() {
		reserved += n.StreamMaxBytesFor(s)
	}
	reserved += n.MqttStoreReservation()
	reserved += n.KvReservation()

	maxFileStore := shippedMaxFileStore(t)
	if reserved > maxFileStore {
		t.Errorf("the chart's ceilings reserve %d B against the %d B store the shipped "+
			"PV provides: a fresh bring-up crashloops with \"insufficient storage "+
			"resources available\"", reserved, maxFileStore)
	}
	if kv.Count(kv.Cache) == 0 || len(streams.Suffixes()) == 0 {
		t.Fatal("an inventory is empty — the sum above is vacuous")
	}
}

// shippedMaxFileStore is the JetStream store ceiling the shipped OpenTofu
// actually provisions, read from the embedded infrastructure config.
//
// It is read rather than restated because the alternative had already gone wrong:
// the PV magnitude was hand-copied into three separate places, and a test holding
// its own copy keeps asserting against the old volume after the real one changes —
// passing loudest exactly when the PV shrinks, which is the change most likely to
// break the budget. That matters immediately: the compact preset drops this PV,
// and a hardcoded ceiling would wave it through.
//
// The derivation must match modules/nats/main.tf exactly. It is NOT 90% of the
// size: the module splits the value into magnitude and unit, floors 90% of the
// MAGNITUDE, and reattaches the unit — so 12Gi yields floor(12 * 0.9) = 10 ->
// "10Gi", not 10.8Gi. Modelling it as a true 90% would overstate the real ceiling
// by 819 MiB and hand this test headroom that does not exist.
func shippedMaxFileStore(t *testing.T) int64 {
	t.Helper()

	raw, err := fs.ReadFile(assets.OpenTofu(), "variables.tf")
	if err != nil {
		t.Fatalf("reading embedded variables.tf: %v", err)
	}
	// The `default` immediately following the nats_jetstream_storage declaration.
	decl := regexp.MustCompile(`(?s)variable\s+"nats_jetstream_storage"\s*\{.*?default\s*=\s*"([0-9]+)([A-Za-z]+)"`)
	m := decl.FindSubmatch(raw)
	if m == nil {
		t.Fatal("could not find the nats_jetstream_storage default in the embedded " +
			"variables.tf: this test can no longer see the PV it is checking against, " +
			"so it must fail rather than assume one")
	}
	magnitude, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		t.Fatalf("parsing PV magnitude %q: %v", m[1], err)
	}

	units := map[string]int64{"Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40}
	unit, ok := units[string(m[2])]
	if !ok {
		t.Fatalf("PV unit %q is not one this test knows how to convert; add it rather "+
			"than letting the comparison silently use the wrong scale", m[2])
	}
	// floor(90% of the MAGNITUDE), unit reattached — see the doc comment.
	return (magnitude * 9 / 10) * unit
}

// dcctl must reject a config the services would refuse, before it starts waiting
// on workloads.
//
// The services already fail closed on this — but everything after the install call
// waits on readiness, so a rejected config never reaches the operator AS a config
// error. It reaches them as a ten-minute timeout whose real cause is in the logs of
// pods that have already been replaced. The gate does not add a rule; it moves an
// existing rule's verdict to where someone can act on it.
func TestBootstrapRejectsAConfigTheServicesWouldRefuse(t *testing.T) {
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	vals := func(nats map[string]interface{}) map[string]interface{} {
		infra := map[string]interface{}{
			"secrets": map[string]interface{}{
				"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
			},
		}
		// A nil value here would not mean "no override" — Helm reads an explicit null
		// as DELETE THIS KEY, which would strip the chart's whole nats block
		// (hostname and port included) and make the gate reject for the wrong reason.
		if nats != nil {
			infra["nats"] = nats
		}
		return map[string]interface{}{
			"instance": map[string]interface{}{
				"id":     "dctest",
				"config": map[string]interface{}{"infrastructure": infra},
			},
		}
	}

	// The realistic mistake: lowering the hot bound for a small deployment and
	// leaving the cold one at its default, which silently makes the control-plane
	// streams the largest on disk.
	err = validateRenderedInstanceConfig(t.Context(), ch, vals(map[string]interface{}{
		"streamMaxBytes": int64(64 << 20),
	}))
	if err == nil {
		t.Fatal("a half-lowered stream bound passed the bootstrap gate: the operator " +
			"will learn about it as a readiness timeout instead")
	}
	if !strings.Contains(err.Error(), "streamMaxBytesCold") {
		t.Errorf("error %q does not name the offending key, which is the whole reason "+
			"to check here rather than let the pods report it", err)
	}

	// The counterweight. A gate that rejects a good configuration is worse than the
	// timeout it replaces, because it fails a bring-up that would have succeeded.
	if err := validateRenderedInstanceConfig(t.Context(), ch, vals(map[string]interface{}{
		"streamMaxBytes":     int64(64 << 20),
		"streamMaxBytesCold": int64(16 << 20),
	})); err != nil {
		t.Errorf("a correctly lowered pair was rejected: %v", err)
	}
	if err := validateRenderedInstanceConfig(t.Context(), ch, vals(nil)); err != nil {
		t.Errorf("the chart's own defaults were rejected: %v", err)
	}
}

// The gate has to be WIRED, not merely present.
//
// The test above exercises validateRenderedInstanceConfig directly, which means it
// passes just as happily when helmInstall never calls it — the function is not the
// risky part, the call site is. This drives the real helmInstall and asserts the
// failure it reports is the config verdict rather than anything downstream, which
// is only true if the check runs where it was put: before the install starts
// waiting on a cluster.
//
// The lever is the secret-store root key. The chart's own guard only checks that
// one is PRESENT (templates/_helpers.tpl), so a malformed key renders cleanly and
// is caught by config.Validate — reaching the gate through State exactly as a real
// bootstrap would.
func TestHelmInstallChecksTheConfigBeforeTouchingTheCluster(t *testing.T) {
	st := &State{
		Instance: "dctest",
		Profile:  "default",
		// Deliberately unreachable. If the gate does not run first, the error we get
		// back will be about this rather than about the config.
		KubeContext:   "dc-nonexistent-context-for-test",
		ImageRegistry: DefaultImageRegistry,
		ImageVersion:  "v0.0.0-test",
		Values: map[string]string{
			"secretsRootKey": "this is not valid base64 !!!",
			"ingressHost":    "localhost",
		},
	}

	err := helmInstall(t.Context(), st)
	if err == nil {
		t.Fatal("helmInstall accepted a malformed secret-store root key")
	}
	if !strings.Contains(err.Error(), "the instance configuration this deploy would render") {
		t.Errorf("helmInstall failed with %q, not the config verdict: the gate either "+
			"does not run or does not run before the cluster work, which is the only "+
			"thing that makes it better than a readiness timeout", err)
	}
}
