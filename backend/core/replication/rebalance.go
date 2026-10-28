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
	Err  error
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

// ApplyMoves asks the broker to carry out each move, in order, and reports each outcome. A
// failed move does not stop the rest: a leader election that races a restart is the ordinary
// reason, and the groups after it are independent. perMove bounds each request.
func ApplyMoves(ctx context.Context, nc *nats.Conn, moves []Move, perMove time.Duration) []MoveResult {
	out := make([]MoveResult, 0, len(moves))
	for _, m := range moves {
		if ctx.Err() != nil {
			out = append(out, MoveResult{Move: m, Err: ctx.Err()})
			continue
		}
		var body stepDownRequest
		body.Placement.Preferred = m.To
		raw, _ := json.Marshal(body)
		rctx, cancel := context.WithTimeout(ctx, perMove)
		msg, err := nc.RequestWithContext(rctx, stepDownSubject(m), raw)
		cancel()
		out = append(out, MoveResult{Move: m, Err: stepDownOutcome(msg, err)})
	}
	return out
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
