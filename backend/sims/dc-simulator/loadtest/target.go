// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/mqttload"
	"github.com/devicechain-io/dc-simulator/sim"
	"github.com/rs/zerolog/log"
)

// This is the target mode: the loss-verdict discipline aimed at ANY MQTT broker, not only a
// DeviceChain gateway. A bring-your-own credential list says who the devices are; the run
// publishes as them at a rate for a duration, counting only what the broker PUBACKed; and a
// read-back TAP, a subscriber on the target, says what was delivered. A QoS 1 publish the broker
// PUBACKed is a promise, so a PUBACKed event that never reaches the subscriber is a LOSS, with
// the same exit codes and the same unconfirmed-versus-lost distinction as the platform mode.
//
// 🔴 THE VERDICT'S LIMIT IS THE TAP. It measures PUBACK -> delivery to one QoS 1 subscriber on
// the target broker. It does not see what the customer's own pipeline does with a message after
// the broker, and a subscriber that is slower than the broker's per-client queue allows reads as
// the broker's loss, because from the publisher's side it is one. The report states this.

// InvTapContinuity is the invariant that the tap's own session held for the whole run.
const InvTapContinuity = "tap-continuity"

// InvAckRegression is the invariant that the PUBACK p99 did not regress against a baseline.
const InvAckRegression = "ack-latency-regression"

// TargetManifest is the Manifest a target report carries: there is no scenario.
const TargetManifest = "target"

// TargetTenant is the Tenant a target report carries: there is none.
const TargetTenant = "(target)"

// TargetLimit is the statement of what a target verdict does and does not measure.
const TargetLimit = "measures PUBACK to delivery on one QoS 1 subscriber of the target broker (broker-to-subscriber loss); " +
	"it does not observe what any downstream pipeline does with a message afterward, and a subscriber slower than the " +
	"broker's per-client queue reads as the broker's loss"

// TargetInfo is what a target run says about its own method, carried in the report.
type TargetInfo struct {
	Broker string `json:"broker"`
	// Authentication counts the devices by how they authenticated, never what they presented.
	Authentication map[string]int    `json:"authentication"`
	TopicTemplate  string            `json:"topicTemplate"`
	Tap            mqttload.TapStats `json:"tap"`
	Limit          string            `json:"limit"`
	// Baseline is the PUBACK p99 (ms) the run was compared with, when it was.
	BaselineAckP99Ms float64 `json:"baselineAckP99Ms,omitempty"`
}

// TargetProfile is one target run.
type TargetProfile struct {
	// MQTTProfile carries the rate, duration, thresholds and transport knobs. Its Manifest,
	// Seed and Devices are ignored: the fleet is the credential list. MQTT.Broker is the target.
	MQTTProfile
	Credentials []mqttload.Credential
	// Tap says how the read-back tap reaches the broker; Filter is derived from TopicTemplate
	// when empty.
	Tap           mqttload.TapConfig
	TopicTemplate string

	// Baseline, when set, is an earlier PASSing report whose PUBACK p99 this run is compared
	// with; MaxP99Regression is the share by which it may grow (0.25 = 25%).
	Baseline         *MQTTReport
	MaxP99Regression float64
}

// ParseTarget turns a --target address into a broker address and whether it is TLS. The
// platform's HTTP and websocket transports are NOT silently approximated: a scheme this build
// cannot drive is refused by name.
func ParseTarget(target string) (broker string, useTLS bool, err error) {
	t := strings.TrimSpace(target)
	if t == "" {
		return "", false, fmt.Errorf("--target is empty")
	}
	if !strings.Contains(t, "://") {
		return "", false, fmt.Errorf("--target %q has no scheme: use mqtt://host:1883 or mqtts://host:8883", target)
	}
	u, err := url.Parse(t)
	if err != nil {
		// The parse error quotes the whole address, and an address may carry a password.
		return "", false, fmt.Errorf("--target is not a valid URL (scheme://host:port)")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", false, fmt.Errorf("--target %q has no host", target)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "mqtt", "tcp", "mqtts", "ssl", "tls":
	case "ws", "wss", "http", "https":
		return "", false, fmt.Errorf("--target scheme %q is not driven by this build: only mqtt:// and mqtts:// are (websocket and HTTP targets are not observed, and a rehearsal that approximated them would report a transport it did not use)", u.Scheme)
	default:
		return "", false, fmt.Errorf("--target scheme %q is not one of mqtt, mqtts", u.Scheme)
	}
	if u.User != nil {
		return "", false, fmt.Errorf("--target carries credentials in the URL: put them in the credential list (the address is not repeated here)")
	}
	if p := strings.Trim(u.Path, "/"); p != "" || u.RawQuery != "" {
		return "", false, fmt.Errorf("--target %q has a path or query: an MQTT broker address is scheme://host:port", target)
	}
	switch scheme {
	case "mqtt", "tcp":
		return "tcp://" + u.Host, false, nil
	default:
		return "ssl://" + u.Host, true, nil
	}
}

