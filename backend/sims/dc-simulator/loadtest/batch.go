// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-simulator/cmdreceiver"
	"github.com/devicechain-io/dc-simulator/sim"
	"github.com/rs/zerolog/log"
)

// L2d-4: the fleet write under load.
//
// L2d-3 proves ONE command reaches ONE device. A batch is a different claim: one
// request, one record, and N durable command rows that must correspond to the target
// set EXACTLY — not in count, in membership. The failure this layer exists to catch
// is a fan-out that reports success while the rows it created do not match what was
// asked for, which no per-device probe can see.
//
// 🔴 A COUNT IS NOT A MEMBERSHIP. Two rows for one target and none for another counts
// correctly, the doubled device answers both of its commands, the missed device has
// nothing to fail on, and every count-based check passes — while one device in the
// fleet never got the actuation and another got it twice. So the fan-out invariant
// reads the batch's rows as ONE page and compares the deviceToken MULTISET against
// the target set. That subsumes the count and catches duplication and omission at once.
//
// The batch is tens of rows on purpose. BATCH_TOO_LARGE (10,000 tokens), the 1,000-row
// chunked insert and the delivery-machinery reserve are NOT measured at their real
// boundaries by this layer, and it does not claim to.

// Harness topology — TWO profiles, and the second one is the whole fail-closed half.
const (
	// HarnessBatchProfileToken carries the published command every target receives.
	HarnessBatchProfileToken = "harness-batch-profile"
	// HarnessBatchPoisonProfileToken is the profile that must REFUSE the batch command.
	//
	// 🔴 IT PUBLISHES A COMMAND OF ITS OWN, AND THAT IS THE POINT. The obvious way to
	// build a device that cannot receive a command is to give its profile no command
	// definitions at all — and it does not work: a profile with an empty vocabulary is
	// UNCONSTRAINED, and an unconstrained profile accepts any command key. A poison
	// device built that way would be admitted, the whole-batch refusal would never
	// fire, and the fail-closed invariant below would be a check that cannot fail.
	// So the poison profile declares a DIFFERENT command, which makes it constrained
	// and makes the batch command genuinely absent from its vocabulary.
	HarnessBatchPoisonProfileToken = "harness-batch-poison-profile"

	HarnessBatchDeviceTypeToken       = "harness-batch-device"
	HarnessBatchPoisonDeviceTypeToken = "harness-batch-poison-device"

	// HarnessBatchCommandKey is the command the batch fans out. HarnessBatchPoisonKey
	// is the unrelated command that makes the poison profile constrained; nothing ever
	// sends it.
	HarnessBatchCommandKey = "harness-batch-reset"
	HarnessBatchPoisonKey  = "harness-batch-ignored"

	HarnessBatchDefToken       = "harness-batch-def"
	HarnessBatchPoisonDefToken = "harness-batch-poison-def"

	batchTargetTokenPattern    = "harness-batch-tgt-{n:03d}"
	batchBystanderTokenPattern = "harness-batch-by-{n:03d}"
	batchPoisonTokenPattern    = "harness-batch-poison-{n:02d}"
	batchBgTokenPattern        = "harness-batch-bg-{n:05d}"
	batchTargetTokenPrefix     = "harness-batch-tgt-"
	batchBystanderTokenPrefix  = "harness-batch-by-"
	batchPoisonTokenPrefix     = "harness-batch-poison-"
	batchBgTokenPrefix         = "harness-batch-bg-"

	// Wire values the harness matches on. Mirrored as literals — the sim speaks only
	// the wire.
	batchRejectPartialRefused = "BATCH_PARTIAL_REFUSED"
	batchRefusalNotInVocab    = "COMMAND_NOT_IN_VOCABULARY"

	// ControlDeafDevice is the negative control that needs no privilege at all: leave
	// ONE target device without an MQTT receiver. Its command row is created — so the
	// fan-out invariant still holds — and nothing ever answers it, so the round trip
	// cannot complete. It is deterministic and races nothing.
	ControlDeafDevice = "deaf-device"
)

// The gated invariant names, declared as constants because the control compares
// against a literal set of them.
const (
	InvBatchLoadFloor        = "batch-load-floor"
	InvBatchFanoutComplete   = "batch-fanout-complete"
	InvBatchRoundTrip        = "batch-round-trip"
	InvBatchTargetOnly       = "batch-touched-only-its-target"
	InvBatchReplayIdempotent = "batch-replay-is-idempotent"
	InvBatchRefusedWhole     = "batch-refused-whole-creates-nothing"
	// InvBatchDevicesReceived is the WIRE witness, and it exists because the durable
	// one is not sufficient on its own — see classifyBatchReceipt.
	InvBatchDevicesReceived = "batch-devices-actually-received"
)

// BatchInvariants is the COMPLETE set this harness evaluates, declared beside the
// classifier rather than derived from it. A verdict — or a negative control — over a
// partial set is not the verdict this gate promises, and without a declared set an
// invariant that stopped being appended is invisible to both.
var BatchInvariants = []string{
	InvBatchLoadFloor,
	InvBatchFanoutComplete,
	InvBatchRoundTrip,
	InvBatchDevicesReceived,
	InvBatchTargetOnly,
	InvBatchReplayIdempotent,
	InvBatchRefusedWhole,
}

// Batch-harness defaults.
const (
	DefaultBatchTargets    = 12
	DefaultBatchBystanders = 4
	DefaultBatchPoison     = 1
	DefaultBatchBackground = 50
	DefaultBatchBgInterval = 200 * time.Millisecond
	// DefaultBatchReplays is K: how many replays of the same token fire AT ONCE. A
	// serial replay proves nothing a single call does not; the concurrency is the test.
	DefaultBatchReplays     = 8
	DefaultBatchMinAccepted = 1000
	DefaultBatchPoll        = 3 * time.Second
	// DefaultBatchTimeout backstops the wait for every row to reach SUCCESSFUL through
	// dispatch (a 30s sweep) → MQTT → response, under load.
	DefaultBatchTimeout = 240 * time.Second
	DefaultBatchSettle  = 10 * time.Second
	// batchPageSize is generous relative to a tens-of-rows batch, and the read fails
	// closed when totalRecords outruns it rather than comparing a truncated multiset.
	batchPageSize = 500
)

// BatchConfig configures one fleet-write run.
type BatchConfig struct {
	Seed int64

	TargetDevices     int
	BystanderDevices  int
	PoisonDevices     int
	BackgroundDevices int

	BackgroundInterval time.Duration
	Concurrency        int

	Replays     int
	MinAccepted int64

	Poll    time.Duration
	Timeout time.Duration
	Settle  time.Duration

	MqttBroker      string
	MqttTLSInsecure bool

	// Control names a negative control to run instead of a plain pass.
	Control string
}

// withDefaults fills every unset field. UNSET means exactly zero — a negative is a
// caller mistake and is passed through so Validate can say so, rather than folded
// into the default where it would make Validate's floors unreachable.
func (c BatchConfig) withDefaults() BatchConfig {
	if c.TargetDevices == 0 {
		c.TargetDevices = DefaultBatchTargets
	}
	if c.BystanderDevices == 0 {
		c.BystanderDevices = DefaultBatchBystanders
	}
	if c.PoisonDevices == 0 {
		c.PoisonDevices = DefaultBatchPoison
	}
	if c.BackgroundDevices == 0 {
		c.BackgroundDevices = DefaultBatchBackground
	}
	if c.BackgroundInterval == 0 {
		c.BackgroundInterval = DefaultBatchBgInterval
	}
	if c.Replays == 0 {
		c.Replays = DefaultBatchReplays
	}
	if c.MinAccepted == 0 {
		c.MinAccepted = DefaultBatchMinAccepted
	}
	if c.Poll == 0 {
		c.Poll = DefaultBatchPoll
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultBatchTimeout
	}
	if c.Settle == 0 {
		c.Settle = DefaultBatchSettle
	}
	if strings.TrimSpace(c.MqttBroker) == "" {
		c.MqttBroker = defaultMqttBroker
	}
	return c
}

