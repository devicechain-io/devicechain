// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-simulator/loadtest/detmonitor"
	"github.com/devicechain-io/dc-simulator/sim"
	"github.com/rs/zerolog/log"
)

// L2d-1: planted detection→alarm probes (ADR-064, research/load-test-harness.md §4.2).
//
// Where L1 asserts whole-fleet completeness by counting and L2c asserts the
// fixture-free safety invariants, this layer plants a DEDICATED harness profile
// with a pinned threshold DetectionRule whose raiseAlarm action drives a durable
// alarm, then drives a saturating background fleet THROUGH THE SAME RULE so the
// detection + alarm path is under real contention while two kinds of probe assert:
//
//   - no-spurious-alarm (safety): a probe that emits strictly BELOW the threshold,
//     forever, must never raise its alarm. A spurious raise writes a DURABLE alarm
//     row on that device — which a query WILL see — so this is the false-detection
//     class a passive count oracle cannot catch, closed authoritatively.
//   - alarm-liveness (liveness): a probe that crosses the threshold up-and-down must
//     drive its alarm through the full ACTIVE→CLEARED lifecycle — the durable row
//     ends CLEARED and carries the rule's real TIER (MAJOR). A dropped rising edge
//     whose resolve still lands does NOT leave "no alarm" — it leaves a CLEARED
//     TOMBSTONE stamped INDETERMINATE (the consumer creates a row on a resolve-only
//     edge with raisedTime set), so liveness keys on the tier, not the presence of a
//     raisedTime (which every row has); a dropped falling edge leaves it stuck ACTIVE
//     — both fail. The edge probe is the liveness WITNESS: a dead detection/alarm
//     path leaves the durable alarm absent or tombstoned, which fails, so silence
//     never passes.
//
// THE ORACLE IS DURABLE STATE, NOT THE LIVE STREAM. detectionStream is a live NATS
// tap that SILENTLY DROPS under load (a slow-consumer bridge, no error frame), so it
// cannot be the authority for a correctness gate — a spurious raise or a dropped
// edge could vanish in the bridge and the gate would never know (the L1 lesson: the
// live subscription is not authoritative truth). The platform persists no per-edge
// detection history, but the ALARM is the durable, per-device integrator of those
// edges (ADR-057) — so the gate reconciles against the durable alarm row via the
// same tenant GraphQL a real client uses (the fidelity rule). detectionStream is
// still subscribed, but ONLY as a non-gating live-latency signal reported as
// evidence, explicitly lossy.
//
// The DETECT `threshold` kind is edge-triggered (engine.go's emit latches per
// series: first matching event raises, idempotent while raised, first metric-present
// non-matching event resolves). Its raiseAlarm action adds/removes a contributor on
// the durable alarm (ADR-057): a rising edge → ACTIVE, a falling edge → CLEARED, in
// place. Probes emit at a LOW FIXED cadence so a lost alarm transition is a real
// loss, never merely shed governed traffic; the background fleet is the load.

// Harness topology — a DEDICATED profile, NOT a reference demo scenario (the pinned
// rule and the crafted probe values are test fixtures, not a realistic scenario, so
// they live here in the harness rather than in the sim's demo Registry).
const (
	// HarnessProfileToken is the pinned profile the detection rule and every probe
	// hang off. detectionStream is subscribed by profile.
	HarnessProfileToken = "harness-profile"
	// HarnessDeviceTypeToken is the device type every probe + background device
	// instantiates.
	HarnessDeviceTypeToken = "harness-device"
	// HarnessMetricKey is the single numeric metric the rule compares and every
	// probe emits. It MUST equal the rule's `when.metric` so the CEL guard
	// (`"temp" in m && m["temp"] > threshold`) matches the emitted measurement.
	HarnessMetricKey = "temp"
	// HarnessRuleToken is the authored threshold rule's token.
	HarnessRuleToken = "harness-overtemp"
	// HarnessAlarmKey is the (originator, key) the rule's raiseAlarm action keys its
	// durable alarm on. Per-device: each probe/background device gets its own alarm
	// row keyed (device, HarnessAlarmKey), so a per-device query is exact.
	HarnessAlarmKey = "harness-overtemp"
	// HarnessSeverity is the rule severity the raiseAlarm action raises at (the
	// authoring vocabulary, lowercase). A raiseAlarm action requires a valid
	// non-empty tier (event-processing validateReact); any ADR-041 tier works —
	// major is a middle default.
	HarnessSeverity = "major"
	// harnessAlarmSeverityWire is the tier as it lands on the DURABLE alarm row: the
	// raise-alarm consumer uppercases the rule's authoring severity
	// (raise_alarm_consumer.go strings.ToUpper), so "major" is stored "MAJOR". This
	// is load-bearing: a real raise stamps this tier and the clear leaves it intact,
	// whereas a resolve-only TOMBSTONE row (a raise dropped while its resolve landed)
	// is stamped INDETERMINATE — so the tier is the only durable evidence the alarm
	// ever went genuinely ACTIVE. Mirrored here as a literal since the sim speaks the
	// wire; kept in lockstep with HarnessSeverity via ToUpper.
	harnessAlarmSeverityWire = "MAJOR"

	// harnessThreshold is the rule's `> threshold` bound. Probe values straddle it
	// with margin; nothing is emitted AT the threshold (gt is strict, so an exact
	// value would be a non-match — a subtle way to make a "crossing" not cross).
	harnessThreshold  = 30.0
	harnessAboveValue = 45.0 // strictly above → a raised edge (alarm ACTIVE)
	harnessBelowValue = 15.0 // strictly below → a resolved edge (alarm CLEARED, or no-op if not raised)

	// Distinct, non-overlapping token prefixes so a rendered device partitions
	// unambiguously into its role. Zero-padded widths keep tokens sortable and are
	// well under core.MaxTokenLen.
	safetyTokenPattern = "harness-safe-{n:03d}"
	edgeTokenPattern   = "harness-edge-{n:03d}"
	bgTokenPattern     = "harness-bg-{n:05d}"
	safetyTokenPrefix  = "harness-safe-"
	edgeTokenPrefix    = "harness-edge-"
	bgTokenPrefix      = "harness-bg-"

	// Durable alarm state wire values (device-management schema: Alarm.state is a
	// String "ACTIVE"|"CLEARED"). Mirrored here as literals — the sim speaks only the
	// wire — like emit.go mirrors the credential/event-type strings.
	alarmStateActive  = "ACTIVE"
	alarmStateCleared = "CLEARED"
)

