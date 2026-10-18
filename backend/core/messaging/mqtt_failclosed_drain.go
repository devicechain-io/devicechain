// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	nats "github.com/nats-io/nats.go"
)

// WHETHER A PLATFORM DURABLE IS DRAINED IS READ FROM THE STORE, NOT FROM THE DURABLE.
//
// 🔴 A DURABLE'S PENDING COUNT IS NOT EVIDENCE THAT IT IS DRAINED. The broker computes a
// consumer's NumPending on the consumer's LEADER, from that server's own copy of the stream,
// and resets it to 0 whenever the consumer's next sequence is past the last sequence that copy
// holds (nats-server 2.15, consumer.checkNumPending). A leader whose copy is behind, after a
// consumer or stream leader change or a server that was killed and is catching up, can
// therefore report 0 pending and 0 ack pending while the store holds messages it has yet to
// deliver. That is read from the server's code, NOT observed: an earlier multi-process chaos
// run seemed to show it for 45 to 90 s, but those zeros were ConsumerInfo requests that timed
// out during the SIGKILLs and that the harness printed as 0, and runs on three nats-server
// 2.15.1 processes that moved the events durable's leader onto a server catching up on 250k
// messages saw the count right every time. The guard stands on the code path. A finish that
// took such a zero for "drained" would delete the durables, and with no consumer left,
// interest retention removes the store copies, which are the only copies of what pods with
// the setting on skipped: messages devices were PUBACKed on.
//
// What the finish relies on instead:
//
//   - THE ACK FLOOR. It is the consumer's replicated state; a leader that is behind reports it
//     LOWER, never higher, so it can only make the finish wait.
//   - THE STORE ITSELF, asked through its leader (a message get, which only the stream leader
//     answers, unlike a direct get): the first message at or above the ack floor + 1 that the
//     durable's filter matches. If it is at or below the drain cut, the durable is not drained.
//     The answer is only taken from a leader that holds the whole cut (its last sequence is at
//     or above it) and that was the leader both before and after the get.
//   - A SETTLED CLUSTER. The store and the durable each have a leader, every replica is
//     online and current, and as many replicas as configured are reported. A pass that finds
//     less does not finish and the next pass looks again: finishing late costs duplicates
//     (both copies of a publish are forwarded meanwhile), finishing early costs loss. The
//     drain cut itself is recorded only from a settled store too: it is the last sequence as
//     the store's leader has it, and a leader still applying what it was elected with would
//     put it below copies it does not hold yet.

// mqttDrainSettleTries is how many times a finish pass reads a durable whose cluster is not
// settled before it leaves the durable to the next pass, and mqttDrainSettleWait the pause
// between reads. A follower under steady load is often a few entries behind for an instant;
// a few reads apart let a pass that would have finished not wait a whole interval for it.
var (
	mqttDrainSettleTries = 5
	mqttDrainSettleWait  = 200 * time.Millisecond
)

// jsErrNoMessageFound is JetStream's API error code for a message get that matched nothing.
const jsErrNoMessageFound = 10037

// mqttDrainVerdict is what a pass learned about one durable: whether it is drained, and when
// it is not, whether that is because the cluster could not be trusted to say (unsettled)
// rather than because the store holds what the durable has not acked.
type mqttDrainVerdict struct {
	drained   bool
	unsettled bool
	why       string
}

// mqttDurableDrained decides from the store whether the platform durable d holds nothing at or
// below the drain cut that it has not acked, retrying a cluster that is not settled a few
// times (mqttDrainSettleTries). absent is true when the durable does not exist.
func (nmgr *NatsManager) mqttDurableDrained(ctx context.Context, d MqttFailClosedDurable, drainTo uint64) (
	v mqttDrainVerdict, absent bool, err error) {
	for try := 1; ; try++ {
		v, absent, err = nmgr.mqttDurableDrainedOnce(ctx, d, drainTo)
		if err != nil || absent || !v.unsettled || try >= mqttDrainSettleTries {
			return v, absent, err
		}
		select {
		case <-ctx.Done():
			return v, absent, ctx.Err()
		case <-time.After(mqttDrainSettleWait):
		}
	}
}

