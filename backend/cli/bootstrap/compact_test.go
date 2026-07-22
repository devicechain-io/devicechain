// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
	"sigs.k8s.io/yaml"
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
			// The broker-auth material a real bootstrap has minted by the time the
			// Helm step runs (ADR-025). It is here, rather than omitted as
			// irrelevant to sizing, because the compact ceilings are merged into the
			// SAME nats map: without it that map is empty when the merge happens, and
			// the difference between merging and assigning — which decides whether an
			// instance gets its ceilings or its credentials — becomes unobservable.
			"natsTlsEnabled":         "true",
			"natsCA":                 "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
			"natsCalloutIssuerSeed":  "SUAtestseedvaluefortestingonly",
			"natsServicePassword":    "test-service-password",
			"natsServicePasswordEnc": "unused",
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

	// The ceilings and the broker credentials share one nats map, so applying the
	// preset by ASSIGNING that map rather than merging into it would drop whichever
	// was written first. Both halves are checked here because losing either is
	// silent: no credentials means a service that never reaches the broker, and no
	// ceilings means a full-size instance on a compact volume.
	if n.Auth.CalloutIssuerSeed == "" || n.Auth.Password == "" {
		t.Error("--compact overwrote the broker credentials instead of merging into " +
			"them: no service in this instance can authenticate to NATS")
	}
	if !n.Tls.Enabled || n.Tls.Ca == "" {
		t.Error("--compact overwrote the broker TLS material instead of merging into it")
	}
	if n.Hostname == "" || n.Port == 0 {
		t.Error("--compact replaced the chart's nats block rather than merging into it: " +
			"the broker coordinates are gone")
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
	// state.
	//
	// 192 MiB is a ROUND TRIPWIRE, not a derivation, and the difference matters:
	// nothing in this repo measures what $MQTT_sess costs per connected session or
	// what a durable consumer's ack state costs, so no floor here can be honestly
	// called sized. What it does is fail if a future change eats the margin, which
	// at the default 12Gi would go unnoticed and at 2Gi would not. C3 replaces it
	// with a measured number, or explains why the measurement was not worth it.
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

// compactVolumeDecisions records, for every persistent volume the infrastructure
// provisions, whether --compact sizes it — and when it does not, why not.
//
// This map is the point of the test below. The preset originally set
// postgres_storage and not timescale_storage, which shrank the relational store
// (catalog-scale, does not grow with traffic) and left TimescaleDB — where
// telemetry actually lands — at its full-size default. The footprint claim then
// described a fraction of the disk the instance used, and nothing failed, because
// the budget test only ever looked at JetStream.
//
// A volume is not allowed to be simply absent from this map. Adding one to the
// infrastructure now forces a decision here about whether the small tier sizes it.
var compactVolumeDecisions = map[string]string{
	"nats_jetstream_storage": "", // sized; TestCompactReservationFitsItsSmallerVolume
	"postgres_storage":       "", // sized
	"timescale_storage":      "", // sized
	"monitoring_prometheus_storage": "not sized: --compact removes the monitoring " +
		"stack outright, so no Prometheus PVC is created. An operator who keeps " +
		"monitoring with --no-monitoring=false has made that call explicitly and " +
		"gets the stack's own default.",
}

// Every volume the infrastructure provisions must have a compact decision, and
// every volume marked as sized must actually be passed by the apply.
func TestCompactSizesEveryGrowingVolume(t *testing.T) {
	raw, err := fs.ReadFile(assets.OpenTofu(), "variables.tf")
	if err != nil {
		t.Fatalf("reading embedded variables.tf: %v", err)
	}
	// Every `variable "<name>_storage"`, excluding the _storage_class siblings,
	// which select a StorageClass rather than a size.
	decl := regexp.MustCompile(`variable\s+"([a-z0-9_]+_storage)"\s*\{`)
	found := decl.FindAllSubmatch(raw, -1)
	if len(found) == 0 {
		t.Fatal("no storage variables found in the embedded variables.tf: this test " +
			"can no longer see the volumes it is checking, so it must fail rather than " +
			"pass vacuously")
	}

	vars := infraVars(compactState(true))
	for _, m := range found {
		name := string(m[1])
		why, known := compactVolumeDecisions[name]
		if !known {
			t.Errorf("the infrastructure provisions %q and --compact makes no decision "+
				"about it. Either size it in compactSizing, or record in "+
				"compactVolumeDecisions why the small tier leaves it alone — an "+
				"unsized volume that grows with traffic makes the published footprint "+
				"describe less disk than the instance actually uses", name)
			continue
		}
		set := slices.ContainsFunc(vars, func(v string) bool {
			return strings.HasPrefix(v, name+"=")
		})
		switch {
		case why == "" && !set:
			t.Errorf("%q is recorded as sized by --compact but the infra apply does not "+
				"pass it: the volume keeps its full-size default", name)
			continue
		case why != "" && set:
			t.Errorf("%q is recorded as NOT sized by --compact (%s) but the apply passes "+
				"it anyway: the comment and the code disagree", name, why)
			continue
		case why != "":
			continue
		}

		// Presence is not enough — the magnitude has to be a real one. A sized
		// volume that is not below the default it overrides shrinks nothing, and one
		// small enough to be unusable trades a working instance for a smaller
		// number.
		got, def := volumeMiB(t, valueOf(t, vars, name)), volumeMiB(t, tofuDefault(t, raw, name))
		if got >= def {
			t.Errorf("--compact sets %s to %d MiB, which is not below the %d MiB it "+
				"overrides: the preset is not making this volume smaller", name, got, def)
		}
		// A floor, stated as the tripwire it is rather than a derivation. Only the
		// JetStream volume is genuinely derived (its reservation is summed and
		// checked); the database volumes are judgement, so this catches a nonsense
		// magnitude rather than certifying a right one. 1Gi is where a Postgres data
		// directory plus the module's stock max_wal_size stops fitting at all.
		const floorMiB int64 = 1024
		if got < floorMiB {
			t.Errorf("--compact sets %s to %d MiB, below the %d MiB floor: at this size "+
				"the volume is too small to function rather than merely small", name, got, floorMiB)
		}
	}
}

// valueOf returns the value of a "name=value" var, failing if it is absent.
func valueOf(t *testing.T, vars []string, name string) string {
	t.Helper()

	i := slices.IndexFunc(vars, func(v string) bool { return strings.HasPrefix(v, name+"=") })
	if i < 0 {
		t.Fatalf("no %s among the infra vars", name)
	}
	return strings.TrimPrefix(vars[i], name+"=")
}

// tofuDefault reads a variable's shipped default out of the embedded variables.tf,
// so the comparison tracks the artifact rather than a copy of it.
func tofuDefault(t *testing.T, variablesTF []byte, name string) string {
	t.Helper()

	decl := regexp.MustCompile(`(?s)variable\s+"` + regexp.QuoteMeta(name) + `"\s*\{.*?default\s*=\s*"([^"]+)"`)
	m := decl.FindSubmatch(variablesTF)
	if m == nil {
		t.Fatalf("could not find the %s default in the embedded variables.tf", name)
	}
	return string(m[1])
}

// volumeMiB converts a volume quantity to MiB, failing on a form it does not
// understand rather than returning a zero that would satisfy every comparison.
func volumeMiB(t *testing.T, size string) int64 {
	t.Helper()

	m := regexp.MustCompile(`^([0-9]+)([A-Za-z]+)$`).FindStringSubmatch(size)
	if m == nil {
		t.Fatalf("volume size %q is not a whole magnitude + unit; a decimal or unitless "+
			"value floors differently at the module than it reads here", size)
	}
	return mib(t, m[1]+m[2])
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
// Requests decide whether a pod fits a small node; the memory limit decides what
// happens when a pod outgrows it. Lowering the memory limit here would convert a
// footprint change into OOMKills, and lowering the CPU limit would throttle — a
// different, worse kind of small either way.
//
// The assertions are on the RENDERED pod spec and against the chart's own
// defaults, not against the compact struct. Comparing reqs["cpu"] to
// compact.CPURequest is a tautology: expected and actual are the same variable, so
// it holds for any value at all — including `memory: 1Gi`, which against the
// chart's untouched 256Mi limit is a pod spec the API server rejects outright. A
// preset that made every pod uncreatable would have passed.
func TestCompactLowersRequestsAndNotLimits(t *testing.T) {
	if _, ok := helmValues(compactState(true))["resources"]; !ok {
		t.Fatal("--compact set no resources: the pods keep the full-size requests and " +
			"the preset's whole scheduling claim is unfounded")
	}

	shipped := byArea(t, renderContainers(t, nil))
	small := byArea(t, renderContainers(t, helmValues(compactState(true))))

	for area, got := range small {
		base, ok := shipped[area]
		if !ok {
			t.Errorf("area %q renders only under --compact", area)
			continue
		}
		for _, dim := range []string{"cpu", "memory"} {
			// Strictly smaller than what the chart ships, or the preset is not a
			// preset — it is a relabelling of the default.
			if q(t, dim, got.requests[dim]) >= q(t, dim, base.requests[dim]) {
				t.Errorf("%s: compact %s request %q is not below the chart's %q: --compact "+
					"is not making this pod easier to schedule", area, dim,
					got.requests[dim], base.requests[dim])
			}
			// A request above its limit is rejected at admission, so this is not a
			// sizing preference — it is the difference between a small instance and
			// one where not a single pod can be created.
			if q(t, dim, got.requests[dim]) > q(t, dim, got.limits[dim]) {
				t.Errorf("%s: compact %s request %q exceeds the limit %q; the API server "+
					"refuses the pod outright", area, dim, got.requests[dim], got.limits[dim])
			}
			// The limits must be the chart's, untouched.
			if got.limits[dim] != base.limits[dim] {
				t.Errorf("%s: --compact changed the %s limit from %q to %q. A Helm map "+
					"coalesces, so omitting the key keeps the chart's limits; changing "+
					"the memory limit converts pressure into OOMKills and changing the "+
					"CPU limit throttles — neither shrinks anything",
					area, dim, base.limits[dim], got.limits[dim])
			}
		}
	}
}

// byArea indexes rendered containers by area, skipping the nginx console — it is
// not a Go service and the preset's scheduling story is about the Go workloads.
func byArea(t *testing.T, cs []renderedContainer) map[string]renderedContainer {
	t.Helper()

	out := map[string]renderedContainer{}
	for _, c := range goContainers(cs) {
		out[c.area] = c
	}
	if len(out) == 0 {
		t.Fatal("no Go containers rendered — every comparison would be vacuous")
	}
	return out
}

// q parses a Kubernetes quantity into a comparable scalar: millicores for cpu,
// MiB for memory. It fails on a form it does not understand rather than returning
// a zero, which would silently satisfy every "is smaller" comparison above.
func q(t *testing.T, dim, quantity string) int64 {
	t.Helper()

	if quantity == "" {
		t.Fatalf("no %s quantity rendered: the comparison it feeds would be vacuous", dim)
	}
	if dim == "memory" {
		return mib(t, quantity)
	}
	if m, ok := strings.CutSuffix(quantity, "m"); ok {
		n, err := strconv.ParseInt(m, 10, 64)
		if err != nil {
			t.Fatalf("parsing cpu quantity %q: %v", quantity, err)
		}
		return n
	}
	n, err := strconv.ParseInt(quantity, 10, 64)
	if err != nil {
		t.Fatalf("cpu quantity %q uses a form this test cannot convert", quantity)
	}
	return n * 1000
}

// cert-manager is dropped BECAUSE TLS is off, not because the preset is on.
//
// An instance that still terminates TLS needs cert-manager. With the chart's
// default self-signed issuer it renders a cert-manager Issuer, so the install
// fails outright against a missing CRD; with an external clusterIssuer it renders
// only an annotation, so the install succeeds and the certificate is never issued.
// Keying the two together means `--compact --no-tls=false` gives up three pods of
// footprint and keeps working, instead of giving up the certificate.
func TestCompactDropsCertManagerOnlyWhenTLSIsOff(t *testing.T) {
	const off = "enable_cert_manager=false"

	if !slices.Contains(infraVars(compactState(true)), off) {
		t.Error("--compact with TLS off still installs cert-manager: nothing issues a " +
			"certificate that is never requested")
	}

	tlsOn := compactState(true)
	tlsOn.NoTLS = false
	if slices.Contains(infraVars(tlsOn), off) {
		t.Error("--compact dropped cert-manager while ingress TLS is on: nothing issues " +
			"the certificate. With the chart's default self-signed issuer the install " +
			"fails against a missing CRD; with an external issuer it succeeds and serves " +
			"no cert at all")
	}
}

// The MQTT NodePort is a LOCAL-only exposure: it must be set on a kind context (so a
// host device reaches the broker through the kind host:1883 -> node port map) and
// MUST NOT be set on a cloud context, where a NodePort would publish MQTT on every
// node IP.
//
// The expected value is READ FROM the embedded kind config, not a second copy of the
// literal 31883: the NodePort dcctl asks tofu to create must equal the node port the
// kind config maps host 1883 to, or the host route is dead again with the exact
// paho-EOF symptom this whole change exists to bury. Deriving it here means editing
// one without the other fails this test instead of shipping a silent dead route.
func TestMqttNodePortIsLocalOnly(t *testing.T) {
	nodePort := kindHostPortMapping(t, 1883)
	want := "nats_mqtt_node_port=" + strconv.Itoa(nodePort)

	local := compactState(true) // KubeContext "kind-dctest" -> looksLocal
	if !slices.Contains(infraVars(local), want) {
		t.Errorf("local kind context did not set %q; the kind host 1883 -> node %d map "+
			"needs a Service on node %d or host:1883 is a dead route (device clients see "+
			"EOF). infraVars: %v", want, nodePort, nodePort, infraVars(local))
	}

	cloud := compactState(true)
	cloud.KubeContext = "prod-eks" // not a local heuristic match
	for _, v := range infraVars(cloud) {
		if strings.HasPrefix(v, "nats_mqtt_node_port=") {
			t.Errorf("cloud context set %q: a NodePort would publish MQTT on every "+
				"node IP; it must stay ClusterIP-only off a local box", v)
		}
	}
}

// kindHostPortMapping returns the containerPort (node port) the embedded kind config
// maps the given host port to. It fails the test if no such mapping exists — the kind
// host map and the NodePort dcctl provisions are two ends of one route, and this is
// the join that keeps them from drifting apart.
func kindHostPortMapping(t *testing.T, hostPort int) int {
	t.Helper()
	var cfg struct {
		Nodes []struct {
			ExtraPortMappings []struct {
				ContainerPort int `json:"containerPort"`
				HostPort      int `json:"hostPort"`
			} `json:"extraPortMappings"`
		} `json:"nodes"`
	}
	if err := yaml.Unmarshal(assets.KindClusterConfig(), &cfg); err != nil {
		t.Fatalf("parsing embedded kind config: %v", err)
	}
	for _, n := range cfg.Nodes {
		for _, m := range n.ExtraPortMappings {
			if m.HostPort == hostPort {
				return m.ContainerPort
			}
		}
	}
	t.Fatalf("embedded kind config maps no node port to host %d; the MQTT host route "+
		"cannot work without it", hostPort)
	return 0
}
