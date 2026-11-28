// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	nats "github.com/nats-io/nats.go"
)

// Evicting ONE persistent MQTT session, by its client id, the way nats-server clears a session
// itself (mqttSession.clear): delete its subscription consumers on $MQTT_msgs, its PUBREL
// consumer on $MQTT_out, and then its record in $MQTT_sess. It is what lets device-management's
// auth callout bound the sessions one device holds without refusing the device: a new client id
// past the device's count evicts the session of the one it used least recently.

// mqttHashDigits and mqttHashLen are nats-server's getHash (server/events.go, sysHashLen = 8;
// the alphabet and base are server/accounts.go's digits and base = 62).
const (
	mqttHashDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	mqttHashLen    = 8
)

// MqttSessionClientIDHash is the digest nats-server files a client id's session record under
// ("$MQTT.sess.{domainTk}{digest}"): the first eight bytes of the id's SHA-256, each reduced mod
// 62 onto the server's digit alphabet.
//
// 🔴 IT IS A COPY OF ANOTHER PROJECT'S DERIVATION, AND ONLY A TEST KEEPS IT HONEST. A server
// that changed getHash would leave this naming subjects that hold nothing: an eviction would
// find no record, report nothing to do, and release a slot over a session that still exists.
// TestMqttSessionClientIDHashAgreesWithTheServer connects a real client in both JetStream
// domain modes and compares this against the subject the server wrote.
func MqttSessionClientIDHash(clientID string) string {
	sum := sha256.Sum256([]byte(clientID))
	out := make([]byte, mqttHashLen)
	for i := range out {
		out[i] = mqttHashDigits[int(sum[i]%62)]
	}
	return string(out)
}

// mqttEvictRecord is the part of nats-server's mqttPersistedSession an eviction reads: the client
// id (to confirm whose record this is) and the durable consumers the session owns. A local
// snapshot, like mqttSessionRecord, for the same reason.
type mqttEvictRecord struct {
	ID   string `json:"id,omitempty"`
	Cons map[string]*struct {
		Durable string `json:"durable_name,omitempty"`
	} `json:"cons,omitempty"`
	PubRel *struct {
		Durable string `json:"durable_name,omitempty"`
	} `json:"pubrel,omitempty"`
}

// MqttSessionEvictor removes single persistent sessions from the MQTT gateway's stores.
type MqttSessionEvictor struct {
	js nats.JetStreamContext

	mu sync.Mutex
	// prefix is the session subject prefix including the JetStream domain token when one is
	// configured, learned from the stream's own subjects the first time it is needed.
	prefix string
}

// NewMqttSessionEvictor returns an evictor over the connection's JetStream.
func NewMqttSessionEvictor(nc *nats.Conn) (*MqttSessionEvictor, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, err
	}
	return &MqttSessionEvictor{js: js}, nil
}

// sessionPrefix returns "$MQTT.sess.{domainTk}", "" when the session store does not exist yet
// (no MQTT client has ever connected, so there is nothing to evict).
func (e *MqttSessionEvictor) sessionPrefix(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.prefix != "" {
		return e.prefix, nil
	}
	info, err := e.js.StreamInfo(MqttSessionStore, nats.Context(ctx))
	if err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", MqttSessionStore, err)
	}
	for _, s := range info.Config.Subjects {
		if p, ok := strings.CutSuffix(s, ">"); ok && strings.HasPrefix(p, mqttSessSubjectPrefix) {
			e.prefix = p
			return p, nil
		}
	}
	return "", fmt.Errorf("%s declares no subject of the form %s>: %v", MqttSessionStore, mqttSessSubjectPrefix, info.Config.Subjects)
}

// Evict removes the persistent session filed under idHash (MqttSessionClientIDHash of its client
// id) when owned accepts the client id the record carries. It reports whether a session was
// removed. No record is not an error: a clean session leaves none once it disconnects, and a
// record someone else removed is the outcome wanted. A record owned refuses (a digest collision
// with another client's id) is left alone and reported as not removed.
//
// Every call carries ctx, so an eviction inside a connect is bounded by that connect's deadline.
func (e *MqttSessionEvictor) Evict(ctx context.Context, idHash string, owned func(clientID string) bool) (bool, error) {
	if len(idHash) != mqttHashLen || strings.ContainsAny(idHash, ".*> ") {
		return false, fmt.Errorf("refusing to evict an MQTT session by the malformed digest %q", idHash)
	}
	prefix, err := e.sessionPrefix(ctx)
	if err != nil || prefix == "" {
		return false, err
	}
	msg, err := e.js.GetLastMsg(MqttSessionStore, prefix+idHash, nats.Context(ctx))
	if err != nil {
		if errors.Is(err, nats.ErrMsgNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("reading the MQTT session record %s: %w", idHash, err)
	}
	var rec mqttEvictRecord
	if err := json.Unmarshal(msg.Data, &rec); err != nil {
		return false, fmt.Errorf("the MQTT session record %s at sequence %d cannot be read: %w", idHash, msg.Sequence, err)
	}
	if !owned(rec.ID) {
		return false, nil
	}
	for _, c := range rec.Cons {
		if c == nil || c.Durable == "" {
			continue
		}
		if err := e.js.DeleteConsumer(MqttMessageStore, c.Durable, nats.Context(ctx)); err != nil && !errors.Is(err, nats.ErrConsumerNotFound) {
			return false, fmt.Errorf("deleting consumer %s of the MQTT session %s: %w", c.Durable, idHash, err)
		}
	}
	if rec.PubRel != nil && rec.PubRel.Durable != "" {
		if err := e.js.DeleteConsumer(MqttPubRelStore, rec.PubRel.Durable, nats.Context(ctx)); err != nil && !errors.Is(err, nats.ErrConsumerNotFound) {
			return false, fmt.Errorf("deleting the PUBREL consumer of the MQTT session %s: %w", idHash, err)
		}
	}
	if err := e.js.DeleteMsg(MqttSessionStore, msg.Sequence, nats.Context(ctx)); err != nil && !errors.Is(err, nats.ErrMsgNotFound) {
		return false, fmt.Errorf("deleting the MQTT session record %s: %w", idHash, err)
	}
	return true, nil
}
