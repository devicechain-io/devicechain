// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
)

// compactState is a State shaped like the one a real `--compact` bootstrap hands
// to the Helm and infra steps: the preset flag set, plus the minimum values those
// steps require to build anything at all.
func compactState(compactOn bool) *State {
	return &State{
		Instance:      "dctest",
		Profile:       "default",
		KubeContext:   "kind-dctest",
		ImageRegistry: DefaultImageRegistry,
		ImageVersion:  "v0.0.0-test",
		Compact:       compactOn,
		NoTLS:         compactOn,
		NoMonitoring:  compactOn,
		Values: map[string]string{
			"ingressHost": "localhost",
			// The chart refuses to render a secret-store area without a root key
			// (ADR-059); a throwaway is fine since nothing decrypts anything.
			"secretsRootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
	}
}

// renderFromState renders the embedded chart with the values a State produces and
// decodes the instance configuration the way a service would.
//
// It goes through helmValues rather than restating the value map, which is the
// only reason the assertions below say anything about a real bootstrap: a test
// that built its own map would keep passing after the preset stopped being wired
// into the install.
//
// ApplyDefaults is deliberately not called — it would substitute the platform
// default for a value the chart failed to deliver and hide the exact failure the
// compact sizing is exposed to.
func renderFromState(t *testing.T, st *State) *config.InstanceConfiguration {
	t.Helper()

	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	cfg, err := renderInstanceConfigFromChart(t.Context(), ch, helmValues(st))
	if err != nil {
		t.Fatalf("rendering instance config: %v", err)
	}
	if cfg == nil {
		t.Fatal("no rendered Secret carried an `instance` key: the instance config never " +
			"reached a pod, so nothing below is measuring the deployment")
	}
	return cfg
}

// The compact ceilings must survive into the config a service reads.
//
// The failure this guards is silent by construction: every service decodes the
// instance config with a plain json.Unmarshal, which drops a key it does not
// recognise. A misnamed key here does not error — it produces a full-size instance
// wearing the compact label, on a volume sized for the compact one, and the first
// symptom is a stream that will not create.
func TestCompactLowersTheCeilingsAServiceReads(t *testing.T) {
	n := renderFromState(t, compactState(true)).Infrastructure.Nats

	for _, tc := range []struct {
		key       string
		got, want int64
		platform  int64
	}{
		{"streamMaxBytes", n.StreamMaxBytes, compact.StreamMaxBytes, config.DefaultStreamMaxBytes},
		{"streamMaxBytesCold", n.StreamMaxBytesCold, compact.StreamMaxBytesCold, config.DefaultStreamMaxBytesCold},
		{"mqttStoreMaxBytes", n.MqttStoreMaxBytes, compact.MqttStoreMaxBytes, config.DefaultMqttStoreMaxBytes},
		{"mqttQoS2StoreMaxBytes", n.MqttQoS2StoreMaxBytes, compact.MqttQoS2StoreMaxBytes, config.DefaultMqttQoS2StoreMaxBytes},
		{"kvCacheMaxBytes", n.KvCacheMaxBytes, compact.KvCacheMaxBytes, config.DefaultKvCacheMaxBytes},
		{"kvStateMaxBytes", n.KvStateMaxBytes, compact.KvStateMaxBytes, config.DefaultKvStateMaxBytes},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d under --compact, want %d", tc.key, tc.got, tc.want)
		}
		// A preset that happens to equal the default lowers nothing, and every
		// assertion about the compact budget below would be measuring the default
		// deployment while claiming to measure the small one.
		if tc.want >= tc.platform {
			t.Errorf("the compact %s (%d) is not below the platform default (%d): the "+
				"preset is not making this instance smaller", tc.key, tc.want, tc.platform)
		}
	}
}

// Without --compact nothing changes. The preset must be opt-in all the way
// through, or the published DEFAULT footprint is measuring the compact sizing.
func TestWithoutCompactTheCeilingsAreUntouched(t *testing.T) {
	n := renderFromState(t, compactState(false)).Infrastructure.Nats

	if n.StreamMaxBytes != config.DefaultStreamMaxBytes {
		t.Errorf("streamMaxBytes = %d without --compact, want the chart default %d",
			n.StreamMaxBytes, config.DefaultStreamMaxBytes)
	}
	if n.KvStateMaxBytes != config.DefaultKvStateMaxBytes {
		t.Errorf("kvStateMaxBytes = %d without --compact, want the chart default %d",
			n.KvStateMaxBytes, config.DefaultKvStateMaxBytes)
	}
	for _, v := range infraVars(compactState(false)) {
		if strings.HasPrefix(v, "nats_jetstream_storage=") || strings.HasPrefix(v, "postgres_storage=") {
			t.Errorf("a default bootstrap passed %q: it is overriding a volume size it "+
				"should be leaving to the shipped default", v)
		}
	}
}

