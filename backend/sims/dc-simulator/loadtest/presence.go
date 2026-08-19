// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-simulator/cmdreceiver"
	"github.com/devicechain-io/dc-simulator/sim"
	"github.com/rs/zerolog/log"
)

// L4: broker-asserted MQTT presence under churn.
//
// Where L2d-3 proves a command reaches a device, this layer proves the platform
// knows WHICH devices are there. Presence is not a value a device sends — it is a
// projection driven by the broker's own connection advisories, tapped by
// event-sources and folded into device-state. Nothing else in the harness can speak
// for it: an event proves a device WAS there when it published, which is exactly the
// inference this subsystem exists to replace.
//
// Four cohorts, each isolating one direction the projection can be wrong:
//
//   - steady   — connects once and never leaves. Asserts the FALSE-DEATH class, and
//     asserts it CONTINUOUSLY: a device that flickered offline and back would pass an
//     end-state read while having fired every offline alarm in the fleet. So the oracle
//     polls across the whole hold and keeps the FIRST deviation, not the last reading.
//   - churn    — connects and disconnects R times, ending connected. Asserts session
//     ordering, the property a repair depends on.
//   - departed — connects, leaves once, stays gone. Asserts the DISCONNECT EDGE, the
//     direction that is lost entirely when a broker shuts down gracefully, and the one
//     that makes the steady cohort's verdict non-vacuous: an oracle that reported every
//     device online would sail through the steady cohort and fail here.
//   - background — HTTP telemetry only, never an MQTT connection. Supplies contention.
//
// 🔴 THE PRESENCE COHORTS PUBLISH NO TELEMETRY, and that is a constraint rather than
// an omission. A device state row records the source of whichever event last drove it,
// so a device connected over MQTT that also publishes over the HTTP ingress has its
// row flip to the HTTP source — after which assertedDeviceStates(source: <the mqtt
// source>), the reconciler's OWN enumeration, cannot see it. Mixing transports on one
// cohort device would make this harness measure that overwrite instead of presence.
//
// 🔴 THE VERDICT HAS THREE DISPOSITIONS, NOT TWO. Presence transitions pass through
// the same per-tenant ingest ceiling as telemetry, so a background fleet saturating
// that ceiling can SHED the very transitions the oracle gates on — a steady device
// flickers, a departed device misses its edge, and every one of those reads like a
// presence defect while presence is perfectly correct. The harness does not guess
// where the ceiling is: it watches the driver's own shed counter, and a run that
// observed ANY shed is INCONCLUSIVE on its own exit code. A shed transition and a
// LOST transition are indistinguishable from out here, and reporting either verdict
// would be a guess dressed as a finding.
//
// The same discipline covers the tap itself: broker-asserted presence is OFF unless
// configured, and on a cluster where it is off these devices produce no state row at
// all rather than an inferred one. That is checked BEFORE the load starts, against an
// idle tenant, and a tap that is not running ends the run INCONCLUSIVE naming every
// switch that turns it off — never a pass, and never a data verdict.

// Harness topology — its own profile and device type, distinct from every other
// harness's, so a token names its harness and its cohort role in a log line.
const (
	// HarnessPresenceProfileToken is the pinned profile every presence cohort device
	// instantiates. It carries the temp metric only because the BACKGROUND fleet
	// publishes it; the presence cohorts themselves never emit.
	HarnessPresenceProfileToken = "harness-pres-profile"
	// HarnessPresenceDeviceTypeToken is the device type every cohort instantiates.
	HarnessPresenceDeviceTypeToken = "harness-pres-device"

	presSteadyTokenPattern = "harness-pres-steady-{n:03d}"
	presChurnTokenPattern  = "harness-pres-churn-{n:03d}"
	presGoneTokenPattern   = "harness-pres-gone-{n:03d}"
	presBgTokenPattern     = "harness-pres-bg-{n:05d}"
	presSteadyTokenPrefix  = "harness-pres-steady-"
	presChurnTokenPrefix   = "harness-pres-churn-"
	presGoneTokenPrefix    = "harness-pres-gone-"
	presBgTokenPrefix      = "harness-pres-bg-"

	// presenceSourceAsserted is the wire value that says this row's Active flag was
	// driven by a broker StateChange rather than by the inactivity sweep. Mirrored as a
	// literal — the sim speaks only the wire. Its opposite, INFERRED, is deliberately
	// NOT a constant here: the harness never tests for it. A cluster with the tap off
	// produces no row at all for a device that publishes nothing, so "is it INFERRED"
	// is a question whose answer is absent rather than false.
	presenceSourceAsserted = "ASSERTED"

	// ControlDropSteadyDevice is the negative control: disconnect one device from the
	// cohort whose whole invariant is "never goes offline", and require the run to fail
	// on EXACTLY the invariants that departure must flip — no more, no fewer.
	ControlDropSteadyDevice = "drop-a-steady-device"
)

// The gated invariant names. Declared as constants because the negative control
// compares against a literal SET of them: a control that demanded merely "some
// failure" would be passed by an oracle that failed everything, and one that demanded
// "only this failure" would be failed by a healthy oracle whose other invariants
// legitimately flip too. Naming the exact set is the only form that discriminates.
const (
	InvPresenceLoadFloor         = "presence-load-floor"
	InvPresenceConnectAsserted   = "presence-connect-asserted"
	InvPresenceSteadyOnline      = "presence-steady-online"
	InvPresenceDepartedOffline   = "presence-departed-offline"
	InvPresenceSessionsMonotonic = "presence-sessions-monotonic"
	InvPresenceReconcilerExact   = "presence-reconciler-read-exact"
)

// Presence-harness defaults.
const (
	DefaultPresenceSteady      = 3
	DefaultPresenceChurn       = 3
	DefaultPresenceDeparted    = 2
	DefaultPresenceBackground  = 50
	DefaultPresenceBgInterval  = 200 * time.Millisecond
	DefaultPresenceChurnRounds = 3
	// DefaultPresenceMinAccepted is the background load floor. Without it a run whose
	// background fleet never got off the ground would certify presence "under load"
	// having applied none — the same vacuous green the other layers gate against.
	DefaultPresenceMinAccepted = 1000
	DefaultPresencePoll        = 3 * time.Second
	// DefaultPresenceTapTimeout backstops the pre-load wait for the FIRST steady
	// device's asserted row. It runs against an idle tenant, so it is generous only to
	// absorb service startup, not contention.
	DefaultPresenceTapTimeout = 60 * time.Second
	// DefaultPresenceFlipTimeout backstops each churn transition under load: broker
	// advisory → tap → ingest → device-state, with the per-tenant ceiling in the path.
	DefaultPresenceFlipTimeout = 90 * time.Second
	// DefaultPresenceTailHold is the steady-observation window AFTER the last churn
	// round: it is what gives the departed cohort's advisory (and, in the negative
	// control, the dropped steady device's) time to propagate and be seen by at least
	// one more poll before the run reads its final state.
	DefaultPresenceTailHold = 20 * time.Second
	DefaultPresenceSettle   = 10 * time.Second
	// DefaultPresencePageSize is deliberately TINY. The paged read is the surface this
	// invariant exists to exercise, and a page size that swallows the whole cohort in
	// one request tests the cursor not at all.
	DefaultPresencePageSize = 2
	// DefaultPresenceMinSteadyPolls is the floor on how many CLEAN steady reads a
	// verdict rests on. "No bad observation" across zero observations is not a pass.
	DefaultPresenceMinSteadyPolls = 5

	// presenceMaxPages caps the assertedDeviceStates walk. A server whose cursor does
	// not advance would otherwise page forever; the cap turns that into a read error
	// rather than a hung gate.
	presenceMaxPages = 1000
)

