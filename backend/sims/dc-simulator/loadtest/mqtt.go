// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/mqttload"
	"github.com/devicechain-io/dc-simulator/sim"
	"github.com/rs/zerolog/log"
)

// This is the MQTT telemetry mode: the same fleet of provisioned devices the HTTP mode drives,
// publishing over the platform's MQTT gateway with their own credentials, reconciled against
// what the platform persisted.
//
// What it exists to answer is the one question the HTTP mode cannot: a QoS 1 publish the broker
// PUBACKed is a promise to the device that the event is stored, so every PUBACKed event must be
// persisted. A run where it is not is a LOSS, reported as one and failed with its own exit code.
// Rounds 13 to 15 found this loss (a capture-stream leader step-down dropping 150 to 560 PUBACKed
// events at a time, and 795,499 after a broker OOM) only because a throwaway driver counted
// PUBACKs against the database by hand; this is that driver, in the repository.

// Invariant names the MQTT mode adds to the HTTP mode's.
const (
	InvFleetConnected   = "fleet-connected"
	InvTransport        = "transport-as-requested"
	InvLoadOffered      = "load-offered"
	InvPubackDurability = "puback-durability"
	InvQoS0Delivery     = "qos0-delivery"
	InvAckLatency       = "ack-latency"
)

// Identity read-back modes.
const (
	// IdentityAuto reads every event back when the run is small enough to (see
	// DefaultIdentityMaxEvents) and compares counts otherwise.
	IdentityAuto = "auto"
	// IdentityFull reads every stored event of every device back and compares it, one by one,
	// with the PUBACK ledger. It is the only mode that names an exact loss.
	IdentityFull = "full"
	// IdentityCount compares the windowed persisted count with the PUBACK count. It costs one
	// query, so it is the mode for a 60 minute run, but it can be fooled: see the report's
	// maskedLossBound.
	IdentityCount = "count"
)

const (
	// DefaultIdentityMaxEvents is the PUBACKed volume above which IdentityAuto compares counts:
	// a full read is one query per 1,000 events.
	DefaultIdentityMaxEvents = 2_000_000
	// DefaultMinOfferedRatio is the share of the requested load a run must actually have
	// offered, AND had acknowledged, for its verdict to count: a run is published as
	// sustaining the rate it asked for, so it must have sustained it.
	DefaultMinOfferedRatio = 0.95
	// DefaultMQTTReportInterval is how often a run logs and records a progress slice.
	DefaultMQTTReportInterval = 10 * time.Second
	// Exit codes of the MQTT command.
	ExitPass          = 0
	ExitFail          = 1
	ExitError         = 2
	ExitLoss          = 3
	devicepulseMetric = sim.DevicepulseMetricKey
)

// MQTTProfile is one MQTT load run.
type MQTTProfile struct {
	// Manifest/Seed/Devices size the fleet exactly as the HTTP mode does. A manifest other than
	// devicepulse needs MetricKey, because the event carries one of the scenario's metrics.
	Manifest string
	Seed     int64
	Devices  int

	// Rate is the total events per second; Duration how long to publish (60 minutes is a
	// supported value: nothing in the run keeps per-event state beyond the identity ledger,
	// and IdentityAuto stops reading events back above DefaultIdentityMaxEvents).
	Rate     float64
	Duration time.Duration
	// MetricKey and PadBytes shape the payload (see mqttload.DriveSpec).
	MetricKey string
	PadBytes  int

	// MQTT is the transport configuration. Broker and TLSInsecure default to the handshake's.
	MQTT mqttload.Config
	// TLSInsecureExplicit says MQTT.TLSInsecure was set by the operator, in either direction, and
	// so overrides the handshake's recorded trust decision (mqttTLSInsecure).
	TLSInsecureExplicit bool
	// ConnectTimeout bounds the wait for every device to connect before the drive starts.
	ConnectTimeout time.Duration
	// ReportInterval is the cadence of the progress slices (0 = DefaultMQTTReportInterval,
	// negative disables).
	ReportInterval time.Duration

	// Identity is IdentityAuto, IdentityFull or IdentityCount; IdentityMaxEvents the PUBACKed
	// volume above which auto stops reading events back.
	Identity          string
	IdentityMaxEvents int64

	// MinAccepted is the floor of acknowledged publishes (written, for QoS 0) the run must
	// apply; MinOfferedRatio the share of Rate*Duration it must have offered; QoS0Tolerance the
	// share of written QoS 0 events allowed to be missing; MaxAckP99 a ceiling on the PUBACK p99
	// (0 = report only).
	MinAccepted     int64
	MinOfferedRatio float64
	QoS0Tolerance   float64
	MaxAckP99       time.Duration

	QuiescePoll    time.Duration
	QuiesceTimeout time.Duration
	QuiesceSettle  time.Duration
}