// The compact reservation must fit the compact volume.
//
// This is the assertion the whole preset rests on, and it is deliberately NOT the
// one TestRenderedReservationFitsTheJetStreamStore makes. That test reads the PV
// from the OpenTofu *default*, so it follows compact only if compact changed that
// default — it does not, it passes a -var. Compact therefore needs its own check
// against its own volume, and its much tighter budget makes it correspondingly
// less forgiving.
//
// Both halves come from the production path: the ceilings from helmValues, the
// volume from infraVars. Restating either would let the two drift apart while the
// test kept agreeing with itself.
func TestCompactReservationFitsItsSmallerVolume(t *testing.T) {
	st := compactState(true)
	n := renderFromState(t, st).Infrastructure.Nats

	// Every ceiling has to have arrived. StreamMaxBytesFor and KvMaxBytesFor
	// substitute the platform default for a zero, so a config that delivered
	// nothing would sum to the DEFAULT reservation — which, against the compact
	// volume, would fail loudly rather than pass. The explicit check is here so
	// that failure names the discarded key instead of looking like over-reservation.
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
			t.Fatalf("%s did not survive into the compact config: the sum below would "+
				"fall back to the platform default and measure nothing about --compact", f.key)
		}
	}

	var reserved int64
	for _, s := range streams.Suffixes() {
		reserved += n.StreamMaxBytesFor(s)
	}
	reserved += n.MqttStoreReservation()
	reserved += n.KvReservation()

	if len(streams.Suffixes()) == 0 || kv.Count(kv.Cache) == 0 {
		t.Fatal("an inventory is empty — the sum above is vacuous")
	}

	ceiling := maxFileStoreFor(t, compactJetStreamStorage(t, st))
	if reserved > ceiling {
		t.Errorf("--compact reserves %d MiB against the %d MiB store its own %s volume "+
			"provides: a fresh compact bring-up crashloops its last stream-creating "+
			"services with \"insufficient storage resources available\"",
			reserved>>20, ceiling>>20, compact.JetStreamStorage)
	}

	// A headroom floor, because "fits" is not the same as "works". The reservation
	// is only the part JetStream claims up front; the store also has to hold the two
	// MQTT system streams left deliberately unbounded ($MQTT_sess and $MQTT_rmsgs,
	// both DiscardOld — a ceiling there would evict live sessions and retained
	// messages rather than protect anything) plus JetStream's own per-consumer
	// state. At the default 12Gi this slack is incidental; at 2Gi it is the whole
	// margin, so it is asserted rather than assumed.
	const minHeadroom int64 = 192 << 20
	if headroom := ceiling - reserved; headroom < minHeadroom {
		t.Errorf("--compact leaves %d MiB unreserved, below the %d MiB floor: the "+
			"unbounded MQTT session/retained stores and per-consumer state have to live "+
			"there. Raise compact.JetStreamStorage to the next magnitude whose "+
			"floor(magnitude x 0.9) clears the reservation, or lower the ceilings",
			headroom>>20, minHeadroom>>20)
	}
}

// compactJetStreamStorage reads the JetStream volume size the compact infra apply
// actually passes.
//
// Read from infraVars rather than from compact.JetStreamStorage directly: the
// value being checked is the one that reaches OpenTofu, and the gap between "the
// struct says 2Gi" and "the apply passes 2Gi" is exactly where a preset stops
// being wired without any test noticing.
func compactJetStreamStorage(t *testing.T, st *State) string {
	t.Helper()

	const key = "nats_jetstream_storage="
	i := slices.IndexFunc(infraVars(st), func(v string) bool { return strings.HasPrefix(v, key) })
	if i < 0 {
		t.Fatal("the compact infra apply passes no nats_jetstream_storage: the volume " +
			"keeps the shipped default while the ceilings shrink, so this test would be " +
			"checking the compact reservation against the FULL-SIZE volume and pass no " +
			"matter how the ceilings were set")
	}
	return strings.TrimPrefix(infraVars(st)[i], key)
}