// PresenceConfig configures one broker-asserted presence run.
type PresenceConfig struct {
	Seed int64

	SteadyDevices     int
	ChurnDevices      int
	DepartedDevices   int
	BackgroundDevices int

	BackgroundInterval time.Duration
	Concurrency        int

	// ChurnRounds is R: how many disconnect/reconnect cycles each churn device
	// performs. Each round yields one session reading, so R rounds give R+1 readings
	// (the initial connect plus one per round) and R strict increases to assert.
	ChurnRounds int

	MinAccepted int64

	Poll           time.Duration
	TapTimeout     time.Duration
	FlipTimeout    time.Duration
	TailHold       time.Duration
	Settle         time.Duration
	PageSize       int
	MinSteadyPolls int

	MqttBroker      string
	MqttTLSInsecure bool

	// Control names a negative control to run instead of a plain pass. Empty for a
	// normal run; ControlDropSteadyDevice for the one this harness ships.
	Control string
}

// withDefaults fills every unset field.
//
// 🔴 UNSET means EXACTLY zero, not "not positive". The other harnesses here fold a
// negative into the default, which quietly turns `--bg-devices -5` into fifty — and,
// worse, makes every "must be at least one" line in Validate below a check that
// cannot fail, since nothing negative ever survives long enough to reach it. A
// negative is a caller mistake, and it is passed through so that Validate can say so.
func (c PresenceConfig) withDefaults() PresenceConfig {
	if c.SteadyDevices == 0 {
		c.SteadyDevices = DefaultPresenceSteady
	}
	if c.ChurnDevices == 0 {
		c.ChurnDevices = DefaultPresenceChurn
	}
	if c.DepartedDevices == 0 {
		c.DepartedDevices = DefaultPresenceDeparted
	}
	if c.BackgroundDevices == 0 {
		c.BackgroundDevices = DefaultPresenceBackground
	}
	if c.BackgroundInterval == 0 {
		c.BackgroundInterval = DefaultPresenceBgInterval
	}
	if c.ChurnRounds == 0 {
		c.ChurnRounds = DefaultPresenceChurnRounds
	}
	if c.MinAccepted == 0 {
		c.MinAccepted = DefaultPresenceMinAccepted
	}
	if c.Poll == 0 {
		c.Poll = DefaultPresencePoll
	}
	if c.TapTimeout == 0 {
		c.TapTimeout = DefaultPresenceTapTimeout
	}
	if c.FlipTimeout == 0 {
		c.FlipTimeout = DefaultPresenceFlipTimeout
	}
	if c.TailHold == 0 {
		c.TailHold = DefaultPresenceTailHold
	}
	if c.Settle == 0 {
		c.Settle = DefaultPresenceSettle
	}
	if c.PageSize == 0 {
		c.PageSize = DefaultPresencePageSize
	}
	if c.MinSteadyPolls == 0 {
		c.MinSteadyPolls = DefaultPresenceMinSteadyPolls
	}
	if strings.TrimSpace(c.MqttBroker) == "" {
		c.MqttBroker = defaultMqttBroker
	}
	return c
}

// Validate rejects a config that cannot answer the question it claims to.
func (c PresenceConfig) Validate() error {
	if c.SteadyDevices < 1 {
		return fmt.Errorf("at least one steady device is required — it is the false-death oracle and the tap probe")
	}
	if c.ChurnDevices < 1 {
		return fmt.Errorf("at least one churn device is required — it is the session-ordering oracle")
	}
	if c.DepartedDevices < 1 {
		return fmt.Errorf("at least one departed device is required — it is the disconnect-edge oracle, and without it a check that reported every device online would pass")
	}
	if c.BackgroundDevices < 0 || c.MinAccepted < 0 {
		return fmt.Errorf("background devices and min-accepted must not be negative")
	}
	if c.ChurnRounds < 1 {
		return fmt.Errorf("churn rounds must be at least 1 — zero rounds asserts no session ordering at all")
	}
	if c.PageSize < 1 {
		return fmt.Errorf("page size must be at least 1")
	}
	if c.MinSteadyPolls < 1 {
		return fmt.Errorf("the steady-poll floor must be at least 1 — a verdict resting on zero observations is not a verdict")
	}
	if c.Poll <= 0 || c.TapTimeout <= 0 || c.FlipTimeout <= 0 || c.TailHold <= 0 || c.Settle <= 0 {
		return fmt.Errorf("every poll cadence, timeout and hold must be positive")
	}
	// The paged read is an invariant, not a convenience: a page size that fits the
	// whole asserted cohort walks one page and proves nothing about the cursor.
	if cohort := c.SteadyDevices + c.ChurnDevices; c.PageSize >= cohort {
		return fmt.Errorf("page size %d is not smaller than the %d asserted-active devices, so the reconciler read would complete in one page and exercise no cursor", c.PageSize, cohort)
	}
	if c.PageSize > 1000 {
		return fmt.Errorf("page size %d exceeds the assertedDeviceStates cap of 1000", c.PageSize)
	}
	if c.Control != "" && c.Control != ControlDropSteadyDevice {
		return fmt.Errorf("unknown control %q (want %q)", c.Control, ControlDropSteadyDevice)
	}
	if c.Control == ControlDropSteadyDevice && c.SteadyDevices < 2 {
		return fmt.Errorf("the %q control needs at least 2 steady devices: it drops one, and the first is the tap probe whose asserted row establishes that presence is running at all", ControlDropSteadyDevice)
	}
	return nil
}

func (c PresenceConfig) load() sim.Load {
	return sim.Load{DeviceCount: 0, EmitInterval: c.BackgroundInterval, Concurrency: c.Concurrency}
}

// harnessManifest builds the presence topology: one profile, one device type, four
// populations. Pure — (config, seed) fully determines it.
func (c PresenceConfig) harnessManifest() sim.SimManifest {
	return sim.SimManifest{
		Name: "harness-presence",
		Seed: c.Seed,
		Profiles: []sim.ProfileSpec{
			{
				Token:    HarnessPresenceProfileToken,
				Name:     "Load-test Presence Harness Profile",
				Category: "sensor",
				Metrics: []sim.MetricSpec{
					{Key: HarnessMetricKey, Name: "Temperature", DataType: "DOUBLE", Unit: "C"},
				},
			},
		},
		DeviceTypes: []sim.DeviceTypeSpec{
			{Token: HarnessPresenceDeviceTypeToken, Name: "Load-test Presence Harness Device", ProfileToken: HarnessPresenceProfileToken},
		},
		Populations: []sim.PopulationSpec{
			{OfType: HarnessPresenceDeviceTypeToken, Count: c.SteadyDevices, TokenPattern: presSteadyTokenPattern, ExternalIdPattern: "HARNESS-PRES-STEADY-{n:03d}"},
			{OfType: HarnessPresenceDeviceTypeToken, Count: c.ChurnDevices, TokenPattern: presChurnTokenPattern, ExternalIdPattern: "HARNESS-PRES-CHURN-{n:03d}"},
			{OfType: HarnessPresenceDeviceTypeToken, Count: c.DepartedDevices, TokenPattern: presGoneTokenPattern, ExternalIdPattern: "HARNESS-PRES-GONE-{n:03d}"},
			{OfType: HarnessPresenceDeviceTypeToken, Count: c.BackgroundDevices, TokenPattern: presBgTokenPattern, ExternalIdPattern: "HARNESS-PRES-BG-{n:05d}"},
		},
	}
}

// --- disposition --------------------------------------------------------------