func (p MQTTProfile) withDefaults() MQTTProfile {
	if p.Manifest == "" {
		p.Manifest = "devicepulse"
	}
	if p.MetricKey == "" && p.Manifest == "devicepulse" {
		p.MetricKey = devicepulseMetric
	}
	if p.Identity == "" {
		p.Identity = IdentityAuto
	}
	if p.IdentityMaxEvents <= 0 {
		p.IdentityMaxEvents = DefaultIdentityMaxEvents
	}
	if p.MinAccepted <= 0 {
		p.MinAccepted = DefaultMinAccepted
	}
	if p.MinOfferedRatio <= 0 {
		p.MinOfferedRatio = DefaultMinOfferedRatio
	}
	if p.ConnectTimeout <= 0 {
		p.ConnectTimeout = 5 * time.Minute
	}
	if p.ReportInterval == 0 {
		p.ReportInterval = DefaultMQTTReportInterval
	}
	if p.QuiescePoll <= 0 {
		p.QuiescePoll = DefaultQuiescePoll
	}
	if p.QuiesceTimeout <= 0 {
		p.QuiesceTimeout = DefaultQuiesceTimeout
	}
	if p.QuiesceSettle <= 0 {
		p.QuiesceSettle = DefaultQuiesceSettle
	}
	return p
}

// Validate rejects a profile that cannot be run as asked.
func (p MQTTProfile) Validate() error {
	if p.Rate <= 0 {
		return fmt.Errorf("rate %v must be a positive number of events per second", p.Rate)
	}
	if p.Duration <= 0 {
		return fmt.Errorf("duration %s must be positive", p.Duration)
	}
	if p.Devices < 0 {
		return fmt.Errorf("device count %d is negative", p.Devices)
	}
	if p.MetricKey == "" {
		return fmt.Errorf("scenario %q needs --metric: the MQTT mode sends one of the scenario's metrics and only devicepulse's is known", p.Manifest)
	}
	switch p.Identity {
	case IdentityAuto, IdentityFull, IdentityCount:
	default:
		return fmt.Errorf("identity mode %q is not one of %s, %s, %s", p.Identity, IdentityAuto, IdentityFull, IdentityCount)
	}
	if p.MinOfferedRatio < 0 || p.MinOfferedRatio > 1 {
		return fmt.Errorf("min offered ratio %v must be within [0, 1]", p.MinOfferedRatio)
	}
	if p.QoS0Tolerance < 0 || p.QoS0Tolerance > 1 {
		return fmt.Errorf("qos0 tolerance %v must be within [0, 1]", p.QoS0Tolerance)
	}
	if p.MaxAckP99 < 0 || p.ConnectTimeout < 0 || p.QuiescePoll < 0 || p.QuiesceTimeout < 0 || p.QuiesceSettle < 0 {
		return fmt.Errorf("negative timeout")
	}
	if p.MinAccepted < 0 {
		return fmt.Errorf("min accepted %d is negative", p.MinAccepted)
	}
	if m, ok := sim.ScenarioManifest(p.Manifest); ok && m.DevicesPublishTheirOwnTelemetry {
		return fmt.Errorf("scenario %q publishes its telemetry from its own devices, so a load run would drive nothing; load-drivable scenarios: %v",
			p.Manifest, sim.LoadDrivableManifestIds())
	}
	return p.MQTT.Validate()
}