// maxFileStoreFor applies the max_file_store derivation to a volume size.
//
// It must match modules/nats/main.tf exactly, and it is NOT 90% of the size: the
// module splits the value into magnitude and unit, floors 90% of the MAGNITUDE,
// and reattaches the unit. 12Gi yields floor(12 * 0.9) = 10 -> "10Gi", not
// 10.8Gi. That flooring is why the compact volume is 2Gi and not 1Gi — a 1Gi
// volume yields floor(1 * 0.9) = 0, a zero-byte store on which nothing starts —
// and why sizing a PV as (sum / 0.9) is unsafe.
func maxFileStoreFor(t *testing.T, size string) int64 {
	t.Helper()

	m := regexp.MustCompile(`^([0-9]+)([A-Za-z]+)$`).FindStringSubmatch(size)
	if m == nil {
		t.Fatalf("volume size %q is not a magnitude+unit quantity this derivation "+
			"understands; a decimal or unitless value would floor to something else "+
			"entirely at the module", size)
	}
	magnitude, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		t.Fatalf("parsing volume magnitude %q: %v", m[1], err)
	}
	units := map[string]int64{"Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40}
	unit, ok := units[m[2]]
	if !ok {
		t.Fatalf("volume unit %q is not one this test knows how to convert; add it "+
			"rather than letting the comparison silently use the wrong scale", m[2])
	}
	return (magnitude * 9 / 10) * unit
}

// Compact must not deploy a config the services would refuse. The tier-ordering
// guard (cold <= hot, cache <= state) rejects an inverted pair at startup, and a
// preset that lowered one side further than the other would ship an instance that
// cannot boot — the exact mistake that guard exists to catch, made by us.
func TestCompactPassesTheConfigTheServicesEnforce(t *testing.T) {
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	if err := validateRenderedInstanceConfig(t.Context(), ch, helmValues(compactState(true))); err != nil {
		t.Fatalf("the compact preset renders a config the services refuse: %v", err)
	}
}

// Compact lowers scheduling REQUESTS and leaves limits alone.
//
// Requests decide whether a pod fits a small node; limits decide what happens when
// it does not fit its own memory. Lowering limits here would convert a footprint
// change into OOMKills, which is a different — and much worse — kind of small.
func TestCompactLowersRequestsAndNotLimits(t *testing.T) {
	res, ok := helmValues(compactState(true))["resources"].(map[string]interface{})
	if !ok {
		t.Fatal("--compact set no resources: the pods keep the full-size requests and " +
			"the preset's whole scheduling claim is unfounded")
	}
	if _, ok := res["limits"]; ok {
		t.Error("--compact set resource limits. A Helm map coalesces, so omitting the " +
			"key keeps the chart's limits; naming it converts memory pressure into " +
			"OOMKills instead of shrinking anything")
	}
	reqs, ok := res["requests"].(map[string]interface{})
	if !ok {
		t.Fatal("--compact set resources without requests")
	}
	if reqs["cpu"] != compact.CPURequest || reqs["memory"] != compact.MemoryRequest {
		t.Errorf("compact requests = %v, want %s/%s", reqs, compact.CPURequest, compact.MemoryRequest)
	}
}

// cert-manager is dropped BECAUSE TLS is off, not because the preset is on.
//
// The chart renders a cert-manager Issuer whenever ingress TLS is enabled, so an
// instance that still terminates TLS on a cluster with no cert-manager fails the
// install outright against a missing CRD. Keying the two together means
// `--compact --no-tls=false` gives up three pods of footprint and keeps working,
// instead of giving up the install.
func TestCompactDropsCertManagerOnlyWhenTLSIsOff(t *testing.T) {
	const off = "enable_cert_manager=false"

	if !slices.Contains(infraVars(compactState(true)), off) {
		t.Error("--compact with TLS off still installs cert-manager: nothing issues a " +
			"certificate that is never requested")
	}

	tlsOn := compactState(true)
	tlsOn.NoTLS = false
	if slices.Contains(infraVars(tlsOn), off) {
		t.Error("--compact dropped cert-manager while ingress TLS is on: the chart will " +
			"render a cert-manager Issuer against a CRD that is not installed, and the " +
			"install fails outright rather than merely running larger")
	}
}