// Disposition is the run's verdict, and there are THREE of them because two is a
// lie. A gate that can only say PASS or FAIL has to report an environment it could
// not measure in as one of the two, and whichever it picks is wrong: a false green
// certifies nothing, and a false red sends someone hunting a defect that is not there.
type Disposition string

const (
	DispositionPass Disposition = "PASS"
	DispositionFail Disposition = "FAIL"
	// DispositionInconclusive means the run could not measure what it claims to
	// measure. It is NOT a soft failure and NOT a soft pass — it is the statement that
	// this run has no opinion, and it carries its own exit code so a caller cannot
	// mistake it for either.
	DispositionInconclusive Disposition = "INCONCLUSIVE"
)

// tapOffAdvice is what an INCONCLUSIVE-because-the-tap-is-off run prints. It names
// every switch that turns broker-asserted presence off, because there are six and
// naming one would misdiagnose the other five. The harness does not guess which
// fired — event-sources logs a distinct line for each, and that log is the answer.
const tapOffAdvice = "broker-asserted MQTT presence does not appear to be running on this instance, so this run " +
	"cannot tell a presence defect from a cluster that never had presence. Any ONE of these turns it off, and " +
	"event-sources logs a DISTINCT line for whichever fired — read that log rather than guessing: " +
	"(1) brokerPresence is disabled in configuration; " +
	"(2) infrastructure.nats.auth.sysUser/sysPassword are empty, which is every cluster not brought up by dcctl bootstrap; " +
	"(3) no event source is pointed at the platform broker, so there are no connection advisories to read; " +
	"(4) infrastructure.serviceAuth.secret or the user-management hostname is unset, so the repair path cannot run and the tap stays off rather than running without it; " +
	"(5) the NATS system-account dial failed; " +
	"(6) the advisory subscribe failed."

// decideDisposition folds the run's observations into the verdict. Pure, and
// deliberately so: the INCONCLUSIVE branches are the ones a live run reaches least
// often and can least afford to get wrong, so they are decided by a function a unit
// test can drive into every branch without a cluster.
//
// The ORDER is the substance. Inconclusive outranks a failed invariant, because both
// inconclusive conditions are reasons an invariant would fail for a cause that is not
// presence: a shed transition never reached device-state, and a tap that is off never
// produced one. Reporting FAIL in either case would name the platform for the
// environment's problem.
func decideDisposition(tapLive bool, shed int64, invs []Invariant) (Disposition, string) {
	if !tapLive {
		return DispositionInconclusive, tapOffAdvice
	}
	if shed > 0 {
		return DispositionInconclusive, fmt.Sprintf("the background fleet was shed %d time(s) at the per-tenant ingest ceiling, "+
			"and presence transitions pass through that SAME ceiling — so a transition this run did not observe may have been "+
			"refused rather than lost, and no presence verdict can be told apart from a governance one. Lower the background "+
			"load (--bg-devices / --bg-interval) or raise the tenant's ingest ceiling, and re-run", shed)
	}
	// An empty invariant set is not a pass: a report with nothing asserted has proven
	// nothing, which is a broken harness rather than a healthy platform.
	if len(invs) == 0 {
		return DispositionFail, "no invariants were evaluated, so this run asserted nothing"
	}
	var failed []string
	for _, inv := range invs {
		if !inv.Passed {
			failed = append(failed, inv.Name)
		}
	}
	if len(failed) > 0 {
		return DispositionFail, "failed: " + strings.Join(failed, ", ")
	}
	return DispositionPass, "every presence invariant held"
}

// controlExpectations maps a control name to the EXACT set of invariants its
// perturbation must flip. Dropping a steady device flips two, not one, and both are
// load-bearing: the cohort's own continuous-online invariant, and the reconciler's
// exact-set read, which can no longer enumerate a device that is genuinely offline.
// A control demanding only the first would be satisfied by an oracle that had lost
// the ability to enumerate at all.
var controlExpectations = map[string][]string{
	ControlDropSteadyDevice: {InvPresenceSteadyOnline, InvPresenceReconcilerExact},
}