func (nmgr *NatsManager) mqttDurableDrainedOnce(ctx context.Context, d MqttFailClosedDurable, drainTo uint64) (
	mqttDrainVerdict, bool, error) {
	opt := nats.Context(ctx)
	before, err := nmgr.js.StreamInfo(MqttMessageStore, opt)
	if err != nil {
		return mqttDrainVerdict{}, false, fmt.Errorf("reading %s: %w", MqttMessageStore, err)
	}
	replicas := max(before.Config.Replicas, 1)
	if why := unsettledGroup(before.Cluster, replicas, true); why != "" {
		return mqttDrainVerdict{unsettled: true, why: MqttMessageStore + ": " + why}, false, nil
	}
	ci, err := nmgr.js.ConsumerInfo(MqttMessageStore, d.Name, opt)
	if errors.Is(err, nats.ErrConsumerNotFound) {
		return mqttDrainVerdict{}, true, nil
	}
	if err != nil {
		return mqttDrainVerdict{}, false, fmt.Errorf("reading the platform durable %s on %s: %w", d.Name, MqttMessageStore, err)
	}
	if why := unsettledGroup(ci.Cluster, consumerReplicas(ci, replicas), true); why != "" {
		return mqttDrainVerdict{unsettled: true, why: d.Name + ": " + why}, false, nil
	}
	floor := ci.AckFloor.Stream
	if floor >= drainTo {
		return mqttDrainVerdict{drained: true, why: fmt.Sprintf("acked up to %d, at or past the cut %d", floor, drainTo)}, false, nil
	}
	if before.State.LastSeq < drainTo {
		// A leader whose copy does not reach the cut cannot say what is below it.
		return mqttDrainVerdict{unsettled: true, why: fmt.Sprintf("%s: its leader holds up to %d, below the cut %d",
			MqttMessageStore, before.State.LastSeq, drainTo)}, false, nil
	}
	seq, found, err := nmgr.mqttStoreNextSeq(ctx, floor+1, mqttDurableSubject(d))
	if err != nil {
		return mqttDrainVerdict{}, false, err
	}
	after, err := nmgr.js.StreamInfo(MqttMessageStore, opt)
	if err != nil {
		return mqttDrainVerdict{}, false, fmt.Errorf("reading %s: %w", MqttMessageStore, err)
	}
	if !sameLeader(before.Cluster, after.Cluster) {
		return mqttDrainVerdict{unsettled: true, why: MqttMessageStore + ": its leader changed during the look"}, false, nil
	}
	if found && seq <= drainTo {
		return mqttDrainVerdict{why: fmt.Sprintf("the store holds sequence %d for it, at or below the cut %d and above "+
			"its ack floor %d", seq, drainTo, floor)}, false, nil
	}
	return mqttDrainVerdict{drained: true, why: fmt.Sprintf("the store holds nothing for it from %d to the cut %d",
		floor+1, drainTo)}, false, nil
}

// mqttSettledStore is info when the store it describes is settled, or a fresh look at it that
// is, read again a few times (mqttDrainSettleTries) as mqttDurableDrained does; why says what
// is unsettled when none was.
func (nmgr *NatsManager) mqttSettledStore(ctx context.Context, info *nats.StreamInfo) (*nats.StreamInfo, string, error) {
	for try := 1; ; try++ {
		why := unsettledGroup(info.Cluster, max(info.Config.Replicas, 1), true)
		if why == "" {
			return info, "", nil
		}
		if try >= mqttDrainSettleTries {
			return nil, why, nil
		}
		select {
		case <-ctx.Done():
			return nil, why, ctx.Err()
		case <-time.After(mqttDrainSettleWait):
		}
		fresh, err := nmgr.js.StreamInfo(MqttMessageStore, nats.Context(ctx))
		if err != nil {
			return nil, "", fmt.Errorf("reading %s: %w", MqttMessageStore, err)
		}
		info = fresh
	}
}

// mqttDurableSubject is the subject a message get matches d's messages with: its filter, or,
// for the unfiltered events durable, every subject of the store.
func mqttDurableSubject(d MqttFailClosedDurable) string {
	if d.Filter == "" {
		return mqttMsgsSubjectPrefix + ">"
	}
	return d.Filter
}