// MQTTTransport is what the run connected over, as requested and as observed.
type MQTTTransport struct {
	Broker        string           `json:"broker"`
	TLSRequested  bool             `json:"tlsRequested"`
	TLSInsecure   bool             `json:"tlsInsecureSkipVerify"`
	QoS           byte             `json:"qos"`
	CleanSession  bool             `json:"cleanSession"`
	TLSObserved   int64            `json:"tlsConnectionsObserved"`
	PlainObserved int64            `json:"plaintextConnectionsObserved"`
	TLSVersions   map[string]int64 `json:"tlsVersions,omitempty"`
}

// MQTTDrive is what the run applied and what the broker said back.
type MQTTDrive struct {
	// Devices is the fleet size and Connections the sessions it held: one per device, because
	// the broker confines a device credential to its own client id.
	Devices     int `json:"devices"`
	Connections int `json:"connections"`

	TargetRatePS  float64 `json:"targetRatePerSec"`
	OfferedRatePS float64 `json:"offeredRatePerSec"`
	AckedRatePS   float64 `json:"pubackedRatePerSec"`
	HoldSeconds   float64 `json:"holdSeconds"`
	DrainSeconds  float64 `json:"drainSeconds"`
	PadBytes      int     `json:"padBytes"`

	mqttload.Snapshot
	AckLatency mqttload.Percentiles `json:"ackLatency"`
	Intervals  []mqttload.Interval  `json:"intervals,omitempty"`
}

// MQTTStorage is the persisted side of the reconciliation.
type MQTTStorage struct {
	// Promised is the events the broker promised to store: PUBACKed for QoS 1, written for
	// QoS 0 (which promises nothing, see qos0-delivery).
	Promised  int64 `json:"promised"`
	Persisted int64 `json:"persisted"`
	// Reached: the windowed persisted count reached Promised within the quiesce timeout.
	Reached     bool    `json:"reachedTarget"`
	QuiesceSecs float64 `json:"quiesceSeconds"`
	// Lost is the number of PUBACKed (QoS 1) events not persisted; 0 when none were lost.
	// LostExact says whether the number came from the identity read-back (exact) or the
	// count comparison (a lower bound, see MaskedLossBound).
	Lost      int64 `json:"lost"`
	LostExact bool  `json:"lostExact"`
	// Excess is persisted events beyond everything that could have been stored.
	Excess int64 `json:"excess"`
	// MaskedLossBound is, in count mode, how many PUBACKed losses the count could be hiding:
	// the unconfirmed publishes, any of which may have been stored in a lost event's place,
	// plus the re-sent ones, any of which may have been stored twice. A count with a bound
	// above zero reaches no durability verdict (it fails as inconclusive), because a count
	// that cannot exclude a loss must not be published as having excluded one.
	MaskedLossBound int64 `json:"maskedLossBound"`
	// Undrained is, when the persisted count was still RISING at the quiesce timeout, how far
	// it was short of the promise. It is not called a loss (the rows may still be arriving)
	// and the run fails as inconclusive; raise --quiesce-timeout.
	Undrained    int64  `json:"undrained,omitempty"`
	IdentityMode string `json:"identityMode"`
}

// MQTTReport is the machine- and human-readable result of one MQTT run.
type MQTTReport struct {
	Manifest   string    `json:"manifest"`
	Seed       int64     `json:"seed"`
	Tenant     string    `json:"tenant"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`

	Transport MQTTTransport `json:"transport"`
	Drive     MQTTDrive     `json:"drive"`
	Storage   MQTTStorage   `json:"storage"`

	// Identity is the identity reconciliation's evidence (full mode only).
	Identity *IdentityReport `json:"identity,omitempty"`
	// Loss is true when PUBACKed events were not persisted.
	Loss       bool        `json:"loss"`
	Invariants []Invariant `json:"invariants"`
}

// Passed reports whether every invariant held. A report with nothing asserted, or without the
// durability verdict, proves nothing and fails.
func (r *MQTTReport) Passed() bool {
	if len(r.Invariants) == 0 || invariantByName(r.Invariants, InvPubackDurability) == nil && invariantByName(r.Invariants, InvQoS0Delivery) == nil {
		return false
	}
	for _, inv := range r.Invariants {
		if !inv.Passed {
			return false
		}
	}
	return true
}