// Detection-harness defaults.
const (
	DefaultBackgroundDevices  = 50
	DefaultSafetyProbes       = 3
	DefaultEdgeProbes         = 3
	DefaultCycles             = 5
	DefaultProbeInterval      = 1 * time.Second
	DefaultBackgroundInterval = 200 * time.Millisecond
	// DefaultDetectionMinAccepted is the background load floor. Like L1's floor, a
	// run that applied less than this never stressed the detection/alarm path, so its
	// no-spurious verdict would be vacuous — the run fails rather than certifies.
	DefaultDetectionMinAccepted = 1000
	DefaultAlarmPoll            = 2 * time.Second
	// DefaultAlarmTimeout backstops the wait for the edge probes' alarms to reach
	// their settled CLEARED state through ingest→resolve→detect→raiseAlarm→projection
	// under load. A timeout with an alarm not yet raised-and-cleared is a real finding
	// (a dropped transition), turned into a failed liveness invariant — never a silent
	// pass (the `reached` gate).
	DefaultAlarmTimeout = 120 * time.Second
	// DefaultAlarmSettle holds after the edge alarms reach CLEARED, so a LATE spurious
	// raise on a SAFETY probe (arriving just after the edges settle) is still in the
	// durable row when the final snapshot is taken.
	DefaultAlarmSettle = 5 * time.Second
	// subscribeSettle lets the detectionStream SUBSCRIBE frame attach before the first
	// probe edge. Since the live stream is NON-gating evidence here, an imperfect
	// attach only costs a little evidence completeness — never the verdict — so this
	// is a modest best-effort, not a correctness guard.
	subscribeSettle = 1 * time.Second
)

// DetectionConfig configures one detection→alarm probe run. The probe topology
// (safety/edge counts, cycles) and the background load are separate knobs: the
// probes are the oracle (fixed, low cadence), the background is the contention.
type DetectionConfig struct {
	Seed int64

	// BackgroundDevices sizes the saturating fleet; BackgroundInterval is its tick
	// cadence; Concurrency bounds in-flight background emits.
	BackgroundDevices  int
	BackgroundInterval time.Duration
	Concurrency        int

	// SafetyProbes / EdgeProbes are how many of each probe device to plant; Cycles
	// is K, the number of threshold crossings each edge probe performs; ProbeInterval
	// is the (low, fixed) probe emit cadence. More edge probes = more independent
	// durable liveness witnesses (the durable alarm records lifecycle completion, not
	// a per-crossing count — so breadth of probes, not depth of cycles, is coverage).
	SafetyProbes  int
	EdgeProbes    int
	Cycles        int
	ProbeInterval time.Duration

	// MinAccepted is the background load floor for the verdict to count.
	MinAccepted int64

	// AlarmPoll/AlarmTimeout tune the wait for the durable alarm state to settle;
	// AlarmSettle holds past the edge target so a late spurious safety raise lands.
	AlarmPoll    time.Duration
	AlarmTimeout time.Duration
	AlarmSettle  time.Duration
}

func (c DetectionConfig) withDefaults() DetectionConfig {
	if c.BackgroundDevices <= 0 {
		c.BackgroundDevices = DefaultBackgroundDevices
	}
	if c.BackgroundInterval <= 0 {
		c.BackgroundInterval = DefaultBackgroundInterval
	}
	if c.SafetyProbes <= 0 {
		c.SafetyProbes = DefaultSafetyProbes
	}
	if c.EdgeProbes <= 0 {
		c.EdgeProbes = DefaultEdgeProbes
	}
	if c.Cycles <= 0 {
		c.Cycles = DefaultCycles
	}
	if c.ProbeInterval <= 0 {
		c.ProbeInterval = DefaultProbeInterval
	}
	if c.MinAccepted <= 0 {
		c.MinAccepted = DefaultDetectionMinAccepted
	}
	if c.AlarmPoll <= 0 {
		c.AlarmPoll = DefaultAlarmPoll
	}
	if c.AlarmTimeout <= 0 {
		c.AlarmTimeout = DefaultAlarmTimeout
	}
	if c.AlarmSettle <= 0 {
		c.AlarmSettle = DefaultAlarmSettle
	}
	return c
}

