// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// Who owns a device, as the live view knows it (ADR-081). A member limited to some
// customers sees a device's live state — and, through it, its latest readings and its
// position — while one of its customers owns the device NOW. The live view is a projection
// with no history, so unlike an event or an alarm it is not stamped once and left: it is
// RESTAMPED whenever ownership changes.
//
// Two writers stamp it, and nothing orders one against the other:
//
//   - every resolved event carries the device's anchors as device-management read them when
//     it resolved the event, and the projection folds them in with the event;
//   - device-management publishes a device ownership-changed fact after every write that may
//     have moved a device's owners, carrying its complete current set, so a device that
//     reports nothing after it is sold still leaves its old owner's view. The write queues
//     the fact in its own transaction and device-management's drain publishes it, retrying
//     until the broker accepts it, so a committed change is announced however long the
//     broker is away (device-management model/ownership_outbox.go).
//
// They arrive on two streams. An event device-management resolved BEFORE a move can reach
// this service AFTER the move's fact — under any backlog in the resolved-events consumer, or
// on redelivery — and still carries the old owner; last-writer-wins would hand the device
// straight back to the customer that sold it, and keep it there for as long as the device
// stays quiet. So the stamp is ORDERED, by a precedence time kept beside it
// (DeviceState.ScopeAnchorsAt), and a stamp replaces the stored one only when strictly
// later:
//
//   - an event's precedence is its RECEIPT time (ResolvedEvent.ProcessedTime, set by the
//     platform when the event arrived, never by the device), which is no later than the
//     moment its anchors were read;
//   - a fact's precedence is the moment its anchors were read PLUS ownershipSettle. The drain
//     reads them after the change has committed, so that moment is never earlier than the
//     change — later by however long the fact waited in the queue.
//
// An event received before a change therefore never overrides the change's fact, whichever
// arrives first. One received inside the settle window does not either; it carries the new
// owners anyway once device-management's caches have caught up, so nothing is lost. One
// received after the window does: it was resolved after the change, so it carries the new
// owners too, and it is what restamps a device whose fact has not arrived yet — or was
// dropped HERE, after its last delivery failed to write (handle). Two facts order by the
// moments their anchors were read.
// The rule is a max over a total order, so it commutes, which is what lets the batch path
// fold a device's events in Go or in SQL and land where merging them one at a time would.
//
// What it does not close, stated rather than implied: an event received more than
// ownershipSettle after a change but resolved from a device-management cache that missed the
// change's eviction (a broadcast lost with nobody disconnected, see the device-management
// Caches doc) carries the old owners until that cache expires. The event-history stamp has
// the same exposure for the same events; it is bounded by that cache's time to live, and
// the next event resolved after it repairs the live view. And a fact for a device with no
// live state yet stamps nothing — there is no row — so the device's first event stamps
// whatever it was resolved with.
//
// 🔴 ONLY THE LIVE VIEW IS RESTAMPED. An alarm keeps the stamp it was raised with, as an event
// does: ADR-081 decision 4 stamps history when it is written, so a customer that owned a
// device when an alarm was raised keeps seeing that alarm after selling it, including while
// it is still active. That is device-management's to decide and is unchanged here.

// ownershipSettle is how much later than a change an event must have been received to
// override the change's fact. It covers the two things that can make an event received just
// after a change still carry the old owners: the clock skew between the event-sources pod
// that stamped its receipt time and the device-management pod that stamped the change, and
// the moment it takes device-management's other replicas to drop a cached owner set.
const ownershipSettle = 30 * time.Second

// Owners is a device's owners as one event carried them, and the precedence of that stamp.
// A zero At means the event carried no stamp at all, and leaves the stored one alone; an
// empty Anchors with a real At is a stamp of its own — "owned by nobody".
type Owners struct {
	Anchors []string
	At      time.Time
}

// ownerSet is anchors sorted and de-duplicated, never nil: an empty set is stored as '{}',
// which reads the same to every scope as NULL but says "checked, owned by nobody".
func ownerSet(anchors []string) rdb.Anchors {
	out := rdb.Anchors{}
	for _, a := range anchors {
		if a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

// applyOwners folds an event's stamp into row: it replaces the stored stamp when it is
// strictly later (see the file comment). It is the Go half of the rule; heartbeatSet's
// scope_anchors arms are the SQL half, and projection_equivalence_test.go pins the two.
func applyOwners(row *DeviceState, o Owners) {
	if o.At.IsZero() {
		return
	}
	at := storedTime(o.At)
	if row.ScopeAnchorsAt.Valid && !at.After(row.ScopeAnchorsAt.Time) {
		return
	}
	row.ScopeAnchors = ownerSet(o.Anchors)
	row.ScopeAnchorsAt = sql.NullTime{Time: at, Valid: true}
}

// ApplyDeviceOwnership restamps a device's live view with its complete current owners from a
// device ownership-changed fact, and reports whether it did. It does not when the device has
// no live state yet (no row: its first event stamps it), or when the stored stamp is not
// older than the fact's precedence — an older fact redelivered, or an event received well
// after this change, which already carries what this fact says.
//
// ONE row is written, and every latest reading and position of the device moves with it:
// they hold no stamp of their own (deviceOwners). The statement is one conditional UPDATE of
// one row in a transaction of its own, so it takes one lock and cannot deadlock with the
// batch writers, which take device rows in token order.
func (api *Api) ApplyDeviceOwnership(ctx context.Context, deviceToken string, anchors []string, changedAt time.Time) (bool, error) {
	at := storedTime(changedAt).Add(ownershipSettle)
	res := api.RDB.DB(ctx).Model(&DeviceState{}).
		Where("device_token = ?", deviceToken).
		Where("scope_anchors_at IS NULL OR scope_anchors_at < ?", at).
		Updates(map[string]any{
			"scope_anchors":    ownerSet(anchors),
			"scope_anchors_at": sql.NullTime{Time: at, Valid: true},
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