// ExitCode is the process exit code the report calls for: ExitLoss for a run that lost PUBACKed
// events (always a failure, and the one a release gate must not mistake for a flake),
// ExitFail for any other failed invariant, ExitPass otherwise.
func (r *MQTTReport) ExitCode() int {
	switch {
	case r.Loss:
		return ExitLoss
	case !r.Passed():
		return ExitFail
	default:
		return ExitPass
	}
}

// JSON renders the report as indented JSON for the run's artifact.
func (r *MQTTReport) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// Human renders the operator-readable summary: the verdict, the load, the loss line, then one
// line per invariant.
func (r *MQTTReport) Human() string {
	var b strings.Builder
	verdict := "FAIL"
	switch {
	case r.Loss:
		verdict = "LOSS"
	case r.Passed():
		verdict = "PASS"
	}
	t := r.Transport
	fmt.Fprintf(&b, "load-test mqtt %s — %s (seed %d, tenant %s)\n", verdict, r.Manifest, r.Seed, r.Tenant)
	fmt.Fprintf(&b, "  transport: %s, QoS %d, clean session %v; observed %d TLS and %d plaintext connection(s) %v\n",
		t.Broker, t.QoS, t.CleanSession, t.TLSObserved, t.PlainObserved, t.TLSVersions)
	d := r.Drive
	fmt.Fprintf(&b, "  drive: %d devices on %d connections, target %.1f ev/s, offered %.1f, pubacked %.1f over %.0fs (+%.0fs drain) — sent %d, pubacked %d, unconfirmed %d, re-sent %d, not sent %d disconnected + %d in-flight cap, overruns %d\n",
		d.Devices, d.Connections, d.TargetRatePS, d.OfferedRatePS, d.AckedRatePS, d.HoldSeconds, d.DrainSeconds,
		d.Sent, d.Acked, d.Unconfirmed, d.Resent, d.NotSentDisconnected, d.SkippedInflight, d.Overruns)
	fmt.Fprintf(&b, "  connections: %d connects, %d losses, %d connect failures; ack latency p50 %.1fms p99 %.1fms p99.9 %.1fms max %.1fms\n",
		d.Connects, d.ConnectionLosses, d.ConnectFailures, d.AckLatency.P50, d.AckLatency.P99, d.AckLatency.P999, d.AckLatency.Max)
	s := r.Storage
	exact := "lower bound"
	if s.LostExact {
		exact = "exact"
	}
	fmt.Fprintf(&b, "  storage: promised %d, persisted %d (reached %v in %.0fs); lost %d (%s), excess %d; identity mode %s\n",
		s.Promised, s.Persisted, s.Reached, s.QuiesceSecs, s.Lost, exact, s.Excess, s.IdentityMode)
	if s.Lost > 0 {
		fmt.Fprintf(&b, "  LOSS: %d event(s) the broker PUBACKed are not in the store\n", s.Lost)
	}
	if s.MaskedLossBound > 0 {
		fmt.Fprintf(&b, "  NO DURABILITY VERDICT: a count-only comparison could be masking up to %d PUBACKed loss(es) (%d unconfirmed, %d re-sent); rerun with --identity full\n",
			s.MaskedLossBound, d.Unconfirmed, d.Resent)
	}
	for _, inv := range r.Invariants {
		mark := "FAIL"
		if inv.Passed {
			mark = "ok"
		}
		fmt.Fprintf(&b, "  [%-4s] %s — %s\n", mark, inv.Name, inv.Detail)
	}
	return b.String()
}

// mqttMeasured is everything the verdict reads, gathered so the verdict is a pure function.
type mqttMeasured struct {
	Profile MQTTProfile
	Drive   mqttload.Snapshot
	Latency mqttload.Percentiles
	// Held is how long the drive published.
	Held time.Duration
	// Persisted/Reached/QuiesceSecs are the oracle's windowed count. Rising says the count
	// had not reached the promise at the timeout and was still going up when re-read: the
	// pipeline was draining, so the shortfall is not (yet) a loss.
	Persisted   int64
	Reached     bool
	Rising      bool
	QuiesceSecs float64
	// NeverConnected is sessions that never connected once.
	NeverConnected int
	// Identity is the identity reconciliation's report and invariant; nil in count mode.
	Identity    *IdentityReport
	IdentityInv *Invariant
	// Mode is the identity mode that actually ran (never auto).
	Mode string
}

