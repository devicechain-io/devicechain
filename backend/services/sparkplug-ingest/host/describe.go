// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"fmt"

	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// Record is the parsed, decoded description of one received Sparkplug message —
// the structured form the SP1 slice logs. It is the seam a later slice replaces
// with a session-machine update and a StateChange/measurement emission; here it
// exists so the receive path is a pure, testable function rather than logging
// buried inside the MQTT callback.
type Record struct {
	Topic Topic

	// StateOnline is set (non-nil) only for a Host STATE message, carrying the
	// announced online/offline flag.
	StateOnline *bool

	// Seq is the Sparkplug payload sequence number, valid only when HasSeq is
	// true (proto2 leaves it absent rather than defaulting to a real 0).
	Seq    uint64
	HasSeq bool

	// PayloadTimestamp is the node/device payload timestamp in ms since epoch, or
	// the STATE timestamp for a STATE message. Zero when the payload omitted it.
	PayloadTimestamp uint64

	// MetricCount is the number of metrics in a node/device payload.
	MetricCount int

	// Payload is the decoded Sparkplug B payload for a node/device message, nil
	// for a STATE message. It is the seam the SP2 session machine consumes (seq,
	// aliases, bdSeq live in it) — kept on the Record so the receive path decodes
	// exactly once.
	Payload *sppb.Payload
}

// Label is the metric label for this record's message type — STATE, or the
// node/device message type from the topic.
func (r Record) Label() string {
	return string(r.Topic.MessageType)
}

// Describe parses a Sparkplug topic and decodes its payload into a Record. It
// fails closed: an unparseable topic or an undecodable payload is an error, never
// a silently-empty Record, so a corrupt message is logged and skipped rather than
// mis-counted as a clean one.
//
// STATE topics carry a Sparkplug 3.0 JSON body; every node/device message carries
// a Sparkplug B protobuf body, decoded through the SP0 codec.
func Describe(topic string, payload []byte) (Record, error) {
	t, err := ParseTopic(topic)
	if err != nil {
		return Record{}, err
	}
	rec := Record{Topic: t}

	if t.IsState {
		st, err := ParseState(payload)
		if err != nil {
			return rec, fmt.Errorf("STATE %s: %w", t.HostID, err)
		}
		online := st.Online
		rec.StateOnline = &online
		rec.PayloadTimestamp = uint64(st.Timestamp)
		return rec, nil
	}

	p, err := codec.Decode(payload)
	if err != nil {
		return rec, fmt.Errorf("decode %s payload: %w", t.MessageType, err)
	}
	rec.HasSeq = p.Seq != nil
	rec.Seq = p.GetSeq()
	rec.PayloadTimestamp = p.GetTimestamp()
	rec.MetricCount = len(p.GetMetrics())
	rec.Payload = p
	return rec, nil
}
