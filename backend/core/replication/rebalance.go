// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package replication

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	nats "github.com/nats-io/nats.go"
)

// MoveResult is the outcome of one step-down request.
type MoveResult struct {
	Move Move
	// Skipped is set when the move was not sent because the group no longer needed it; Err is nil.
	Skipped bool
	Err     error
}

// ApplyOptions bound the executor.
type ApplyOptions struct {
	// PerMove bounds each request.
	PerMove time.Duration
	// Settle bounds the wait after a move for the new leader to be elected and every replica of the
	// group to be current again. 0 means 15 s.
	Settle time.Duration
	// Poll is the interval of that wait. 0 means 250 ms.
	Poll time.Duration
	// Behind bounds how long a move waits, before it is sent, for a replica of its group that reads
	// as not current to catch up. Under load a follower is briefly behind all the time, and refusing
	// on one reading would refuse the move exactly when it is needed. A replica that is OFFLINE is
	// refused at once. 0 means 12 s.
	Behind time.Duration
}

// stepDownRequest is the JetStream API body that names the server to hand leadership to.
type stepDownRequest struct {
	Placement struct {
		Preferred string `json:"preferred"`
	} `json:"placement"`
}

// stepDownSubject is the API subject for one move.
func stepDownSubject(m Move) string {
	if m.IsConsumer() {
		return "$JS.API.CONSUMER.LEADER.STEPDOWN." + m.Stream + "." + m.Consumer
	}
	return "$JS.API.STREAM.LEADER.STEPDOWN." + m.Stream
}

// groupState reads a group's leader and replicas from the broker.
func groupState(js nats.JetStreamContext, m Move) (leader string, peers []Peer, err error) {
	var c *nats.ClusterInfo
	if m.IsConsumer() {
		ci, err := js.ConsumerInfo(m.Stream, m.Consumer)
		if err != nil {
			return "", nil, err
		}
		c = ci.Cluster
	} else {
		si, err := js.StreamInfo(m.Stream)
		if err != nil {
			return "", nil, err
		}
		c = si.Cluster
	}
	return leaderOf(c), peersOf(c), nil
}

func settled(leader string, peers []Peer) bool {
	if leader == "" {
		return false
	}
	for _, p := range peers {
		if !p.Current || p.Offline {
			return false
		}
	}
	return true
}

// ApplyMoves asks the broker to carry out each move, ONE AT A TIME, and reports each outcome.
//
// Before each move the group is read again, and the move is skipped when the group no longer needs
// it (its leader is already the target, or is neither of the servers the plan saw) or when the
// target is no longer a current, online replica: the plan is a snapshot and the broker moves on.
// After each move the next is not sent until the new leader is elected and every replica of the
// group is current again, so two elections never overlap. A move that does not settle in time stops
// the run: what is left is reported as not attempted rather than sent into a broker that is still
// changing. A failed move before that does not stop the rest.
func ApplyMoves(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, moves []Move, o ApplyOptions) []MoveResult {
	return applyMoves(ctx, moveIO{
		state: func(m Move) (string, []Peer, error) { return groupState(js, m) },
		stepDown: func(ctx context.Context, m Move) error {
			var body stepDownRequest
			body.Placement.Preferred = m.To
			raw, _ := json.Marshal(body)
			rctx, cancel := context.WithTimeout(ctx, o.PerMove)
			defer cancel()
			msg, err := nc.RequestWithContext(rctx, stepDownSubject(m), raw)
			return stepDownOutcome(msg, err)
		},
	}, moves, o)
}

// stateReader reads a move's group from the broker: its leader and replicas.
type stateReader func(m Move) (leader string, peers []Peer, err error)

// moveIO is what applyMoves needs from the broker, so the sequencing can be tested without one.
type moveIO struct {
	state    stateReader
	stepDown func(ctx context.Context, m Move) error
}

