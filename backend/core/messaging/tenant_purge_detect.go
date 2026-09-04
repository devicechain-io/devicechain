// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	nats "github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-microservice/core"
)

// Erasing one tenant from the DETECT engine (ADR-077 slice 2c).
//
// This is the one storage system in the platform whose only accessor is a running
// process. The engine's windows, timers, accumulators and dead-man armings live in one
// Go process's memory and are checkpointed to a single row per partition — one opaque
// JSON blob holding every tenant's state at once, with tenancy carried INSIDE it as the
// prefix of each rule id. No SQL predicate reaches into that, and deleting the row is
// not an erasure but a corruption: it is the checkpoint every other tenant's
// replay-correct recovery depends on.
//
// So the erasure is a REQUEST to the process that owns the state, and this file is the
// wire between the purge coordinator and that process. It sits in core/messaging for the
// same reason the broker's subject arithmetic does: the request subject and the payload
// shape have to agree on both sides, and the way that goes wrong is not a build failure
// but a request nobody answers — which reads exactly like an engine holding nothing.
//
// # Why the subject is not under the instance's own subject tree
//
// Every platform subject is "{instance}.{tenant}.{suffix}", and streams capture that
// tree with wildcards — "{instance}.*.{suffix}" and one level deeper. A control request
// placed in that tree would be captured and DURABLY STORED by any stream whose suffix
// happened to match, today or after someone adds one, and would then be replayed to a
// consumer as though it were a platform message. Rooting it at "$DC." puts it where no
// stream looks, which is the same reasoning that keeps "$JS.API." and "$SYS.REQ." out of
// the message space. Nothing else in the platform publishes under "$DC.".

const (
	// detectPurgeSubjectRoot is the control-subject root: outside every stream's capture
	// space, and outside the "$JS"/"$SYS"/"$MQTT" trees nats-server reserves for itself.
	detectPurgeSubjectRoot = "$DC."

	// detectPurgeSubjectLeaf names the operation. The instance id sits between the root
	// and this, so one broker serving several instances (ADR-048) keeps their eviction
	// requests apart — a responder answers only for its own instance.
	detectPurgeSubjectLeaf = ".detect.tenant-purge"

	// DetectPurgeWindow bounds how long a caller waits for the partitions to answer.
	//
	// It is a GATHER window, not a request timeout: there may be more than one responder
	// (one per DETECT partition), so the call cannot return on the first reply, and there
	// is no count on the wire to tell it when it has them all. The caller supplies the
	// set it expects — read from the partitions that have actually checkpointed — and
	// this bounds the wait when one of them never answers. An engine with no responder at
	// all is detected far sooner than this: the broker replies "no responders" to the
	// request itself.
	DetectPurgeWindow = 5 * time.Second

	// detectPurgeQuiesce is how long the gather keeps listening after the first CLEAN reply
	// received while NO expected partition is still outstanding. Neither an error reply nor
	// a clean one arriving while `awaited` is non-empty starts it — see the gather loop for
	// why each of those is the same bug at a different scale.
	//
	// It exists for exactly one shape: the gather has nothing left to satisfy, so without
	// this the call can only end on the deadline — five seconds per pass, per purging
	// tenant, on the coordinator's goroutine under the instance-wide advisory lock, on any
	// instance whose engine has never committed a checkpoint. In that shape every responder
	// on the broker saw the same request at the same time and none of them owes us a
	// specific answer, so a peer that has not spoken within this of the first is not merely
	// slower; something is wrong with it. The consequence of guessing short is bounded and
	// safe: the partition is reported as not having answered, which DEFERS the purge rather
	// than completing it, and the next pass asks again.
	//
	// It MUST stay well under DetectPurgeWindow, or it stops being a shortcut and the two
	// waits collapse into one. Asserted in the tests rather than left to a reader.
	detectPurgeQuiesce = 300 * time.Millisecond
)