// Validate rejects a config that cannot answer the question it claims to.
func (c BatchConfig) Validate() error {
	if c.TargetDevices < 2 {
		return fmt.Errorf("at least two target devices are required — a one-device batch is a single enqueue with extra steps, and the membership invariant it exists to test cannot distinguish a duplicate from an omission")
	}
	if c.BystanderDevices < 1 {
		return fmt.Errorf("at least one bystander device is required — it is the over-resolution oracle, and a fan-out that wrote rows on devices nobody targeted would otherwise go unseen")
	}
	if c.PoisonDevices < 1 {
		return fmt.Errorf("at least one poison device is required — it is the cause of the whole-batch refusal, and the fail-closed invariant cannot fire without one")
	}
	if c.BackgroundDevices < 0 || c.MinAccepted < 0 {
		return fmt.Errorf("background devices and min-accepted must not be negative")
	}
	if c.Replays < 2 {
		return fmt.Errorf("at least two concurrent replays are required — a serial replay proves nothing a single call does not, and the concurrency is what is under test")
	}
	if c.Poll <= 0 || c.Timeout <= 0 || c.Settle <= 0 {
		return fmt.Errorf("the poll cadence, timeout and settle must be positive")
	}
	if c.Control != "" && c.Control != ControlDeafDevice {
		return fmt.Errorf("unknown control %q (want %q)", c.Control, ControlDeafDevice)
	}
	if c.Control == ControlDeafDevice && c.TargetDevices < 2 {
		return fmt.Errorf("the %q control needs at least 2 target devices: it leaves one without a receiver and the rest must still complete the round trip", ControlDeafDevice)
	}
	return nil
}

func (c BatchConfig) load() sim.Load {
	return sim.Load{DeviceCount: 0, EmitInterval: c.BackgroundInterval, Concurrency: c.Concurrency}
}

// harnessManifest builds the batch topology: two profiles, two device types, four
// populations. Pure — (config, seed) fully determines it.
func (c BatchConfig) harnessManifest() sim.SimManifest {
	metric := []sim.MetricSpec{{Key: HarnessMetricKey, Name: "Temperature", DataType: "DOUBLE", Unit: "C"}}
	return sim.SimManifest{
		Name: "harness-batch",
		Seed: c.Seed,
		Profiles: []sim.ProfileSpec{
			{Token: HarnessBatchProfileToken, Name: "Load-test Batch Harness Profile", Category: "sensor", Metrics: metric},
			{Token: HarnessBatchPoisonProfileToken, Name: "Load-test Batch Poison Profile", Category: "sensor", Metrics: metric},
		},
		DeviceTypes: []sim.DeviceTypeSpec{
			{Token: HarnessBatchDeviceTypeToken, Name: "Load-test Batch Harness Device", ProfileToken: HarnessBatchProfileToken},
			{Token: HarnessBatchPoisonDeviceTypeToken, Name: "Load-test Batch Poison Device", ProfileToken: HarnessBatchPoisonProfileToken},
		},
		Populations: []sim.PopulationSpec{
			{OfType: HarnessBatchDeviceTypeToken, Count: c.TargetDevices, TokenPattern: batchTargetTokenPattern, ExternalIdPattern: "HARNESS-BATCH-TGT-{n:03d}"},
			{OfType: HarnessBatchDeviceTypeToken, Count: c.BystanderDevices, TokenPattern: batchBystanderTokenPattern, ExternalIdPattern: "HARNESS-BATCH-BY-{n:03d}"},
			{OfType: HarnessBatchPoisonDeviceTypeToken, Count: c.PoisonDevices, TokenPattern: batchPoisonTokenPattern, ExternalIdPattern: "HARNESS-BATCH-POISON-{n:02d}"},
			{OfType: HarnessBatchDeviceTypeToken, Count: c.BackgroundDevices, TokenPattern: batchBgTokenPattern, ExternalIdPattern: "HARNESS-BATCH-BG-{n:05d}"},
		},
	}
}

// --- observations -------------------------------------------------------------

// batchRecord is the part of a createCommandBatch response the oracle reconciles
// against. Created distinguishes "the server answered with a batch" from "the server
// answered with a rejection", which is the first thing every invariant here asks.
type batchRecord struct {
	Created  bool
	Token    string
	Resolved int
	Accepted int
}

// batchRefusal is one device the platform declined, and why.
type batchRefusal struct {
	DeviceToken string
	Code        string
}

// batchObservations is everything the classifier gets: a plain value with no cluster
// in it, so every invariant can be driven into both verdicts by a test.
type batchObservations struct {
	// The first batch — the fan-out under test.
	Batch batchRecord
	// FanoutRows is the deviceToken of EVERY command row the batch created, read as
	// one page and kept as a MULTISET: a duplicate must survive into the classifier,
	// because a duplicate and an omission cancel out in any count.
	FanoutRows []string
	// FanoutTotal is the server's own COUNT(*) for the batch, which the multiset must
	// agree with — a page that silently truncated would otherwise read as an omission.
	FanoutTotal int
	// Statuses tallies the durable lifecycle across the batch's rows, and Successful is
	// how many reached the terminal state.
	Statuses   map[string]int
	Successful int
	// SettleReached records whether the wait for every row to finish actually confirmed
	// it, so a final snapshot that looks complete does not quietly overrule a timeout.
	SettleReached bool

	// BystanderCounts maps a bystander token to how many command rows it holds. Read
	// only after every token has been confirmed to still RESOLVE — the command query
	// answers an unresolvable token with an empty set too, and a zero that means "no
	// such device" would be a no-spurious pass for the wrong reason.
	BystanderCounts map[string]int

	// Replays are the K concurrent re-issues of the SAME token, and ReplayErrs the
	// transport failures among them. ReplayTotalAfter is the batch's row count once
	// they have all returned.
	Replays          []batchRecord
	ReplayErrs       []string
	ReplayTotalAfter int

	// Receipts is the per-device WIRE evidence: how many DISTINCT command tokens each
	// device's own MQTT subscription actually delivered. Misrouted counts frames that
	// arrived addressed to a different device — a dispatch defect, and the only signal
	// that can tell "this device answered" apart from "some device answered".
	Receipts  map[string]int
	Misrouted int
	// Deaf names the target deliberately left without a receiver by the negative
	// control, which must therefore receive nothing.
	Deaf string

	// The whole-batch refusal.
	Refused           batchRecord // Created TRUE here is itself the finding
	RefusedCode       string
	RefusedRefusals   []batchRefusal
	RefusedRecordSeen bool // commandBatchesByToken found a record for the refused token
	// TargetDelta maps a target token to the CHANGE in its command count across the
	// refused attempt. Asserted as a delta, not an absolute: the targets legitimately
	// carry the first batch's commands, and an absolute would have to assume a count
	// and would pass for the wrong reason if the first batch had under-created.
	TargetDelta map[string]int
}

// --- classification (pure) ----------------------------------------------------