func applyMoves(ctx context.Context, io moveIO, moves []Move, o ApplyOptions) []MoveResult {
	if o.Settle <= 0 {
		o.Settle = 15 * time.Second
	}
	if o.Poll <= 0 {
		o.Poll = 250 * time.Millisecond
	}
	if o.Behind <= 0 {
		o.Behind = 12 * time.Second
	}
	out := make([]MoveResult, 0, len(moves))
	var stopped error
	for _, m := range moves {
		if stopped != nil {
			out = append(out, MoveResult{Move: m, Err: stopped})
			continue
		}
		if ctx.Err() != nil {
			stopped = fmt.Errorf("not attempted: %w", ctx.Err())
			out = append(out, MoveResult{Move: m, Err: stopped})
			continue
		}
		skip, err := awaitMoveable(ctx, io.state, m, o)
		if err != nil {
			out = append(out, MoveResult{Move: m, Err: err})
			continue
		}
		if skip {
			out = append(out, MoveResult{Move: m, Skipped: true})
			continue
		}
		if err := io.stepDown(ctx, m); err != nil {
			out = append(out, MoveResult{Move: m, Err: err})
			continue
		}
		landed, err := awaitSettled(ctx, io.state, m, o)
		if err != nil {
			stopped = fmt.Errorf("not attempted: an earlier move did not settle")
			out = append(out, MoveResult{Move: m, Err: err})
			continue
		}
		if landed != m.To {
			// The broker honours the step-down's placement as a preference. A group that settled
			// on a third server is steady, so the run goes on, but the move did not do what the
			// plan said and must not read as done.
			out = append(out, MoveResult{Move: m, Err: fmt.Errorf("moved, not to plan: the group settled on %q, not %q", landed, m.To)})
			continue
		}
		out = append(out, MoveResult{Move: m})
	}
	return out
}

// errOffline and errBehind are why precheck refuses a move.
var (
	errOffline = fmt.Errorf("not moved: a replica of the group is offline")
	errBehind  = fmt.Errorf("not moved: a replica of the group is behind")
)

// awaitMoveable reads the group and judges the move (precheck). A replica that is merely behind is
// waited for, up to o.Behind, and re-read each time; an offline one, or a read that fails, is not.
func awaitMoveable(ctx context.Context, read stateReader, m Move, o ApplyOptions) (skip bool, err error) {
	deadline := time.Now().Add(o.Behind)
	for {
		leader, peers, rerr := read(m)
		if rerr != nil {
			return false, fmt.Errorf("reading the group before the move: %w", rerr)
		}
		skip, err = precheck(leader, peers, m)
		if err != errBehind || time.Now().After(deadline) || ctx.Err() != nil {
			return skip, err
		}
		select {
		case <-ctx.Done():
		case <-time.After(o.Poll):
		}
	}
}

// precheck judges a move against the group as it is NOW. It is skipped when the group no longer needs
// it (the leader is already the target, or is neither of the servers the plan saw) or the target is
// no longer a current, online replica. It is refused, as an error, while any replica of the group is
// offline or behind (errOffline, errBehind; the caller waits for the second): a plan is a snapshot, and an outage that began after it was taken is no time to
// start an election.
func precheck(leader string, peers []Peer, m Move) (skip bool, err error) {
	switch {
	case leader == m.To, leader != m.From:
		return true, nil
	case !targetPresent(peers, m.To):
		return true, nil // not a replica of the group, or offline: the plan is stale
	}
	behind := false
	for _, p := range peers {
		if p.Offline {
			return false, errOffline
		}
		behind = behind || !p.Current
	}
	if behind {
		return false, errBehind
	}
	return false, nil
}

// targetPresent is whether the target is a replica of the group and online. Whether it is current is
// the caller's wait, not a reason to drop the move.
func targetPresent(peers []Peer, to string) bool {
	for _, p := range peers {
		if p.Name == to {
			return !p.Offline
		}
	}
	return false
}

// awaitSettled waits for the group's leader to be elected (somewhere other than where it was) and all
// its replicas current, and returns the leader it saw.
func awaitSettled(ctx context.Context, read stateReader, m Move, o ApplyOptions) (string, error) {
	deadline := time.Now().Add(o.Settle)
	var last string
	for {
		leader, peers, err := read(m)
		// Settled means the leadership LEFT the old leader: a reading of the old one after an accepted
		// step-down is the election not yet seen, and the next move must not start on it.
		if err == nil && leader != m.From && settled(leader, peers) {
			return leader, nil
		}
		if err != nil {
			last = err.Error()
		} else {
			last = fmt.Sprintf("leader %q, replicas %v", leader, peers)
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return "", fmt.Errorf("the group had not settled %s after the move (%s)", o.Settle, last)
		}
		select {
		case <-ctx.Done():
		case <-time.After(o.Poll):
		}
	}
}

// stepDownOutcome reads the response of a step-down. A response that says neither success nor
// an error is a failure: a request nobody handled must not read as a move that happened.
func stepDownOutcome(msg *nats.Msg, err error) error {
	if err != nil {
		return err
	}
	var resp struct {
		Error *struct {
			Code        int    `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return fmt.Errorf("unreadable step-down response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("%s (%d)", resp.Error.Description, resp.Error.Code)
	}
	if !resp.Success {
		return fmt.Errorf("the broker did not confirm the step-down")
	}
	return nil
}
