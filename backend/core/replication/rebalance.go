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
	if o.Settle <= 0 {
		o.Settle = 15 * time.Second
	}
	if o.Poll <= 0 {
		o.Poll = 250 * time.Millisecond
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
		leader, peers, err := groupState(js, m)
		switch {
		case err != nil:
			out = append(out, MoveResult{Move: m, Err: fmt.Errorf("reading the group before the move: %w", err)})
			continue
		case leader == m.To:
			out = append(out, MoveResult{Move: m, Skipped: true})
			continue
		case leader != m.From:
			out = append(out, MoveResult{Move: m, Skipped: true})
			continue
		case !targetFit(peers, m.To):
			out = append(out, MoveResult{Move: m, Skipped: true})
			continue
		}
		var body stepDownRequest
		body.Placement.Preferred = m.To
		raw, _ := json.Marshal(body)
		rctx, cancel := context.WithTimeout(ctx, o.PerMove)
		msg, err := nc.RequestWithContext(rctx, stepDownSubject(m), raw)
		cancel()
		if err = stepDownOutcome(msg, err); err != nil {
			out = append(out, MoveResult{Move: m, Err: err})
			continue
		}
		if err := awaitSettled(ctx, js, m, o); err != nil {
			stopped = fmt.Errorf("not attempted: an earlier move did not settle")
			out = append(out, MoveResult{Move: m, Err: err})
			continue
		}
		out = append(out, MoveResult{Move: m})
	}
	return out
}

func targetFit(peers []Peer, to string) bool {
	for _, p := range peers {
		if p.Name == to {
			return p.Current && !p.Offline
		}
	}
	return false
}

// awaitSettled waits for the group's leader to be elected and all its replicas current.
func awaitSettled(ctx context.Context, js nats.JetStreamContext, m Move, o ApplyOptions) error {
	deadline := time.Now().Add(o.Settle)
	var last string
	for {
		leader, peers, err := groupState(js, m)
		if err == nil && settled(leader, peers) {
			return nil
		}
		if err != nil {
			last = err.Error()
		} else {
			last = fmt.Sprintf("leader %q, replicas %v", leader, peers)
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("the group had not settled %s after the move (%s)", o.Settle, last)
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
