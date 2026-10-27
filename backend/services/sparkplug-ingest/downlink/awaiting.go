// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
// Every resolution of a command (a report of the requested value, the end of its window, a newer
// command for the same metric, a broker that dropped the session right after the publish) deletes
// its entry once the response has been published. A new leader lists the bucket before it serves
// and re-arms what is left; see confirm.go.
//
// The same bucket also holds one small mark per node that has been sent a reboot (RebootKey), so
// the rule of one reboot per window survives a change of leader. A mark is NOT deleted when the
// reboot resolves: it is the memory of the reboot, not the wait for its outcome.

// Kinds of awaiting entry.
const (
	KindWrite   = "write"
	KindRebirth = "rebirth"
	KindReboot  = "reboot"
)

// Awaiting is one published command awaiting its outcome.
type Awaiting struct {
	Command string `json:"command"`
	Device  string `json:"device"`
	// Source is the identity of the source (the Client's ingest source, "sparkplug:<hostId>") the
	// command was published on, and Target the Sparkplug identity it was addressed to ("group/node"
	// or "group/node/device"; the node, for a rebirth). Reports are matched on both: a node of the
	// same name on another source's broker is another node.
	Source string  `json:"source"`
	Target string  `json:"target"`
	Nonce  string  `json:"nonce"`
	Kind   string  `json:"kind"`
	Metric string  `json:"metric,omitempty"`
	Alias  *uint64 `json:"alias,omitempty"`
	// Requested is the value the operator asked for, as offered (JSON). Absent for rebirth/reboot.
	Requested json.RawMessage `json:"requested,omitempty"`
	Datatype  uint32          `json:"datatype"`
	// SessionEpoch, BdSeq and Gen identify the BIRTH table the command was addressed against.
	SessionEpoch uint64 `json:"sessionEpoch"`
	BdSeq        uint64 `json:"bdSeq"`
	HasBdSeq     bool   `json:"hasBdSeq"`
	HostRole     string `json:"hostRole"`
	// AtRequestedValue is set when the node's last report of the metric was already the requested
	// value at the moment of the publish, and LastReportedAt is when it says so. A window that
	// closes with no contrary report then confirms it as UNCHANGED (never on a secondary host).
	AtRequestedValue bool      `json:"atRequestedValue,omitempty"`
	LastReportedAt   time.Time `json:"lastReportedAt,omitempty"`
	// TableGen is the generation of the BIRTH table the command was addressed against (with
	// SessionEpoch, it names the table). UNCHANGED is re-checked against that table at the end of
	// the window.
	TableGen uint64 `json:"tableGen,omitempty"`
	// Expires is the command's own expiry, when it has one; a response is not retried past it.
	Expires time.Time `json:"expires,omitempty"`
	// Decided is the outcome chosen for the command when its response could not be published. It is
	// stored so a new leader settles the command with what was decided, not with a fresh guess: a
	// command that was confirmed is never re-decided into a timeout.
	Decided *StoredOutcome `json:"decided,omitempty"`
	// PublishedAt is the payload timestamp and Deadline the end of the confirmation window, clamped
	// to the command's own expiry.
	PublishedAt time.Time `json:"publishedAt"`
	Deadline    time.Time `json:"deadline"`
	// Wire is what was sent: the topic, the alias or name, the datatype, the value, and the digest
	// of the bytes. It is the evidence kept with the outcome.
	Wire AwaitingWire `json:"wire"`
}

// StoredOutcome is an outcome persisted with its entry (Awaiting.Decided).
type StoredOutcome struct {
	Success   bool   `json:"success"`
	Reason    string `json:"reason,omitempty"`
	Text      string `json:"text,omitempty"`
	Payload   string `json:"payload,omitempty"`
	Result    string `json:"result"`
	Confirmed bool   `json:"confirmed,omitempty"`
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
	// List returns everything in the bucket: the awaiting entries, and the reboot marks.
	List(ctx context.Context) (Stored, error)
	// PutReboot records that the node was sent a reboot at at; DeleteReboot takes the mark back
	// when the reboot turned out not to have been sent.
	PutReboot(ctx context.Context, tenant, group, node string, at time.Time) error
	DeleteReboot(ctx context.Context, tenant, group, node string) error
}

