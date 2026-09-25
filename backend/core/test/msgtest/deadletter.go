// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package msgtest

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
)

// OnlyDeadLetter returns the one letter the instance's platform dead-letter stream holds,
// and the subject it was stored on, read straight off the broker.
//
// It exists for the per-service wiring tests: each one writes a single letter through the
// sink its main.go builds and then asks the STREAM what arrived, rather than asking the
// sink what it meant to write. It fails the test unless the stream exists and holds
// exactly one message, so a letter written somewhere else reads as "nothing arrived"
// rather than as a pass.
func OnlyDeadLetter(t testing.TB, nc *nats.Conn, instanceId string) (string, deadletter.Envelope) {
	t.Helper()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("opening JetStream: %v", err)
	}
	stream := messaging.StreamName(instanceId, streams.DeadLetters)
	info, err := js.StreamInfo(stream)
	if err != nil {
		t.Fatalf("the dead-letter stream %s is not there: %v", stream, err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("the dead-letter stream %s holds %d messages, want exactly the 1 this test wrote",
			stream, info.State.Msgs)
	}
	raw, err := js.GetMsg(stream, info.State.FirstSeq)
	if err != nil {
		t.Fatalf("reading the letter back from %s: %v", stream, err)
	}
	env, err := deadletter.Unmarshal(raw.Data)
	if err != nil {
		t.Fatalf("the stored letter does not decode as an envelope: %v", err)
	}
	return raw.Subject, env
}