// promised is the events the broker promised to store.
func (m mqttMeasured) promised() int64 {
	if m.Profile.MQTT.QoS == 0 {
		return m.Drive.Written
	}
	return m.Drive.Acked
}

// mqttVerdict is the pure verdict of an MQTT run: the invariants, the storage section and
// whether it was a LOSS. Every branch is unit-tested without a broker or a cluster.
//
// The durability rule, for QoS 1, in order of how much the evidence can say:
//   - with the identity read-back, LOST is exactly the PUBACKed identities not stored;
//   - without it, LOST is PUBACKed minus persisted when positive. That is a LOWER bound: an
//     unconfirmed publish (no PUBACK, so the broker never promised it) may still have been
//     stored, and it would take the place of a lost one in the count. The report carries that
//     as maskedLossBound rather than hiding it, and the identity mode closes it.
//
// A count ABOVE what could have been stored (PUBACKed plus unconfirmed) is a duplicate or a
// contaminated window, and fails too.
func mqttVerdict(m mqttMeasured) ([]Invariant, MQTTStorage, bool) {
	p := m.Profile
	qos1 := p.MQTT.QoS == 1
	promised := m.promised()
	st := MQTTStorage{
		Promised: promised, Persisted: m.Persisted, Reached: m.Reached, QuiesceSecs: m.QuiesceSecs,
		IdentityMode: m.Mode,
	}

	var inv []Invariant
	add := func(name string, ok bool, format string, args ...any) {
		inv = append(inv, Invariant{Name: name, Passed: ok, Detail: fmt.Sprintf(format, args...)})
	}

	add(InvFleetConnected, m.NeverConnected == 0, "%d of %d device(s) never connected", m.NeverConnected, p.Devices)

	// The flag must have been truthful: the connections observed are the ones requested.
	inv = append(inv, transportInvariant(p.MQTT.TLS, m.Drive))

	floor := p.MinAccepted
	add(InvLoadApplied, promised >= floor, "%d event(s) acknowledged by the broker (floor %d — a gate must apply real load)", promised, floor)

	// The rate must have been both offered and acknowledged: a run whose broker answered half
	// of what it was sent did not sustain the rate it will be published at, however every
	// one of those answers reconciled.
	asked := p.Rate * m.Held.Seconds()
	offered := float64(m.Drive.Sent)
	acked := float64(promised)
	add(InvLoadOffered, asked > 0 && offered >= asked*p.MinOfferedRatio && acked >= asked*p.MinOfferedRatio,
		"offered %.0f and acknowledged %.0f of the %.0f requested (%.1f%% / %.1f%%; floor %.0f%%): a run that offered or had acknowledged less is a weaker test than asked for",
		offered, acked, asked, pct(offered, asked), pct(acked, asked), p.MinOfferedRatio*100)

	switch {
	case qos1:
		lost, exact, excess, maskable := durability(m)
		st.Lost, st.LostExact, st.Excess = lost, exact, excess
		if !exact {
			st.MaskedLossBound = maskable
		}
		draining := !m.Reached && m.Rising && lost > 0
		if draining {
			// Still arriving: the shortfall is reported, not concluded.
			st.Undrained, st.Lost = lost, 0
		}
		ok := st.Lost == 0 && excess == 0 && m.Reached && st.MaskedLossBound == 0
		var detail string
		switch {
		case draining:
			detail = fmt.Sprintf("inconclusive: the persisted count %d was %d short of the %d PUBACKed at the quiesce timeout and still rising; raise --quiesce-timeout",
				m.Persisted, st.Undrained, promised)
		case lost > 0:
			how := "lower bound, from the counts"
			if exact {
				how = "exact, by identity"
			}
			detail = fmt.Sprintf("LOSS: %d PUBACKed event(s) are not persisted (%s): PUBACKed %d, persisted %d", lost, how, promised, m.Persisted)
		case excess > 0:
			detail = fmt.Sprintf("UNEXPECTED: %d event(s) persisted beyond the %d PUBACKed + %d unconfirmed that could have been stored (duplicates, or a contaminated window)",
				excess, promised, m.Drive.Unconfirmed)
		case !m.Reached:
			detail = fmt.Sprintf("inconclusive: the persisted count %d did not reach the %d PUBACKed within the quiesce timeout", m.Persisted, promised)
		case exact:
			detail = fmt.Sprintf("every one of the %d PUBACKed events is persisted (by identity); %d unconfirmed", promised, m.Drive.Unconfirmed)
		case st.MaskedLossBound > 0:
			detail = fmt.Sprintf("inconclusive: persisted %d >= PUBACKed %d by count only, and up to %d PUBACKed loss(es) could be masked by %d unconfirmed publish(es) that were stored and %d re-sent one(s) stored twice; no durability verdict: use --identity full",
				m.Persisted, promised, st.MaskedLossBound, m.Drive.Unconfirmed, m.Drive.Resent)
		default:
			detail = fmt.Sprintf("persisted %d == PUBACKed %d, by count only (nothing unconfirmed or re-sent; a duplicate the platform itself stored would still offset a loss in a count: --identity full rules that out too)",
				m.Persisted, promised)
		}
		add(InvPubackDurability, ok, "%s", detail)
	default:
		// QoS 0 promises nothing, so a shortfall is reported, and gated only by the tolerance.
		short := promised - m.Persisted
		if short < 0 {
			short = 0
		}
		st.Lost = short
		st.Excess = max(m.Persisted-promised, 0)
		ratio := 0.0
		if promised > 0 {
			ratio = float64(short) / float64(promised)
		}
		ok := ratio <= p.QoS0Tolerance && st.Excess == 0
		add(InvQoS0Delivery, ok, "written %d, persisted %d: %d missing (%.4f%%; tolerance %.4f%%), %d excess — QoS 0 promises no delivery, so this is a measurement, gated only by the tolerance",
			promised, m.Persisted, short, ratio*100, p.QoS0Tolerance*100, st.Excess)
	}

	if m.IdentityInv != nil {
		inv = append(inv, *m.IdentityInv)
	}
	if p.MaxAckP99 > 0 && qos1 {
		got := time.Duration(m.Latency.P99 * float64(time.Millisecond))
		add(InvAckLatency, got <= p.MaxAckP99, "PUBACK p99 %s (ceiling %s), p50 %.1fms, max %.1fms", got.Round(time.Millisecond), p.MaxAckP99, m.Latency.P50, m.Latency.Max)
	}

	return inv, st, qos1 && st.Lost > 0
}

