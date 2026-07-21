// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-simulator/loadtest/cmdreceiver"
	"github.com/devicechain-io/dc-simulator/sim"
	"github.com/rs/zerolog/log"
)

// L2d-3: planted command round-trip probes (ADR-064, research/load-test-harness.md §4.2).
//
// Where L2d-1 plants a threshold rule whose raiseAlarm action drives a durable
// ALARM, this layer plants the SAME threshold rule with a sendCommand action, so a
// crossing enqueues a durable COMMAND that command-delivery dispatches to the device
// over MQTT — and the device (a real cmdreceiver, ADR-043 two-way) answers it,
// driving the command QUEUED→SENT→SUCCESSFUL. A saturating background fleet crosses
// the same rule so the whole detect→react→enqueue→dispatch→respond path is under
// real contention while two kinds of probe assert:
//
//   - no-spurious-command (safety): a probe that emits strictly BELOW the threshold,
//     forever, must never enqueue its command. A spurious send writes a DURABLE
//     command row on that device — visible the instant createCommand runs, before
//     any dispatch — so this is the false-react class a passive oracle cannot catch,
//     closed authoritatively against durable state.
//   - command round-trip (liveness): a probe that crosses the threshold K times must
//     drive exactly K commands (one per RISING edge — sendCommand has no falling-edge
//     twin), each completing the full loop to durable SUCCESSFUL. A dropped rising
//     edge leaves fewer than K; a command that reached the device but whose response
//     never landed stays SENT; both fail. The command probe is the liveness WITNESS:
//     a dead detect/react/dispatch path leaves the durable commands absent or
//     unfinished, so silence never passes.
//
// THE ORACLE IS DURABLE COMMAND STATE, NOT THE MQTT RECEIVE STREAM. A durable
// SUCCESSFUL is only reachable if the device genuinely received the command (over
// MQTT) AND answered it, so it is an authoritative, non-lossy round-trip proof read
// over the same tenant GraphQL a real client uses (the fidelity rule). The MQTT
// cmdreceiver is the mechanism that makes SUCCESSFUL reachable — it is the device —
// and its per-device raw-vs-distinct tally is reported as NON-GATING evidence that
// measures the documented at-least-once redelivery (delivery is publish-before-mark,
// so the same command token can arrive more than once). The gate never reads that
// lossy tally: a receiver that fell behind would only ever cause a SAFE liveness
// false-FAIL (its device would not answer, so SUCCESSFUL is not reached), never a
// false-PASS — the no-spurious and round-trip authorities are both durable.
//
// Commands enqueued by REACT carry no TTL (createCommand omits expiresAt ⇒ NULL), so
// they never auto-EXPIRE/TIMEOUT: a command waits indefinitely for its response, and
// a stuck SENT is therefore a genuine round-trip failure, never a 30s-sweep-vs-TTL
// timing artifact.

// Harness topology — a DEDICATED command profile, distinct from the detection
// harness's, carrying the temp metric, a published command definition (so the
// sendCommand key is in the profile's constrained vocabulary and enqueue is not
// rejected), and the pinned threshold+sendCommand rule.
const (
	// HarnessCommandProfileToken is the pinned profile the command rule, the command
	// definition, and every probe hang off.
	HarnessCommandProfileToken = "harness-cmd-profile"
	// HarnessCommandDeviceTypeToken is the device type every probe + background
	// device instantiates.
	HarnessCommandDeviceTypeToken = "harness-cmd-device"
	// HarnessCommandRuleToken is the authored threshold+sendCommand rule's token.
	HarnessCommandRuleToken = "harness-cmd-overtemp"
	// HarnessCommandDefToken is the CommandDefinition's own token on the profile.
	HarnessCommandDefToken = "harness-cmd-def"
	// HarnessCommandKey is the command the sendCommand action names and the command
	// definition declares. It MUST be identical on both sides: the REACT dispatcher
	// sends it as the createCommand `name`, and command-delivery's enqueue gate
	// matches it against the profile's published CommandDefinition.CommandKey — a
	// mismatch on a constrained profile is REJECTED (and retried to poison), not a
	// clean drop. ADR-042 token grammar (lowercase alnum + hyphen).
	HarnessCommandKey = "harness-reset"

	// Distinct command-harness token prefixes (its own, not the detection harness's,
	// so a token unambiguously names its harness AND its role in logs).
	cmdSafeTokenPattern  = "harness-cmd-safe-{n:03d}"
	cmdProbeTokenPattern = "harness-cmd-probe-{n:03d}"
	cmdBgTokenPattern    = "harness-cmd-bg-{n:05d}"
	cmdSafeTokenPrefix   = "harness-cmd-safe-"
	cmdProbeTokenPrefix  = "harness-cmd-probe-"
	cmdBgTokenPrefix     = "harness-cmd-bg-"

	// Durable command status wire values (command-delivery model.CommandStatus).
	// Mirrored here as literals — the sim speaks only the wire. SUCCESSFUL is the
	// settled terminal a completed round-trip reaches; QUEUED/SENT are the in-flight
	// states a dropped or unanswered command is stranded in.
	cmdStatusQueued     = "QUEUED"
	cmdStatusSent       = "SENT"
	cmdStatusSuccessful = "SUCCESSFUL"

	// defaultMqttBroker is the NATS MQTT gateway address the cmdreceiver dials. It
	// defaults to a local port-forward of dc-nats:1883 over TLS (the NATS MQTT
	// listener terminates server-side TLS), since the load-test client runs outside
	// the cluster like dc-loadtest against the HTTP ingress.
	defaultMqttBroker = "ssl://127.0.0.1:1883"
)

