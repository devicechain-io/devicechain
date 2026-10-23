// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"container/list"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// WHAT THE AGENT DOES WITH A REFUSAL.
//
// The agent speaks for its children, and the platform decides, per event, whether this gateway
// may: an event for a device that is not assigned to it (or does not exist) is refused in
// platform code, recorded there (failed events, a counter), and answered with a NOTICE on the
// gateway's own notices topic. The broker cannot say so itself: MQTT 3.1.1 has no way to
// refuse a publish, so the PUBACK the agent waited for was a true statement about the broker
// and says nothing about the platform.
//
// By the time a notice arrives the spool message is long acked, so "do not empty the spool for
// a refused event" cannot mean "do not ack". It means the agent keeps its own copy of what it
// sent for a while (recentSent), and when a notice names an event it still holds, writes it
// to a local dead-letter file an operator can read (refusedEventsFile). Three honest limits:
//
//   - a notice is best effort (a gateway that was offline, or a broker restart, misses it), so
//     the record of truth is the platform's failed-events plus its counter;
//   - the copy is bounded, so a refusal that arrives after the copy rolled out is counted as
//     unmatched and logged, not kept;
//   - the copy is the payload as FORWARDED (body credentials already removed), so the
//     dead-letter file never holds a secret.

const (
	// recentSentMaxEntries / recentSentMaxBytes bound the in-memory copy of forwarded events
	// that a late notice can still be matched against.
	recentSentMaxEntries = 8192
	recentSentMaxBytes   = 16 << 20

	// refusedEventsFile is the local dead-letter file, in the store directory (0600). It is
	// one JSON object per line; when it would pass refusedEventsMaxBytes it is rotated to
	// refusedEventsFile+".1" (replacing any previous one), so it is bounded to about twice
	// that on disk.
	refusedEventsFile     = "refused-events.jsonl"
	refusedEventsMaxBytes = 16 << 20

	// refusalLogEvery rate-limits the per-child WARN so a mis-assigned child publishing at
	// full rate cannot flood the log; the counter still counts every refusal.
	refusalLogEvery = 30 * time.Second
)

// Refusal reasons the agent itself records, beside the platform's own
// (messaging.GatewayNoticeReasonNotGatewayChild).
const (
	// refusalGatewaySelf: a local device is publishing under the gateway's OWN token. A
	// gateway is not a child of itself, so the event cannot ride the child path.
	refusalGatewaySelf = "gateway_self"
	// refusalInvalidToken: the captured subject names a device token the platform's grammar
	// cannot hold, so no topic can be built for it.
	refusalInvalidToken = "invalid_device_token"
	// refusalOther labels a notice reason the agent does not know, so a platform that adds
	// one cannot grow the metric's label set without bound.
	refusalOther = "other"
)

// knownRefusalReasons is every value of the refused_total reason label.
var knownRefusalReasons = []string{
	messaging.GatewayNoticeReasonNotGatewayChild,
	refusalGatewaySelf,
	refusalInvalidToken,
	refusalOther,
}

func normaliseRefusalReason(r string) string {
	for _, k := range knownRefusalReasons {
		if r == k {
			return k
		}
	}
	return refusalOther
}

// sentEvent is one forwarded event, held so a late notice can be matched to it.
type sentEvent struct {
	altId   string
	child   string
	topic   string
	payload []byte
}

// recentSent is a bounded FIFO of forwarded events keyed by altId.
type recentSent struct {
	mu    sync.Mutex
	order *list.List // of *sentEvent, oldest first
	byAlt map[string]*list.Element
	bytes int
}

func newRecentSent() *recentSent {
	return &recentSent{order: list.New(), byAlt: map[string]*list.Element{}}
}

// put records e, evicting the oldest entries to stay within the bounds. A repeat altId
// (a redelivery) replaces the earlier entry.
func (r *recentSent) put(e *sentEvent) {
	if e.altId == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.byAlt[e.altId]; ok {
		r.removeLocked(old)
	}
	r.byAlt[e.altId] = r.order.PushBack(e)
	r.bytes += len(e.payload)
	for r.order.Len() > recentSentMaxEntries || r.bytes > recentSentMaxBytes {
		front := r.order.Front()
		if front == nil {
			break
		}
		r.removeLocked(front)
	}
}

// take removes and returns the event with altId, if still held.
func (r *recentSent) take(altId string) (*sentEvent, bool) {
	if altId == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	el, ok := r.byAlt[altId]
	if !ok {
		return nil, false
	}
	e := el.Value.(*sentEvent)
	r.removeLocked(el)
	return e, true
}

func (r *recentSent) removeLocked(el *list.Element) {
	e := el.Value.(*sentEvent)
	r.order.Remove(el)
	delete(r.byAlt, e.altId)
	r.bytes -= len(e.payload)
}

// refusedRecord is one line of the local dead-letter file.
type refusedRecord struct {
	At            time.Time       `json:"at"`
	Reason        string          `json:"reason"`
	Device        string          `json:"device"`
	AltId         string          `json:"altId,omitempty"`
	Topic         string          `json:"topic,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`       // when the payload is JSON
	PayloadBase64 []byte          `json:"payloadBase64,omitempty"` // otherwise (encoded base64 by encoding/json)
}

// deadLetters appends refused events to the local dead-letter file.
type deadLetters struct {
	mu   sync.Mutex
	path string
	max  int64
}

func newDeadLetters(storeDir string) *deadLetters {
	return &deadLetters{path: filepath.Join(storeDir, refusedEventsFile), max: refusedEventsMaxBytes}
}

// write appends one record, rotating first when it would pass the size bound.
func (d *deadLetters) write(rec refusedRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	d.mu.Lock()
	defer d.mu.Unlock()
	if fi, err := os.Stat(d.path); err == nil && fi.Size()+int64(len(line)) > d.max {
		if err := os.Rename(d.path, d.path+".1"); err != nil {
			return fmt.Errorf("rotate dead-letter file: %w", err)
		}
	}
	f, err := os.OpenFile(d.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// newRefusedRecord shapes a held event as a dead-letter record.
func newRefusedRecord(reason string, e *sentEvent) refusedRecord {
	rec := refusedRecord{At: time.Now().UTC(), Reason: reason, Device: e.child, AltId: e.altId, Topic: e.topic}
	if json.Valid(e.payload) {
		rec.Payload = e.payload
	} else {
		rec.PayloadBase64 = e.payload
	}
	return rec
}

// logLimiter admits one log line per key per interval.
type logLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (l *logLimiter) allow(key string, every time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	now := time.Now()
	if t, ok := l.last[key]; ok && now.Sub(t) < every {
		return false
	}
	// Bound the map: a flood of distinct keys must not grow it without limit.
	if len(l.last) > 4096 {
		l.last = map[string]time.Time{}
	}
	l.last[key] = now
	return true
}