func (p TargetProfile) withDefaults() (TargetProfile, error) {
	if p.TopicTemplate == "" {
		p.TopicTemplate = mqttload.DefaultTopicTemplate
	}
	p.Manifest, p.Seed, p.Devices = TargetManifest, 0, len(p.Credentials)
	if p.MetricKey == "" {
		p.MetricKey = "value"
	}
	p.MQTT.Target = true
	// The tap reaches the same broker the fleet does, over the same transport, unless told
	// otherwise by its own address.
	if p.Tap.Broker == "" {
		p.Tap.Broker, p.Tap.TLS, p.Tap.TLSInsecure, p.Tap.TLSServerName =
			p.MQTT.Broker, p.MQTT.TLS, p.MQTT.TLSInsecure, p.MQTT.TLSServerName
	}
	if p.Tap.Filter == "" {
		f, err := mqttload.TapFilter(p.TopicTemplate)
		if err != nil {
			return p, err
		}
		p.Tap.Filter = f
	}
	if p.Tap.ClientID == "" {
		p.Tap.ClientID = fmt.Sprintf("dc-rehearsal-tap-%d", time.Now().UnixNano())
	}
	p.MQTTProfile = p.MQTTProfile.withDefaults()
	return p, nil
}

// Validate rejects a target run that cannot be carried out as asked.
func (p TargetProfile) Validate() error {
	if len(p.Credentials) == 0 {
		return fmt.Errorf("no credentials: --credentials names a CSV with one row per device")
	}
	if p.MaxP99Regression < 0 {
		return fmt.Errorf("max p99 regression %v is negative", p.MaxP99Regression)
	}
	if p.MaxP99Regression > 0 && p.Baseline == nil {
		return fmt.Errorf("--max-p99-regression needs --baseline: a ceiling on growth has nothing to grow from")
	}
	if p.Baseline != nil && p.MaxP99Regression <= 0 {
		return fmt.Errorf("--baseline needs --max-p99-regression above 0: a baseline with no allowed growth set would compare nothing and pass whatever the latency")
	}
	for _, c := range p.Credentials {
		if !mqttload.FilterMatches(p.Tap.Filter, c.Topic) {
			return fmt.Errorf("device %q publishes to %q, which the tap filter %q does not cover: its events would be delivered and read as lost; set --tap-topic or fix the topic", c.Device, c.Topic, p.Tap.Filter)
		}
	}
	if p.Baseline != nil {
		if !p.Baseline.Passed() {
			return fmt.Errorf("the baseline report did not pass: a regression is measured against a run that held")
		}
		if p.Baseline.Drive.AckLatency.P99 <= 0 {
			return fmt.Errorf("the baseline report has no PUBACK latency to compare with")
		}
	}
	if p.Tap.Filter == "" {
		return fmt.Errorf("the tap has no topic filter")
	}
	return p.MQTTProfile.Validate()
}

// tapRead reads the tap through the two seams the platform's read-back uses, so the existing
// quiesce, identity and verdict logic judges a target run unchanged.
type tapRead struct{ tap *mqttload.Tap }

func (t tapRead) Count(_ context.Context, w Window) (int64, error) {
	return t.tap.CountBetween(w.Start, w.End), nil
}

func (t tapRead) DeviceIdentities(_ context.Context, device string, w Window) ([]int64, int, error) {
	return t.tap.DeviceBetween(device, w.Start, w.End), 0, nil
}