// DetectPurgeSubject is the request subject for one instance's DETECT tenant eviction.
// Both sides derive it here so the responder cannot subscribe to a subject the caller
// does not use — a mismatch that fails as silence, never as an error.
func DetectPurgeSubject(instanceId string) string {
	return detectPurgeSubjectRoot + instanceId + detectPurgeSubjectLeaf
}

// DetectPurgeRequest asks the engine holding a partition to evict one tenant.
//
// It carries the tenant and nothing else — in particular NOT the purge epoch, which
// every other part of ADR-077 keys on. The epoch exists to tell a tenant's residue apart
// from a same-token successor's data in a store that outlives the tenant row. The engine
// holds no such ambiguity: its state is addressed by token alone, it is evicted in full,
// and a successor's state can only be built from events that arrive after the eviction.
// Carrying an epoch the responder could not act on would invite the reader to believe it
// was being checked.
type DetectPurgeRequest struct {
	Tenant string `json:"tenant"`
}

// DetectPurgeReply is one partition's answer.
type DetectPurgeReply struct {
	// PartitionId identifies the answering engine. It is what lets a caller tell a full
	// answer from a partial one: with several partitions running, silence from one is
	// indistinguishable from "there was only ever one" unless the replies say who sent
	// them.
	PartitionId string `json:"partitionId"`
	// Evicted counts the state entries removed — rules, windows, timers, accumulators,
	// armings. Zero from a healthy partition is the steady state after the first pass and
	// is what the settle window is waiting to see.
	Evicted int64 `json:"evicted"`
	// Error is set when the partition could not complete the eviction — it evicted state
	// but failed to commit the checkpoint, or it is a halted split-brain writer. A reply
	// carrying it is NOT evidence of erasure; the caller must treat it as data still held.
	Error string `json:"error,omitempty"`
}

// DetectPurgeResult is everything one eviction request learned.
type DetectPurgeResult struct {
	// Replies is one entry per partition that answered, in arrival order.
	Replies []DetectPurgeReply
	// NoResponders records that the broker answered for them: the subject has no
	// subscriber, so no engine is running on this instance. It is a different fact from
	// an empty Replies after the window elapsed (an engine that is running and did not
	// answer), and the two want different sentences in the deletion record.
	NoResponders bool
}