// classifyBatch turns the observations into the gated invariants. Pure — a function
// of the observations plus the target membership — so every branch is exhaustively
// unit-testable with no cluster.
func classifyBatch(obs batchObservations, targets, bystanders []string, cfg BatchConfig, accepted int64) []Invariant {
	var invs []Invariant

	invs = append(invs, Invariant{
		Name:   InvBatchLoadFloor,
		Passed: accepted >= cfg.MinAccepted,
		Detail: fmt.Sprintf("background accepted %d (floor %d)", accepted, cfg.MinAccepted),
	})

	// 1. Fan-out complete — the record's own numbers, and then the MEMBERSHIP.
	invs = append(invs, classifyBatchFanout(obs, targets))

	// 2. Round trip — every row the fan-out created reached the terminal state,
	// answered by a real device over MQTT. A stranded QUEUED or SENT fails, and so does
	// a HELD row: every target here is connected, so a command withheld for a device
	// the platform believes absent is a finding, not a state to pass over.
	switch {
	case !obs.Batch.Created:
		invs = append(invs, Invariant{Name: InvBatchRoundTrip, Passed: false,
			Detail: "no batch was created, so no round trip could complete"})
	case len(obs.FanoutRows) == 0:
		invs = append(invs, Invariant{Name: InvBatchRoundTrip, Passed: false,
			Detail: "the batch created no command rows, so there is no round trip to complete"})
	case obs.Successful != len(obs.FanoutRows):
		invs = append(invs, Invariant{Name: InvBatchRoundTrip, Passed: false,
			Detail: fmt.Sprintf("%d of %d command(s) reached the terminal state; statuses %v (settle confirmed: %v)",
				obs.Successful, len(obs.FanoutRows), sortedTally(obs.Statuses), obs.SettleReached)})
	case !obs.SettleReached:
		// Defensive: the final snapshot looks complete but the wait never confirmed it.
		// Treat the disagreement as a failure rather than trusting the later read.
		invs = append(invs, Invariant{Name: InvBatchRoundTrip, Passed: false,
			Detail: "every row reads terminal but the settle wait never confirmed it within the timeout — an unreadable oracle, or a status that changed after the deadline"})
	default:
		invs = append(invs, Invariant{Name: InvBatchRoundTrip, Passed: true,
			Detail: fmt.Sprintf("all %d command(s) reached the terminal state; statuses %v", len(obs.FanoutRows), sortedTally(obs.Statuses))})
	}

	// 2b. The wire witness for the round trip above.
	invs = append(invs, classifyBatchReceipt(obs, targets))

	// 3. Touched only its target. The class a passive oracle cannot catch: a fan-out
	// that over-resolved writes durable rows on devices nobody named.
	var touched []string
	for _, tok := range bystanders {
		if n := obs.BystanderCounts[tok]; n > 0 {
			touched = append(touched, fmt.Sprintf("%s holds %d command(s)", tok, n))
		}
	}
	sort.Strings(touched)
	if len(touched) == 0 {
		invs = append(invs, Invariant{Name: InvBatchTargetOnly, Passed: true,
			Detail: fmt.Sprintf("all %d bystander device(s) hold zero commands", len(bystanders))})
	} else {
		invs = append(invs, Invariant{Name: InvBatchTargetOnly, Passed: false,
			Detail: fmt.Sprintf("%d bystander device(s) were written to by a batch that did not name them: %s", len(touched), strings.Join(touched, "; "))})
	}

	// 4. Replay is idempotent. Re-issuing a token that already names a batch returns
	// THAT batch unchanged — never a rejection, and never topped up, because admitting
	// more devices under one token would make `accepted` a moving number.
	//
	// This asserts the RESPONSE SHAPE, and says so rather than claiming more: the
	// advisory lock the fan-out takes is about ceiling accounting across DIFFERENT
	// concurrent batches, and a replay of the same token is answered before the
	// transaction that takes it ever opens. Reaching that lock needs two batches large
	// enough to overrun the ceiling, which is out of scope here and recorded as a gap.
	invs = append(invs, classifyBatchReplay(obs))

	// 5. Refused whole creates nothing — the fail-closed half.
	invs = append(invs, classifyBatchRefusal(obs, targets))

	return invs
}

// classifyBatchReceipt is the WIRE witness, and it is GATED rather than reported as
// evidence because the durable one cannot stand alone.
//
// 🔴 A DURABLE `SUCCESSFUL` DOES NOT SAY WHICH DEVICE ANSWERED. A device's JWT scopes
// SUBSCRIBE to its own command topic but grants PUBLISH on the tenant-wide responses
// subject, and a response carries a command token and no device identity — so the
// platform marks whatever token it is handed, by whoever hands it over. The sibling
// command harness calls durable status "an authoritative, non-lossy round-trip
// proof"; that is a claim about the platform, and the platform does not enforce it.
//
// Inside this harness the durable half is still sound BY CONSTRUCTION — every
// receiver answers only tokens delivered to its own subscription, and the receiver
// now refuses a frame addressed elsewhere — but "sound by construction" is exactly
// the kind of premise that stops being true without anyone noticing. So the
// construction is ASSERTED: every connected target must have received its command on
// its own wire, the deaf device must have received nothing, and no frame may have
// been misrouted.
//
// It is safe against the documented at-least-once redelivery because it counts
// DISTINCT tokens and demands at least one: redelivery can only inflate the raw
// tally, never reduce the distinct one below what a device really got.
func classifyBatchReceipt(obs batchObservations, targets []string) Invariant {
	if obs.Misrouted > 0 {
		return Invariant{Name: InvBatchDevicesReceived, Passed: false,
			Detail: fmt.Sprintf("%d command(s) were delivered to a device they were not addressed to — a dispatch mis-route. "+
				"Any durable SUCCESSFUL on this run may have been written by a device that is not the one the command names", obs.Misrouted)}
	}
	var silent []string
	for _, tok := range targets {
		if tok == obs.Deaf {
			continue
		}
		if obs.Receipts[tok] == 0 {
			silent = append(silent, tok)
		}
	}
	sort.Strings(silent)
	if len(silent) > 0 {
		return Invariant{Name: InvBatchDevicesReceived, Passed: false,
			Detail: fmt.Sprintf("%d connected target(s) never received their command on their own MQTT subscription: %s. "+
				"If the durable rows nevertheless read SUCCESSFUL, something other than the target device answered them",
				len(silent), strings.Join(silent, ", "))}
	}
	if obs.Deaf != "" && obs.Receipts[obs.Deaf] > 0 {
		return Invariant{Name: InvBatchDevicesReceived, Passed: false,
			Detail: fmt.Sprintf("the control's deaf device %q received %d command(s) despite having no receiver — the control did not arm",
				obs.Deaf, obs.Receipts[obs.Deaf])}
	}
	connected := len(targets)
	if obs.Deaf != "" {
		connected--
	}
	return Invariant{Name: InvBatchDevicesReceived, Passed: true,
		Detail: fmt.Sprintf("all %d connected target(s) received their command on their own subscription, none misrouted", connected)}
}