// evaluateControl decides whether a negative-control run behaved. Pure.
//
// It demands the failure set EXACTLY: the expected invariants red and every other
// one green. Demanding "at least" would be passed by an oracle that failed
// everything — the very breakage a control exists to detect — and demanding "only"
// would fail a healthy oracle whose other invariants legitimately flip as a
// consequence. Neither weaker reading discriminates; this one does.
func evaluateControl(control string, invs []Invariant) (satisfied bool, detail string) {
	want, ok := controlExpectations[control]
	if !ok {
		return false, fmt.Sprintf("no expected failure set is declared for control %q", control)
	}
	if len(invs) == 0 {
		return false, "the control run evaluated no invariants, so it cannot have flipped any"
	}
	wantSet := make(map[string]bool, len(want))
	for _, n := range want {
		wantSet[n] = true
	}
	var missing, unexpected []string
	seen := make(map[string]bool, len(invs))
	for _, inv := range invs {
		seen[inv.Name] = true
		switch {
		case wantSet[inv.Name] && inv.Passed:
			missing = append(missing, inv.Name)
		case !wantSet[inv.Name] && !inv.Passed:
			unexpected = append(unexpected, inv.Name)
		}
	}
	// An expected invariant that is not in the report at all did not "pass" — it was
	// never evaluated, and a control cannot be satisfied by an assertion that did not run.
	for _, n := range want {
		if !seen[n] {
			missing = append(missing, n+" (not evaluated)")
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	if len(missing) == 0 && len(unexpected) == 0 {
		return true, fmt.Sprintf("the control flipped exactly its expected set {%s}", strings.Join(want, ", "))
	}
	return false, fmt.Sprintf("control %q expected exactly {%s} to fail; still passing: [%s]; unexpectedly failing: [%s]",
		control, strings.Join(want, ", "), strings.Join(missing, ", "), strings.Join(unexpected, ", "))
}

// --- observations -------------------------------------------------------------

// deviceStateObs is one read of one device's presence projection. Present
// distinguishes "the row says offline" from "there is no row", which are different
// claims: the first is a transition the platform applied, the second is a device the
// platform has never heard of. Collapsing them is how a cluster with no presence tap
// reads as a fleet that is uniformly offline.
type deviceStateObs struct {
	Present        bool
	Active         bool
	PresenceSource string
	SessionID      int64
	Source         string
}

func (o deviceStateObs) assertedActive() bool {
	return o.Present && o.Active && o.PresenceSource == presenceSourceAsserted
}

func (o deviceStateObs) assertedOffline() bool {
	return o.Present && !o.Active && o.PresenceSource == presenceSourceAsserted
}

// describe renders one observation for a failure detail.
func (o deviceStateObs) describe() string {
	if !o.Present {
		return "no device-state row"
	}
	return fmt.Sprintf("active=%v presenceSource=%s sessionId=%d source=%s", o.Active, o.PresenceSource, o.SessionID, o.Source)
}

// presenceObservations is everything the classifier gets. It is a plain value with
// no cluster in it, so every invariant can be driven into both verdicts by a test.
type presenceObservations struct {
	// CleanSteadyPolls counts steady polls whose read SUCCEEDED. A poll that errored
	// is counted separately and is not evidence of anything: "no bad observation"
	// across reads that never happened is not a pass, which is why the classifier
	// gates on this count rather than on an empty bad-set alone.
	CleanSteadyPolls int
	SteadyReadErrors int
	// SteadyBad maps a steady token to its first bad observation across the hold.
	SteadyBad map[string]string

	// ConnectBaseline is the pre-load read of every cohort device, taken once every
	// one of them has been connected. It is what makes the departed cohort's verdict
	// mean something: a device that never came online cannot prove a disconnect edge
	// by being offline at the end.
	ConnectBaseline map[string]deviceStateObs

	// DepartedFinal is the post-settle read of the departed cohort.
	DepartedFinal map[string]deviceStateObs

	// ChurnSessions maps a churn token to its session readings: index 0 is the initial
	// connect, index i the reading after round i. R rounds give R+1 readings.
	ChurnSessions map[string][]int64
	// ChurnFlipTimeouts records transitions the projection did not show within the
	// flip timeout. NON-GATING by itself — the consequence of a missed flip is a
	// session that did not climb and a device the reconciler cannot enumerate, both of
	// which are gated above — but reported, because a run full of them explains a
	// failure that would otherwise read as a mystery.
	ChurnFlipTimeouts map[string]int

	// AssertedActive is the exhaustively-paged reconciler enumeration, and Pages is how
	// many requests it took. Pages is evidence the cursor was actually exercised.
	AssertedActive []string
	Pages          int
	// AssertedReadErr is set when the paged walk could not complete. A read that failed
	// is not an empty set; the classifier fails the invariant rather than reporting an
	// enumeration of nothing as an exact-set mismatch.
	AssertedReadErr string
}

// --- classification (pure) ----------------------------------------------------

// classifyPresence turns the observations into the gated invariants. Pure — a
// function of observations plus the cohort membership — so every branch is
// exhaustively unit-testable with no cluster.
func classifyPresence(obs presenceObservations, steady, churn, departed []string, cfg PresenceConfig, accepted int64) []Invariant {
	var invs []Invariant

	// 1. Load floor. A presence verdict reached with no contention is not the verdict
	// this layer claims to produce.
	invs = append(invs, Invariant{
		Name:   InvPresenceLoadFloor,
		Passed: accepted >= cfg.MinAccepted,
		Detail: fmt.Sprintf("background accepted %d (floor %d)", accepted, cfg.MinAccepted),
	})

	// 2. Connect baseline. Every cohort device reached asserted-online BEFORE the load
	// started. This is the assertion the two end-state invariants rest on: without it,
	// a departed device reading offline at the end proves nothing (it may never have
	// been online), and the steady cohort would be certifying a connection it never made.
	all := append(append(append([]string{}, steady...), churn...), departed...)
	var notEstablished []string
	for _, tok := range all {
		if !obs.ConnectBaseline[tok].assertedActive() {
			notEstablished = append(notEstablished, fmt.Sprintf("%s (%s)", tok, obs.ConnectBaseline[tok].describe()))
		}
	}
	sort.Strings(notEstablished)
	if len(notEstablished) == 0 {
		invs = append(invs, Invariant{Name: InvPresenceConnectAsserted, Passed: true,
			Detail: fmt.Sprintf("all %d cohort device(s) reached asserted-online before the load started", len(all))})
	} else {
		invs = append(invs, Invariant{Name: InvPresenceConnectAsserted, Passed: false,
			Detail: fmt.Sprintf("%d cohort device(s) never reached asserted-online: %s", len(notEstablished), strings.Join(notEstablished, "; "))})
	}

	// 3. Steady online, CONTINUOUSLY. ANY deviation across the hold is the verdict,
	// not the last reading — a flicker that recovered is still every offline alarm in
	// the fleet having fired.
	switch {
	case obs.CleanSteadyPolls < cfg.MinSteadyPolls:
		invs = append(invs, Invariant{Name: InvPresenceSteadyOnline, Passed: false,
			Detail: fmt.Sprintf("only %d clean steady poll(s) (floor %d, %d read error(s)) — too few observations to certify that the cohort never went offline",
				obs.CleanSteadyPolls, cfg.MinSteadyPolls, obs.SteadyReadErrors)})
	case len(obs.SteadyBad) > 0:
		bad := make([]string, 0, len(obs.SteadyBad))
		for tok, d := range obs.SteadyBad {
			bad = append(bad, fmt.Sprintf("%s (%s)", tok, d))
		}
		sort.Strings(bad)
		invs = append(invs, Invariant{Name: InvPresenceSteadyOnline, Passed: false,
			Detail: fmt.Sprintf("%d of %d steady device(s) were not asserted-online at every one of %d poll(s): %s",
				len(obs.SteadyBad), len(steady), obs.CleanSteadyPolls, strings.Join(bad, "; "))})
	default:
		invs = append(invs, Invariant{Name: InvPresenceSteadyOnline, Passed: true,
			Detail: fmt.Sprintf("all %d steady device(s) read asserted-online at every one of %d clean poll(s)", len(steady), obs.CleanSteadyPolls)})
	}

	// 4. Departed offline. The disconnect edge — the direction a graceful broker
	// shutdown loses entirely.
	var stillOn []string
	for _, tok := range departed {
		if !obs.DepartedFinal[tok].assertedOffline() {
			stillOn = append(stillOn, fmt.Sprintf("%s (%s)", tok, obs.DepartedFinal[tok].describe()))
		}
	}
	sort.Strings(stillOn)
	if len(stillOn) == 0 {
		invs = append(invs, Invariant{Name: InvPresenceDepartedOffline, Passed: true,
			Detail: fmt.Sprintf("all %d departed device(s) read asserted-offline", len(departed))})
	} else {
		invs = append(invs, Invariant{Name: InvPresenceDepartedOffline, Passed: false,
			Detail: fmt.Sprintf("%d departed device(s) did not read asserted-offline: %s", len(stillOn), strings.Join(stillOn, "; "))})
	}

	// 5. Session ordering. An advisory-driven reconnect must climb, because the
	// advisory path carries no compare-and-set and presence rejects a lower session
	// without one.
	//
	// BOUNDED OBSERVATION, stated rather than assumed: a lower session IS accepted when
	// it arrives with a satisfied compare-and-set, which is exactly the reconciler's
	// repair path — so a regression that occurred and was repaired between two of this
	// harness's readings is invisible to it. And on a MULTI-NODE broker a trailing
	// clock can mint a lower session legitimately; the platform counts that rather than
	// eliminating it. This invariant therefore states a single-broker-node assumption,
	// and against an HA broker it would be a standing false finding rather than a defect.
	wantReadings := cfg.ChurnRounds + 1
	var badSessions []string
	for _, tok := range churn {
		s := obs.ChurnSessions[tok]
		if len(s) != wantReadings {
			badSessions = append(badSessions, fmt.Sprintf("%s has %d session reading(s), want %d", tok, len(s), wantReadings))
			continue
		}
		for i := 1; i < len(s); i++ {
			if s[i] <= s[i-1] {
				badSessions = append(badSessions, fmt.Sprintf("%s round %d session %d did not exceed the previous %d", tok, i, s[i], s[i-1]))
				break
			}
		}
	}
	sort.Strings(badSessions)
	if len(badSessions) == 0 {
		invs = append(invs, Invariant{Name: InvPresenceSessionsMonotonic, Passed: true,
			Detail: fmt.Sprintf("all %d churn device(s) climbed strictly across %d reconnect(s) (single-broker-node assumption)", len(churn), cfg.ChurnRounds)})
	} else {
		invs = append(invs, Invariant{Name: InvPresenceSessionsMonotonic, Passed: false,
			Detail: fmt.Sprintf("%d churn session-ordering violation(s): %s", len(badSessions), strings.Join(badSessions, "; "))})
	}

	// 6. The reconciler's own enumeration, EXACTLY. Not a superset, not a sample: this
	// is the read a reconciler uses to decide who to repair, and one that under-reads
	// marks live devices offline while one that over-reads probes devices that left.
	switch {
	case obs.AssertedReadErr != "":
		invs = append(invs, Invariant{Name: InvPresenceReconcilerExact, Passed: false,
			Detail: "the paged assertedDeviceStates walk did not complete: " + obs.AssertedReadErr})
	default:
		want := make(map[string]bool, len(steady)+len(churn))
		for _, t := range append(append([]string{}, steady...), churn...) {
			want[t] = true
		}
		got := make(map[string]bool, len(obs.AssertedActive))
		var duplicates []string
		for _, t := range obs.AssertedActive {
			if got[t] {
				duplicates = append(duplicates, t)
			}
			got[t] = true
		}
		var missing, extra []string
		for t := range want {
			if !got[t] {
				missing = append(missing, t)
			}
		}
		for t := range got {
			if !want[t] {
				extra = append(extra, t)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		sort.Strings(duplicates)
		if len(missing) == 0 && len(extra) == 0 && len(duplicates) == 0 {
			invs = append(invs, Invariant{Name: InvPresenceReconcilerExact, Passed: true,
				Detail: fmt.Sprintf("the paged walk enumerated exactly the %d asserted-online device(s) across %d page(s)", len(want), obs.Pages)})
		} else {
			invs = append(invs, Invariant{Name: InvPresenceReconcilerExact, Passed: false,
				Detail: fmt.Sprintf("the paged walk (%d row(s), %d page(s)) did not equal the %d asserted-online device(s) — missing: [%s]; unexpected: [%s]; duplicated: [%s]",
					len(obs.AssertedActive), obs.Pages, len(want), strings.Join(missing, ", "), strings.Join(extra, ", "), strings.Join(duplicates, ", "))})
		}
	}

	return invs
}

// --- the presence oracle (reads over the real tenant API) ----------------------

// queryDeviceStates reads the live presence projection for a set of devices. It is
// the same tenant-scoped surface a console or an SDK reads — the fidelity rule: this
// harness has no database reach and no admin plane.
const queryDeviceStates = `query($tokens:[String!]!){
  deviceStatesByDeviceToken(deviceTokens:$tokens){
    deviceToken
    active
    presenceSource
    sessionId
    source
  }
}`

// queryAssertedStates is the RECONCILER'S OWN enumeration, read exactly as a
// reconciler reads it: source-scoped, cursor-paged, walked to a short page.
const queryAssertedStates = `query($source:String!,$activeOnly:Boolean!,$afterId:ID,$pageSize:Int!){
  assertedDeviceStates(source:$source,activeOnly:$activeOnly,afterId:$afterId,pageSize:$pageSize){
    id
    deviceToken
    active
    presenceSource
  }
}`

// presenceOracle reads device-state over the tenant GraphQL API.
type presenceOracle struct {
	session  sessionQuerier
	endpoint string
}

// states reads one observation per requested token. A token with no row comes back
// as a zero-value observation with Present false — the read does not fail, because
// "this device has no state row" is an ANSWER (and, before any device connects, the
// expected one), not an error.
func (o *presenceOracle) states(ctx context.Context, tokens []string) (map[string]deviceStateObs, error) {
	var out struct {
		DeviceStatesByDeviceToken []struct {
			DeviceToken    string `json:"deviceToken"`
			Active         bool   `json:"active"`
			PresenceSource string `json:"presenceSource"`
			SessionID      string `json:"sessionId"`
			Source         string `json:"source"`
		} `json:"deviceStatesByDeviceToken"`
	}
	if err := o.session.Query(ctx, o.endpoint, queryDeviceStates, map[string]any{"tokens": tokens}, &out); err != nil {
		return nil, fmt.Errorf("deviceStatesByDeviceToken: %w", err)
	}
	obs := make(map[string]deviceStateObs, len(tokens))
	for _, t := range tokens {
		obs[t] = deviceStateObs{}
	}
	for _, r := range out.DeviceStatesByDeviceToken {
		// 🔴 sessionId is a String in the schema because it is a UnixNano that overflows
		// a 32-bit GraphQL Int. Parse it; comparing it as TEXT would order "9" above
		// "10" and turn a perfectly ordered fleet into a monotonicity finding.
		session, perr := strconv.ParseInt(r.SessionID, 10, 64)
		if perr != nil {
			return nil, fmt.Errorf("device %q returned a sessionId %q that is not an integer: %w", r.DeviceToken, r.SessionID, perr)
		}
		obs[r.DeviceToken] = deviceStateObs{
			Present:        true,
			Active:         r.Active,
			PresenceSource: r.PresenceSource,
			SessionID:      session,
			Source:         r.Source,
		}
	}
	return obs, nil
}

// assertedActiveTokens walks assertedDeviceStates to exhaustion the way the contract
// documents: from no cursor, then from the last row's id, until a page comes back
// shorter than pageSize. It returns the tokens in the order the pages served them,
// plus the number of requests it took — evidence that the cursor was exercised at all.
func (o *presenceOracle) assertedActiveTokens(ctx context.Context, source string, pageSize int) (tokens []string, pages int, err error) {
	var afterId *string
	for {
		vars := map[string]any{"source": source, "activeOnly": true, "pageSize": pageSize}
		if afterId != nil {
			vars["afterId"] = *afterId
		}
		var out struct {
			AssertedDeviceStates []struct {
				ID             string `json:"id"`
				DeviceToken    string `json:"deviceToken"`
				Active         bool   `json:"active"`
				PresenceSource string `json:"presenceSource"`
			} `json:"assertedDeviceStates"`
		}
		if qerr := o.session.Query(ctx, o.endpoint, queryAssertedStates, vars, &out); qerr != nil {
			return nil, pages, fmt.Errorf("assertedDeviceStates page %d: %w", pages+1, qerr)
		}
		pages++
		for _, r := range out.AssertedDeviceStates {
			// The query already filters to asserted-and-active, so a row that is neither
			// is the SURFACE misbehaving rather than a device to record. Fail closed: an
			// enumeration silently carrying rows it was asked to exclude is the class of
			// defect this invariant exists to find, and folding them into the token set
			// would report it as a membership mismatch with no hint of the real cause.
			if !r.Active || r.PresenceSource != presenceSourceAsserted {
				return nil, pages, fmt.Errorf("assertedDeviceStates returned %q with active=%v presenceSource=%q, which the query's own filters exclude",
					r.DeviceToken, r.Active, r.PresenceSource)
			}
			tokens = append(tokens, r.DeviceToken)
		}
		if len(out.AssertedDeviceStates) < pageSize {
			return tokens, pages, nil
		}
		last := out.AssertedDeviceStates[len(out.AssertedDeviceStates)-1].ID
		if afterId != nil && last == *afterId {
			return nil, pages, fmt.Errorf("assertedDeviceStates returned the same cursor id %q twice, so the walk is not advancing", last)
		}
		afterId = &last
		if pages >= presenceMaxPages {
			return nil, pages, fmt.Errorf("assertedDeviceStates never returned a short page in %d requests", pages)
		}
	}
}

// awaitStates polls until every token satisfies want, or the deadline passes. It
// returns the LAST observation set either way, plus whether the wait was satisfied —
// a timeout is an observation to classify, not an error to abort on.
func (o *presenceOracle) awaitStates(ctx context.Context, tokens []string, want func(deviceStateObs) bool, timeout, poll time.Duration) (map[string]deviceStateObs, bool) {
	deadline := time.Now().Add(timeout)
	var last map[string]deviceStateObs
	for {
		obs, err := o.states(ctx, tokens)
		if err != nil {
			log.Warn().Err(err).Msg("transient device-state read while awaiting a presence transition; retrying")
		} else {
			last = obs
			satisfied := true
			for _, t := range tokens {
				if !want(obs[t]) {
					satisfied = false
					break
				}
			}
			if satisfied {
				return last, true
			}
		}
		if time.Now().After(deadline) {
			return last, false
		}
		select {
		case <-ctx.Done():
			return last, false
		case <-time.After(poll):
		}
	}
}

// --- the steady watcher -------------------------------------------------------

// steadyWatch accumulates the CONTINUOUS steady-cohort observation. It keeps the
// FIRST DEVIATION per device and never lets a later reading overwrite it, because the
// false-death class it exists to catch is transient by nature: a device that dropped
// offline and came back looks perfect at the end and has already fired every offline
// alarm downstream. First rather than last is deliberate — the first deviation is the
// one nearest whatever caused it, and a device that flapped repeatedly would otherwise
// report only its final flap, which is the least informative of them.
type steadyWatch struct {
	mu         sync.Mutex
	cleanPolls int
	readErrors int
	bad        map[string]string
}

func newSteadyWatch() *steadyWatch { return &steadyWatch{bad: map[string]string{}} }

func (w *steadyWatch) recordError() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.readErrors++
}

func (w *steadyWatch) record(obs map[string]deviceStateObs, tokens []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cleanPolls++
	for _, t := range tokens {
		if obs[t].assertedActive() {
			continue
		}
		if _, seen := w.bad[t]; !seen {
			w.bad[t] = obs[t].describe()
		}
	}
}

func (w *steadyWatch) snapshot() (clean, errs int, bad map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	bad = make(map[string]string, len(w.bad))
	for k, v := range w.bad {
		bad[k] = v
	}
	return w.cleanPolls, w.readErrors, bad
}

// watchSteady polls the steady cohort until ctx is cancelled.
func watchSteady(ctx context.Context, o *presenceOracle, tokens []string, poll time.Duration, w *steadyWatch) {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			obs, err := o.states(ctx, tokens)
			if err != nil {
				// A read that failed is not evidence the cohort was healthy. Counted apart,
				// and the classifier gates on the CLEAN count so a run whose reads all failed
				// cannot pass by having observed nothing bad.
				if ctx.Err() == nil {
					w.recordError()
				}
				continue
			}
			w.record(obs, tokens)
		}
	}
}

// --- report -------------------------------------------------------------------

// PresenceReport is the machine- and human-readable result of one presence run.
type PresenceReport struct {
	Seed        int64       `json:"seed"`
	Tenant      string      `json:"tenant"`
	StartedAt   time.Time   `json:"startedAt"`
	FinishedAt  time.Time   `json:"finishedAt"`
	Drive       DriveStats  `json:"drive"`
	Disposition Disposition `json:"disposition"`
	// Reason explains the disposition in one sentence — and for INCONCLUSIVE it is the
	// whole point of the field: an operator reading a non-zero exit needs to know
	// whether to hunt a defect or fix a cluster.
	Reason string `json:"reason"`

	// TapLive and PresenceSourceId record the pre-load precondition: broker-asserted
	// presence was running, under this source id. The source is READ from a connected
	// device's own row rather than assumed, because the gateway source id is
	// operator-chosen and an oracle that hardcoded the shipped default would quietly
	// enumerate an empty set on any instance that renamed it.
	TapLive          bool   `json:"tapLive"`
	PresenceSourceId string `json:"presenceSourceId,omitempty"`

	SteadyDevices   int `json:"steadyDevices"`
	ChurnDevices    int `json:"churnDevices"`
	DepartedDevices int `json:"departedDevices"`
	ChurnRounds     int `json:"churnRounds"`

	CleanSteadyPolls  int               `json:"cleanSteadyPolls"`
	SteadyReadErrors  int               `json:"steadyReadErrors"`
	ChurnSessions     map[string]string `json:"churnSessions"`
	ChurnFlipTimeouts map[string]int    `json:"churnFlipTimeouts,omitempty"`
	AssertedPages     int               `json:"assertedReadPages"`
	AssertedCount     int               `json:"assertedReadRows"`

	// Control is the negative control that was run, if any, and whether it behaved.
	Control          string `json:"control,omitempty"`
	ControlSatisfied bool   `json:"controlSatisfied,omitempty"`
	ControlDetail    string `json:"controlDetail,omitempty"`

	Invariants []Invariant `json:"invariants"`
}

// Passed is true only for a plain PASS. An INCONCLUSIVE run has NOT passed — it has
// no opinion — and callers that only ask this question get the fail-closed answer.
func (r *PresenceReport) Passed() bool { return r.Disposition == DispositionPass }

// ExitCode is the gate. THREE codes, because there are three dispositions and
// collapsing the third into either of the others is how a gate reports a cluster it
// could not measure in as a platform defect (or, worse, as a green).
//
//	0 — PASS
//	1 — FAIL: a presence invariant was violated (or, in a control run, the control
//	    did not flip exactly the set it must)
//	2 — INCONCLUSIVE: this run could not measure presence. Not a verdict.
func (r *PresenceReport) ExitCode() int {
	switch r.Disposition {
	case DispositionPass:
		return 0
	case DispositionFail:
		return 1
	default:
		return 2
	}
}

// finish folds the run's observations into the report's verdict: the disposition,
// the reason, and — for a control run — whether the control behaved.
//
// It exists as a method rather than as the tail of the orchestration because the
// orchestration needs a cluster and this decision does not. Left inline, the two
// judgements that matter most here would be reachable only by a manual gate: that a
// tap-off run stays INCONCLUSIVE, and that a CONTROL run's verdict is about the
// CONTROL rather than about the platform.
//
// 🔴 A control run inverts the meaning of its own invariants. They are EXPECTED to
// fail, so leaving the disposition as FAIL would make a working control
// indistinguishable from a broken platform — every green control would read as a
// release-blocking finding. The one thing a control cannot rescue is an INCONCLUSIVE
// run: a perturbation nobody could observe proves nothing about the oracle either,
// so that disposition stands and the control is not even evaluated.
func (r *PresenceReport) finish(shed int64, invs []Invariant, control string) {
	r.Invariants = invs
	r.Control = control
	r.Disposition, r.Reason = decideDisposition(r.TapLive, shed, invs)
	if control == "" || r.Disposition == DispositionInconclusive {
		return
	}
	r.ControlSatisfied, r.ControlDetail = evaluateControl(control, invs)
	if r.ControlSatisfied {
		r.Disposition, r.Reason = DispositionPass, r.ControlDetail
	} else {
		r.Disposition, r.Reason = DispositionFail, r.ControlDetail
	}
}

// JSON renders the report for a CI artifact.
func (r *PresenceReport) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// Human renders a terse operator summary. The exit-code legend is printed with it,
// on purpose: a report whose headline and whose exit code have to be reconciled by
// the reader is how "rows did NOT survive" ends up printed four lines above the
// legend saying the drill could not run at all.
func (r *PresenceReport) Human() string {
	var b strings.Builder
	fmt.Fprintf(&b, "presence %s (seed %d, tenant %s) — exit %d [0=pass 1=presence defect 2=could not measure]\n",
		r.Disposition, r.Seed, r.Tenant, r.ExitCode())
	fmt.Fprintf(&b, "  %s\n", r.Reason)
	fmt.Fprintf(&b, "  tap: live=%v source=%q\n", r.TapLive, r.PresenceSourceId)
	fmt.Fprintf(&b, "  drive: %d bg devices, achieved %.1f ev/s over %.0fs — accepted %d, shed %d, failed %d\n",
		r.Drive.Devices, r.Drive.AchievedRatePS, r.Drive.HoldSeconds, r.Drive.Accepted, r.Drive.Shed, r.Drive.Failed)
	fmt.Fprintf(&b, "  cohorts: %d steady (%d clean poll(s), %d read error(s)), %d churn × %d round(s), %d departed; asserted walk %d row(s) over %d page(s)\n",
		r.SteadyDevices, r.CleanSteadyPolls, r.SteadyReadErrors, r.ChurnDevices, r.ChurnRounds, r.DepartedDevices, r.AssertedCount, r.AssertedPages)
	if r.Control != "" {
		mark := "VIOLATED"
		if r.ControlSatisfied {
			mark = "satisfied"
		}
		fmt.Fprintf(&b, "  control %s: %s — %s\n", r.Control, mark, r.ControlDetail)
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

// --- orchestration ------------------------------------------------------------

// RunPresence runs the L4 broker-asserted presence harness end to end.
//
// The ORDER below is the design, not an implementation detail:
//
//  1. the tap precondition runs on an IDLE tenant, BEFORE any load. A tap-off cluster
//     produces no state row at all for a device that publishes nothing, so this check
//     cannot be "is a connected device INFERRED" — that row does not exist to be read.
//     It is "does a connected device have an ASSERTED row at all", and it has to run
//     before contention, or tap LAG under load reads as tap OFF.
//  2. every cohort device is driven to asserted-online and that baseline is RECORDED,
//     because the departed cohort's whole verdict is the difference between then and now.
//  3. only then does the background load start.
//
// A returned error means the harness could not complete a run at all; the caller
// reports that as INCONCLUSIVE rather than as a presence verdict, for the same reason
// the disposition has three values.
func RunPresence(ctx context.Context, hs *sim.Handshake, cfg PresenceConfig) (*PresenceReport, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(hs.Endpoints.DeviceStateGraphQL) == "" {
		return nil, fmt.Errorf("handshake has no endpoints.deviceStateGraphQL — the presence harness reads the live projection over device-state's tenant API and cannot run without it; re-create the sim record with a current dcctl")
	}
	eventEndpoint, err := httpGraphQLFromWS(hs.Endpoints.EventMgmtWS)
	if err != nil {
		return nil, err
	}

	manifest := cfg.harnessManifest()
	rt, err := sim.NewRuntime(hs, cfg.load(), sim.DeviceCount(manifest))
	if err != nil {
		return nil, err
	}
	oracle := &presenceOracle{session: rt.Session, endpoint: rt.Endpoints.DeviceStateGraphQL}

	// Two preconditions, and they cover different things. requireCleanTenant sees
	// EVENTS, which is what catches a competing producer; it is blind to device-state
	// rows, so it cannot see the residual this harness actually cares about.
	counter := &graphqlEventCounter{session: rt.Session, endpoint: eventEndpoint}
	if err := requireCleanTenant(ctx, counter); err != nil {
		return nil, err
	}

	if err := sim.Provision(ctx, rt, manifest); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}

	cohorts, err := partitionByPrefixes(rt.Devices,
		[]string{presSteadyTokenPrefix, presChurnTokenPrefix, presGoneTokenPrefix, presBgTokenPrefix})
	if err != nil {
		return nil, err
	}
	steady, churn, departed, background := cohorts[0], cohorts[1], cohorts[2], cohorts[3]
	if len(steady) == 0 || len(churn) == 0 || len(departed) == 0 {
		return nil, fmt.Errorf("harness provisioned %d steady + %d churn + %d departed device(s) — each cohort answers a different question and none is optional",
			len(steady), len(churn), len(departed))
	}
	steadyTokens, churnTokens, departedTokens := tokensOf(steady), tokensOf(churn), tokensOf(departed)
	cohortTokens := append(append(append([]string{}, steadyTokens...), churnTokens...), departedTokens...)

	// Residual-state guard. The exact-set invariant demands the asserted-active set
	// equal this run's cohorts and NOTHING else, so a leftover row from a crashed prior
	// run fails a healthy platform. Refuse rather than reconcile.
	pre, err := oracle.states(ctx, cohortTokens)
	if err != nil {
		return nil, fmt.Errorf("pre-connect device-state read: %w", err)
	}
	for _, tok := range cohortTokens {
		if pre[tok].Present {
			return nil, fmt.Errorf("cohort device %q already has a device-state row (%s) before it has connected — the presence harness needs a FRESH tenant, because its reconciler invariant asserts an EXACT set and residual rows fail it on a healthy platform",
				tok, pre[tok].describe())
		}
	}

	var mqttTLS *tls.Config
	if strings.HasPrefix(cfg.MqttBroker, "ssl://") || strings.HasPrefix(cfg.MqttBroker, "tls://") {
		// #nosec G402 — same opt-in as the command harness: a kind/dev gateway cert whose
		// SAN is the in-cluster DNS name cannot be matched by a 127.0.0.1 port-forward.
		mqttTLS = &tls.Config{InsecureSkipVerify: cfg.MqttTLSInsecure}
	}
	receiver := cmdreceiver.New(rt.InstanceId, rt.Tenant, cfg.MqttBroker, mqttTLS)
	defer receiver.Close()

	report := &PresenceReport{
		Seed:            cfg.Seed,
		Tenant:          hs.Tenant,
		StartedAt:       time.Now().UTC(),
		SteadyDevices:   len(steady),
		ChurnDevices:    len(churn),
		DepartedDevices: len(departed),
		ChurnRounds:     cfg.ChurnRounds,
	}

	// --- 1. the tap precondition, on an idle tenant --------------------------
	tapDevice := steady[0]
	if serr := receiver.Subscribe(ctx, tapDevice.Token, tapDevice.CredentialId); serr != nil {
		return nil, fmt.Errorf("connect MQTT receiver for the tap probe %q: %w (check the dc-nats:1883 port-forward)", tapDevice.Token, serr)
	}
	tapObs, tapOK := oracle.awaitStates(ctx, []string{tapDevice.Token},
		deviceStateObs.assertedActive, cfg.TapTimeout, cfg.Poll)
	if !tapOK {
		report.FinishedAt = time.Now().UTC()
		report.finish(0, nil, cfg.Control)
		log.Warn().Str("device", tapDevice.Token).Dur("waited", cfg.TapTimeout).
			Msg("the tap probe never reached an ASSERTED device-state row; the run is inconclusive")
		return report, nil
	}
	source := strings.TrimSpace(tapObs[tapDevice.Token].Source)
	if source == "" {
		report.FinishedAt = time.Now().UTC()
		report.Disposition = DispositionInconclusive
		report.Reason = fmt.Sprintf("the tap probe %q is asserted-online but its row carries no source id, and the reconciler's enumeration is source-scoped — there is nothing to enumerate it under", tapDevice.Token)
		return report, nil // deliberately NOT via finish: TapLive is true here, so finish would overwrite this with a verdict
	}
	report.TapLive = true
	report.PresenceSourceId = source
	log.Info().Str("device", tapDevice.Token).Str("source", source).
		Msg("broker-asserted presence is live; the source id is read from the device's own row, not assumed")

	// Foreign-state guard, now that a source id exists to ask under: with exactly one
	// device connected, the reconciler's enumeration must hold exactly that device.
	// Anything else is another producer on this tenant, which the exact-set invariant
	// cannot survive — and finding that out here is a refusal, not a finding.
	if enumerated, _, aerr := oracle.assertedActiveTokens(ctx, source, cfg.PageSize); aerr != nil {
		return nil, fmt.Errorf("pre-load assertedDeviceStates walk: %w", aerr)
	} else if len(enumerated) != 1 || enumerated[0] != tapDevice.Token {
		return nil, fmt.Errorf("before this run connected anything but its tap probe, assertedDeviceStates(source:%q) already enumerated %v — this tenant is not exclusively ours, and the reconciler invariant asserts an EXACT set", source, enumerated)
	}

	// --- 2. connect the rest, and RECORD the baseline ------------------------
	for _, d := range append(append(append([]sim.DeviceInstance{}, steady[1:]...), churn...), departed...) {
		if serr := receiver.Subscribe(ctx, d.Token, d.CredentialId); serr != nil {
			return nil, fmt.Errorf("connect MQTT receiver for %q: %w", d.Token, serr)
		}
	}
	baseline, _ := oracle.awaitStates(ctx, cohortTokens, deviceStateObs.assertedActive, cfg.FlipTimeout, cfg.Poll)
	obs := presenceObservations{
		ConnectBaseline:   baseline,
		ChurnSessions:     make(map[string][]int64, len(churnTokens)),
		ChurnFlipTimeouts: map[string]int{},
	}
	for _, t := range churnTokens {
		obs.ChurnSessions[t] = []int64{baseline[t].SessionID}
	}

	// --- 3. load, churn, departure -------------------------------------------
	rt.Devices = background
	driveStart := time.Now()
	rt.Stats.Reset(driveStart)
	runCtx, cancelDrive := context.WithCancel(ctx)
	var bgWg sync.WaitGroup
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		driveBackground(runCtx, rt, cfg.BackgroundInterval)
	}()

	watchCtx, cancelWatch := context.WithCancel(ctx)
	watch := newSteadyWatch()
	var watchWg sync.WaitGroup
	watchWg.Add(1)
	go func() {
		defer watchWg.Done()
		watchSteady(watchCtx, oracle, steadyTokens, cfg.Poll, watch)
	}()
	stop := func() {
		cancelDrive()
		bgWg.Wait()
		cancelWatch()
		watchWg.Wait()
	}

	// The departed cohort leaves once and stays gone. Its edge is not awaited here:
	// the final read after the tail hold and settle is the authoritative one, and
	// awaiting it now would only move the same observation earlier.
	for _, d := range departed {
		if derr := receiver.Disconnect(d.Token); derr != nil {
			stop()
			return nil, fmt.Errorf("disconnect departed device %q: %w", d.Token, derr)
		}
	}

	// The negative control drops one STEADY device — the cohort whose invariant is
	// "never goes offline". It fires here, before the churn rounds, so the advisory has
	// the whole remaining run plus the settle to propagate and be seen by more than one
	// poll: a control that lands too late to be observed fails a healthy oracle and
	// teaches exactly the wrong lesson.
	if cfg.Control == ControlDropSteadyDevice {
		victim := steady[len(steady)-1]
		if derr := receiver.Disconnect(victim.Token); derr != nil {
			stop()
			return nil, fmt.Errorf("control %q: disconnect steady device %q: %w", cfg.Control, victim.Token, derr)
		}
		log.Warn().Str("control", cfg.Control).Str("device", victim.Token).
			Msg("negative control armed: a steady device was disconnected on purpose")
	}

	for round := 1; round <= cfg.ChurnRounds; round++ {
		for _, d := range churn {
			if derr := receiver.Disconnect(d.Token); derr != nil {
				stop()
				return nil, fmt.Errorf("churn round %d: disconnect %q: %w", round, d.Token, derr)
			}
		}
		// 🔴 Wait for the PROJECTION to show the departure before reconnecting, rather
		// than trusting the client's own Disconnect. The MQTT client returns from a
		// disconnect before the broker has necessarily processed it, and it reports
		// itself not-connected the moment it starts closing — so nothing on this side
		// proves the advisory was ever emitted. The observable is the only witness, and
		// reconnecting without it would race a new session against the old one's
		// departure and read the result as a session that failed to climb.
		if _, ok := oracle.awaitStates(ctx, churnTokens, deviceStateObs.assertedOffline, cfg.FlipTimeout, cfg.Poll); !ok {
			obs.ChurnFlipTimeouts["offline"]++
			log.Warn().Int("round", round).Msg("churn devices did not read asserted-offline within the flip timeout")
		}
		for _, d := range churn {
			if serr := receiver.Subscribe(ctx, d.Token, d.CredentialId); serr != nil {
				stop()
				return nil, fmt.Errorf("churn round %d: reconnect %q: %w", round, d.Token, serr)
			}
		}
		back, ok := oracle.awaitStates(ctx, churnTokens, deviceStateObs.assertedActive, cfg.FlipTimeout, cfg.Poll)
		if !ok {
			obs.ChurnFlipTimeouts["online"]++
			log.Warn().Int("round", round).Msg("churn devices did not read asserted-online within the flip timeout")
		}
		for _, t := range churnTokens {
			obs.ChurnSessions[t] = append(obs.ChurnSessions[t], back[t].SessionID)
		}
		if ctx.Err() != nil {
			stop()
			return nil, fmt.Errorf("run aborted: %w", ctx.Err())
		}
	}

	// Tail hold: keep the steady cohort under observation past the last churn round,
	// so a departure this run caused has been seen by more than one poll before the
	// authoritative read.
	select {
	case <-ctx.Done():
	case <-time.After(cfg.TailHold):
	}

	cancelDrive()
	bgWg.Wait()
	driveEnd := time.Now()
	rt.Stats.Freeze(driveEnd)
	snap := rt.Stats.Snapshot(driveEnd)

	// Settle with the load off, still watching: a late transition — a departed
	// device's advisory that queued behind the load — lands here rather than being
	// read as lost. BOUNDED OBSERVATION: a transition beyond this window is outside
	// the gate's observation scope, which is why the shed check above matters so much.
	select {
	case <-ctx.Done():
	case <-time.After(cfg.Settle):
	}
	cancelWatch()
	watchWg.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("run aborted: %w", ctx.Err())
	}

	// --- 4. the authoritative reads ------------------------------------------
	obs.CleanSteadyPolls, obs.SteadyReadErrors, obs.SteadyBad = watch.snapshot()
	obs.DepartedFinal, _ = oracle.awaitStates(ctx, departedTokens, deviceStateObs.assertedOffline, cfg.Settle, cfg.Poll)
	enumerated, pages, aerr := oracle.assertedActiveTokens(ctx, source, cfg.PageSize)
	obs.AssertedActive, obs.Pages = enumerated, pages
	if aerr != nil {
		obs.AssertedReadErr = aerr.Error()
	}

	invs := classifyPresence(obs, steadyTokens, churnTokens, departedTokens, cfg, snap.Emitted)

	report.FinishedAt = time.Now().UTC()
	report.Drive = DriveStats{
		Devices:        len(background),
		TargetRatePS:   rt.Load.TargetRate(len(background)),
		AchievedRatePS: snap.Rate,
		Accepted:       snap.Emitted,
		Shed:           snap.Shed,
		Failed:         snap.Failed,
		Ticks:          snap.Ticks,
		HoldSeconds:    driveEnd.Sub(driveStart).Seconds(),
	}
	report.CleanSteadyPolls = obs.CleanSteadyPolls
	report.SteadyReadErrors = obs.SteadyReadErrors
	report.ChurnSessions = summarizeSessions(obs.ChurnSessions)
	if len(obs.ChurnFlipTimeouts) > 0 {
		report.ChurnFlipTimeouts = obs.ChurnFlipTimeouts
	}
	report.AssertedPages = obs.Pages
	report.AssertedCount = len(obs.AssertedActive)
	report.finish(snap.Shed, invs, cfg.Control)
	return report, nil
}

// summarizeSessions renders the per-device session readings for the report.
func summarizeSessions(sessions map[string][]int64) map[string]string {
	out := make(map[string]string, len(sessions))
	for tok, s := range sessions {
		parts := make([]string, len(s))
		for i, v := range s {
			parts[i] = strconv.FormatInt(v, 10)
		}
		out[tok] = strings.Join(parts, " → ")
	}
	return out
}