// Validate rejects a config that cannot run as asked, rather than silently
// substituting — the same house rule as Profile.Validate.
func (c DetectionConfig) Validate() error {
	if c.BackgroundDevices < 0 || c.SafetyProbes < 0 || c.EdgeProbes < 0 {
		return fmt.Errorf("device counts must not be negative")
	}
	if c.EdgeProbes == 0 {
		return fmt.Errorf("at least one edge probe is required — it is the alarm path's liveness witness")
	}
	if c.SafetyProbes == 0 {
		return fmt.Errorf("at least one safety probe is required — it is the no-spurious-alarm oracle")
	}
	if c.Cycles < 1 {
		return fmt.Errorf("cycles (K) must be at least 1")
	}
	if c.MinAccepted < 0 {
		return fmt.Errorf("min accepted must not be negative")
	}
	return nil
}

func (c DetectionConfig) load() sim.Load {
	return sim.Load{
		DeviceCount:  0, // the manifest's populations are sized directly, not resized
		EmitInterval: c.BackgroundInterval,
		Concurrency:  c.Concurrency,
	}
}

// harnessManifest builds the dedicated harness topology: one profile with the temp
// metric, one device type, and three populations (safety probes, edge probes,
// background fleet). It is pure — (config, seed) fully determines it — so Provision
// is idempotent and the tokens are reproducible.
func (c DetectionConfig) harnessManifest() sim.SimManifest {
	return sim.SimManifest{
		Name: "harness-detection",
		Seed: c.Seed,
		Profiles: []sim.ProfileSpec{
			{
				Token:    HarnessProfileToken,
				Name:     "Load-test Harness Profile",
				Category: "sensor",
				Metrics: []sim.MetricSpec{
					{Key: HarnessMetricKey, Name: "Temperature", DataType: "DOUBLE", Unit: "C"},
				},
			},
		},
		DeviceTypes: []sim.DeviceTypeSpec{
			{Token: HarnessDeviceTypeToken, Name: "Load-test Harness Device", ProfileToken: HarnessProfileToken},
		},
		Populations: []sim.PopulationSpec{
			{OfType: HarnessDeviceTypeToken, Count: c.SafetyProbes, TokenPattern: safetyTokenPattern, ExternalIdPattern: "HARNESS-SAFE-{n:03d}"},
			{OfType: HarnessDeviceTypeToken, Count: c.EdgeProbes, TokenPattern: edgeTokenPattern, ExternalIdPattern: "HARNESS-EDGE-{n:03d}"},
			{OfType: HarnessDeviceTypeToken, Count: c.BackgroundDevices, TokenPattern: bgTokenPattern, ExternalIdPattern: "HARNESS-BG-{n:05d}"},
		},
	}
}

// ruleDefinitionJSON builds the opaque rules.Rule JSON string the detection rule is
// authored with: a threshold rule whose leaf compares the temp metric against the
// harness threshold and whose raiseAlarm action drives the durable alarm the oracle
// reconciles against. The keys are exactly the rules.Rule / Condition / Action json
// tags — event-processing decodes with DisallowUnknownFields, so a stray key would
// be rejected at publish. A raiseAlarm action requires a non-empty rule severity.
func (c DetectionConfig) ruleDefinitionJSON() (string, error) {
	threshold := harnessThreshold
	def := map[string]any{
		"name":     "Load-test harness over-temp",
		"type":     "threshold",
		"severity": HarnessSeverity,
		"when": map[string]any{
			"metric":    HarnessMetricKey,
			"op":        "gt",
			"threshold": threshold,
		},
		"actions": []any{
			map[string]any{
				"type":       "raiseAlarm",
				"raiseAlarm": map[string]any{"alarmKey": HarnessAlarmKey},
			},
		},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal rule definition: %w", err)
	}
	return string(raw), nil
}

// --- detection rule authoring -------------------------------------------------

const queryDetectionRulesByToken = `query($tokens:[String!]!){detectionRulesByToken(tokens:$tokens){token}}`

const mutationCreateDetectionRule = `mutation($request:DetectionRuleCreateRequest!){createDetectionRule(request:$request){token}}`

const mutationPublishHarnessProfile = `mutation($token:String!){publishDeviceProfile(token:$token){version}}`