// transportInvariant gates that the sockets the run opened are the transport it was asked for:
// every connection TLS when TLS was requested, every one plaintext when it was not. The flag
// chooses the scheme, but the verdict reads the sockets, so a flag that did not do what it says
// (an earlier round's --no-tls left the broker's TLS on) fails the run instead of mislabelling
// it.
func transportInvariant(tlsRequested bool, snap mqttload.Snapshot) Invariant {
	ok := snap.TLSConns > 0 && snap.PlainConns == 0
	want := "TLS"
	if !tlsRequested {
		ok = snap.PlainConns > 0 && snap.TLSConns == 0
		want = "plaintext"
	}
	return Invariant{Name: InvTransport, Passed: ok,
		Detail: fmt.Sprintf("requested %s; observed %d TLS and %d plaintext connection(s)", want, snap.TLSConns, snap.PlainConns)}
}

// durability computes a QoS 1 run's loss: exactly from the identity read-back when it ran,
// else from the counts. maskable is the count mode's masked-loss bound.
func durability(m mqttMeasured) (lost int64, exact bool, excess int64, maskable int64) {
	acked, unconfirmed := m.Drive.Acked, m.Drive.Unconfirmed
	if m.Identity != nil {
		// A failed or inconclusive read-back has no exact answer: fall through to the counts
		// (the identity invariant carries the failure) unless it decided.
		if len(m.Identity.Inconclusive) == 0 {
			return m.Identity.Missing, true, m.Identity.Unexpected + m.Identity.ExtraCopies, 0
		}
	}
	if m.Persisted < acked {
		lost = acked - m.Persisted
	}
	if m.Persisted > acked+unconfirmed {
		excess = m.Persisted - acked - unconfirmed
	}
	return lost, false, excess, unconfirmed + m.Drive.Resent
}

