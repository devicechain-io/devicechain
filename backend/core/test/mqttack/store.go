// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package mqttack tells a test which of a paho client's QoS 1 publishes the broker actually
// PUBACKed.
//
// 🔴 A COMPLETED PUBLISH TOKEN IS NOT A PUBACK. When an auto-reconnecting client with a
// persistent session (CleanSession false) reconnects, paho (v1.5.1, messageIds.claimID) re-sends
// each publish still in its store under a NEW token and completes the OLD one with no error. The
// token a test is waiting on therefore reports success for a publish the broker never
// acknowledged: the publish was only queued to be sent again, and its real PUBACK, if one ever
// comes, completes a token nobody holds. A test that counts "token done, no error" as "the broker
// was told this was stored" counts publishes no broker acknowledged, and when such a re-send is
// then cut off (the client disconnects before it is answered) the test reports the broker as
// having lost a message it never acknowledged.
//
// What does prove a PUBACK is the session store: paho deletes a QoS 1 publish's outbound entry
// when its PUBACK arrives (persistInbound), and at no other time while the session is persistent.
// Store watches for exactly that.
package mqttack

import (
	"strconv"
	"strings"
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"
)

// outboundPrefix is the key prefix paho stores an outbound packet under ("o." + message id).
const outboundPrefix = "o."

// Store is a paho session store that records which outbound QoS 1 publishes were PUBACKed. Pass
// it to ClientOptions.SetStore; it keeps the session in memory, as mqtt.NewMemoryStore does.
type Store struct {
	mqtt.Store
	mu      sync.Mutex
	open    map[uint16]bool // stored, not yet PUBACKed
	pubacks map[uint16]bool // PUBACKed since the publish under this id was stored
}

// NewStore returns an empty Store over an in-memory session store.
func NewStore() *Store {
	return &Store{Store: mqtt.NewMemoryStore(), open: map[uint16]bool{}, pubacks: map[uint16]bool{}}
}

// Put stores the packet, and notes an outbound publish as awaiting its PUBACK. A re-send of a
// publish still awaiting one is stored again under the same key and changes nothing.
func (s *Store) Put(key string, m packets.ControlPacket) {
	if p, ok := m.(*packets.PublishPacket); ok && strings.HasPrefix(key, outboundPrefix) {
		s.mu.Lock()
		if !s.open[p.MessageID] {
			// A new publish under this id (ids are reused once freed): forget the last one's PUBACK.
			s.open[p.MessageID] = true
			delete(s.pubacks, p.MessageID)
		}
		s.mu.Unlock()
	}
	s.Store.Put(key, m)
}

// Del removes the packet; for an outbound publish awaiting its PUBACK, that is the PUBACK.
func (s *Store) Del(key string) {
	if mid, ok := outboundID(key); ok {
		s.mu.Lock()
		if s.open[mid] {
			delete(s.open, mid)
			s.pubacks[mid] = true
		}
		s.mu.Unlock()
	}
	s.Store.Del(key)
}

// Reset empties the store, as paho does for a clean session: nothing is awaiting a PUBACK after.
func (s *Store) Reset() {
	s.mu.Lock()
	s.open = map[uint16]bool{}
	s.mu.Unlock()
	s.Store.Reset()
}

// PubAcked reports whether the publish stored under message id mid (PublishToken.MessageID) has
// been PUBACKed. It answers for the latest publish under that id.
func (s *Store) PubAcked(mid uint16) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubacks[mid]
}

// Outstanding is the number of publishes stored and not yet PUBACKed: the re-sends a reconnect
// queued among them are answered on tokens nobody holds, so a client that wants them delivered
// waits for this to reach zero before disconnecting.
func (s *Store) Outstanding() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.open)
}

func outboundID(key string) (uint16, bool) {
	if !strings.HasPrefix(key, outboundPrefix) {
		return 0, false
	}
	mid, err := strconv.ParseUint(strings.TrimPrefix(key, outboundPrefix), 10, 16)
	return uint16(mid), err == nil
}