// ensureDetectionRule authors the pinned threshold+raiseAlarm rule on the harness
// profile and publishes it into the active version so the DETECT engine loads it.
//
// It REFUSES to run if the rule already exists: a pre-existing rule means the tenant
// is not fresh, which — combined with the durable alarm oracle — risks residual
// state (a stale detection latch swallowing this run's first raise, or a leftover
// alarm row) that would produce a false verdict. The harness is a fresh-tenant tool
// (dcctl sim create mints one per sim); a re-run destroys + recreates the sim. This
// is stricter than the create-or-get the sim's own Provision uses, deliberately.
func ensureDetectionRule(ctx context.Context, rt *sim.Runtime, cfg DetectionConfig) error {
	var existing struct {
		DetectionRulesByToken []struct {
			Token string `json:"token"`
		} `json:"detectionRulesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryDetectionRulesByToken,
		map[string]any{"tokens": []string{HarnessRuleToken}}, &existing); err != nil {
		return fmt.Errorf("detectionRulesByToken: %w", err)
	}
	if len(existing.DetectionRulesByToken) > 0 {
		return fmt.Errorf("harness detection rule %q already exists on this tenant — the detection harness requires a FRESH tenant (destroy + recreate the sim) so residual detection latches / alarm rows cannot skew the durable verdict", HarnessRuleToken)
	}

	def, err := cfg.ruleDefinitionJSON()
	if err != nil {
		return err
	}
	req := map[string]any{
		"token":              HarnessRuleToken,
		"deviceProfileToken": HarnessProfileToken,
		"name":               "Load-test harness over-temp",
		"definition":         def,
		"enabled":            true,
	}
	var created struct {
		CreateDetectionRule struct {
			Token string `json:"token"`
		} `json:"createDetectionRule"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateDetectionRule,
		map[string]any{"request": req}, &created); err != nil {
		return fmt.Errorf("createDetectionRule: %w", err)
	}

	// Publish so the draft rule freezes into the active version and event-processing
	// compiles + loads it (a created-but-unpublished rule never fires).
	var published struct {
		PublishDeviceProfile struct {
			Version int `json:"version"`
		} `json:"publishDeviceProfile"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationPublishHarnessProfile,
		map[string]any{"token": HarnessProfileToken}, &published); err != nil {
		return fmt.Errorf("publishDeviceProfile: %w", err)
	}
	log.Info().Str("rule", HarnessRuleToken).Int("profileVersion", published.PublishDeviceProfile.Version).
		Msg("authored + published harness detection rule (threshold + raiseAlarm)")
	return nil
}

// --- durable alarm oracle -----------------------------------------------------

// alarmSnapshot is the durable state of one device's harness alarm, read over the
// tenant alarm query — the authoritative truth the gate reconciles against.
type alarmSnapshot struct {
	// Found is whether ANY alarm row exists for (device, HarnessAlarmKey). A raise
	// writes a genuine (ACTIVE, tiered) row; a resolve-only edge writes a TOMBSTONE
	// (CLEARED, INDETERMINATE) row — so Found alone does NOT mean the alarm went
	// active. no-spurious keys on Found (any row is a raise OR a tombstone, and a
	// below-forever safety probe produces neither, so Found there is genuinely a
	// spurious raise); liveness additionally requires the tier (see raisedAndCleared).
	Found     bool
	State     string // "ACTIVE" | "CLEARED" | "" when not found
	Severity  string // durable tier: "MAJOR" for a real raise, "INDETERMINATE" for a tombstone
	ClearedAt string // clearedTime, used to detect a settled (non-oscillating) CLEARED state
}

// wentActive reports whether the durable row carries the rule's real tier, which the
// raise-alarm consumer stamps ONLY on a genuine rising edge (createAlarmFromContributors
// / the reactivation branch) and the clear branch leaves intact. A resolve-only tombstone
// is stamped INDETERMINATE, so a MAJOR tier is durable proof the alarm truly went ACTIVE —
// the discriminator a bare raisedTime (set on every row, including tombstones) cannot make.
func (s alarmSnapshot) wentActive() bool {
	return s.Found && s.Severity == harnessAlarmSeverityWire
}

// raisedAndCleared reports the settled durable lifecycle an edge probe must reach:
// the alarm genuinely went ACTIVE (a MAJOR tier, not a tombstone) and is now back to
// CLEARED. A dropped rising edge (whose resolve still lands) leaves a CLEARED
// TOMBSTONE with INDETERMINATE tier — which fails wentActive, so it is NOT mistaken
// for a completed lifecycle; a dropped falling edge leaves State=ACTIVE — also fails.
func (s alarmSnapshot) raisedAndCleared() bool {
	return s.wentActive() && s.State == alarmStateCleared
}

const queryHarnessAlarm = `query($c: AlarmSearchCriteria!) {
  alarms(criteria: $c) {
    results { state raisedTime clearedTime severity }
    pagination { totalRecords }
  }
}`

// alarmOracle reads a device's durable harness-alarm state over the real
// tenant-scoped device-management GraphQL API (the fidelity rule — no backdoor).
type alarmOracle struct {
	session  sessionQuerier
	endpoint string
}

// sessionQuerier is the tenant-session Query surface the oracle needs; it matches
// *userclient.TenantSession's Query method, and lets tests substitute a fake.
type sessionQuerier interface {
	Query(ctx context.Context, endpoint, query string, vars map[string]any, out any) error
}

// snapshot reads the durable alarm state for one device. It filters by
// (originatorType=device, originator=token, alarmKey) so the result is exactly that
// device's harness alarm — at most one row (the per-(originator,key) unique index).
func (o *alarmOracle) snapshot(ctx context.Context, deviceToken string) (alarmSnapshot, error) {
	vars := map[string]any{
		"c": map[string]any{
			"pageNumber":     1,
			"pageSize":       1,
			"originatorType": "device",
			"originator":     deviceToken,
			"alarmKey":       HarnessAlarmKey,
		},
	}
	var out struct {
		Alarms struct {
			Results []struct {
				State       string  `json:"state"`
				RaisedTime  *string `json:"raisedTime"`
				ClearedTime *string `json:"clearedTime"`
				Severity    string  `json:"severity"`
			} `json:"results"`
			Pagination struct {
				TotalRecords *int64 `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"alarms"`
	}
	if err := o.session.Query(ctx, o.endpoint, queryHarnessAlarm, vars, &out); err != nil {
		return alarmSnapshot{}, fmt.Errorf("query alarm for %s: %w", deviceToken, err)
	}
	// A null totalRecords is a fail-closed error, not a silent "no alarm" that a
	// no-spurious check would read as a clean pass.
	if out.Alarms.Pagination.TotalRecords == nil {
		return alarmSnapshot{}, fmt.Errorf("query alarm for %s: server returned a null totalRecords", deviceToken)
	}
	if len(out.Alarms.Results) == 0 {
		return alarmSnapshot{Found: false}, nil
	}
	r := out.Alarms.Results[0]
	clearedAt := ""
	if r.ClearedTime != nil {
		clearedAt = *r.ClearedTime
	}
	return alarmSnapshot{
		Found:     true,
		State:     r.State,
		Severity:  r.Severity,
		ClearedAt: clearedAt,
	}, nil
}

const queryDevicesExist = `query($tokens:[String!]!){devicesByToken(tokens:$tokens){token}}`

// requireDevicesExist confirms every token resolves to a real device. It backstops
// the no-spurious check: the alarm query returns an empty set for an unresolvable
// originator just as it does for a device that legitimately never raised, so an
// absent Found could otherwise be a silent "token didn't resolve" rather than a
// clean probe. On a fresh harness tenant these always resolve (Provision created
// them); the check makes the failure mode explicit rather than a vacuous pass.
func requireDevicesExist(ctx context.Context, rt *sim.Runtime, tokens []string) error {
	var out struct {
		DevicesByToken []struct {
			Token string `json:"token"`
		} `json:"devicesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryDevicesExist,
		map[string]any{"tokens": tokens}, &out); err != nil {
		return fmt.Errorf("devicesByToken: %w", err)
	}
	found := make(map[string]bool, len(out.DevicesByToken))
	for _, d := range out.DevicesByToken {
		found[d.Token] = true
	}
	for _, tok := range tokens {
		if !found[tok] {
			return fmt.Errorf("device %q does not resolve — a no-spurious 'no alarm' for it would be a vacuous pass", tok)
		}
	}
	return nil
}