// PurgeTenantDetect asks every running DETECT engine on the instance to evict a tenant
// and re-checkpoint, and gathers what they report.
//
// It is idempotent by construction: eviction removes state matching the tenant, so a
// second call finds nothing and reports zero — which is exactly what the coordinator's
// settle window needs, since a pass that evicts anything restarts it.
//
// partitions is the set of partition ids the caller already knows have durable state (in
// practice, the rows in detect_snapshots). It is used ONLY to stop gathering early once
// they have all answered; a reply from a partition outside the set is still collected,
// because a running engine that has never checkpointed holds state that no row names yet.
func PurgeTenantDetect(ctx context.Context, nc *nats.Conn, instanceId, tenant string,
	partitions []string) (DetectPurgeResult, error) {
	var res DetectPurgeResult
	// 🔴 THE TENANT IS VALIDATED ON BOTH SIDES OF THIS CALL, AND NEITHER IS REDUNDANT.
	//
	// It does not travel in the subject, so unlike the broker purge it cannot widen a
	// filter here. What it CAN do is widen a prefix match at the far end: the engine
	// evicts every rule id under "{tenant}/", and an empty tenant makes that prefix "/",
	// while a token carrying "/" would match a deeper segment of somebody else's id.
	// Checking it here refuses the request before it is sent; the responder checks it
	// again because that is where the consequence lands, and a responder that trusts its
	// caller is one process away from erasing an instance.
	if err := core.ValidateToken(tenant); err != nil {
		return res, fmt.Errorf("refusing to evict tenant %q from the DETECT engine: %w — the "+
			"engine matches its state by tenant prefix, so a token that is not a plain "+
			"identifier evicts more than the tenant", tenant, err)
	}
	if strings.TrimSpace(instanceId) == "" {
		return res, fmt.Errorf("refusing to evict from the DETECT engine with no instance id: it " +
			"is the whole address of the request subject, so the request would reach no engine " +
			"and the silence would read as an engine holding nothing")
	}
	// The same stall the broker purge guards against: the manager reconnects forever, so
	// while the broker is down a publish is buffered rather than refused and the gather
	// below waits out its whole window on the coordinator's single goroutine.
	if !nc.IsConnected() {
		return res, fmt.Errorf("the broker connection is down, so the DETECT engine cannot be "+
			"asked to evict %q on this pass", tenant)
	}
	ctx, cancel := context.WithTimeout(ctx, DetectPurgeWindow)
	defer cancel()

	payload, err := json.Marshal(DetectPurgeRequest{Tenant: tenant})
	if err != nil {
		return res, fmt.Errorf("encoding the DETECT eviction request for %q: %w", tenant, err)
	}

	// A private inbox with a synchronous subscription, rather than nc.Request: Request
	// hands back the first reply and unsubscribes, which would silently reduce a
	// multi-partition fleet to whichever engine answered fastest.
	inbox := nats.NewInbox()
	//subconfirm:ok the request below goes out on this same connection, so the server reads
	// SUB before PUB — see the paragraph after the defer for the full reasoning
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return res, fmt.Errorf("subscribing to the DETECT eviction reply inbox: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// No Flush between the subscribe and the publish. An earlier draft had one, justified by
	// "otherwise a fast responder replies into a subject the server is not yet routing" —
	// which is not how the client works: both calls append to the same connection write
	// buffer under the same lock, so the server reads SUB before PUB regardless. nats.go's
	// own request path does exactly this (subscribe, publish, wait) with no flush. What the
	// flush actually bought was a broker round trip per pass, on the coordinator's goroutine,
	// under the instance-wide advisory lock.
	if err := nc.PublishRequest(DetectPurgeSubject(instanceId), inbox, payload); err != nil {
		return res, fmt.Errorf("publishing the DETECT eviction request for %q: %w", tenant, err)
	}

	awaited := make(map[string]bool, len(partitions))
	for _, p := range partitions {
		awaited[p] = true
	}
	// 🔴 THE QUIESCENCE WINDOW IS WHAT STOPS THIS COSTING FIVE SECONDS EVERY PASS.
	//
	// The gather has no count on the wire to tell it when it has heard from everyone, so with
	// no expected partitions there is nothing to satisfy and the loop can only end on the
	// deadline. That is not a rare shape: a partition appears in the expected set only once
	// it has committed a checkpoint, and an engine that has processed nothing never sets
	// dirty and so never writes a row. On a quiet instance the responder answers in under a
	// millisecond and the call then blocks for the rest of DetectPurgeWindow — every pass,
	// per purging tenant, holding the advisory lock the whole time.
	//
	// So once ANY reply has arrived, stop waiting on the full window and give stragglers a
	// short one instead. It is a heuristic, and it is bounded in the direction that matters:
	// a partition that misses it is reported as not having answered, which DEFERS the purge
	// rather than completing it, and the next pass asks again.
	gather := ctx
	for {
		msg, err := sub.NextMsgWithContext(gather)
		if errors.Is(err, nats.ErrNoResponders) {
			// 🔴 THIS ARRIVES AS AN ERROR, NOT AS A MESSAGE, and the difference is the whole
			// value of separating it from the branch below. nats-server answers a request with
			// no subscriber by sending an empty 503-status message to the reply inbox, and a
			// SYNC subscription converts that into this sentinel rather than handing it over.
			// Folding it into the timeout case — which is what happens if you only check for a
			// message — turns "no DETECT engine is running on this instance" into an
			// indistinguishable five-second silence, and the ledger then sends an operator to
			// investigate an engine that does not exist.
			res.NoResponders = true
			return res, nil
		}
		if err != nil {
			// The window closed. That is not an error: the caller decides what a missing
			// partition means, and it has the expected set to say WHICH one is missing.
			return res, nil
		}
		var reply DetectPurgeReply
		if err := json.Unmarshal(msg.Data, &reply); err != nil {
			return res, fmt.Errorf("decoding a DETECT eviction reply for %q: %w", tenant, err)
		}
		res.Replies = append(res.Replies, reply)
		// 🔴 ONLY A REPLY THAT ERASED SOMETHING SATISFIES THE PARTITION, and this is not
		// pedantry about error handling — a partition can have TWO responders, and the one
		// that answers first is the one that did not do the work.
		//
		// A writer that lost a split brain latches `stale`, cancels its loop, and keeps
		// running with its subscription intact (Stop runs only at service shutdown). Asked to
		// evict, it fails immediately on the cancelled context — microseconds — while the
		// healthy writer is still doing a real eviction and a database commit. If an error
		// reply cleared the partition from this set, the gather would return on the halted
		// pod's answer every single time and never read the committed one, and the ledger
		// would carry "this tenant's windows are still in its checkpoint" about a partition
		// that had just erased them. Deterministic, self-repeating, and clearable only by
		// noticing a pod that looks healthy.
		if reply.Error == "" && awaited[reply.PartitionId] {
			delete(awaited, reply.PartitionId)
			if len(awaited) == 0 {
				return res, nil // the last partition we were waiting on has committed
			}
		}
		if reply.Error == "" && len(awaited) == 0 && gather == ctx {
			// First clean reply WITH NOTHING STILL EXPECTED: something is alive and
			// answering and no partition we know holds durable state is outstanding, so drop
			// from the full window to the straggler window. Derived from ctx rather than the
			// caller's context, so it can only ever shorten the wait — never outlive
			// DetectPurgeWindow, and still cancelled the moment the caller is.
			//
			// 🔴 AN ERROR REPLY MUST NOT START THIS CLOCK, and leaving it out was the same
			// bug as clearing `awaited` above — reintroduced 260 milliseconds later. The
			// paragraph above says why a partition can answer twice and why the FAST answer
			// is the one that did nothing; a warm standby (ADR-070) answers "I hold no term"
			// in microseconds, and the halted split-brain writer answers just as fast. If
			// that reply shortened the gather to 300ms, the replica that is actually
			// evicting — a sweep of the whole instance's state, a snapshot serialization
			// and a database commit — would have 300ms to finish, and past that the caller
			// records "this tenant's windows are still in its checkpoint" about a partition
			// that erased them. Deterministic, self-repeating, and worse under a warm
			// standby than under the split brain, because a standby is the NORMAL posture
			// rather than a fault.
			//
			// 🔴 NOR MAY A CLEAN REPLY FROM ONE PARTITION START IT WHILE ANOTHER IS STILL
			// EXPECTED, which is the same bug one level out and the one a fleet reaches
			// rather than a rollout. A partition holding nothing of this tenant sweeps
			// nothing, never sets dirty, never commits, and answers in under a millisecond;
			// a partition holding its windows does the full sweep plus a database commit.
			// Both are clean, and the first is systematically faster — so keyed on "a clean
			// reply" alone, the empty partition would hand the busy one 300ms, every pass,
			// for as long as that tenant is being purged. The `awaited` set is exactly the
			// partitions known to hold durable state, so while it is non-empty there is a
			// specific outstanding answer to wait for and no heuristic should displace the
			// window. When it empties, the loop above has already returned.
			//
			// This is UNREACHABLE TODAY — GA runs one partition per instance — and it is
			// written now because the sharded fleet is what makes it live, and by then the
			// symptom (a purge that defers forever, on a ledger nothing re-checks) looks
			// nothing like this line.
			//
			// The cost of not starting it is bounded and lands where it should: a pass in
			// which nothing answered cleanly, or in which an expected partition is still
			// outstanding, waits out the full DetectPurgeWindow. Those are the cases where
			// something really is wrong, and the wait buys the one thing worth waiting for —
			// the chance that the replica doing the work still answers.
			quiesced, cancelQuiesce := context.WithTimeout(ctx, detectPurgeQuiesce)
			defer cancelQuiesce()
			gather = quiesced
		}
	}
}