func pct(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b * 100
}

// mqttRun is a run's collaborators, so the sequence from "fleet connected" to "verdict" is
// tested against a fake broker and fake platform reads, without a cluster.
type mqttRun struct {
	Tenant  string
	Profile MQTTProfile
	Seed    int64
	Fleet   *mqttload.Fleet
	Devices []string
	Ledger  *sim.IdentityLedger
	Counter eventCounter
	Reader  identityReader
}

// driveAndReconcile publishes at the profile's rate for its duration, waits for the platform to
// persist what the broker acknowledged, reads it back (per the identity mode) and returns the
// report. A context cancelled mid-drive is an error: the ledger is not a clean stop boundary,
// so no verdict is reached on it.
func (r mqttRun) driveAndReconcile(ctx context.Context) (*MQTTReport, error) {
	p := r.Profile
	started := time.Now()
	spec := mqttload.DriveSpec{
		Rate: p.Rate, Duration: p.Duration, MetricKey: p.MetricKey, PadBytes: p.PadBytes,
	}
	if p.ReportInterval > 0 {
		spec.ReportInterval = p.ReportInterval
		spec.OnInterval = func(iv mqttload.Interval) {
			log.Info().Float64("t", iv.AtSeconds).Int64("sent", iv.Sent).Int64("pubacked", iv.Acked).
				Int64("unconfirmed", iv.Unconfirmed).Int64("notSent", iv.NotSent).Int64("up", iv.Up).
				Int64("inflight", iv.Inflight).Int64("connLosses", iv.Losses).Float64("ackP99ms", iv.P99Ms).
				Msg("mqtt load interval")
		}
	}
	res, err := r.Fleet.Drive(ctx, spec, r.Ledger)
	if err != nil {
		return nil, err
	}
	snap := r.Fleet.Counters.Snapshot()
	lat := r.Fleet.Lat.Percentiles()
	held := res.End.Sub(res.Start)

	w := deriveWindow(res.Start, res.End)
	promised := snap.Acked
	if p.MQTT.QoS == 0 {
		promised = snap.Written
	}
	oracle := &Oracle{Counter: r.Counter, Poll: p.QuiescePoll, Timeout: p.QuiesceTimeout, Settle: p.QuiesceSettle}
	qr, err := oracle.Await(ctx, w, promised)
	if err != nil {
		return nil, fmt.Errorf("oracle read-back: %w", err)
	}
	// A count short of the promise at the timeout is a loss only if it has stopped moving:
	// re-read it once more after a poll, and a count still rising is a drain, not a loss.
	rising := false
	if !qr.Reached {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(max(p.QuiescePoll, p.QuiesceSettle)):
		}
		again, err := r.Counter.Count(ctx, w)
		if err != nil {
			return nil, fmt.Errorf("oracle re-read: %w", err)
		}
		if again > qr.Persisted {
			rising = true
			qr.Persisted = again
		}
	}

	mode := p.Identity
	if mode == IdentityAuto {
		mode = IdentityCount
		if promised <= p.IdentityMaxEvents {
			mode = IdentityFull
		}
	}
	m := mqttMeasured{
		Profile: p, Drive: snap, Latency: lat, Held: held,
		Persisted: qr.Persisted, Reached: qr.Reached, Rising: rising, QuiesceSecs: qr.Elapsed.Seconds(),
		NeverConnected: r.Fleet.NeverConnected(), Mode: mode,
	}
	if mode == IdentityFull {
		// The ledger holds Accepted for PUBACKs and Ambiguous for everything else, so it is
		// compared with the driver's counters in the same terms.
		drive := sim.Snapshot{Emitted: snap.Acked, Failed: snap.Unconfirmed + snap.Written}
		rep, inv, _ := checkIdentity(ctx, r.Counter, r.Reader, r.Ledger, r.Devices, drive, w, res.End, p.QuiesceSettle)
		m.Identity, m.IdentityInv = &rep, &inv
	}
	inv, st, lost := mqttVerdict(m)

	cfg := r.Fleet.Config()
	return &MQTTReport{
		Manifest: p.Manifest, Seed: r.Seed, Tenant: r.Tenant,
		StartedAt: started.UTC(), FinishedAt: time.Now().UTC(),
		Transport: MQTTTransport{
			Broker: r.Fleet.Broker(), TLSRequested: cfg.TLS, TLSInsecure: cfg.TLSInsecure,
			QoS: cfg.QoS, CleanSession: cfg.CleanSession,
			TLSObserved: snap.TLSConns, PlainObserved: snap.PlainConns, TLSVersions: r.Fleet.TLSVersions(),
		},
		Drive: MQTTDrive{
			Devices: len(r.Devices), Connections: r.Fleet.Devices(),
			TargetRatePS: p.Rate, OfferedRatePS: perSec(snap.Sent, held), AckedRatePS: perSec(promised, held),
			HoldSeconds: held.Seconds(), DrainSeconds: res.Drained.Sub(res.End).Seconds(), PadBytes: p.PadBytes,
			Snapshot: snap, AckLatency: lat, Intervals: res.Intervals,
		},
		Storage:    st,
		Identity:   m.Identity,
		Loss:       lost,
		Invariants: inv,
	}, nil
}