// snapshotAll reads the durable alarm state for every token, failing on the first
// query error (a read error is a run error, never a 0 that reads as "no alarm").
func (o *alarmOracle) snapshotAll(ctx context.Context, tokens []string) (map[string]alarmSnapshot, error) {
	out := make(map[string]alarmSnapshot, len(tokens))
	for _, tok := range tokens {
		snap, err := o.snapshot(ctx, tok)
		if err != nil {
			return nil, err
		}
		out[tok] = snap
	}
	return out, nil
}

// awaitAlarmsDrained polls the durable alarm state until EVERY edge probe has
// reached a SETTLED raised-and-cleared lifecycle, or the timeout backstops. Callers
// stop ALL emission (probes AND background) before calling, so the pipeline drains
// monotonically to a steady state and this can prove drain by STABILITY.
//
// "Settled" is not a single CLEARED read: the edge alarm oscillates ACTIVE↔CLEARED
// across the K cycles, so a lone CLEARED read can be a mid-drain trough (cycle j<K
// resolved, cycle j+1 raise still queued) — the false-FAIL the earlier single-shot
// version had under load. Instead this requires every edge to be raisedAndCleared
// AND its clearedTime to be UNCHANGED since the previous poll: once emission has
// stopped and the backlog drains, clearedTime stops advancing, so two consecutive
// polls with equal clearedTimes prove the last transition landed. It tolerates a
// transient snapshot error (a GraphQL blip at peak contention) by treating that poll
// as "not settled yet" and retrying within the deadline, rather than aborting the
// whole run — only a deadline with the state never settling is a finding.
//
// A timeout without every edge settled is NOT concluded as reached — it is a real
// dropped transition the classifier fails as liveness. Returns whether the target
// was reached and the elapsed time.
func (o *alarmOracle) awaitAlarmsDrained(ctx context.Context, edge []sim.DeviceInstance, cfg DetectionConfig) (reached bool, elapsed time.Duration) {
	start := time.Now()
	deadline := start.Add(cfg.AlarmTimeout)
	prevCleared := make(map[string]string, len(edge)) // token → last-seen clearedTime
	haveprev := false
	for {
		cur := make(map[string]string, len(edge))
		allSettled := true
		for _, d := range edge {
			snap, serr := o.snapshot(ctx, d.Token)
			if serr != nil {
				// Transient read blip under load: this poll is inconclusive, not fatal.
				log.Warn().Err(serr).Str("device", d.Token).Msg("transient alarm read during drain-await; retrying")
				allSettled = false
				break
			}
			cur[d.Token] = snap.ClearedAt
			// Settled requires the completed lifecycle AND clearedTime unchanged since
			// the previous poll (so a trough between cycles, whose next resolve advances
			// clearedTime, is not mistaken for the final state).
			if !snap.raisedAndCleared() || !haveprev || prevCleared[d.Token] != snap.ClearedAt {
				allSettled = false
			}
		}
		if allSettled {
			return true, time.Since(start)
		}
		if len(cur) == len(edge) {
			prevCleared, haveprev = cur, true
		}
		if time.Now().After(deadline) {
			return false, time.Since(start)
		}
		select {
		case <-ctx.Done():
			return false, time.Since(start)
		case <-time.After(cfg.AlarmPoll):
		}
	}
}

// --- classification (pure) ----------------------------------------------------