// classifyBatchFanout is invariant 1: the record's numbers AND the row membership.
func classifyBatchFanout(obs batchObservations, targets []string) Invariant {
	if !obs.Batch.Created {
		return Invariant{Name: InvBatchFanoutComplete, Passed: false,
			Detail: "createCommandBatch returned a rejection rather than a batch, so nothing was fanned out"}
	}
	var problems []string
	if obs.Batch.Resolved != len(targets) {
		problems = append(problems, fmt.Sprintf("the record resolved %d device(s), want %d", obs.Batch.Resolved, len(targets)))
	}
	if obs.Batch.Accepted != len(targets) {
		problems = append(problems, fmt.Sprintf("the record accepted %d device(s), want %d", obs.Batch.Accepted, len(targets)))
	}
	// totalRecords is a real COUNT(*); the rows are a page. Disagreement between them
	// means the page was truncated, and comparing a truncated multiset would report a
	// paging problem as a fan-out omission.
	if obs.FanoutTotal != len(obs.FanoutRows) {
		problems = append(problems, fmt.Sprintf("the server counts %d command row(s) but returned %d — the page was truncated, so the membership below cannot be trusted", obs.FanoutTotal, len(obs.FanoutRows)))
	}
	if obs.FanoutTotal != obs.Batch.Accepted {
		problems = append(problems, fmt.Sprintf("the record says it accepted %d device(s) but %d command row(s) exist", obs.Batch.Accepted, obs.FanoutTotal))
	}
	// 🔴 The membership, as a MULTISET. This is what a count cannot say.
	missing, extra, duplicated := diffMultiset(targets, obs.FanoutRows)
	if len(missing) > 0 {
		problems = append(problems, "no command row for: "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		problems = append(problems, "command rows for devices the batch did not target: "+strings.Join(extra, ", "))
	}
	if len(duplicated) > 0 {
		problems = append(problems, "more than one command row for: "+strings.Join(duplicated, ", "))
	}
	if len(problems) == 0 {
		return Invariant{Name: InvBatchFanoutComplete, Passed: true,
			Detail: fmt.Sprintf("the batch resolved and accepted all %d target(s), and exactly one command row exists for each", len(targets))}
	}
	return Invariant{Name: InvBatchFanoutComplete, Passed: false, Detail: strings.Join(problems, "; ")}
}

// classifyBatchReplay is invariant 4.
func classifyBatchReplay(obs batchObservations) Invariant {
	if !obs.Batch.Created {
		return Invariant{Name: InvBatchReplayIdempotent, Passed: false,
			Detail: "there was no batch to replay"}
	}
	var problems []string
	if len(obs.ReplayErrs) > 0 {
		problems = append(problems, fmt.Sprintf("%d replay(s) failed outright: %s", len(obs.ReplayErrs), strings.Join(obs.ReplayErrs, "; ")))
	}
	if len(obs.Replays) == 0 && len(obs.ReplayErrs) == 0 {
		problems = append(problems, "no replay was issued, so idempotency was not exercised")
	}
	for i, r := range obs.Replays {
		switch {
		case !r.Created:
			problems = append(problems, fmt.Sprintf("replay %d was REJECTED; a replayed token is an idempotent re-read, never a conflict", i+1))
		case r.Token != obs.Batch.Token:
			problems = append(problems, fmt.Sprintf("replay %d returned batch %q, not the original %q", i+1, r.Token, obs.Batch.Token))
		case r.Accepted != obs.Batch.Accepted || r.Resolved != obs.Batch.Resolved:
			problems = append(problems, fmt.Sprintf("replay %d returned resolved=%d accepted=%d, not the original resolved=%d accepted=%d — the batch was topped up",
				i+1, r.Resolved, r.Accepted, obs.Batch.Resolved, obs.Batch.Accepted))
		}
	}
	// The response can be perfectly idempotent while the rows are not. Counting them
	// afterwards is what catches a replay that answered with the original record and
	// created a second set of commands anyway.
	if obs.ReplayTotalAfter != obs.Batch.Accepted {
		problems = append(problems, fmt.Sprintf("after %d concurrent replay(s) the batch holds %d command row(s), not the %d it accepted",
			len(obs.Replays), obs.ReplayTotalAfter, obs.Batch.Accepted))
	}
	if len(problems) == 0 {
		return Invariant{Name: InvBatchReplayIdempotent, Passed: true,
			Detail: fmt.Sprintf("%d concurrent replay(s) all returned the original batch unchanged, and created no further command rows", len(obs.Replays))}
	}
	return Invariant{Name: InvBatchReplayIdempotent, Passed: false, Detail: strings.Join(problems, "; ")}
}

// classifyBatchRefusal is invariant 5: allowPartial:false with one device that cannot
// receive the command must refuse the batch WHOLE and leave nothing behind.
func classifyBatchRefusal(obs batchObservations, targets []string) Invariant {
	// 🔴 The admitted-poison case gets its own wording, because the likeliest cause is
	// not a product defect. A nil batch validator SKIPS the vocabulary gate rather than
	// failing it, so an unwired device-management service secret produces exactly this
	// symptom — and a release gate that reported "fan-out fail-closed is broken" for a
	// cluster misconfiguration burns a day before anyone reads the config.
	if obs.Refused.Created {
		return Invariant{Name: InvBatchRefusedWhole, Passed: false,
			Detail: fmt.Sprintf("a batch naming a device whose profile does not publish %q was CREATED (resolved=%d accepted=%d) with allowPartial:false. "+
				"Before reading this as a fan-out defect, check that the enqueue gate is wired at all: an unconfigured device-management service secret makes the vocabulary check SKIP rather than fail, which looks exactly like this",
				HarnessBatchCommandKey, obs.Refused.Resolved, obs.Refused.Accepted)}
	}

	var problems []string
	if obs.RefusedCode != batchRejectPartialRefused {
		problems = append(problems, fmt.Sprintf("the rejection code is %q, want %q", obs.RefusedCode, batchRejectPartialRefused))
	}
	// The refusal must NAME the offender. Without it an operator bisects a fleet by hand.
	named := false
	for _, r := range obs.RefusedRefusals {
		if strings.HasPrefix(r.DeviceToken, batchPoisonTokenPrefix) {
			named = true
			if r.Code != batchRefusalNotInVocab {
				problems = append(problems, fmt.Sprintf("the poison device was refused as %q, want %q", r.Code, batchRefusalNotInVocab))
			}
		}
	}
	if !named {
		problems = append(problems, fmt.Sprintf("the rejection names no poison device among %d refusal(s), so it does not say which device caused it", len(obs.RefusedRefusals)))
	}
	// "Nothing was created" is two claims, and both are checked: no RECORD, and no
	// change to any target's command count.
	if obs.RefusedRecordSeen {
		problems = append(problems, "a CommandBatch record exists for the refused token — a refused batch must leave no record, because nothing happened to record")
	}
	var moved []string
	for _, tok := range targets {
		if d := obs.TargetDelta[tok]; d != 0 {
			moved = append(moved, fmt.Sprintf("%s gained %d command(s)", tok, d))
		}
	}
	sort.Strings(moved)
	if len(moved) > 0 {
		problems = append(problems, "the refused batch still wrote commands: "+strings.Join(moved, "; "))
	}

	if len(problems) == 0 {
		return Invariant{Name: InvBatchRefusedWhole, Passed: true,
			Detail: fmt.Sprintf("the batch was refused whole as %s naming the poison device, left no record, and changed no target's command count", batchRejectPartialRefused)}
	}
	return Invariant{Name: InvBatchRefusedWhole, Passed: false, Detail: strings.Join(problems, "; ")}
}

// batchControlExpectations maps a control to the EXACT set of invariants it must flip.
//
// The deaf device's row IS created and DISPATCHED — a device the platform has never
// heard of has no state row, and the delivery gate dispatches rather than holds when
// it knows nothing (an earlier comment here had that backwards). Nothing is
// subscribed to receive it, and command delivery over MQTT is live-only, so the
// command is simply lost and the row stays SENT. The fan-out invariant therefore
// still holds and the round trip cannot: exactly one invariant flips.
//
// The receipt invariant does NOT flip, and that is deliberate — it EXPECTS the deaf
// device to receive nothing, so it stays green and the control's expected set stays a
// single name.
// 🔴 ONE CONTROL COVERS ONE OF SEVEN INVARIANTS, AND THE COMMENT ABOVE USED TO
// DESCRIBE A SECOND ONE THAT DOES NOT EXIST. The other — deleting a command row out
// of band, flipping the fan-out invariant instead — needs the perturber to reach
// command-delivery, which it deliberately does not. Until it is built,
// `batch-fanout-complete` (the invariant this whole layer is justified by) has never
// been shown capable of failing on a cluster, and neither have four others. Recorded
// here rather than left to be inferred from this map's length.
var batchControlExpectations = map[string][]string{
	ControlDeafDevice: {InvBatchRoundTrip},
}

// evaluateBatchControl decides whether a batch negative-control run behaved.
func evaluateBatchControl(control string, invs []Invariant) (satisfied bool, detail string) {
	want, known := batchControlExpectations[control]
	return evaluateExpectedFailureSet(control, BatchInvariants, want, known, invs)
}

// --- small pure helpers -------------------------------------------------------

// diffMultiset compares an observed token multiset against the wanted set: which
// wanted tokens have no observation, which observed tokens were never wanted, and
// which appear more than once. A count cannot express any of the three.
func diffMultiset(want, got []string) (missing, extra, duplicated []string) {
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	wanted := map[string]bool{}
	for _, w := range want {
		wanted[w] = true
		switch seen[w] {
		case 0:
			missing = append(missing, w)
		case 1:
		default:
			duplicated = append(duplicated, fmt.Sprintf("%s (%d)", w, seen[w]))
		}
	}
	for g, n := range seen {
		if !wanted[g] {
			extra = append(extra, fmt.Sprintf("%s (%d)", g, n))
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	sort.Strings(duplicated)
	return missing, extra, duplicated
}

// sortedTally renders a status tally in a stable order, so a report diff between two
// runs is about the numbers rather than about Go's map iteration.
func sortedTally(t map[string]int) string {
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, t[k]))
	}
	return "{" + strings.Join(parts, " ") + "}"
}

// --- the batch oracle (reads over the real tenant API) -------------------------

// mutationCreateCommandBatch is the fleet write itself. BOTH halves of the result are
// selected on every call, always: exactly one of them is non-null, and a harness that
// asked only for `batch` would read a REFUSAL as a batch that came back empty.
const mutationCreateCommandBatch = `mutation($request: CommandBatchCreateRequest!) {
  createCommandBatch(request: $request) {
    batch { token resolved accepted refusals { deviceToken code reason } }
    rejection {
      code
      reason
      resolved
      refusals { deviceToken code reason }
      refusalCounts { code count }
    }
  }
}`

