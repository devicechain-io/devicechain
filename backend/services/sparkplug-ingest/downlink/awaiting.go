// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	nats "github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// The awaiting set.
//
// A command that has been put on the wire is not finished: Sparkplug has no command
// acknowledgement, so the outcome is learned later (or never). Until then it is held in a NATS KV
// bucket, written BEFORE the publish, so that a new leader can find what the last one left in
// flight. Nothing in this service's memory is the only copy.
//
// This slice writes the entry and deletes it when it resolves a command for a reason of its own
// (a command superseded by a newer one for the same metric, a broker that dropped the session
// right after the publish, a publish that did not happen). Matching a report against the entries,
// the window expiry and the re-arm at the start of a term are the confirmation slice's.

// Kinds of awaiting entry.
const (
	KindWrite   = "write"
	KindRebirth = "rebirth"
	KindReboot  = "reboot"
)

// Awaiting is one published command awaiting its outcome.
type Awaiting struct {
	Command string  `json:"command"`
	Device  string  `json:"device"`
	Nonce   string  `json:"nonce"`
	Kind    string  `json:"kind"`
	Metric  string  `json:"metric,omitempty"`
	Alias   *uint64 `json:"alias,omitempty"`
	// Requested is the value the operator asked for, as offered (JSON). Absent for rebirth/reboot.
	Requested json.RawMessage `json:"requested,omitempty"`
	Datatype  uint32          `json:"datatype"`
	// SessionEpoch, BdSeq and Gen identify the BIRTH table the command was addressed against.
	SessionEpoch uint64 `json:"sessionEpoch"`
	BdSeq        uint64 `json:"bdSeq"`
	HasBdSeq     bool   `json:"hasBdSeq"`
	HostRole     string `json:"hostRole"`
	// PublishedAt is the payload timestamp and Deadline the end of the confirmation window, clamped
	// to the command's own expiry.
	PublishedAt time.Time `json:"publishedAt"`
	Deadline    time.Time `json:"deadline"`
	// Wire is what was sent: the topic, the alias or name, the datatype, the value, and the digest
	// of the bytes. It is the evidence kept with the outcome.
	Wire AwaitingWire `json:"wire"`
}

// AwaitingWire is the decoded wire form of the published command.
type AwaitingWire struct {
	Topic    string  `json:"topic"`
	Alias    *uint64 `json:"alias,omitempty"`
	Name     string  `json:"name,omitempty"`
	Datatype uint32  `json:"datatype"`
	Value    any     `json:"value,omitempty"`
	SHA256   string  `json:"sha256"`
	Size     int     `json:"size"`
}

func awaitingWire(w host.CommandWire) AwaitingWire {
	return AwaitingWire{Topic: w.Topic, Alias: w.Alias, Name: w.Name, Datatype: w.Datatype, Value: w.Value, SHA256: w.SHA256, Size: w.Size}
}

// AwaitingStore is the durable home of the awaiting set.
type AwaitingStore interface {
	// Put records the entry under key, replacing any.
	Put(ctx context.Context, key string, a Awaiting) error
	// Delete removes the entry. Deleting an absent key is not an error.
	Delete(ctx context.Context, key string) error
}

// AwaitingKey is the entry key for a command: the tenant, a separator, the command token. It is
// the same layout every tenant-scoped bucket uses, so a tenant's entries can be found by it.
func AwaitingKey(tenant, commandToken string) string { return tenant + "|" + commandToken }

// kvAwaitingStore keeps the awaiting set in a JetStream KV bucket.
type kvAwaitingStore struct{ kv nats.KeyValue }

// NewKVAwaitingStore wraps the awaiting bucket (messaging.NatsManager.SparkplugCommandsAwaitingStore).
func NewKVAwaitingStore(kv nats.KeyValue) AwaitingStore { return kvAwaitingStore{kv: kv} }

// kvKey encodes a key into the KV key charset the way every bucket of the platform does
// (base64url), which is also what a tenant purge decodes.
func kvKey(key string) string { return base64.RawURLEncoding.EncodeToString([]byte(key)) }

func (s kvAwaitingStore) Put(_ context.Context, key string, a Awaiting) error {
	b, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encoding the awaiting entry: %w", err)
	}
	if _, err := s.kv.Put(kvKey(key), b); err != nil {
		return fmt.Errorf("writing the awaiting entry: %w", err)
	}
	return nil
}

func (s kvAwaitingStore) Delete(_ context.Context, key string) error {
	if err := s.kv.Delete(kvKey(key)); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
		return fmt.Errorf("deleting the awaiting entry: %w", err)
	}
	return nil
}