// classifyAlarms turns the settled DURABLE alarm snapshots into the gated
// invariants. It is a pure function of the snapshots + expectations so it is
// exhaustively unit-testable with no cluster. Any single failed invariant fails the
// run (Report.Passed). This is the AUTHORITATIVE verdict — it reads durable state,
// not the lossy live stream.
func classifyAlarms(safetyStates, edgeStates map[string]alarmSnapshot, reached bool, cfg DetectionConfig, accepted int64) []Invariant {
	var invs []Invariant

	// 1. Load floor — a run that never stressed the path cannot certify no-spurious.
	invs = append(invs, Invariant{
		Name:   "load-floor",
		Passed: accepted >= cfg.MinAccepted,
		Detail: fmt.Sprintf("background accepted %d (floor %d)", accepted, cfg.MinAccepted),
	})

	// 2. no-spurious-alarm — every safety probe's durable alarm was NEVER raised.
	spurious := 0
	var firstSpurious string
	for tok, s := range safetyStates {
		if s.Found {
			spurious++
			if firstSpurious == "" {
				firstSpurious = fmt.Sprintf("%s state=%s severity=%s", tok, s.State, s.Severity)
			}
		}
	}
	if spurious == 0 {
		invs = append(invs, Invariant{Name: "no-spurious-alarm", Passed: true,
			Detail: fmt.Sprintf("all %d below-threshold safety probe(s) have no durable alarm (never raised)", len(safetyStates))})
	} else {
		invs = append(invs, Invariant{Name: "no-spurious-alarm", Passed: false,
			Detail: fmt.Sprintf("%d safety probe(s) raised a spurious durable alarm (first: %s)", spurious, firstSpurious)})
	}

	// 3. alarm-liveness — every edge probe drove its alarm ACTIVE→CLEARED (durably).
	// `reached` gates the timeout: if the durable await never saw every edge settle,
	// this is a real dropped transition, not a slow drain we quietly pass.
	notLive := 0
	var firstNotLive string
	for tok, s := range edgeStates {
		if !s.raisedAndCleared() {
			notLive++
			if firstNotLive == "" {
				firstNotLive = fmt.Sprintf("%s found=%v state=%s severity=%s (want CLEARED+%s)", tok, s.Found, s.State, s.Severity, harnessAlarmSeverityWire)
			}
		}
	}
	switch {
	case notLive > 0:
		invs = append(invs, Invariant{Name: "alarm-liveness", Passed: false,
			Detail: fmt.Sprintf("%d edge probe(s) did not complete the raised→cleared lifecycle (first: %s)", notLive, firstNotLive)})
	case !reached:
		// Defensive: every snapshot looks live but the await reported a timeout — treat
		// the disagreement as a failure rather than trust the later snapshot.
		invs = append(invs, Invariant{Name: "alarm-liveness", Passed: false,
			Detail: "durable alarm await timed out before every edge probe settled (dropped transition under load)"})
	default:
		invs = append(invs, Invariant{Name: "alarm-liveness", Passed: true,
			Detail: fmt.Sprintf("all %d edge probe(s) drove their durable alarm through raised→cleared", len(edgeStates))})
	}

	return invs
}

// --- report -------------------------------------------------------------------

// DetectionReport is the machine- and human-readable result of a detection→alarm
// probe run. The gated verdict lives in Invariants (durable-alarm authoritative);
// Detection is the NON-GATING live-stream evidence (lossy, informational).
type DetectionReport struct {
	Seed          int64             `json:"seed"`
	Tenant        string            `json:"tenant"`
	StartedAt     time.Time         `json:"startedAt"`
	FinishedAt    time.Time         `json:"finishedAt"`
	Drive         DriveStats        `json:"drive"`
	Cycles        int               `json:"cycles"`
	SafetyProbes  int               `json:"safetyProbes"`
	EdgeProbes    int               `json:"edgeProbes"`
	AlarmsReached bool              `json:"alarmsReachedTarget"`
	AlarmSecs     float64           `json:"alarmSettleSeconds"`
	ProbeFailures int               `json:"probeEmitFailures"`
	SafetyAlarms  map[string]string `json:"safetyAlarmStates"` // token → durable state summary
	EdgeAlarms    map[string]string `json:"edgeAlarmStates"`
	LiveSignal    bool              `json:"liveSignalPresent"`   // whether detectionStream was watched at all
	Detection     detmonitor.Report `json:"detectionLiveSignal"` // NON-GATING evidence
	Invariants    []Invariant       `json:"invariants"`
}

// Passed reports whether every invariant held. An empty invariant set is NOT a pass
// (the same discipline as Report.Passed) — a run that asserted nothing proved nothing.
func (r *DetectionReport) Passed() bool {
	if len(r.Invariants) == 0 {
		return false
	}
	for _, inv := range r.Invariants {
		if !inv.Passed {
			return false
		}
	}
	return true
}

