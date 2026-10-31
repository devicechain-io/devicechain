// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package holdtest is the shared fixture for testing a consumer wired onto storehold: a
// message whose acknowledgement is recorded, a probe the test controls, and the errors that
// stand for "the store is down" and "the message is bad".
package holdtest

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/jackc/pgx/v5/pgconn"
)

// Ack records what the broker was told about one message.
type Ack struct {
	Acked, Progress atomic.Int32
}

func (a *Ack) Ack() error        { a.Acked.Add(1); return nil }
func (a *Ack) InProgress() error { a.Progress.Add(1); return nil }

// Msg builds a consumed message on subject, delivered numDelivered times, whose broker
// interactions land in the returned Ack.
func Msg(subject string, value []byte, numDelivered int) (messaging.Message, *Ack) {
	a := &Ack{}
	return messaging.NewConsumedMessage(subject, value, numDelivered, nil, a), a
}

// DiskFull is the server saying it cannot take writes (SQLSTATE 53100).
var DiskFull = &pgconn.PgError{Code: "53100", Message: "could not extend file: No space left on device"}

// ConnLost is a connection-class error: an outage only when the probe also fails.
var ConnLost error = io.ErrUnexpectedEOF

// Store stands in for the database for the probe: Down makes every probe fail.
type Store struct {
	Down   atomic.Bool
	Probes atomic.Int32
}

// Probe is a storehold.Holder.Probe.
func (s *Store) Probe(context.Context) error {
	s.Probes.Add(1)
	if s.Down.Load() {
		return errors.New("connection refused")
	}
	return nil
}

// FastTuning is storehold timing short enough for a unit test.
func FastTuning() *storehold.Tuning {
	t := storehold.DefaultTuning()
	t.ProbeMin, t.ProbeMax, t.ProbeFresh = 5_000_000, 20_000_000, 0
	t.ProgressEvery = 60_000_000
	return t
}

// Gauge is a Counter that remembers its value.
type Gauge struct{ v atomic.Int64 }

func (g *Gauge) Add(n float64) { g.v.Add(int64(n)) }

// Value is the gauge's current value.
func (g *Gauge) Value() int64 { return g.v.Load() }