// queryBatchCommands reads the command rows one batch created — the present-tense
// question the record deliberately cannot answer, since its resolved/accepted are
// stored facts about the moment it fired.
const queryBatchCommands = `query($c: CommandSearchCriteria!) {
  commands(criteria: $c) {
    results { token deviceToken status }
    pagination { totalRecords }
  }
}`

// queryBatchRecord asks whether a batch RECORD exists at all. A refused batch must
// have none, because nothing happened to record.
const queryBatchRecord = `query($tokens:[String!]!){commandBatchesByToken(tokens:$tokens){token accepted resolved}}`

// batchOracle reads durable batch and command state over the real tenant-scoped
// command-delivery API — the fidelity rule; no database, no admin plane.
type batchOracle struct {
	session  sessionQuerier
	endpoint string
}

type batchMutationResult struct {
	CreateCommandBatch struct {
		Batch *struct {
			Token    string `json:"token"`
			Resolved int    `json:"resolved"`
			Accepted int    `json:"accepted"`
			Refusals []struct {
				DeviceToken string `json:"deviceToken"`
				Code        string `json:"code"`
				Reason      string `json:"reason"`
			} `json:"refusals"`
		} `json:"batch"`
		Rejection *struct {
			Code     string `json:"code"`
			Reason   string `json:"reason"`
			Resolved *int   `json:"resolved"`
			Refusals []struct {
				DeviceToken string `json:"deviceToken"`
				Code        string `json:"code"`
				Reason      string `json:"reason"`
			} `json:"refusals"`
			RefusalCounts []struct {
				Code  string `json:"code"`
				Count int    `json:"count"`
			} `json:"refusalCounts"`
		} `json:"rejection"`
	} `json:"createCommandBatch"`
}

// create fires one batch and returns what came back. The record and the refusal are
// returned SEPARATELY rather than folded into one verdict, because "a batch was
// created" and "a batch was refused" are different answers and every caller here
// branches on which one arrived.
func (o *batchOracle) create(ctx context.Context, req map[string]any) (batchRecord, string, []batchRefusal, error) {
	var out batchMutationResult
	if err := o.session.Query(ctx, o.endpoint, mutationCreateCommandBatch, map[string]any{"request": req}, &out); err != nil {
		return batchRecord{}, "", nil, fmt.Errorf("createCommandBatch: %w", err)
	}
	res := out.CreateCommandBatch
	// 🔴 Exactly one of the two is non-null. Neither being set is not "an empty batch"
	// — it is a response this harness cannot interpret, and folding it into either
	// branch would invent an answer the server did not give.
	if res.Batch == nil && res.Rejection == nil {
		return batchRecord{}, "", nil, fmt.Errorf("createCommandBatch returned neither a batch nor a rejection")
	}
	if res.Batch != nil && res.Rejection != nil {
		return batchRecord{}, "", nil, fmt.Errorf("createCommandBatch returned BOTH a batch and a rejection, which the contract says is impossible")
	}
	if res.Batch != nil {
		// A CREATED batch can still carry refusals — a partial fan-out admits some
		// devices and names the rest. They are returned alongside the record rather than
		// dropped, because "accepted fewer than we targeted" is a finding whose whole
		// value is knowing WHICH devices, and a caller that only saw the numbers would
		// have to bisect the fleet by hand to find out.
		refused := make([]batchRefusal, 0, len(res.Batch.Refusals))
		for _, r := range res.Batch.Refusals {
			refused = append(refused, batchRefusal{DeviceToken: r.DeviceToken, Code: r.Code})
		}
		return batchRecord{Created: true, Token: res.Batch.Token, Resolved: res.Batch.Resolved, Accepted: res.Batch.Accepted}, "", refused, nil
	}
	refusals := make([]batchRefusal, 0, len(res.Rejection.Refusals))
	for _, r := range res.Rejection.Refusals {
		refusals = append(refusals, batchRefusal{DeviceToken: r.DeviceToken, Code: r.Code})
	}
	return batchRecord{}, res.Rejection.Code, refusals, nil
}

// batchRows is one read of a batch's command rows: the per-device multiset, the
// server's own count, and the status tally.
type batchRows struct {
	Devices    []string
	Total      int
	Statuses   map[string]int
	Successful int
}