// Command-harness defaults.
const (
	DefaultCommandBackgroundDevices = 50
	DefaultCommandSafetyProbes      = 3
	DefaultCommandProbes            = 3
	DefaultCommandCycles            = 5
	DefaultCommandProbeInterval     = 1 * time.Second
	DefaultCommandBackgroundInt     = 200 * time.Millisecond
	// DefaultCommandMinAccepted is the background load floor. Like L1's floor, a run
	// that applied less than this never stressed the enqueue/dispatch path, so its
	// no-spurious verdict would be vacuous.
	DefaultCommandMinAccepted = 1000
	DefaultCommandPoll        = 3 * time.Second
	// DefaultCommandTimeout backstops the wait for every command probe's K commands
	// to reach durable SUCCESSFUL through enqueue→sweep(30s)→dispatch→MQTT→respond
	// under load. Generous because command-delivery dispatches on a 30s ticker, so a
	// command created just before quiesce waits up to a full sweep before it is even
	// SENT.
	DefaultCommandTimeout = 180 * time.Second
	// DefaultCommandSettle holds after the probes' commands settle so a LATE spurious
	// command on a SAFETY probe is still captured by the final durable snapshot.
	DefaultCommandSettle = 5 * time.Second
)

// CommandConfig configures one command round-trip probe run.
type CommandConfig struct {
	Seed int64

	BackgroundDevices  int
	BackgroundInterval time.Duration
	Concurrency        int

	// SafetyProbes / CommandProbes are how many of each probe to plant; Cycles is K,
	// the number of threshold crossings each command probe performs → K commands each
	// (one per rising edge). ProbeInterval is the low, fixed probe emit cadence. More
	// command probes = more independent durable round-trip witnesses.
	SafetyProbes  int
	CommandProbes int
	Cycles        int
	ProbeInterval time.Duration

	MinAccepted int64

	// CommandPoll/CommandTimeout tune the wait for durable command state to settle to
	// SUCCESSFUL; CommandSettle holds past the probe target so a late spurious safety
	// command lands.
	CommandPoll    time.Duration
	CommandTimeout time.Duration
	CommandSettle  time.Duration

	// MqttBroker is the NATS MQTT gateway the cmdreceiver dials (default a local
	// port-forward of dc-nats:1883 over TLS). MqttTLSInsecure skips the gateway's
	// server-cert verification for an ssl:// broker — the default for a load-test run
	// against a kind/dev cluster, whose gateway cert is self-signed AND whose SAN is
	// the in-cluster DNS name (dc-nats.dc-system), which a 127.0.0.1 port-forward
	// cannot match either way.
	MqttBroker      string
	MqttTLSInsecure bool
}

func (c CommandConfig) withDefaults() CommandConfig {
	if c.BackgroundDevices <= 0 {
		c.BackgroundDevices = DefaultCommandBackgroundDevices
	}
	if c.BackgroundInterval <= 0 {
		c.BackgroundInterval = DefaultCommandBackgroundInt
	}
	if c.SafetyProbes <= 0 {
		c.SafetyProbes = DefaultCommandSafetyProbes
	}
	if c.CommandProbes <= 0 {
		c.CommandProbes = DefaultCommandProbes
	}
	if c.Cycles <= 0 {
		c.Cycles = DefaultCommandCycles
	}
	if c.ProbeInterval <= 0 {
		c.ProbeInterval = DefaultCommandProbeInterval
	}
	if c.MinAccepted <= 0 {
		c.MinAccepted = DefaultCommandMinAccepted
	}
	if c.CommandPoll <= 0 {
		c.CommandPoll = DefaultCommandPoll
	}
	if c.CommandTimeout <= 0 {
		c.CommandTimeout = DefaultCommandTimeout
	}
	if c.CommandSettle <= 0 {
		c.CommandSettle = DefaultCommandSettle
	}
	if strings.TrimSpace(c.MqttBroker) == "" {
		c.MqttBroker = defaultMqttBroker
	}
	return c
}

