// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
)

// THE LOSS-OR-STALL QUESTION for the leader-move load tests. When a PUBACKed message has not
// been read by a test's deadline (which is past the durable's AckWait, so a redelivery has had
// its chance) it is one of two things, and they call for opposite fixes:
//
//   - STILL IN THE STORE, UNREAD: the store kept it and the durable owes it, but the reader has
//     not been handed it: the reader or the durable stalled.
//   - ABSENT FROM THE STORE AND NEVER READ: the device was PUBACKed and the store does not have
//     it. That is a LOSS, the thing these tests exist to catch, and deliveryLog.report then says
//     whether the store ever held it (a stream sequence no delivery carried) or never did.
//
// In the failures this was written to classify it was neither: the publishes counted as
// PUBACKed had never been PUBACKed (see mqttack), and the count is now taken from the PUBACKs
// themselves.
//
// unreadReport says which, per message, from the store's own contents and the durable's state.

// moveLog records the confirmed leader moves of a test, with their time, for the timeline a
// failure prints.
type moveLog struct {
	mu    sync.Mutex
	start time.Time
	moves []string
}

func newMoveLog() *moveLog { return &moveLog{start: time.Now()} }

func (l *moveLog) add(stream string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.moves = append(l.moves, fmt.Sprintf("+%.1fs %s", time.Since(l.start).Seconds(), stream))
}

func (l *moveLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.moves, ", ")
}

func (l *moveLog) since() string { return fmt.Sprintf("+%.1fs", time.Since(l.start).Seconds()) }

// storeContents reads every message $MQTT_msgs still holds on filter, payload to sequence, by
// sequence: a consumer of any kind on an interest stream must have the stream's replicas, and
// would itself take part in the interest it is there to observe.
func storeContents(js nats.JetStreamContext, filter string) (map[string]uint64, error) {
	si, err := js.StreamInfo(MqttMessageStore)
	if err != nil {
		return nil, err
	}
	out := map[string]uint64{}
	if si.State.Msgs == 0 {
		return out, nil
	}
	for seq, n := si.State.FirstSeq, 0; seq <= si.State.LastSeq && n < 50_000; seq, n = seq+1, n+1 {
		m, err := js.GetMsg(MqttMessageStore, seq)
		if err != nil {
			continue
		}
		if subjectMatches(filter, m.Subject) {
			out[string(m.Data)] = seq
		}
	}
	return out, nil
}

// deliveryLog is every delivery a test's reader took, by stream sequence, so a failure can
// tell a message the store never had from one it had and removed undelivered.
type deliveryLog struct {
	mu    sync.Mutex
	bySeq map[uint64]string
	bySub map[string]uint64
}

func newDeliveryLog() *deliveryLog {
	return &deliveryLog{bySeq: map[uint64]string{}, bySub: map[string]uint64{}}
}

func (d *deliveryLog) add(seq uint64, payload string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.bySeq[seq]; !ok {
		d.bySeq[seq] = payload
	}
	if _, ok := d.bySub[payload]; !ok {
		d.bySub[payload] = seq
	}
}

// report logs the stream sequences up to last no delivery carried (stored and removed
// undelivered, or never used), and where each unread payload's neighbours were stored.
func (d *deliveryLog) report(t *testing.T, last uint64, unread []string) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	var gaps []uint64
	for seq := uint64(1); seq <= last; seq++ {
		if _, ok := d.bySeq[seq]; !ok {
			gaps = append(gaps, seq)
		}
	}
	shown := gaps
	if len(shown) > 40 {
		shown = shown[:40]
	}
	t.Logf("deliveries: %d distinct sequences of 1..%d delivered; %d never delivered %v", len(d.bySeq), last, len(gaps), shown)
	for _, p := range unread {
		i := strings.LastIndexByte(p, '-')
		if i < 0 {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(p[i+1:], "%d", &n); err != nil {
			continue
		}
		prev, next := fmt.Sprintf("%s-%03d", p[:i], n-1), fmt.Sprintf("%s-%03d", p[:i], n+1)
		t.Logf("  %s: previous %s stored at %d, next %s stored at %d", p, prev, d.bySub[prev], next, d.bySub[next])
	}
}

// unreadReport logs, for the PUBACKed messages unread, which of them the store still holds
// (unread) and which it does not (lost), per device, with the durable's and the store's state
// and the timeline of the moves. It returns the lost ones.
func unreadReport(t *testing.T, js nats.JetStreamContext, durable, filter string, unread []string, moves *moveLog) (lost []string) {
	t.Helper()
	t.Logf("unread at %s: %d PUBACKed messages", moves.since(), len(unread))
	if ci, err := js.ConsumerInfo(MqttMessageStore, durable); err == nil {
		leader := ""
		if ci.Cluster != nil {
			leader = ci.Cluster.Leader
		}
		t.Logf("durable %s: leader %q, pending %d, ack-pending %d, redelivered %d, ack floor %d/%d, delivered %d/%d, waiting pulls %d, AckWait %v, MaxDeliver %d",
			durable, leader, ci.NumPending, ci.NumAckPending, ci.NumRedelivered, ci.AckFloor.Consumer, ci.AckFloor.Stream,
			ci.Delivered.Consumer, ci.Delivered.Stream, ci.NumWaiting, ci.Config.AckWait, ci.Config.MaxDeliver)
	} else {
		t.Logf("durable %s: unreadable: %v", durable, err)
	}
	if si, err := js.StreamInfo(MqttMessageStore); err == nil {
		leader := ""
		if si.Cluster != nil {
			leader = si.Cluster.Leader
		}
		t.Logf("store: leader %q, %d msgs, first %d, last %d, deleted %d", leader, si.State.Msgs, si.State.FirstSeq, si.State.LastSeq, si.State.NumDeleted)
	}
	held, err := storeContents(js, filter)
	if err != nil {
		t.Logf("store contents unreadable: %v", err)
	}
	type dev struct{ inStore, absent []string }
	byDevice := map[string]*dev{}
	for _, p := range unread {
		name := p
		if i := strings.IndexByte(p, '-'); i > 0 {
			name = p[:i]
		}
		d := byDevice[name]
		if d == nil {
			d = &dev{}
			byDevice[name] = d
		}
		if seq, ok := held[p]; ok {
			d.inStore = append(d.inStore, fmt.Sprintf("%s@%d", p, seq))
		} else {
			d.absent = append(d.absent, p)
			lost = append(lost, p)
		}
	}
	names := make([]string, 0, len(byDevice))
	for n := range byDevice {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d := byDevice[n]
		t.Logf("  %s: %d still in the store unread %v; %d ABSENT from the store %v", n, len(d.inStore), d.inStore, len(d.absent), d.absent)
	}
	t.Logf("store holds %d messages on the filter in all; moves: %s", len(held), moves)
	return lost
}