// mqttStoreNextSeq is the sequence of the first message in the store at or above from whose
// subject matches subject, found false when there is none. It is a message get, not a direct
// get: only the stream's leader answers it, where a direct get may be answered by any replica,
// one that is behind included.
func (nmgr *NatsManager) mqttStoreNextSeq(ctx context.Context, from uint64, subject string) (uint64, bool, error) {
	req, err := json.Marshal(struct {
		Seq     uint64 `json:"seq"`
		NextFor string `json:"next_by_subj"`
	}{from, subject})
	if err != nil {
		return 0, false, err
	}
	rctx, cancel := context.WithTimeout(ctx, mqttFailClosedRequestTimeout)
	defer cancel()
	msg, err := nmgr.nc.RequestWithContext(rctx, "$JS.API.STREAM.MSG.GET."+MqttMessageStore, req)
	if err != nil {
		return 0, false, fmt.Errorf("looking in %s for %s from %d: %w", MqttMessageStore, subject, from, err)
	}
	var resp struct {
		Error   *jsAPIError `json:"error"`
		Message *struct {
			Seq uint64 `json:"seq"`
		} `json:"message"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return 0, false, fmt.Errorf("looking in %s for %s from %d: %w", MqttMessageStore, subject, from, err)
	}
	if resp.Error != nil {
		if resp.Error.ErrCode == jsErrNoMessageFound {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("looking in %s for %s from %d: %s", MqttMessageStore, subject, from, resp.Error.Description)
	}
	if resp.Message == nil || resp.Message.Seq == 0 {
		return 0, false, fmt.Errorf("looking in %s for %s from %d: the answer names no message", MqttMessageStore, subject, from)
	}
	return resp.Message.Seq, true, nil
}

// consumerReplicas is the replica count of the consumer ci on a stream of streamReplicas: its
// own, or the stream's when it sets none.
func consumerReplicas(ci *nats.ConsumerInfo, streamReplicas int) int {
	if ci.Config.Replicas > 0 {
		return ci.Config.Replicas
	}
	return streamReplicas
}

// unsettledGroup says why a stream's or consumer's replica group cannot be trusted to answer
// for its contents, or "" when it can: it has no leader, a replica is offline, fewer replicas
// are reported than it has, or (strict) a replica is not current. A single server reports
// itself as the stream's leader with no replicas, and no cluster state for a consumer.
func unsettledGroup(ci *nats.ClusterInfo, replicas int, strict bool) string {
	if ci == nil {
		if replicas <= 1 {
			return ""
		}
		return "the broker reported no cluster state"
	}
	if ci.Leader == "" {
		return "it has no leader"
	}
	if len(ci.Replicas) < replicas-1 {
		return fmt.Sprintf("%d of its %d replicas are reported", len(ci.Replicas)+1, replicas)
	}
	for _, p := range ci.Replicas {
		if p == nil {
			continue
		}
		if p.Offline {
			return fmt.Sprintf("its replica on %s is offline", p.Name)
		}
		if strict && !p.Current {
			return fmt.Sprintf("its replica on %s is not current (%d behind)", p.Name, p.Lag)
		}
	}
	return ""
}

// sameLeader reports whether two looks at a group name the same leader, elected at the same
// time when the broker says when.
func sameLeader(a, b *nats.ClusterInfo) bool {
	if a == nil || b == nil || a.Leader != b.Leader {
		return false
	}
	if a.LeaderSince != nil && b.LeaderSince != nil {
		return a.LeaderSince.Equal(*b.LeaderSince)
	}
	return true
}

// mqttPendingTrusted says why a durable's NumPending must not be reported, or "" when it may.
// It is the gauge's half of the check above: the count comes from the consumer's leader's own
// copy, so it is withdrawn while the store or the durable has no leader or a replica offline,
// and a ZERO is withdrawn when the store, asked through its leader, holds a message for the
// durable past what it has been handed and at or below the last sequence the store held
// before the durable was read (so it was there when the zero was computed).
func (nmgr *NatsManager) mqttPendingTrusted(ctx context.Context, d MqttFailClosedDurable, si *nats.StreamInfo,
	ci *nats.ConsumerInfo) (string, error) {
	if why := unsettledGroup(si.Cluster, max(si.Config.Replicas, 1), false); why != "" {
		return MqttMessageStore + ": " + why, nil
	}
	if why := unsettledGroup(ci.Cluster, consumerReplicas(ci, max(si.Config.Replicas, 1)), false); why != "" {
		return why, nil
	}
	if ci.NumPending != 0 {
		return "", nil
	}
	from := ci.Delivered.Stream + 1
	if from > si.State.LastSeq {
		return "", nil
	}
	seq, found, err := nmgr.mqttStoreNextSeq(ctx, from, mqttDurableSubject(d))
	if err != nil {
		return "", err
	}
	if found && seq <= si.State.LastSeq {
		return fmt.Sprintf("it reports nothing pending, and the store holds sequence %d for it past the %d it has "+
			"been handed", seq, ci.Delivered.Stream), nil
	}
	return "", nil
}