// Validate rejects a config that cannot run as asked.
func (c CommandConfig) Validate() error {
	if c.BackgroundDevices < 0 || c.SafetyProbes < 0 || c.CommandProbes < 0 {
		return fmt.Errorf("device counts must not be negative")
	}
	if c.CommandProbes == 0 {
		return fmt.Errorf("at least one command probe is required — it is the round-trip liveness witness")
	}
	if c.SafetyProbes == 0 {
		return fmt.Errorf("at least one safety probe is required — it is the no-spurious-command oracle")
	}
	if c.Cycles < 1 {
		return fmt.Errorf("cycles (K) must be at least 1")
	}
	if c.MinAccepted < 0 {
		return fmt.Errorf("min accepted must not be negative")
	}
	return nil
}

func (c CommandConfig) load() sim.Load {
	return sim.Load{
		DeviceCount:  0,
		EmitInterval: c.BackgroundInterval,
		Concurrency:  c.Concurrency,
	}
}

// harnessManifest builds the dedicated command-harness topology: one profile with
// the temp metric, one device type, and three populations (safety, command, and
// background). Pure — (config, seed) fully determines it.
func (c CommandConfig) harnessManifest() sim.SimManifest {
	return sim.SimManifest{
		Name: "harness-command",
		Seed: c.Seed,
		Profiles: []sim.ProfileSpec{
			{
				Token:    HarnessCommandProfileToken,
				Name:     "Load-test Command Harness Profile",
				Category: "sensor",
				Metrics: []sim.MetricSpec{
					{Key: HarnessMetricKey, Name: "Temperature", DataType: "DOUBLE", Unit: "C"},
				},
			},
		},
		DeviceTypes: []sim.DeviceTypeSpec{
			{Token: HarnessCommandDeviceTypeToken, Name: "Load-test Command Harness Device", ProfileToken: HarnessCommandProfileToken},
		},
		Populations: []sim.PopulationSpec{
			{OfType: HarnessCommandDeviceTypeToken, Count: c.SafetyProbes, TokenPattern: cmdSafeTokenPattern, ExternalIdPattern: "HARNESS-CMD-SAFE-{n:03d}"},
			{OfType: HarnessCommandDeviceTypeToken, Count: c.CommandProbes, TokenPattern: cmdProbeTokenPattern, ExternalIdPattern: "HARNESS-CMD-PROBE-{n:03d}"},
			{OfType: HarnessCommandDeviceTypeToken, Count: c.BackgroundDevices, TokenPattern: cmdBgTokenPattern, ExternalIdPattern: "HARNESS-CMD-BG-{n:05d}"},
		},
	}
}

// ruleDefinitionJSON builds the opaque rules.Rule JSON: a threshold rule whose leaf
// compares the temp metric against the harness threshold and whose sendCommand
// action enqueues the durable command the oracle reconciles against. Keys are
// exactly the rules.Rule / Condition / Action json tags — event-processing decodes
// with DisallowUnknownFields. No `severity` (a sendCommand-only rule needs no tier —
// an empty severity is explicitly valid on a rule with no raiseAlarm) and no
// `payload` (a no-argument command; the definition declares no parameters).
func (c CommandConfig) ruleDefinitionJSON() (string, error) {
	threshold := harnessThreshold
	def := map[string]any{
		"name": "Load-test command harness over-temp",
		"type": "threshold",
		"when": map[string]any{
			"metric":    HarnessMetricKey,
			"op":        "gt",
			"threshold": threshold,
		},
		"actions": []any{
			map[string]any{
				"type":        "sendCommand",
				"sendCommand": map[string]any{"command": HarnessCommandKey},
			},
		},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal rule definition: %w", err)
	}
	return string(raw), nil
}

// --- command rule + definition authoring --------------------------------------

const mutationCreateCommandDefinition = `mutation($request:CommandDefinitionCreateRequest){createCommandDefinition(request:$request){token}}`

const mutationCreateCommandRule = `mutation($request:DetectionRuleCreateRequest!){createDetectionRule(request:$request){token}}`

const queryCommandRulesByToken = `query($tokens:[String!]!){detectionRulesByToken(tokens:$tokens){token}}`

const mutationPublishCommandProfile = `mutation($token:String!){publishDeviceProfile(token:$token){version}}`

