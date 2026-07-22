// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"fmt"
	"strings"
)

// MessageType is a Sparkplug B message type as it appears in the topic (the
// third topic level for node/device messages).
type MessageType string

const (
	NBIRTH MessageType = "NBIRTH" // edge node birth (rebuilds the alias table, resets seq)
	NDEATH MessageType = "NDEATH" // edge node death (the only message exempt from the seq counter)
	NDATA  MessageType = "NDATA"  // edge node data
	NCMD   MessageType = "NCMD"   // edge node command (host→node; a Host publishes these)
	DBIRTH MessageType = "DBIRTH" // device birth
	DDEATH MessageType = "DDEATH" // device death
	DDATA  MessageType = "DDATA"  // device data
	DCMD   MessageType = "DCMD"   // device command (host→node)
	STATE  MessageType = "STATE"  // Host Application state (spBv1.0/STATE/{host_id})
)

// nodeMessageTypes is the set of NODE-scoped message types, which appear at
// exactly four topic levels (no device id). deviceMessageTypes is the DEVICE-
// scoped set, at exactly five levels. STATE is excluded from both — it has its
// own three-level grammar handled separately. Sparkplug fixes the level count per
// type, so validating it (rather than accepting either at 4 or 5) is what keeps a
// D-type at 4 levels — or an N-type at 5 — from being mis-classified, which would
// corrupt the SP2 session machine's device table.
var nodeMessageTypes = map[MessageType]bool{
	NBIRTH: true, NDEATH: true, NDATA: true, NCMD: true,
}

var deviceMessageTypes = map[MessageType]bool{
	DBIRTH: true, DDEATH: true, DDATA: true, DCMD: true,
}

// Topic is a parsed Sparkplug B topic. Sparkplug identity (group, node, device,
// message type) lives entirely in the topic — the protobuf payload carries none
// of it — so parsing the topic is how the adapter knows what it received.
type Topic struct {
	// IsState is true for a Host Application STATE topic (spBv1.0/STATE/{host_id}).
	// When set, only HostID and MessageType (== STATE) are populated.
	IsState bool
	// HostID is the Host Application id, set only for a STATE topic.
	HostID string

	GroupID     string
	MessageType MessageType
	EdgeNodeID  string
	// DeviceID is set only for device-level messages (DBIRTH/DDATA/DDEATH/DCMD),
	// which carry a fifth topic level; empty for node-level messages.
	DeviceID string
}

// IsDevice reports whether this is a device-level message (a fifth topic level
// was present). Node-level and STATE topics return false.
func (t Topic) IsDevice() bool {
	return !t.IsState && t.DeviceID != ""
}

// ParseTopic parses a Sparkplug B MQTT topic into its identity fields. It fails
// closed on a topic that does not match the grammar — a wrong namespace, an
// unknown message type, or a wrong segment count — so a non-Sparkplug or
// malformed topic is a legible error rather than a mis-attributed message.
//
// The two grammars are:
//
//	spBv1.0/STATE/{host_id}                              (Host Application state)
//	spBv1.0/{group}/{type}/{node}[/{device}]             (node / device messages)
func ParseTopic(topic string) (Topic, error) {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		return Topic{}, fmt.Errorf("not a Sparkplug topic: %q", topic)
	}
	if parts[0] != Namespace {
		return Topic{}, fmt.Errorf("topic %q: wrong namespace %q (want %q)", topic, parts[0], Namespace)
	}

	// Host Application STATE: spBv1.0/STATE/{host_id} (exactly three levels).
	if parts[1] == string(STATE) {
		if len(parts) != 3 || parts[2] == "" {
			return Topic{}, fmt.Errorf("malformed STATE topic %q (want spBv1.0/STATE/{host_id})", topic)
		}
		return Topic{IsState: true, HostID: parts[2], MessageType: STATE}, nil
	}

	// Node / device: spBv1.0/{group}/{type}/{node}[/{device}]. The level count is
	// fixed by the message type's scope — node types at four, device types at five.
	mt := MessageType(parts[2])
	isNode := nodeMessageTypes[mt]
	isDevice := deviceMessageTypes[mt]
	if !isNode && !isDevice {
		return Topic{}, fmt.Errorf("topic %q: unknown Sparkplug message type %q", topic, parts[2])
	}
	wantLevels := 4
	if isDevice {
		wantLevels = 5
	}
	if len(parts) != wantLevels {
		return Topic{}, fmt.Errorf("topic %q: %s takes %d levels, got %d", topic, mt, wantLevels, len(parts))
	}
	if parts[1] == "" || parts[3] == "" {
		return Topic{}, fmt.Errorf("topic %q: empty group or node id", topic)
	}
	t := Topic{GroupID: parts[1], MessageType: mt, EdgeNodeID: parts[3]}
	if isDevice {
		if parts[4] == "" {
			return Topic{}, fmt.Errorf("topic %q: empty device id", topic)
		}
		t.DeviceID = parts[4]
	}
	return t, nil
}