// rows reads every command row of one batch as a single page.
func (o *batchOracle) rows(ctx context.Context, batchToken string) (batchRows, error) {
	vars := map[string]any{"c": map[string]any{
		"pageNumber": 1, "pageSize": batchPageSize, "batchToken": batchToken,
	}}
	var out struct {
		Commands struct {
			Results []struct {
				Token       string `json:"token"`
				DeviceToken string `json:"deviceToken"`
				Status      string `json:"status"`
			} `json:"results"`
			Pagination struct {
				TotalRecords *int64 `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"commands"`
	}
	if err := o.session.Query(ctx, o.endpoint, queryBatchCommands, vars, &out); err != nil {
		return batchRows{}, fmt.Errorf("commands for batch %s: %w", batchToken, err)
	}
	// A null totalRecords is a fail-closed error, not a silent zero that a fan-out
	// check would read as "the batch created nothing" and blame the platform for.
	if out.Commands.Pagination.TotalRecords == nil {
		return batchRows{}, fmt.Errorf("commands for batch %s: server returned a null totalRecords", batchToken)
	}
	r := batchRows{Total: int(*out.Commands.Pagination.TotalRecords), Statuses: map[string]int{}}
	for _, row := range out.Commands.Results {
		r.Devices = append(r.Devices, row.DeviceToken)
		r.Statuses[row.Status]++
		if row.Status == cmdStatusSuccessful {
			r.Successful++
		}
	}
	return r, nil
}

// recordExists reports whether a CommandBatch record exists for a token.
func (o *batchOracle) recordExists(ctx context.Context, token string) (bool, error) {
	var out struct {
		CommandBatchesByToken []struct {
			Token string `json:"token"`
		} `json:"commandBatchesByToken"`
	}
	if err := o.session.Query(ctx, o.endpoint, queryBatchRecord, map[string]any{"tokens": []string{token}}, &out); err != nil {
		return false, fmt.Errorf("commandBatchesByToken %s: %w", token, err)
	}
	return len(out.CommandBatchesByToken) > 0, nil
}

// deviceCommandCounts reads how many command rows each device holds, over the same
// per-device query the command harness uses.
func (o *batchOracle) deviceCommandCounts(ctx context.Context, tokens []string) (map[string]int, error) {
	inner := &commandOracle{session: o.session, endpoint: o.endpoint}
	out := make(map[string]int, len(tokens))
	for _, tok := range tokens {
		snap, err := inner.snapshot(ctx, tok)
		if err != nil {
			return nil, err
		}
		out[tok] = snap.Count
	}
	return out, nil
}

// awaitBatchSettled polls until every one of the batch's command rows has reached the
// terminal state, or the timeout backstops. Callers stop nothing for it: the batch's
// row COUNT is fixed the moment the fan-out returns, and a row's status only advances
// toward the terminal state, so reaching "all terminal" IS the settled state.
//
// A transient read error is treated as "not settled yet" and retried inside the
// deadline. A timeout is NOT concluded as reached — it is a real undelivered or
// unanswered command, and the classifier fails it.
// It returns everRead so a caller can tell "the batch created no rows" apart from
// "this oracle never managed to read". They fold to the same zero value and mean
// opposite things — one is a fan-out defect, the other is the harness being blind.
func (o *batchOracle) awaitBatchSettled(ctx context.Context, batchToken string, want int, cfg BatchConfig) (rows batchRows, reached, everRead bool) {
	deadline := time.Now().Add(cfg.Timeout)
	var last batchRows
	for {
		r, err := o.rows(ctx, batchToken)
		if err != nil {
			log.Warn().Err(err).Str("batch", batchToken).Msg("transient batch read while settling; retrying")
		} else {
			last, everRead = r, true
			if r.Total == want && r.Successful == r.Total && r.Total > 0 {
				return last, true, everRead
			}
		}
		if time.Now().After(deadline) {
			return last, false, everRead
		}
		select {
		case <-ctx.Done():
			return last, false, everRead
		case <-time.After(cfg.Poll):
		}
	}
}

// --- report -------------------------------------------------------------------

// BatchReport is the machine- and human-readable result of one fleet-write run.
type BatchReport struct {
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
	// cannotMeasure accumulates every reason this run has no opinion. Unexported: it
	// becomes Reason. Its existence is what gives this harness the third disposition
	// its sibling was built with from the start — without it, a missing MQTT route, a
	// dirty tenant and a genuine fleet-write defect all exit 1, and the gate step then
	// annotates every one of them as "the oracle failed its own control".
	cannotMeasure measurability

	TargetDevices    int `json:"targetDevices"`
	BystanderDevices int `json:"bystanderDevices"`
	PoisonDevices    int `json:"poisonDevices"`
	Replays          int `json:"concurrentReplays"`

	BatchToken    string         `json:"batchToken"`
	BatchAccepted int            `json:"batchAccepted"`
	BatchResolved int            `json:"batchResolved"`
	Statuses      map[string]int `json:"commandStatuses"`
	SettleReached bool           `json:"settleReached"`

	RefusedCode     string   `json:"refusedBatchCode,omitempty"`
	RefusedDevices  []string `json:"refusedBatchDevices,omitempty"`
	RefusedRecorded bool     `json:"refusedBatchLeftARecord"`

	// Receiver is the NON-GATING MQTT receive evidence: lossy by design, since delivery
	// is at-least-once and the same command token may arrive more than once. The gate
	// never reads it — the authority is durable command state.
	Receiver cmdreceiver.Report `json:"mqttReceiveEvidence"`

	Control          string `json:"control,omitempty"`
	ControlSatisfied bool   `json:"controlSatisfied,omitempty"`
	ControlDetail    string `json:"controlDetail,omitempty"`

	Invariants []Invariant `json:"invariants"`
}

// Passed is true only for a plain PASS. An INCONCLUSIVE run has NOT passed — it has
// no opinion — so a caller that only asks this question gets the fail-closed answer.
func (r *BatchReport) Passed() bool { return r.Disposition == DispositionPass }

// ExitCode is the gate. THREE codes, matching the presence harness, because the
// alternative is what this one shipped with first: every "could not measure" cause —
// a missing MQTT route, a tenant that is not fresh, an unauthored vocabulary —
// exiting 1 and being annotated as a fleet-write defect or a broken oracle.
//
//	0 — PASS
//	1 — FAIL: a batch invariant was violated (or, in a control run, the control did
//	    not flip exactly the set it must)
//	2 — INCONCLUSIVE: this run could not measure the fan-out. Not a verdict.
func (r *BatchReport) ExitCode() int {
	switch r.Disposition {
	case DispositionPass:
		return 0
	case DispositionFail:
		return 1
	default:
		return 2
	}
}

// finish folds the invariants into the report's verdict, rewriting it for a control
// run. It is a method rather than the tail of the orchestration so the judgement that
// matters most — that a control which BEHAVED reports success, rather than reading as
// a release-blocking finding — is reachable by a test that needs no cluster.
func (r *BatchReport) finish(invs []Invariant, control string) {
	r.Invariants = invs
	r.Control = control
	r.Disposition, r.Reason = decideDisposition(r.cannotMeasure, BatchInvariants, invs)
	if control == "" || r.Disposition == DispositionInconclusive {
		// An unevaluated control must not leave a verdict behind, and an INCONCLUSIVE
		// run is one a control cannot rescue: a perturbation nobody could observe proves
		// nothing about the oracle either.
		r.ControlSatisfied, r.ControlDetail = false, ""
		return
	}
	r.ControlSatisfied, r.ControlDetail = evaluateBatchControl(control, invs)
	if r.ControlSatisfied {
		r.Disposition, r.Reason = DispositionPass, r.ControlDetail
	} else {
		r.Disposition, r.Reason = DispositionFail, r.ControlDetail
	}
}

// JSON renders the report for a CI artifact.
func (r *BatchReport) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// Human renders a terse operator summary.
func (r *BatchReport) Human() string {
	var b strings.Builder
	fmt.Fprintf(&b, "batch-fanout %s (seed %d, tenant %s) — exit %d [0=pass 1=fleet-write defect 2=could not measure]\n",
		r.Disposition, r.Seed, r.Tenant, r.ExitCode())
	fmt.Fprintf(&b, "  %s\n", r.Reason)
	fmt.Fprintf(&b, "  drive: %d bg devices, achieved %.1f ev/s over %.0fs — accepted %d, shed %d, failed %d\n",
		r.Drive.Devices, r.Drive.AchievedRatePS, r.Drive.HoldSeconds, r.Drive.Accepted, r.Drive.Shed, r.Drive.Failed)
	fmt.Fprintf(&b, "  batch %s: resolved %d, accepted %d, statuses %s (settle confirmed: %v); %d target, %d bystander, %d poison, %d concurrent replay(s)\n",
		r.BatchToken, r.BatchResolved, r.BatchAccepted, sortedTally(r.Statuses), r.SettleReached,
		r.TargetDevices, r.BystanderDevices, r.PoisonDevices, r.Replays)
	fmt.Fprintf(&b, "  whole-batch refusal: code %q naming %v; left a record: %v\n", r.RefusedCode, r.RefusedDevices, r.RefusedRecorded)
	fmt.Fprintf(&b, "  mqtt receive (NON-authoritative evidence): %d raw / %d distinct, %d blind device(s)\n",
		r.Receiver.TotalRaw, r.Receiver.TotalDistinct, len(r.Receiver.Blind))
	if r.Control != "" {
		// An UNEVALUATED control prints that it was not judged, never "VIOLATED". A
		// control cannot be violated by a run that could not observe its perturbation,
		// and printing a verdict beside an INCONCLUSIVE disposition gives the reader two
		// answers to one question.
		switch {
		case r.ControlDetail == "":
			fmt.Fprintf(&b, "  control %s: NOT EVALUATED — this run could not measure, so the control proves nothing either\n", r.Control)
		case r.ControlSatisfied:
			fmt.Fprintf(&b, "  control %s: satisfied — %s\n", r.Control, r.ControlDetail)
		default:
			fmt.Fprintf(&b, "  control %s: VIOLATED — %s\n", r.Control, r.ControlDetail)
		}
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

// --- authoring ----------------------------------------------------------------

// batchDefSpec is one command definition this harness publishes.
type batchDefSpec struct {
	Token, Profile, Key, Name string
}

// batchVocabularySpecs is the vocabulary the harness publishes, and it is a
// SPECIFICATION rather than an implementation detail: the poison profile's entry
// exists so that profile is CONSTRAINED, and its key must differ from the batch
// command's or the whole fail-closed half of this harness stops being able to fail.
func batchVocabularySpecs() []batchDefSpec {
	return []batchDefSpec{
		{HarnessBatchDefToken, HarnessBatchProfileToken, HarnessBatchCommandKey, "Load-test batch reset"},
		{HarnessBatchPoisonDefToken, HarnessBatchPoisonProfileToken, HarnessBatchPoisonKey, "Load-test batch decoy"},
	}
}

const queryBatchDefinitions = `query($c: CommandDefinitionSearchCriteria!){
  commandDefinitions(criteria: $c) { results { token commandKey } }
}`

// ensureBatchVocabularies publishes the two command vocabularies this harness needs
// and freezes them into each profile's active version.
//
// 🔴 BOTH profiles get a command definition, and the poison one is the reason. A
// profile with an EMPTY vocabulary is unconstrained, and an unconstrained profile
// accepts any command key — so a poison device built by simply omitting the
// definition would be admitted, and the whole-batch refusal this harness exists to
// prove would never fire. The poison profile therefore publishes a DIFFERENT command,
// which constrains it and makes the batch command genuinely absent from it.
//
// It REFUSES to run if either definition already exists: a pre-existing vocabulary
// means the tenant is not fresh, and residual command rows would skew a durable verdict.
func ensureBatchVocabularies(ctx context.Context, rt *sim.Runtime) error {
	for _, profile := range []string{HarnessBatchProfileToken, HarnessBatchPoisonProfileToken} {
		var existing struct {
			CommandDefinitions struct {
				Results []struct {
					Token      string `json:"token"`
					CommandKey string `json:"commandKey"`
				} `json:"results"`
			} `json:"commandDefinitions"`
		}
		vars := map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 100, "deviceProfile": profile}}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryBatchDefinitions, vars, &existing); err != nil {
			return fmt.Errorf("commandDefinitions for %s: %w", profile, err)
		}
		if len(existing.CommandDefinitions.Results) > 0 {
			return fmt.Errorf("profile %q already publishes %d command definition(s) — the batch harness requires a FRESH tenant (destroy + recreate the sim) so residual command rows cannot skew the durable verdict",
				profile, len(existing.CommandDefinitions.Results))
		}
	}

	for _, def := range batchVocabularySpecs() {
		req := map[string]any{
			"token": def.Token, "deviceProfileToken": def.Profile,
			"commandKey": def.Key, "name": def.Name,
		}
		var created struct {
			CreateCommandDefinition struct {
				Token string `json:"token"`
			} `json:"createCommandDefinition"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateCommandDefinition,
			map[string]any{"request": req}, &created); err != nil {
			return fmt.Errorf("createCommandDefinition %s: %w", def.Token, err)
		}
	}

	// Publish, or the definitions stay drafts — and a created-but-unpublished command
	// is not in the enqueue vocabulary at all, which would leave BOTH profiles
	// unconstrained and both halves of this harness measuring nothing.
	for _, profile := range []string{HarnessBatchProfileToken, HarnessBatchPoisonProfileToken} {
		var published struct {
			PublishDeviceProfile struct {
				Version int `json:"version"`
			} `json:"publishDeviceProfile"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationPublishCommandProfile,
			map[string]any{"token": profile}, &published); err != nil {
			return fmt.Errorf("publishDeviceProfile %s: %w", profile, err)
		}
		log.Info().Str("profile", profile).Int("version", published.PublishDeviceProfile.Version).
			Msg("published the harness command vocabulary")
	}
	return nil
}

// --- orchestration ------------------------------------------------------------

// RunBatch runs the L2d-4 fleet-write harness end to end: provision two profiles and
// four cohorts, publish both command vocabularies, connect the target cohort's MQTT
// receivers, fan a command out under background load, wait for every row to complete
// its round trip, replay the token concurrently, and finally fire a batch that MUST be
// refused whole. The caller turns the verdict into an exit code.
func RunBatch(ctx context.Context, hs *sim.Handshake, cfg BatchConfig) (*BatchReport, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(hs.Endpoints.CommandMgmtGraphQL) == "" {
		return nil, fmt.Errorf("handshake has no endpoints.commandMgmtGraphQL — the batch harness reconciles against command-delivery's durable command and batch queries and cannot run without it")
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
	oracle := &batchOracle{session: rt.Session, endpoint: rt.Endpoints.CommandMgmtGraphQL}

	counter := &graphqlEventCounter{session: rt.Session, endpoint: eventEndpoint}
	if err := requireCleanTenant(ctx, counter); err != nil {
		return nil, err
	}
	if err := sim.Provision(ctx, rt, manifest); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	if err := ensureBatchVocabularies(ctx, rt); err != nil {
		return nil, fmt.Errorf("author batch vocabularies: %w", err)
	}

	cohorts, err := partitionByPrefixes(rt.Devices,
		[]string{batchTargetTokenPrefix, batchBystanderTokenPrefix, batchPoisonTokenPrefix, batchBgTokenPrefix})
	if err != nil {
		return nil, err
	}
	targets, bystanders, poison, background := cohorts[0], cohorts[1], cohorts[2], cohorts[3]
	if len(targets) < 2 || len(bystanders) == 0 || len(poison) == 0 {
		return nil, fmt.Errorf("harness provisioned %d target + %d bystander + %d poison device(s) — each answers a different question and none is optional",
			len(targets), len(bystanders), len(poison))
	}
	targetTokens, bystanderTokens := tokensOf(targets), tokensOf(bystanders)

	// Residual-state guard: no cohort device may already carry a command row.
	pre, err := oracle.deviceCommandCounts(ctx, append(append([]string{}, targetTokens...), bystanderTokens...))
	if err != nil {
		return nil, fmt.Errorf("pre-run command check: %w", err)
	}
	for tok, n := range pre {
		if n > 0 {
			return nil, fmt.Errorf("device %q already holds %d command(s) before the run — use a fresh tenant", tok, n)
		}
	}

	var mqttTLS *tls.Config
	if strings.HasPrefix(cfg.MqttBroker, "ssl://") || strings.HasPrefix(cfg.MqttBroker, "tls://") {
		// #nosec G402 — the same opt-in as the other harnesses: a kind/dev gateway cert
		// whose SAN is the in-cluster DNS name cannot be matched by a 127.0.0.1 forward.
		mqttTLS = &tls.Config{InsecureSkipVerify: cfg.MqttTLSInsecure}
	}
	receiver := cmdreceiver.New(rt.InstanceId, rt.Tenant, cfg.MqttBroker, mqttTLS)
	defer receiver.Close()

	// 🔴 THE RECEIVERS CONNECT BEFORE THE BATCH FIRES, and the ordering is load-bearing
	// rather than tidy — though not for the reason an earlier comment here gave.
	//
	// It is NOT the hold gate: a device the platform has never heard of has no state
	// row, and the gate dispatches rather than holds when it knows nothing. It is
	// delivery. A command reaches an MQTT device only over the live path — it is
	// published over NATS, never enters the broker's message store, and a persistent
	// subscription's durable consumer never sees it. Firing the batch before the
	// receivers exist would publish every command to a topic nobody is listening on,
	// and the whole cohort would sit at SENT while the fan-out itself was perfectly
	// correct. That is the deaf-device control's mechanism, applied by accident to
	// every target at once.
	//
	// The control leaves the LAST target deaf on purpose: its row is still created, so
	// the fan-out invariant holds, and nothing ever answers it, so the round trip
	// cannot complete. One invariant, deterministically, with no privileged access.
	connect := targets
	var deaf, obsDeaf string
	if cfg.Control == ControlDeafDevice {
		deaf = targets[len(targets)-1].Token
		obsDeaf = deaf
		connect = targets[:len(targets)-1]
		log.Warn().Str("control", cfg.Control).Str("device", deaf).
			Msg("negative control armed: one target device is left without a receiver")
	}
	for _, d := range connect {
		if serr := receiver.Subscribe(ctx, d.Token, d.CredentialId); serr != nil {
			return nil, fmt.Errorf("connect MQTT receiver for %q: %w (check the dc-nats:1883 port-forward)", d.Token, serr)
		}
	}

	rt.Devices = background
	start := time.Now()
	rt.Stats.Reset(start)
	runCtx, cancelDrive := context.WithCancel(ctx)
	var bgWg sync.WaitGroup
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		driveBackground(runCtx, rt, cfg.BackgroundInterval)
	}()
	stopDrive := func() {
		cancelDrive()
		bgWg.Wait()
	}

	obs := batchObservations{
		BystanderCounts: map[string]int{},
		TargetDelta:     map[string]int{},
		Statuses:        map[string]int{},
	}
	report := &BatchReport{
		Seed: cfg.Seed, Tenant: hs.Tenant, StartedAt: start.UTC(),
		TargetDevices: len(targets), BystanderDevices: len(bystanders),
		PoisonDevices: len(poison), Replays: cfg.Replays,
	}

	// --- the fan-out --------------------------------------------------------
	//
	// allowPartial is TRUE here, and that is a diagnostic choice rather than a lax one.
	// Every target is valid, so a full admission is the only correct outcome and the
	// invariant demands accepted == resolved == len(targets) — but if the platform
	// admits fewer, a partial fan-out returns a RECORD naming which devices it refused,
	// where allowPartial:false would return an opaque whole-batch rejection and leave
	// the operator to bisect the fleet by hand. The fail-closed behaviour is tested
	// deliberately and separately, by the refused batch below.
	batchToken := fmt.Sprintf("harness-batch-%d", cfg.Seed)
	batchReq := map[string]any{
		"token": batchToken, "name": HarnessBatchCommandKey,
		"deviceTokens": targetTokens, "allowPartial": true,
	}
	rec, code, refusals, err := oracle.create(ctx, batchReq)
	if err != nil {
		stopDrive()
		return nil, fmt.Errorf("fire the fan-out batch: %w", err)
	}
	obs.Batch = rec
	if !rec.Created {
		log.Error().Str("code", code).Interface("refusals", refusals).Msg("the fan-out batch was REFUSED")
	} else if len(refusals) > 0 {
		log.Warn().Interface("refusals", refusals).Msg("the fan-out batch admitted only part of its target set")
	}

	if rec.Created {
		rows, reached, everRead := oracle.awaitBatchSettled(ctx, rec.Token, rec.Accepted, cfg)
		obs.FanoutRows, obs.FanoutTotal = rows.Devices, rows.Total
		obs.Statuses, obs.Successful, obs.SettleReached = rows.Statuses, rows.Successful, reached
		// 🔴 A READ THAT NEVER SUCCEEDED IS NOT "THE BATCH CREATED NO ROWS". The zero
		// value of a failed poll folds into an empty row set, which the classifier
		// correctly reads as a fan-out that produced nothing — and would print exit 1,
		// "the batch created no command rows", for a command-delivery GraphQL outage.
		// The sibling command harness threads exactly this flag out of the identical
		// loop and calls it oracleReadOK; this one did not.
		if !everRead {
			report.cannotMeasure.cannot("the batch's command rows could not be read even once within %s — command-delivery's query was unreachable, "+
				"which is not evidence that the fan-out created nothing", cfg.Timeout)
		}
	}

	// --- the concurrent replay ----------------------------------------------
	if rec.Created {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := 0; i < cfg.Replays; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, _, _, rerr := oracle.create(ctx, batchReq)
				mu.Lock()
				defer mu.Unlock()
				if rerr != nil {
					obs.ReplayErrs = append(obs.ReplayErrs, rerr.Error())
					return
				}
				obs.Replays = append(obs.Replays, r)
			}()
		}
		wg.Wait()
		after, aerr := oracle.rows(ctx, rec.Token)
		if aerr != nil {
			stopDrive()
			return nil, fmt.Errorf("post-replay batch read: %w", aerr)
		}
		obs.ReplayTotalAfter = after.Total
	}

	// --- the whole-batch refusal --------------------------------------------
	//
	// Measured as a DELTA around the attempt, never as an absolute: the targets
	// legitimately carry the fan-out's commands, so an absolute would have to assume a
	// count — and would pass for the wrong reason if the fan-out had under-created.
	before, err := oracle.deviceCommandCounts(ctx, targetTokens)
	if err != nil {
		stopDrive()
		return nil, fmt.Errorf("pre-refusal command read: %w", err)
	}
	refusedToken := batchToken + "-refused"
	refusedReq := map[string]any{
		"token": refusedToken, "name": HarnessBatchCommandKey,
		"deviceTokens": append(append([]string{}, targetTokens...), poison[0].Token),
		"allowPartial": false,
	}
	refusedRec, refusedCode, refusedRefusals, err := oracle.create(ctx, refusedReq)
	if err != nil {
		stopDrive()
		return nil, fmt.Errorf("fire the refused batch: %w", err)
	}
	obs.Refused, obs.RefusedCode, obs.RefusedRefusals = refusedRec, refusedCode, refusedRefusals

	recorded, err := oracle.recordExists(ctx, refusedToken)
	if err != nil {
		stopDrive()
		return nil, fmt.Errorf("refused-batch record read: %w", err)
	}
	obs.RefusedRecordSeen = recorded

	// Settle before the delta's second read, so a command the refused attempt created
	// LATE is still counted. BOUNDED OBSERVATION: a row written beyond this window is
	// outside what this run can see.
	select {
	case <-ctx.Done():
	case <-time.After(cfg.Settle):
	}
	afterCounts, err := oracle.deviceCommandCounts(ctx, targetTokens)
	if err != nil {
		stopDrive()
		return nil, fmt.Errorf("post-refusal command read: %w", err)
	}
	for _, tok := range targetTokens {
		obs.TargetDelta[tok] = afterCounts[tok] - before[tok]
	}

	// --- bystanders, read LAST ----------------------------------------------
	//
	// 🔴 THE OVER-RESOLUTION ORACLE HAS TO SEE EVERY FLEET WRITE, AND IT USED TO SEE
	// ONE OF THREE. Reading the bystanders straight after the first fan-out left the 8
	// concurrent replays and the refused batch entirely outside its window — so a
	// replay that over-resolved, or a refusal that wrote rows onto a device it never
	// named, was invisible to the invariant whose whole job is catching exactly that.
	//
	// Guarded before it is trusted: the command query answers an UNRESOLVABLE device
	// token with an empty set too, so a zero that really means "no such device" would
	// be a clean bill of health for the wrong reason.
	if err := requireDevicesExist(ctx, rt, bystanderTokens); err != nil {
		stopDrive()
		return nil, fmt.Errorf("bystander existence check: %w", err)
	}
	counts, err := oracle.deviceCommandCounts(ctx, bystanderTokens)
	if err != nil {
		stopDrive()
		return nil, fmt.Errorf("bystander command read: %w", err)
	}
	obs.BystanderCounts = counts

	stopDrive()
	end := time.Now()
	rt.Stats.Freeze(end)
	snap := rt.Stats.Snapshot(end)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("run aborted: %w", ctx.Err())
	}

	receiver.Close()
	receiverReport := receiver.Report()
	obs.Receipts = make(map[string]int, len(receiverReport.Devices))
	for tok, dr := range receiverReport.Devices {
		obs.Receipts[tok] = dr.Distinct
	}
	obs.Misrouted = receiverReport.TotalMisrouted
	obs.Deaf = obsDeaf

	// The same two driver signals the presence harness reads, and for the same reason:
	// a shed run is at the tenant's ingest ceiling, which presence transitions share —
	// and a target the platform believes absent has its command HELD, which fails the
	// round trip for a governance reason rather than a delivery one. A large FAILED
	// share is the shared port-forward dropping, which is a tunnel fault.
	if snap.Shed > 0 {
		report.cannotMeasure.cannot("the background fleet was shed %d time(s) at the per-tenant ingest ceiling. Presence transitions "+
			"share that ceiling, and a target the platform believes absent has its command HELD rather than dispatched — so a failed "+
			"round trip here cannot be told apart from a governed one", snap.Shed)
	}
	if snap.Failed > 0 && snap.Failed*10 >= snap.Emitted {
		report.cannotMeasure.cannot("%d of %d background emit(s) failed outright (not shed — real transport errors), so the contention "+
			"this verdict rests on was not applied. At this gate the usual cause is the shared device-plane port-forward dropping", snap.Failed, snap.Failed+snap.Emitted)
	}

	report.FinishedAt = end.UTC()
	report.Drive = DriveStats{
		Devices:        len(background),
		TargetRatePS:   rt.Load.TargetRate(len(background)),
		AchievedRatePS: snap.Rate,
		Accepted:       snap.Emitted,
		Shed:           snap.Shed,
		Failed:         snap.Failed,
		Ticks:          snap.Ticks,
		HoldSeconds:    end.Sub(start).Seconds(),
	}
	report.BatchToken = rec.Token
	report.BatchAccepted, report.BatchResolved = rec.Accepted, rec.Resolved
	report.Statuses, report.SettleReached = obs.Statuses, obs.SettleReached
	report.RefusedCode = obs.RefusedCode
	for _, r := range obs.RefusedRefusals {
		report.RefusedDevices = append(report.RefusedDevices, fmt.Sprintf("%s(%s)", r.DeviceToken, r.Code))
	}
	sort.Strings(report.RefusedDevices)
	report.RefusedRecorded = obs.RefusedRecordSeen
	report.Receiver = receiverReport
	report.finish(classifyBatch(obs, targetTokens, bystanderTokens, cfg, snap.Emitted), cfg.Control)
	return report, nil
}