// ensureCommandRule authors the command definition AND the threshold+sendCommand
// rule on the harness profile, then publishes the profile so both freeze into the
// active version: the definition into the published command vocabulary (so enqueue
// is not rejected) and the rule so the DETECT engine loads it.
//
// Like the detection harness, it REFUSES to run if the rule already exists: a
// pre-existing rule means the tenant is not fresh, which — combined with the durable
// command oracle — risks residual state (a stale detection latch, or leftover
// command rows) that would skew the verdict. The harness is a fresh-tenant tool.
func ensureCommandRule(ctx context.Context, rt *sim.Runtime, cfg CommandConfig) error {
	var existing struct {
		DetectionRulesByToken []struct {
			Token string `json:"token"`
		} `json:"detectionRulesByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryCommandRulesByToken,
		map[string]any{"tokens": []string{HarnessCommandRuleToken}}, &existing); err != nil {
		return fmt.Errorf("detectionRulesByToken: %w", err)
	}
	if len(existing.DetectionRulesByToken) > 0 {
		return fmt.Errorf("harness command rule %q already exists on this tenant — the command harness requires a FRESH tenant (destroy + recreate the sim) so residual detection latches / command rows cannot skew the durable verdict", HarnessCommandRuleToken)
	}

	// 1. The command definition — a no-argument command in the profile's vocabulary,
	// so the sendCommand key resolves at enqueue and the command is accepted.
	defReq := map[string]any{
		"token":              HarnessCommandDefToken,
		"deviceProfileToken": HarnessCommandProfileToken,
		"commandKey":         HarnessCommandKey,
		"name":               "Load-test harness reset",
	}
	var createdDef struct {
		CreateCommandDefinition struct {
			Token string `json:"token"`
		} `json:"createCommandDefinition"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateCommandDefinition,
		map[string]any{"request": defReq}, &createdDef); err != nil {
		return fmt.Errorf("createCommandDefinition: %w", err)
	}

	// 2. The threshold+sendCommand rule.
	def, err := cfg.ruleDefinitionJSON()
	if err != nil {
		return err
	}
	ruleReq := map[string]any{
		"token":              HarnessCommandRuleToken,
		"deviceProfileToken": HarnessCommandProfileToken,
		"name":               "Load-test command harness over-temp",
		"definition":         def,
		"enabled":            true,
	}
	var createdRule struct {
		CreateDetectionRule struct {
			Token string `json:"token"`
		} `json:"createDetectionRule"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateCommandRule,
		map[string]any{"request": ruleReq}, &createdRule); err != nil {
		return fmt.Errorf("createDetectionRule: %w", err)
	}

	// 3. Publish so the draft command definition + rule freeze into the active version
	// (a created-but-unpublished command is not in the enqueue vocabulary, and an
	// unpublished rule never fires).
	var published struct {
		PublishDeviceProfile struct {
			Version int `json:"version"`
		} `json:"publishDeviceProfile"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationPublishCommandProfile,
		map[string]any{"token": HarnessCommandProfileToken}, &published); err != nil {
		return fmt.Errorf("publishDeviceProfile: %w", err)
	}
	log.Info().Str("rule", HarnessCommandRuleToken).Str("command", HarnessCommandKey).
		Int("profileVersion", published.PublishDeviceProfile.Version).
		Msg("authored + published command definition + threshold/sendCommand rule")
	return nil
}

// --- durable command oracle ---------------------------------------------------

// commandSnapshot is the durable state of one device's harness commands, read over
// the tenant command query — the authoritative truth the gate reconciles against.
type commandSnapshot struct {
	// Count is the number of durable command rows for the device (any status). A
	// safety probe must have 0; a command probe must have exactly K.
	Count int
	// Successful is how many of those commands reached the terminal SUCCESSFUL state
	// (device received AND answered). A completed round-trip has Successful == Count.
	Successful int
	// Statuses tallies the per-status counts for the report/diagnostics.
	Statuses map[string]int
}

// roundTripComplete reports the settled durable lifecycle a command probe must reach:
// at least one command exists and EVERY command reached SUCCESSFUL (received +
// answered). A dropped rising edge leaves Count below K (caught by the count
// invariant); a command stranded in QUEUED/SENT leaves Successful < Count — both
// fail this.
func (s commandSnapshot) roundTripComplete() bool {
	return s.Count >= 1 && s.Successful == s.Count
}

const queryHarnessCommands = `query($c: CommandSearchCriteria!) {
  commands(criteria: $c) {
    results { token status }
    pagination { totalRecords }
  }
}`

// commandOracle reads a device's durable harness-command state over the real
// tenant-scoped command-delivery GraphQL API (the fidelity rule — no backdoor).
type commandOracle struct {
	session  sessionQuerier
	endpoint string
}