// Stored is the content of the awaiting bucket.
type Stored struct {
	// Entries are the awaiting entries by key (AwaitingKey).
	Entries map[string]Awaiting
	// Reboots are the reboot marks: when each node was last sent a reboot.
	Reboots []RebootMark
	// Unreadable are the keys of entries that could not be decoded; they are left alone (they age
	// out with the bucket) and reported so the fault is not silent.
	Unreadable []string
}

// RebootMark is when a node was last sent a reboot.
type RebootMark struct {
	Tenant, Group, Node string
	At                  time.Time
}

// AwaitingKey is the entry key for a command: the tenant, a separator, the command token. It is
// the same layout every tenant-scoped bucket uses, so a tenant's entries can be found by it.
func AwaitingKey(tenant, commandToken string) string { return tenant + "|" + commandToken }

// RebootKey is the key of a node's reboot mark: tenant|reboot|group/node. A tenant (and a command
// token) is letters, digits, hyphen and underscore, so the middle segment cannot be a command
// token's: an awaiting key has two segments, a reboot mark three.
func RebootKey(tenant, group, node string) string {
	return tenant + "|reboot|" + group + "/" + node
}

// parseRebootKey is RebootKey's inverse; ok is false for any other key.
func parseRebootKey(key string) (tenant, group, node string, ok bool) {
	parts := strings.SplitN(key, "|", 3)
	if len(parts) != 3 || parts[1] != "reboot" {
		return "", "", "", false
	}
	g, n, found := strings.Cut(parts[2], "/")
	if !found || g == "" || n == "" || parts[0] == "" {
		return "", "", "", false
	}
	return parts[0], g, n, true
}

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

type rebootRecord struct {
	PublishedAt time.Time `json:"publishedAt"`
}

func (s kvAwaitingStore) PutReboot(_ context.Context, tenant, group, node string, at time.Time) error {
	b, err := json.Marshal(rebootRecord{PublishedAt: at})
	if err != nil {
		return fmt.Errorf("encoding the reboot mark: %w", err)
	}
	if _, err := s.kv.Put(kvKey(RebootKey(tenant, group, node)), b); err != nil {
		return fmt.Errorf("writing the reboot mark: %w", err)
	}
	return nil
}

func (s kvAwaitingStore) DeleteReboot(ctx context.Context, tenant, group, node string) error {
	return s.Delete(ctx, RebootKey(tenant, group, node))
}

func (s kvAwaitingStore) List(_ context.Context) (Stored, error) {
	out := Stored{Entries: map[string]Awaiting{}}
	keys, err := s.kv.Keys()
	if err != nil {
		if errors.Is(err, nats.ErrNoKeysFound) {
			return out, nil
		}
		return Stored{}, fmt.Errorf("listing the awaiting bucket: %w", err)
	}
	for _, k := range keys {
		raw, err := base64.RawURLEncoding.DecodeString(k)
		if err != nil {
			out.Unreadable = append(out.Unreadable, k)
			continue
		}
		key := string(raw)
		e, err := s.kv.Get(k)
		if err != nil {
			if errors.Is(err, nats.ErrKeyNotFound) {
				continue // aged out or resolved between the listing and the read
			}
			return Stored{}, fmt.Errorf("reading an awaiting entry: %w", err)
		}
		if tenant, group, node, ok := parseRebootKey(key); ok {
			var rec rebootRecord
			if json.Unmarshal(e.Value(), &rec) != nil || rec.PublishedAt.IsZero() {
				out.Unreadable = append(out.Unreadable, key)
				continue
			}
			out.Reboots = append(out.Reboots, RebootMark{Tenant: tenant, Group: group, Node: node, At: rec.PublishedAt})
			continue
		}
		var a Awaiting
		if json.Unmarshal(e.Value(), &a) != nil || a.Command == "" {
			out.Unreadable = append(out.Unreadable, key)
			continue
		}
		out.Entries[key] = a
	}
	return out, nil
}

func (s kvAwaitingStore) Delete(_ context.Context, key string) error {
	if err := s.kv.Delete(kvKey(key)); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
		return fmt.Errorf("deleting the awaiting entry: %w", err)
	}
	return nil
}