// regressionInvariant gates that the PUBACK p99 did not grow past the allowed share of the
// baseline's. A run with no PUBACKs has no p99 to compare, and fails rather than passing on zero.
func regressionInvariant(cur, base float64, maxRatio float64) Invariant {
	limit := base * (1 + maxRatio)
	ok := cur > 0 && cur <= limit
	return Invariant{Name: InvAckRegression, Passed: ok,
		Detail: fmt.Sprintf("PUBACK p99 %.1fms against a baseline of %.1fms (allowed growth %.0f%%, ceiling %.1fms)", cur, base, maxRatio*100, limit)}
}

// applyTapEvidence folds the tap's own health into a finished report. A tap whose session
// dropped may have missed deliveries the broker never retried, so a shortfall cannot be called
// the target's loss: the run fails as inconclusive rather than exiting with a loss it cannot
// attribute.
func applyTapEvidence(r *MQTTReport, stats mqttload.TapStats) {
	inv := Invariant{Name: InvTapContinuity, Passed: stats.ConnectionLosses == 0,
		Detail: fmt.Sprintf("the tap's session dropped %d time(s); it received %d deliveries (%d distinct, %d duplicate, %d not events)",
			stats.ConnectionLosses, stats.Received, stats.Unique, stats.Duplicates, stats.Unparsed)}
	r.Invariants = append(r.Invariants, inv)
	if inv.Passed {
		return
	}
	if r.Loss {
		r.Loss = false
		for i := range r.Invariants {
			if r.Invariants[i].Name == InvPubackDurability {
				r.Invariants[i].Detail = "inconclusive: the tap's own session dropped, so the shortfall may be the tap's, not the target's; was: " + r.Invariants[i].Detail
			}
		}
	}
}

// RunTarget executes one target run end to end and returns the report. The caller turns
// report.ExitCode() into the process exit code.
func RunTarget(ctx context.Context, p TargetProfile) (*MQTTReport, error) {
	p, err := p.withDefaults()
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}

	fleet, err := mqttload.NewTargetFleet(p.MQTT, p.Credentials)
	if err != nil {
		return nil, err
	}
	// The tap subscribes BEFORE anything publishes: an event delivered before the subscription
	// would read as lost.
	tap, err := mqttload.NewTap(ctx, p.Tap)
	if err != nil {
		return nil, fmt.Errorf("start the read-back tap: %w", err)
	}
	defer tap.Close()

	fleet.Start(ctx)
	defer fleet.Close()
	log.Info().Int("devices", len(p.Credentials)).Str("broker", fleet.Broker()).Str("tapFilter", p.Tap.Filter).
		Msg("connecting the target fleet")
	if n, err := fleet.AwaitConnected(ctx, p.ConnectTimeout); err != nil {
		return nil, fmt.Errorf("connect the fleet: %w (%d connected)", err, n)
	}
	if err := awaitStart(ctx, p.StartAt); err != nil {
		return nil, fmt.Errorf("waiting for the start instant: %w", err)
	}

	devices := make([]string, len(p.Credentials))
	auth := map[string]int{}
	for i, c := range p.Credentials {
		devices[i] = c.Device
		auth[c.Authentication()]++
	}
	rd := tapRead{tap: tap}
	run := mqttRun{
		Tenant: TargetTenant, Profile: p.MQTTProfile, Fleet: fleet,
		Devices: devices, Ledger: sim.NewIdentityLedger(), Counter: rd, Reader: rd,
	}
	report, err := run.driveAndReconcile(ctx)
	if err != nil {
		return nil, err
	}

	stats := tap.Stats()
	info := &TargetInfo{
		Broker: fleet.Broker(), Authentication: auth, TopicTemplate: p.TopicTemplate, Tap: stats, Limit: TargetLimit,
	}
	if p.Baseline != nil {
		info.BaselineAckP99Ms = p.Baseline.Drive.AckLatency.P99
		if p.MaxP99Regression > 0 {
			report.Invariants = append(report.Invariants,
				regressionInvariant(report.Drive.AckLatency.P99, p.Baseline.Drive.AckLatency.P99, p.MaxP99Regression))
		}
	}
	report.Target = info
	applyTapEvidence(report, stats)
	return report, nil
}