// snapshot reads the durable command state for one device, filtered by deviceToken
// so the result is exactly that device's commands. pageSize is generous relative to
// K so every command row is returned (and the count cross-checks totalRecords).
func (o *commandOracle) snapshot(ctx context.Context, deviceToken string) (commandSnapshot, error) {
	vars := map[string]any{
		"c": map[string]any{
			"pageNumber":  1,
			"pageSize":    500,
			"deviceToken": deviceToken,
		},
	}
	var out struct {
		Commands struct {
			Results []struct {
				Token  string `json:"token"`
				Status string `json:"status"`
			} `json:"results"`
			Pagination struct {
				TotalRecords *int64 `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"commands"`
	}
	if err := o.session.Query(ctx, o.endpoint, queryHarnessCommands, vars, &out); err != nil {
		return commandSnapshot{}, fmt.Errorf("query commands for %s: %w", deviceToken, err)
	}
	// A null totalRecords is a fail-closed error, not a silent "no commands" that a
	// no-spurious check would read as a clean pass.
	if out.Commands.Pagination.TotalRecords == nil {
		return commandSnapshot{}, fmt.Errorf("query commands for %s: server returned a null totalRecords", deviceToken)
	}
	total := int(*out.Commands.Pagination.TotalRecords)
	// The count invariant reconciles against totalRecords (a real COUNT(*)), not the
	// returned page — but guard the two agreeing so a totalRecords that outran a
	// bounded page (more commands than pageSize) is a fail-closed error, not a
	// silently-undercounted SUCCESSFUL tally.
	if total > len(out.Commands.Results) {
		return commandSnapshot{}, fmt.Errorf("query commands for %s: totalRecords %d exceeds returned page %d — raise pageSize", deviceToken, total, len(out.Commands.Results))
	}
	snap := commandSnapshot{Count: total, Statuses: make(map[string]int, len(out.Commands.Results))}
	for _, r := range out.Commands.Results {
		snap.Statuses[r.Status]++
		if r.Status == cmdStatusSuccessful {
			snap.Successful++
		}
	}
	return snap, nil
}

// snapshotAll reads the durable command state for every token, failing on the first
// query error (a read error is a run error, never a 0 that reads as "no command").
func (o *commandOracle) snapshotAll(ctx context.Context, tokens []string) (map[string]commandSnapshot, error) {
	out := make(map[string]commandSnapshot, len(tokens))
	for _, tok := range tokens {
		snap, err := o.snapshot(ctx, tok)
		if err != nil {
			return nil, err
		}
		out[tok] = snap
	}
	return out, nil
}

// awaitCommandsSettled polls the durable command state until EVERY command probe has
// at least K commands in the terminal SUCCESSFUL state, or the timeout backstops.
// Callers stop ALL emission before calling, so no new commands are created after
// quiesce and the count is fixed at K per probe; a command's status only advances
// monotonically toward the terminal SUCCESSFUL (commands carry no TTL, so a delivered
// + answered command stays SUCCESSFUL), so unlike the alarm oracle this needs no
// stability-across-polls — reaching K successful IS the settled state.
//
// It tolerates a transient snapshot error (a GraphQL blip at peak contention) by
// treating that poll as "not settled yet" and retrying within the deadline. A
// timeout without every probe reaching K successful is NOT concluded as reached — it
// is a real dropped or unanswered command the classifier fails as a liveness finding.
func (o *commandOracle) awaitCommandsSettled(ctx context.Context, probes []sim.DeviceInstance, cfg CommandConfig) (reached, sawCleanRead bool, elapsed time.Duration) {
	start := time.Now()
	deadline := start.Add(cfg.CommandTimeout)
	for {
		allSettled := true
		cleanRead := true // every device snapshot in THIS poll succeeded
		for _, d := range probes {
			snap, serr := o.snapshot(ctx, d.Token)
			if serr != nil {
				log.Warn().Err(serr).Str("device", d.Token).Msg("transient command read during settle-await; retrying")
				allSettled = false
				cleanRead = false
				break
			}
			if snap.Successful < cfg.Cycles {
				allSettled = false
			}
		}
		// sawCleanRead records whether the cohort was EVER fully readable. If it never
		// was, a non-reached verdict is an oracle outage (inconclusive), not a platform
		// liveness defect — the report distinguishes the two rather than blaming the
		// dispatch path for the gate's own blindness.
		if cleanRead {
			sawCleanRead = true
		}
		if allSettled {
			return true, sawCleanRead, time.Since(start)
		}
		if time.Now().After(deadline) {
			return false, sawCleanRead, time.Since(start)
		}
		select {
		case <-ctx.Done():
			return false, sawCleanRead, time.Since(start)
		case <-time.After(cfg.CommandPoll):
		}
	}
}

// --- classification (pure) ----------------------------------------------------

// classifyCommands turns the settled DURABLE command snapshots into the gated
// invariants. Pure — a function of the snapshots + expectations — so it is
// exhaustively unit-testable with no cluster. Any single failed invariant fails the
// run. This is the AUTHORITATIVE verdict — it reads durable command state, not the
// MQTT receive tally.
func classifyCommands(safetyStates, probeStates map[string]commandSnapshot, reached bool, cfg CommandConfig, accepted int64) []Invariant {
	var invs []Invariant

	// 1. Load floor — a run that never stressed the enqueue/dispatch path cannot
	// certify no-spurious.
	invs = append(invs, Invariant{
		Name:   "load-floor",
		Passed: accepted >= cfg.MinAccepted,
		Detail: fmt.Sprintf("background accepted %d (floor %d)", accepted, cfg.MinAccepted),
	})

	// 2. no-spurious-command — every safety probe has NO durable command row.
	spurious := 0
	var firstSpurious string
	for tok, s := range safetyStates {
		if s.Count > 0 {
			spurious++
			if firstSpurious == "" {
				firstSpurious = fmt.Sprintf("%s count=%d statuses=%v", tok, s.Count, s.Statuses)
			}
		}
	}
	if spurious == 0 {
		invs = append(invs, Invariant{Name: "no-spurious-command", Passed: true,
			Detail: fmt.Sprintf("all %d below-threshold safety probe(s) enqueued no command", len(safetyStates))})
	} else {
		invs = append(invs, Invariant{Name: "no-spurious-command", Passed: false,
			Detail: fmt.Sprintf("%d safety probe(s) enqueued a spurious command (first: %s)", spurious, firstSpurious)})
	}

	// 3. command-count — every command probe enqueued exactly K commands (one per
	// rising edge). Fewer than K is a dropped detection/enqueue; more than K is a
	// spurious duplicate (the idempotency token should collapse redeliveries, so >K
	// is itself a finding).
	wrongCount := 0
	var firstWrongCount string
	for tok, s := range probeStates {
		if s.Count != cfg.Cycles {
			wrongCount++
			if firstWrongCount == "" {
				firstWrongCount = fmt.Sprintf("%s count=%d (want %d)", tok, s.Count, cfg.Cycles)
			}
		}
	}
	if wrongCount == 0 {
		invs = append(invs, Invariant{Name: "command-count", Passed: true,
			Detail: fmt.Sprintf("all %d command probe(s) enqueued exactly K=%d commands", len(probeStates), cfg.Cycles)})
	} else {
		invs = append(invs, Invariant{Name: "command-count", Passed: false,
			Detail: fmt.Sprintf("%d command probe(s) did not enqueue exactly K=%d commands (first: %s)", wrongCount, cfg.Cycles, firstWrongCount)})
	}

	// 4. command-roundtrip — every command probe's commands ALL reached durable
	// SUCCESSFUL (device received over MQTT AND answered). `reached` gates the
	// timeout: if the durable await never saw every probe reach K successful, this is
	// a real dropped or unanswered command, not a slow settle we quietly pass.
	notComplete := 0
	var firstNotComplete string
	for tok, s := range probeStates {
		if !s.roundTripComplete() {
			notComplete++
			if firstNotComplete == "" {
				firstNotComplete = fmt.Sprintf("%s count=%d successful=%d statuses=%v", tok, s.Count, s.Successful, s.Statuses)
			}
		}
	}
	switch {
	case notComplete > 0:
		invs = append(invs, Invariant{Name: "command-roundtrip", Passed: false,
			Detail: fmt.Sprintf("%d command probe(s) did not complete the round-trip to SUCCESSFUL (first: %s)", notComplete, firstNotComplete)})
	case !reached:
		// Defensive: every final snapshot looks complete but the await never confirmed
		// it — treat the disagreement as a failure rather than trust the later snapshot.
		// Worded cause-neutral: it is EITHER a dropped/unanswered command OR the durable
		// oracle was unreadable through the deadline (oracleReadOK in the report says
		// which — the gate stays fail-closed either way).
		invs = append(invs, Invariant{Name: "command-roundtrip", Passed: false,
			Detail: "durable command await did not confirm every probe reached K SUCCESSFUL within the timeout (a dropped/unanswered command, or an unreadable oracle — see oracleReadOK)"})
	default:
		invs = append(invs, Invariant{Name: "command-roundtrip", Passed: true,
			Detail: fmt.Sprintf("all %d command probe(s) drove every command to durable SUCCESSFUL", len(probeStates))})
	}

	return invs
}

// --- report -------------------------------------------------------------------

// CommandReport is the machine- and human-readable result of a command round-trip
// probe run. The gated verdict lives in Invariants (durable-command authoritative);
// Receiver is the NON-GATING MQTT receive evidence (lossy, informational — measures
// the at-least-once redelivery).
type CommandReport struct {
	Seed            int64              `json:"seed"`
	Tenant          string             `json:"tenant"`
	StartedAt       time.Time          `json:"startedAt"`
	FinishedAt      time.Time          `json:"finishedAt"`
	Drive           DriveStats         `json:"drive"`
	Cycles          int                `json:"cycles"`
	SafetyProbes    int                `json:"safetyProbes"`
	CommandProbes   int                `json:"commandProbes"`
	CommandsReached bool               `json:"commandsReachedTarget"`
	OracleReadOK    bool               `json:"oracleReadOK"` // durable command query was fully readable ≥once (distinguishes a real miss from an oracle outage)
	CommandSecs     float64            `json:"commandSettleSeconds"`
	ProbeFailures   int                `json:"probeEmitFailures"`
	SafetyCommands  map[string]string  `json:"safetyCommandStates"` // token → durable summary
	ProbeCommands   map[string]string  `json:"probeCommandStates"`
	Receiver        cmdreceiver.Report `json:"mqttReceiveEvidence"` // NON-GATING
	Invariants      []Invariant        `json:"invariants"`
}

// Passed reports whether every invariant held. An empty invariant set is NOT a pass.
func (r *CommandReport) Passed() bool {
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
func (r *CommandReport) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// Human renders a terse operator summary.
func (r *CommandReport) Human() string {
	var b strings.Builder
	verdict := "FAIL"
	if r.Passed() {
		verdict = "PASS"
	}
	fmt.Fprintf(&b, "command-roundtrip-probes %s (seed %d, tenant %s)\n", verdict, r.Seed, r.Tenant)
	fmt.Fprintf(&b, "  drive: %d bg devices, achieved %.1f ev/s over %.0fs — accepted %d, failed %d\n",
		r.Drive.Devices, r.Drive.AchievedRatePS, r.Drive.HoldSeconds, r.Drive.Accepted, r.Drive.Failed)
	fmt.Fprintf(&b, "  probes: %d safety, %d command × K=%d cycles, %d emit failure(s); commands reached-target %v in %.0fs (oracle readable: %v)\n",
		r.SafetyProbes, r.CommandProbes, r.Cycles, r.ProbeFailures, r.CommandsReached, r.CommandSecs, r.OracleReadOK)
	fmt.Fprintf(&b, "  mqtt receive (NON-authoritative evidence): %d raw / %d distinct across cohort, %d blind device(s)\n",
		r.Receiver.TotalRaw, r.Receiver.TotalDistinct, len(r.Receiver.Blind))
	for _, inv := range r.Invariants {
		mark := "FAIL"
		if inv.Passed {
			mark = "ok"
		}
		fmt.Fprintf(&b, "  [%-4s] %s — %s\n", mark, inv.Name, inv.Detail)
	}
	return b.String()
}

// summarizeCommands renders a compact per-device durable-state map for the report.
func summarizeCommands(states map[string]commandSnapshot) map[string]string {
	out := make(map[string]string, len(states))
	for tok, s := range states {
		if s.Count == 0 {
			out[tok] = "none"
			continue
		}
		out[tok] = fmt.Sprintf("count=%d successful=%d %v", s.Count, s.Successful, s.Statuses)
	}
	return out
}

// --- orchestration ------------------------------------------------------------

// RunCommandProbes runs the L2d-3 command round-trip harness end to end: provision
// the dedicated command profile + probes + background fleet, author and publish the
// command definition + threshold/sendCommand rule (fresh-tenant only), verify no
// probe command pre-exists, connect the MQTT cmdreceiver cohort (the devices that
// receive + answer their commands), drive the background at load while the probes
// cross (and stay below) the threshold, wait for the command probes' DURABLE
// commands to settle to SUCCESSFUL, then reconcile against durable command state.
// The caller turns the verdict into an exit code.
func RunCommandProbes(ctx context.Context, hs *sim.Handshake, cfg CommandConfig) (*CommandReport, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(hs.Endpoints.CommandMgmtGraphQL) == "" {
		return nil, fmt.Errorf("handshake has no endpoints.commandMgmtGraphQL — the command harness reconciles against command-delivery's durable command query and cannot run without it")
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
	oracle := &commandOracle{session: rt.Session, endpoint: rt.Endpoints.CommandMgmtGraphQL}

	// Clean-tenant precondition: a competing producer emitting on the SAME
	// deterministic probe tokens would enqueue probe commands — a false spurious or a
	// false liveness. Refuse rather than reconcile against a shared tenant.
	counter := &graphqlEventCounter{session: rt.Session, endpoint: eventEndpoint}
	if err := requireCleanTenant(ctx, counter); err != nil {
		return nil, err
	}

	if err := sim.Provision(ctx, rt, manifest); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	if err := ensureCommandRule(ctx, rt, cfg); err != nil {
		return nil, fmt.Errorf("author command rule: %w", err)
	}

	safety, probes, background, err := partitionByPrefix(rt.Devices, cmdSafeTokenPrefix, cmdProbeTokenPrefix, cmdBgTokenPrefix)
	if err != nil {
		return nil, err
	}
	if len(probes) == 0 || len(safety) == 0 {
		return nil, fmt.Errorf("harness provisioned %d safety + %d command probes — need at least one of each", len(safety), len(probes))
	}

	// Residual-state guard: no probe device may already carry a harness command (a
	// leftover from a crashed prior run on a reused tenant).
	probeTokens := tokensOf(append(append([]sim.DeviceInstance{}, safety...), probes...))
	pre, err := oracle.snapshotAll(ctx, probeTokens)
	if err != nil {
		return nil, fmt.Errorf("pre-drive command check: %w", err)
	}
	for tok, s := range pre {
		if s.Count > 0 {
			return nil, fmt.Errorf("probe device %q already has %d durable harness command(s) before the run — use a fresh tenant", tok, s.Count)
		}
	}

	// Connect the MQTT receiver cohort BEFORE driving so a command dispatched during
	// or after the run is received (and answered) by a subscribed device. Every probe
	// device (safety + command) is connected: the command probes receive + answer to
	// reach SUCCESSFUL; the safety probes are the direct wire witness for no-spurious.
	// A device that cannot subscribe fails the run setup with a clear error rather
	// than producing a confusing round-trip failure later.
	var mqttTLS *tls.Config
	if strings.HasPrefix(cfg.MqttBroker, "ssl://") || strings.HasPrefix(cfg.MqttBroker, "tls://") {
		// #nosec G402 — a load-test client against a kind/dev gateway whose cert SAN is
		// the in-cluster DNS name, unmatchable by a 127.0.0.1 port-forward; opt-in via
		// MqttTLSInsecure, off for a real deployment reachable at its cert's SAN.
		mqttTLS = &tls.Config{InsecureSkipVerify: cfg.MqttTLSInsecure}
	}
	receiver := cmdreceiver.New(rt.InstanceId, rt.Tenant, cfg.MqttBroker, mqttTLS)
	defer receiver.Close()
	for _, d := range append(append([]sim.DeviceInstance{}, safety...), probes...) {
		if serr := receiver.Subscribe(ctx, d.Token, d.CredentialId); serr != nil {
			return nil, fmt.Errorf("connect MQTT receiver for %q: %w (check the dc-nats:1883 port-forward)", d.Token, serr)
		}
	}
	log.Info().Int("cohort", len(safety)+len(probes)).Str("broker", cfg.MqttBroker).
		Msg("MQTT command receiver cohort connected + subscribed")

	// Point the runtime's emit set at the background fleet.
	rt.Devices = background

	start := time.Now()
	rt.Stats.Reset(start)

	// Background saturates the enqueue/dispatch path while the probes cross the
	// threshold, so the planted commands are GENERATED under real contention.
	runCtx, cancel := context.WithCancel(ctx)
	var bgWg sync.WaitGroup
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		driveBackground(runCtx, rt, cfg.BackgroundInterval)
	}()

	ps := &probeStats{}
	driveProbes(ctx, rt, safety, probes, cfg.Cycles, cfg.ProbeInterval, ps)
	if ctx.Err() != nil {
		cancel()
		bgWg.Wait()
		return nil, fmt.Errorf("run aborted: %w", ctx.Err())
	}

	// Stop ALL emission BEFORE settling, so no new commands are created after quiesce
	// and the durable count is fixed at K per probe. The probe commands were already
	// generated under load above, which is what matters.
	cancel()
	bgWg.Wait()
	end := time.Now()
	rt.Stats.Freeze(end)
	snap := rt.Stats.Snapshot(end)

	// Wait for the command probes' durable commands to settle to SUCCESSFUL against
	// the quiesced tenant (dispatch is on a 30s sweep, so this covers the sweep +
	// MQTT round-trip + response).
	reached, oracleReadOK, cmdElapsed := oracle.awaitCommandsSettled(ctx, probes, cfg)

	// Extra settle before the authoritative read so a late spurious safety command
	// (created near end-of-run) has landed. BOUNDED OBSERVATION: a spurious command
	// beyond this window is out of the gate's observation scope.
	if reached {
		select {
		case <-ctx.Done():
		case <-time.After(cfg.CommandSettle):
		}
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("run aborted: %w", ctx.Err())
	}

	// Take the FINAL authoritative durable snapshots over the quiesced tenant.
	safetyStates, err := oracle.snapshotAll(ctx, tokensOf(safety))
	if err != nil {
		return nil, fmt.Errorf("final safety command read: %w", err)
	}
	probeStates, err := oracle.snapshotAll(ctx, tokensOf(probes))
	if err != nil {
		return nil, fmt.Errorf("final probe command read: %w", err)
	}

	// Guard: a no-spurious PASS rests on safety devices having NO command row
	// (Count==0). The command query returns an empty set for an UNRESOLVABLE
	// deviceToken too, so confirm every safety token still resolves to a real device
	// before trusting its Count==0.
	if err := requireDevicesExist(ctx, rt, tokensOf(safety)); err != nil {
		return nil, fmt.Errorf("safety-probe existence check: %w", err)
	}

	// Collect the non-gating MQTT receive evidence.
	receiver.Close()
	receiverReport := receiver.Report()
	probeFailures, firstProbeErr := ps.snapshot()

	invs := classifyCommands(safetyStates, probeStates, reached, cfg, snap.Emitted)

	// Probe delivery is a run-integrity gate: a probe emit the ingress rejected means
	// the planted signal never entered the pipeline.
	if probeFailures == 0 {
		invs = append(invs, Invariant{Name: "probe-delivery", Passed: true,
			Detail: "every probe emit was accepted (202) into the pipeline"})
	} else {
		invs = append(invs, Invariant{Name: "probe-delivery", Passed: false,
			Detail: fmt.Sprintf("%d probe emit(s) were not accepted (first: %v) — the planted signal did not enter the pipeline", probeFailures, firstProbeErr)})
	}

	report := &CommandReport{
		Seed:            cfg.Seed,
		Tenant:          hs.Tenant,
		StartedAt:       start.UTC(),
		FinishedAt:      end.UTC(),
		Cycles:          cfg.Cycles,
		SafetyProbes:    len(safety),
		CommandProbes:   len(probes),
		CommandsReached: reached,
		OracleReadOK:    oracleReadOK,
		CommandSecs:     cmdElapsed.Seconds(),
		ProbeFailures:   probeFailures,
		SafetyCommands:  summarizeCommands(safetyStates),
		ProbeCommands:   summarizeCommands(probeStates),
		Receiver:        receiverReport,
		Invariants:      invs,
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