func perSec(n int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / d.Seconds()
}

// RunMQTT executes one MQTT load run end to end against the platform the handshake points at:
// provision the scenario's devices and their credentials exactly as the HTTP mode does, connect
// one MQTT session per device, publish at the profile's rate, wait for the platform to persist
// what the broker acknowledged, and return the report. The caller turns report.ExitCode() into
// the process exit code.
func RunMQTT(ctx context.Context, hs *sim.Handshake, p MQTTProfile) (*MQTTReport, error) {
	p = p.withDefaults()
	// The transport defaults to what the handshake recorded, never to a guess.
	if p.MQTT.Broker == "" {
		p.MQTT.Broker = hs.Endpoints.MqttBroker
	}
	if !p.TLSInsecureExplicit && hs.MqttTLSInsecure && p.MQTT.TLS {
		p.MQTT.TLSInsecure = true
	}
	p.MQTT.InstanceId, p.MQTT.Tenant = hs.InstanceId, hs.Tenant
	if err := p.Validate(); err != nil {
		return nil, err
	}

	eventEndpoint, err := httpGraphQLFromWS(hs.Endpoints.EventMgmtWS)
	if err != nil {
		return nil, err
	}
	load := sim.Load{DeviceCount: p.Devices}
	driver, err := sim.NewSim(p.Manifest, hs.Seed, load)
	if err != nil {
		return nil, err
	}
	rt, err := sim.NewRuntime(hs, load, sim.DeviceCount(driver.Manifest()))
	if err != nil {
		return nil, err
	}
	counter := &graphqlEventCounter{session: rt.Session, endpoint: eventEndpoint}
	led := sim.NewIdentityLedger()
	rt.Identity = led

	// The window reconciliation assumes the tenant is ours alone for the run.
	if err := requireCleanTenant(ctx, counter); err != nil {
		return nil, err
	}
	if err := driver.Bootstrap(ctx, rt); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}

	fleet, err := mqttload.NewFleet(p.MQTT, rt.Devices)
	if err != nil {
		return nil, err
	}
	fleet.Start(ctx)
	defer fleet.Close()
	log.Info().Int("devices", len(rt.Devices)).Str("broker", fleet.Broker()).Bool("tls", p.MQTT.TLS).
		Msg("connecting the MQTT fleet")
	if n, err := fleet.AwaitConnected(ctx, p.ConnectTimeout); err != nil {
		return nil, fmt.Errorf("connect the fleet: %w (%d connected)", err, n)
	}

	run := mqttRun{
		Tenant: hs.Tenant, Profile: p, Seed: hs.Seed, Fleet: fleet,
		Devices: deviceTokens(rt.Devices), Ledger: led,
		Counter: counter, Reader: &graphqlIdentityReader{session: rt.Session, endpoint: eventEndpoint},
	}
	return run.driveAndReconcile(ctx)
}