// JSON renders the report for a CI artifact.
func (r *DetectionReport) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// Human renders a terse operator summary.
func (r *DetectionReport) Human() string {
	var b strings.Builder
	verdict := "FAIL"
	if r.Passed() {
		verdict = "PASS"
	}
	fmt.Fprintf(&b, "detection-alarm-probes %s (seed %d, tenant %s)\n", verdict, r.Seed, r.Tenant)
	fmt.Fprintf(&b, "  drive: %d bg devices, achieved %.1f ev/s over %.0fs — accepted %d, failed %d\n",
		r.Drive.Devices, r.Drive.AchievedRatePS, r.Drive.HoldSeconds, r.Drive.Accepted, r.Drive.Failed)
	fmt.Fprintf(&b, "  probes: %d safety, %d edge × K=%d cycles, %d emit failure(s); alarms reached-target %v in %.0fs\n",
		r.SafetyProbes, r.EdgeProbes, r.Cycles, r.ProbeFailures, r.AlarmsReached, r.AlarmSecs)
	if r.LiveSignal {
		fmt.Fprintf(&b, "  live detection signal (NON-authoritative, lossy): %d edge(s) observed, %d watcher violation(s)\n",
			r.Detection.Observed, len(r.Detection.Violations))
	} else {
		fmt.Fprintf(&b, "  live detection signal: absent (no eventProcessingWS, or subscribe failed) — verdict is on the durable alarm oracle alone\n")
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

// summarizeAlarms renders a compact per-device durable-state map for the report.
func summarizeAlarms(states map[string]alarmSnapshot) map[string]string {
	out := make(map[string]string, len(states))
	for tok, s := range states {
		if !s.Found {
			out[tok] = "none"
			continue
		}
		out[tok] = fmt.Sprintf("%s(severity=%s)", s.State, s.Severity)
	}
	return out
}

// --- orchestration ------------------------------------------------------------

// RunDetectionProbes runs the L2d-1 detection→alarm probe harness end to end:
// provision the dedicated harness profile + probes + background fleet, author and
// publish the threshold+raiseAlarm rule (fresh-tenant only), verify no probe alarm
// pre-exists, drive the background fleet at load while the probes cross (and stay
// below) the threshold, wait for the edge probes' DURABLE alarms to settle, then
// reconcile against durable alarm state. detectionStream is subscribed as a
// non-gating live-latency signal. The caller turns the verdict into an exit code.
func RunDetectionProbes(ctx context.Context, hs *sim.Handshake, cfg DetectionConfig) (*DetectionReport, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// The clean-tenant precheck reads the same event-management endpoint L1 uses.
	eventEndpoint, err := httpGraphQLFromWS(hs.Endpoints.EventMgmtWS)
	if err != nil {
		return nil, err
	}

	manifest := cfg.harnessManifest()
	rt, err := sim.NewRuntime(hs, cfg.load(), sim.DeviceCount(manifest))
	if err != nil {
		return nil, err
	}
	oracle := &alarmOracle{session: rt.Session, endpoint: rt.Endpoints.DeviceMgmtGraphQL}

	// Clean-tenant precondition: a competing producer emitting on the SAME
	// deterministic probe tokens would raise/clear probe alarms — a false spurious or
	// a false liveness. Refuse rather than reconcile against a shared tenant.
	counter := &graphqlEventCounter{session: rt.Session, endpoint: eventEndpoint}
	if err := requireCleanTenant(ctx, counter); err != nil {
		return nil, err
	}

	if err := sim.Provision(ctx, rt, manifest); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	// ensureDetectionRule refuses a non-fresh tenant (rule already exists), the first
	// residual-state guard.
	if err := ensureDetectionRule(ctx, rt, cfg); err != nil {
		return nil, fmt.Errorf("author detection rule: %w", err)
	}

	safety, edge, background, err := partitionByPrefix(rt.Devices, safetyTokenPrefix, edgeTokenPrefix, bgTokenPrefix)
	if err != nil {
		return nil, err
	}
	if len(edge) == 0 || len(safety) == 0 {
		return nil, fmt.Errorf("harness provisioned %d safety + %d edge probes — need at least one of each", len(safety), len(edge))
	}

	// Second residual-state guard: no probe device may already carry a harness alarm
	// (a leftover from a crashed prior run on a reused tenant, which the event-count
	// clean-tenant check would not see). A pre-existing alarm would make no-spurious or
	// liveness read a stale row. Deterministic + cheap.
	probeTokens := tokensOf(append(append([]sim.DeviceInstance{}, safety...), edge...))
	pre, err := oracle.snapshotAll(ctx, probeTokens)
	if err != nil {
		return nil, fmt.Errorf("pre-drive alarm check: %w", err)
	}
	for tok, s := range pre {
		if s.Found {
			return nil, fmt.Errorf("probe device %q already has a durable harness alarm (state %s) before the run — use a fresh tenant", tok, s.State)
		}
	}

	// Subscribe detectionStream as NON-GATING live evidence. A dial/subscribe failure
	// must not fail the run (the durable oracle is the authority), so a failure here
	// downgrades to "no live signal" rather than aborting.
	var mon *detmonitor.Monitor
	if strings.TrimSpace(hs.Endpoints.EventProcessingWS) != "" {
		if m, derr := detmonitor.Dial(ctx, hs.Endpoints.EventProcessingWS, rt.Session.AccessToken); derr != nil {
			log.Warn().Err(derr).Msg("could not dial detectionStream for the live signal — proceeding on the durable alarm oracle alone")
		} else {
			mon = m
			defer mon.Stop()
			if werr := mon.Watch(ctx, HarnessProfileToken); werr != nil {
				log.Warn().Err(werr).Msg("could not subscribe detectionStream — proceeding without the live signal")
				_ = mon.Stop()
				mon = nil
			} else {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(subscribeSettle):
				}
			}
		}
	} else {
		log.Info().Msg("handshake has no eventProcessingWS — running on the durable alarm oracle with no live detection signal")
	}

	// Point the runtime's emit set at the background fleet so sim.EmitAll drives
	// exactly that fleet (with its worker pool + Stats accounting). Probe slices were
	// captured above; nothing else reads rt.Devices after Provision.
	rt.Devices = background

	start := time.Now()
	rt.Stats.Reset(start)

	// Background saturates the pipeline while the probes cross the threshold, so the
	// planted edges are GENERATED under real contention.
	runCtx, cancel := context.WithCancel(ctx)
	var bgWg sync.WaitGroup
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		driveBackground(runCtx, rt, cfg.BackgroundInterval)
	}()

	ps := &probeStats{}
	driveProbes(ctx, rt, safety, edge, cfg.Cycles, cfg.ProbeInterval, ps)
	if ctx.Err() != nil {
		cancel()
		bgWg.Wait()
		return nil, fmt.Errorf("run aborted: %w", ctx.Err())
	}

	// Stop ALL emission (probes are done; stop the background too) BEFORE draining, so
	// the pipeline quiesces to a steady state the durable snapshot can trust. Keeping
	// the background running during the drain would leave the alarm feed perpetually
	// churning — a CLEARED edge read could be a mid-oscillation trough, and a late
	// spurious safety raise could always still be in flight. Quiescing is the same
	// drive-then-settle discipline the L1 oracle uses; the edges were already
	// generated under load above, which is what matters.
	cancel()
	bgWg.Wait()
	end := time.Now()
	rt.Stats.Freeze(end)
	snap := rt.Stats.Snapshot(end)

	// Now wait for the edge probes' durable alarms to SETTLE (raised→cleared, stable
	// across polls) against the quiesced tenant.
	reached, alarmElapsed := oracle.awaitAlarmsDrained(ctx, edge, cfg)

	// Extra settle before the authoritative read: a spurious safety raise drains on
	// roughly the edge timeline (safety + edge emit in the same steps), so once the
	// edges are drain-stable the safety backlog has almost certainly landed too — this
	// margin covers the small cross-device skew. BOUNDED OBSERVATION: a spurious raise
	// that arrives beyond this window is out of the gate's observation scope (there is
	// no cross-series drain barrier the platform exposes to close it absolutely).
	if reached {
		select {
		case <-ctx.Done():
		case <-time.After(cfg.AlarmSettle):
		}
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("run aborted: %w", ctx.Err())
	}

	// Take the FINAL authoritative durable snapshots over the quiesced tenant. Probe
	// tokens are disjoint from background tokens, and emission has stopped, so no row
	// we read is mid-transition on our account.
	safetyStates, err := oracle.snapshotAll(ctx, tokensOf(safety))
	if err != nil {
		return nil, fmt.Errorf("final safety alarm read: %w", err)
	}
	edgeStates, err := oracle.snapshotAll(ctx, tokensOf(edge))
	if err != nil {
		return nil, fmt.Errorf("final edge alarm read: %w", err)
	}

	// MEDIUM-1 guard: a no-spurious PASS rests on safety devices having NO alarm row
	// (Found==false). But the alarm query returns an empty set for an UNRESOLVABLE
	// originator token too — so "no alarm" and "token didn't resolve" are
	// indistinguishable exactly where absence is the pass. Confirm every safety token
	// still resolves to a real device before trusting its Found==false.
	if err := requireDevicesExist(ctx, rt, tokensOf(safety)); err != nil {
		return nil, fmt.Errorf("safety-probe existence check: %w", err)
	}

	// Settle + report the non-gating live signal.
	var liveSignal detmonitor.Report
	if mon != nil {
		_ = mon.Stop()
		liveSignal = mon.Report()
	}
	probeFailures, firstProbeErr := ps.snapshot()

	invs := classifyAlarms(safetyStates, edgeStates, reached, cfg, snap.Emitted)

	// Probe delivery is a run-integrity gate: a probe emit the ingress rejected means
	// the planted signal never entered the pipeline, so the durable alarm state is not
	// a clean measurement.
	if probeFailures == 0 {
		invs = append(invs, Invariant{Name: "probe-delivery", Passed: true,
			Detail: "every probe emit was accepted (202) into the pipeline"})
	} else {
		invs = append(invs, Invariant{Name: "probe-delivery", Passed: false,
			Detail: fmt.Sprintf("%d probe emit(s) were not accepted (first: %v) — the planted signal did not enter the pipeline", probeFailures, firstProbeErr)})
	}

	report := &DetectionReport{
		Seed:          cfg.Seed,
		Tenant:        hs.Tenant,
		StartedAt:     start.UTC(),
		FinishedAt:    end.UTC(),
		Cycles:        cfg.Cycles,
		SafetyProbes:  len(safety),
		EdgeProbes:    len(edge),
		AlarmsReached: reached,
		AlarmSecs:     alarmElapsed.Seconds(),
		ProbeFailures: probeFailures,
		SafetyAlarms:  summarizeAlarms(safetyStates),
		EdgeAlarms:    summarizeAlarms(edgeStates),
		LiveSignal:    mon != nil,
		Detection:     liveSignal,
		Invariants:    invs,
		Drive: DriveStats{
			Devices:        len(background),
			TargetRatePS:   rt.Load.TargetRate(len(background)),
			AchievedRatePS: snap.Rate,
			Accepted:       snap.Emitted,
			Failed:         snap.Failed,
			Ticks:          snap.Ticks,
			HoldSeconds:    end.Sub(start).Seconds(),
		},
	}
	return report, nil
}
