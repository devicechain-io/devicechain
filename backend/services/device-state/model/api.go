// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Api struct {
	RDB *rdb.RdbManager
}

// Create a new API instance.
func NewApi(rdb *rdb.RdbManager) *Api {
	api := &Api{}
	api.RDB = rdb
	return api
}

// Interface for device state API (used for mocking)
type DeviceStateApi interface {
	MergeDeviceState(ctx context.Context, deviceToken string, occurredAt time.Time, pt *PresenceTransition, id DeviceIdentity) (*DeviceState, error)
	DeviceStatesByDeviceToken(ctx context.Context, deviceTokens []string) ([]*DeviceState, error)
	DeviceStatesByExternalId(ctx context.Context, externalIds []string) ([]*DeviceState, error)
	AssertedDeviceStates(ctx context.Context, source string, activeOnly bool, afterId uint64, pageSize int) ([]*DeviceState, error)
	DeviceStates(ctx context.Context, criteria DeviceStateSearchCriteria) (*DeviceStateSearchResults, error)
	SweepInactive(ctx context.Context, now time.Time) (int64, error)
	MergeLatestMeasurements(ctx context.Context, deviceToken string, inputs []LatestMeasurementInput) error
	LatestMeasurementsByDeviceToken(ctx context.Context, deviceToken string) ([]*LatestMeasurement, error)
	MergeLatestLocations(ctx context.Context, deviceToken string, inputs []LatestLocationInput) error
	LatestLocationsByDeviceToken(ctx context.Context, deviceTokens []string) ([]*LatestLocation, error)
}

// PresenceTransition is an authoritative presence claim (ADR-067) carried by a
// StateChange event from a presence-asserting transport. It is nil for a plain data
// event. SessionId is the producer's monotone per-session id (a host-observed connect
// epoch, not e.g. a raw Sparkplug bdSeq). OccurredAt is the transition's event time.
//
// Claim carries what is being asserted, and it is an enum rather than the bool it
// replaced because CONNECTED and DISCONNECTED are no longer the whole vocabulary: a
// DEMOTED claim releases the source's custody of the device and says nothing at all
// about connectivity. Its zero value is invalid, so a literal that forgets the field
// is refused by presence.Decide instead of silently reading as a death.
type PresenceTransition struct {
	Claim     presence.Claim
	SessionId uint64
	// ExpectedSessionId is a compare-and-set precondition, zero for every ordinary
	// transport advisory. It is the only way a transition whose SessionId is LOWER than
	// the stored one is applied: a producer that read this projection can name the
	// session it saw, and the transition applies only if the row still holds it. See
	// presence.Incoming — a session id can legitimately go backwards, because it is
	// minted from the clock of whichever broker node the device landed on.
	ExpectedSessionId uint64
	OccurredAt        time.Time
}

// DeviceIdentity carries the denormalized identity fields the projection stores from
// the resolved event: the device's external id (ADR-049) and the event Source that
// last drove it (e.g. "sparkplug:{hostId}"). Both let the projection be queried by
// transport-native identity for SP4b failover reconciliation without a hop into
// device-management — externalId to key a device to a Sparkplug topic, Source to scope
// the reconciliation to its own adapter. Empty fields are never written over a
// non-empty stored value (they are stable per device; a rare source-less event must not
// blank them).
type DeviceIdentity struct {
	ExternalId string
	Source     string
}

// newDeviceState builds the row for the first event ever seen for a device. A plain
// data event creates it active (today's behavior); an authoritative CONNECTIVITY
// transition creates it ASSERTED with Active set to exactly the transition — so a
// first-ever DISCONNECT records a dead device, never a connected one (ADR-067).
//
// 🔴 A DEMOTION CREATES NOTHING AUTHORITATIVE, and this branch is the reason the check
// is written here rather than left to presence.Decide. Row creation does not go through
// Decide at all — there is no Prior to weigh against — so acceptsDemotion's
// prior.HasTime conjunct, which is what refuses a demotion of a device that was never
// asserted, cannot fire on this path. Without the guard below, a demotion for a device
// with no row would CREATE one marked ASSERTED: the exact state the demotion exists to
// leave, conjured by the event that meant to leave it.
//
// What it creates instead is a bare INFERRED row: no session, no presence time, no
// connect or activity stamps. That is the honest record — a source has released custody
// of a device the projection never knew about, so the platform knows nothing about its
// presence. The row is inert (SweepInactive only scans active rows) and the device's
// next data event populates it the ordinary inferred way.
func newDeviceState(deviceToken string, occurredAt time.Time, pt *PresenceTransition, id DeviceIdentity) *DeviceState {
	ds := &DeviceState{
		DeviceToken:       deviceToken,
		ExternalId:        id.ExternalId,
		Source:            id.Source,
		PresenceSource:    PresenceSourceInferred,
		InactivityTimeout: config.DefaultInactivityTimeout,
	}
	if pt != nil && pt.Claim == presence.ClaimDemoted {
		return ds
	}
	if pt != nil {
		ds.PresenceSource = PresenceSourceAsserted
		ds.SessionId = pt.SessionId
		ds.PresenceTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
		ds.Active = pt.Claim == presence.ClaimConnected
		if pt.Claim == presence.ClaimConnected {
			ds.LastConnectTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
			ds.LastActivityTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
		} else {
			ds.LastDisconnectTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
		}
		return ds
	}
	ds.Active = true
	ds.LastActivityTime = sql.NullTime{Time: occurredAt, Valid: true}
	ds.LastConnectTime = sql.NullTime{Time: occurredAt, Valid: true}
	return ds
}

// MergeDeviceState updates (or creates) the live state projection for a device in
// response to a resolved event. It is the write path of the projection. A non-nil
// pt is an authoritative presence transition (ADR-067): it promotes the device to
// ASSERTED and drives Active under the monotonic (SessionId, OccurredTime) guard. A
// nil pt is a plain data event: for an INFERRED device it is an implicit heartbeat
// (unchanged); for an ASSERTED device it advances activity but NEVER flips Active —
// a stray data event can't resurrect a device the platform knows is dead.
func (api *Api) MergeDeviceState(ctx context.Context, deviceToken string, occurredAt time.Time, pt *PresenceTransition, id DeviceIdentity) (*DeviceState, error) {
	// The 5 decode workers can process two events for the same device
	// concurrently. Read-modify-write the row inside a transaction that takes a
	// row lock (SELECT … FOR UPDATE), so same-device merges serialize and a later
	// write can't regress LastActivityTime or clobber a reconnect.
	found := &DeviceState{}
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("device_token = ?", deviceToken).First(found)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				// First event seen for this device: create a new row. A concurrent
				// first event loses the (tenant_id, device_token) unique index race
				// and errors out (redelivered), rather than producing a duplicate.
				found = newDeviceState(deviceToken, occurredAt, pt, id)
				return tx.Create(found).Error
			}
			return result.Error
		}

		// Keep the denormalized identity fresh (both are stable per device, but a device
		// could be assigned an external id, or first produce via a given source, after its
		// first event). Only overwrite with a non-empty value so a rare event that resolves
		// without one can't blank it.
		if id.ExternalId != "" && found.ExternalId != id.ExternalId {
			found.ExternalId = id.ExternalId
		}
		if id.Source != "" && found.Source != id.Source {
			found.Source = id.Source
		}

		if pt != nil {
			// Authoritative presence transition: promote to ASSERTED (first-sight) and apply
			// the connectivity edge under the shared monotonic guard (ADR-067). presence.Decide
			// SPLITS the two effects the old fused guard conflated: "advance the ordering marker"
			// (any in-order transition) is distinct from "the connectivity state flipped" (Active
			// actually changed). So a day-late higher-session DISCONNECT over an already-dead
			// device advances the marker (rejecting a later stale intermediate-session edge) but
			// does NOT move LastDisconnectTime or re-fire the DETECT offline edge — the S3
			// same-state-higher-session non-event. The identical predicate keys the DETECT engine.
			// wasInferred is the FIRST-authoritative-word promotion: the device's presence was
			// only inferred so far (data-silence sweep), so this StateChange establishes the
			// authoritative baseline and must record its edge even without a state flip — an
			// authoritative death time supersedes a synthetic swept one (captured before we
			// stamp ASSERTED just below).
			wasInferred := found.PresenceSource != PresenceSourceAsserted
			found.PresenceSource = PresenceSourceAsserted
			d := presence.Decide(
				presence.Prior{
					SessionId: found.SessionId,
					Time:      found.PresenceTime.Time,
					HasTime:   found.PresenceTime.Valid,
					Connected: found.Active,
				},
				presence.Incoming{
					SessionId:         pt.SessionId,
					ExpectedSessionId: pt.ExpectedSessionId,
					OccurredAt:        pt.OccurredAt,
					Claim:             pt.Claim,
				},
			)
			if d.Ordered {
				found.SessionId = pt.SessionId
				found.PresenceTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
				found.Active = pt.Claim == presence.ClaimConnected // idempotent when the state did not flip
				if pt.Claim == presence.ClaimConnected {
					// A higher session is a genuine reconnect even when Active was already true
					// (a new epoch is a new physical connection), so refresh LastConnectTime on a
					// flip OR a new session OR the first authoritative word; a same-session
					// duplicate connect leaves it frozen. (A producer that never varies its session
					// id cannot signal a reconnect-over-missed-disconnect this way; both current
					// producers — Sparkplug SP4a, LwM2M L1 — mint a fresh epoch per connect.)
					if d.Flipped || d.NewSession || wasInferred {
						found.LastConnectTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
						found.InactivityAlarmTime = sql.NullTime{}
					}
				} else if d.Flipped || wasInferred {
					// A true CONNECTED→dead flip, or the first authoritative word over an
					// inferred-dead device, records the disconnect time. A higher-session
					// DISCONNECT over an ALREADY-ASSERTED-dead device is a late echo —
					// first-known-dead wins (the S3a history table retains the later row for audit).
					found.LastDisconnectTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
				}
			}
			// A CONNECTED is also activity; a DISCONNECTED is the opposite of activity,
			// so it must not advance LastActivityTime.
			if pt.Claim == presence.ClaimConnected && (!found.LastActivityTime.Valid || pt.OccurredAt.After(found.LastActivityTime.Time)) {
				found.LastActivityTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
			}
			return tx.Save(found).Error
		}

		// Plain data event. An ASSERTED device takes Active ONLY from a StateChange, so
		// a data event must not flip it (it still advances activity below); an INFERRED
		// device treats every event as an implicit heartbeat (unchanged behavior).
		if found.PresenceSource != PresenceSourceAsserted && !found.Active {
			found.Active = true
			found.LastConnectTime = sql.NullTime{Time: occurredAt, Valid: true}
			found.InactivityAlarmTime = sql.NullTime{}
		}
		if !found.LastActivityTime.Valid || occurredAt.After(found.LastActivityTime.Time) {
			found.LastActivityTime = sql.NullTime{Time: occurredAt, Valid: true}
		}
		return tx.Save(found).Error
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// MergeLatestMeasurements upserts the current value of each named measurement for
// a device from a resolved measurement event. Like MergeDeviceState it is a
// read-modify-write under a row lock so the concurrent decode workers serialize
// per key, and it only advances a key when the incoming reading is newer
// (out-of-order safe): a delayed old value never clobbers a newer stored one.
// All entries in the event commit together in one transaction.
func (api *Api) MergeLatestMeasurements(ctx context.Context, deviceToken string, inputs []LatestMeasurementInput) error {
	if len(inputs) == 0 {
		return nil
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, in := range inputs {
			found := &LatestMeasurement{}
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("device_token = ? AND name = ?", deviceToken, in.Name).First(found)
			if result.Error != nil {
				if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					// First value for this (device, name): create it. A concurrent
					// first create loses the unique-index race and errors out
					// (redelivered), rather than producing a duplicate row.
					created := &LatestMeasurement{
						DeviceToken:  deviceToken,
						Name:         in.Name,
						Value:        in.Value,
						Classifier:   in.Classifier,
						Unit:         in.Unit,
						DataType:     in.DataType,
						OccurredTime: in.OccurredTime,
					}
					if err := tx.Create(created).Error; err != nil {
						return err
					}
					continue
				}
				return result.Error
			}
			// Existing row: overwrite only when this reading is strictly newer, so a
			// late-arriving old value (or a redelivered duplicate) is ignored.
			if in.OccurredTime.After(found.OccurredTime) {
				found.Value = in.Value
				found.Classifier = in.Classifier
				found.Unit = in.Unit
				found.DataType = in.DataType
				found.OccurredTime = in.OccurredTime
				if err := tx.Save(found).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// MergeLatestLocations upserts a device's last-known position from the fixes carried
// by one resolved location event. One row per (tenant, device) — a device has exactly
// one current position — so every fix in the event contends for the same row and the
// newest one wins.
//
// 🔴 THE PROJECTION MUST NOT GO BACKWARDS, and that is the whole reason this is a
// read-modify-write under a row lock rather than a blind upsert. The resolved-events
// stream redelivers (an unacked message comes back) and does not guarantee order across
// the five merge workers, so "last write wins" would let a redelivered old fix teleport
// a device back to where it used to be — silently, and indistinguishably from the device
// actually having returned there. The guard is therefore on the fix's OCCURRED time, not
// on arrival: a fix is applied only when it is STRICTLY newer than the stored one, which
// makes redelivery of the current fix a no-op as well.
//
// Same shape as MergeLatestMeasurements deliberately: SELECT … FOR UPDATE serializes the
// concurrent workers on the row, so the compare-then-write cannot interleave, and all the
// event's fixes commit together in one transaction.
func (api *Api) MergeLatestLocations(ctx context.Context, deviceToken string, inputs []LatestLocationInput) error {
	if len(inputs) == 0 {
		return nil
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, in := range inputs {
			found := &LatestLocation{}
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("device_token = ?", deviceToken).First(found)
			if result.Error != nil {
				if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					// First fix ever seen for this device: create it. A concurrent first
					// create loses the (tenant_id, device_token) unique-index race and
					// errors out (redelivered), rather than producing a duplicate row.
					created := &LatestLocation{
						DeviceToken:  deviceToken,
						Latitude:     in.Latitude,
						Longitude:    in.Longitude,
						Elevation:    in.Elevation,
						Accuracy:     in.Accuracy,
						Speed:        in.Speed,
						Heading:      in.Heading,
						OccurredTime: in.OccurredTime,
					}
					if err := tx.Create(created).Error; err != nil {
						return err
					}
					continue
				}
				return result.Error
			}
			// Existing row: overwrite only when this fix is strictly newer. Every field is
			// replaced together — a fix is one atomic observation, so carrying forward the
			// previous fix's speed or heading beside a new position would synthesize a
			// reading no device ever reported.
			if in.OccurredTime.After(found.OccurredTime) {
				found.Latitude = in.Latitude
				found.Longitude = in.Longitude
				found.Elevation = in.Elevation
				found.Accuracy = in.Accuracy
				found.Speed = in.Speed
				found.Heading = in.Heading
				found.OccurredTime = in.OccurredTime
				if err := tx.Save(found).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// LatestLocationsByDeviceToken returns the last-known position of each of the given
// devices, device-token-ordered. A device that has never produced a location event
// simply has no row, so the result is short rather than carrying a placeholder — the
// caller distinguishes "never located" from "located here" by absence.
func (api *Api) LatestLocationsByDeviceToken(ctx context.Context, deviceTokens []string) ([]*LatestLocation, error) {
	found := make([]*LatestLocation, 0)
	result := api.RDB.DB(ctx).Where("device_token in ?", deviceTokens).Order("device_token asc").Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// LatestMeasurementsByDeviceToken returns the current value of every measurement
// name for a device (name-ordered) — the live "current readings" surface.
func (api *Api) LatestMeasurementsByDeviceToken(ctx context.Context, deviceToken string) ([]*LatestMeasurement, error) {
	found := make([]*LatestMeasurement, 0)
	result := api.RDB.DB(ctx).Where("device_token = ?", deviceToken).Order("name asc").Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get device states by originating device token.
func (api *Api) DeviceStatesByDeviceToken(ctx context.Context, deviceTokens []string) ([]*DeviceState, error) {
	found := make([]*DeviceState, 0)
	result := api.RDB.DB(ctx).Find(&found, "device_token in ?", deviceTokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// DeviceStatesByExternalId returns the device states for the given external ids.
func (api *Api) DeviceStatesByExternalId(ctx context.Context, externalIds []string) ([]*DeviceState, error) {
	found := make([]*DeviceState, 0)
	result := api.RDB.DB(ctx).Find(&found, "external_id in ?", externalIds)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// AssertedDeviceStates returns every ASSERTED device state for the given event source
// (ADR-067 SP4b failover reconciliation). It is tenant-scoped by the RDB callback from
// the caller's context, and additionally source-scoped so an adapter reconciling one
// broker never sees — and so can never cross-disconnect — a sibling source's devices on
// the same tenant.
//
// 🔑 activeOnly SEPARATES TWO DIFFERENT QUESTIONS, and the answer to the second is what
// makes a repair applicable at all.
//
//   - true  — "who does the projection believe is online?" This is what a reconciler
//     diffs against a live inventory to find deaths it missed.
//   - false — that, PLUS the asserted devices it believes are OFFLINE. A repair for one
//     of those has to carry a session id that presence.Decide will accept, and Decide
//     takes a different session only when it is HIGHER. A broker node with a trailing
//     clock mints a LOWER session on reconnect, so a repair carrying the connection's
//     own id is rejected — on that pass and on every pass after it. The caller can only
//     avoid that by knowing the session the projection is already holding, which is
//     exactly what the inactive rows carry.
//
// There is deliberately no default: a caller that has not thought about which question
// it is asking is a caller about to emit repairs that cannot apply.
//
// 🔴 IT RETURNS ONE PAGE, walked from a row-id cursor, because this set is exactly as
// large as the tenant's asserted fleet. Read whole, it crosses the cross-service read
// cap somewhere in the thousands of devices and the response is CUT OFF rather than
// shortened — and a truncated presence set is not a smaller answer to the same question,
// it is a wrong answer to a different one: the missing devices read as "not asserted",
// which in the reconciler that consumes this means marking connected devices offline.
//
// Keyset, not offset. The id is stable and ascending, so a page picks up exactly where
// the last one ended even though rows are being written underneath the walk; OFFSET
// would skip a row every time one was inserted before the cursor, and a skipped row here
// is a device that silently does not get reconciled.
func (api *Api) AssertedDeviceStates(ctx context.Context, source string, activeOnly bool,
	afterId uint64, pageSize int) ([]*DeviceState, error) {
	if pageSize < 1 || pageSize > MaxAssertedPageSize {
		return nil, fmt.Errorf("pageSize must be between 1 and %d, got %d", MaxAssertedPageSize, pageSize)
	}
	found := make([]*DeviceState, 0, pageSize)
	db := api.RDB.DB(ctx).
		Where("presence_source = ? AND source = ?", PresenceSourceAsserted, source).
		Where("id > ?", afterId).
		Order("id ASC").
		Limit(pageSize)
	if activeOnly {
		db = db.Where("active = ?", true)
	}
	if result := db.Find(&found); result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for device states that meet criteria.
func (api *Api) DeviceStates(ctx context.Context, criteria DeviceStateSearchCriteria) (*DeviceStateSearchResults, error) {
	results := make([]DeviceState, 0)
	db, pag := api.RDB.ListOf(ctx, &DeviceState{}, func(result *gorm.DB) *gorm.DB {
		if criteria.Active != nil {
			result = result.Where("active = ?", *criteria.Active)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceStateSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// SweepInactive is the core of the background inactivity monitor. The caller
// passes a system context so the scan spans all tenants; each row keeps its own
// tenant_id on Save. It marks every active device whose last activity is older
// than its inactivity timeout as inactive and returns how many were flipped.
func (api *Api) SweepInactive(ctx context.Context, now time.Time) (int64, error) {
	// Only INFERRED devices are swept: an ASSERTED device's offline is an
	// authoritative DEATH/LWT, never a data-silence timeout (ADR-067 decision 6), so
	// the sweep must not flip it. The predicate is applied at the scan so an asserted
	// device is never even considered.
	active := make([]DeviceState, 0)
	result := api.RDB.DB(ctx).Where("active = ? AND presence_source <> ?", true, PresenceSourceAsserted).Find(&active)
	if result.Error != nil {
		return 0, result.Error
	}

	var flipped int64
	for i := range active {
		row := active[i]
		if !isInactive(row.LastActivityTime, row.InactivityTimeout, now) {
			continue
		}
		// Flip with a conditional update keyed to the snapshot's activity time, not
		// a full-row Save of the stale snapshot: if a MergeDeviceState landed new
		// activity since the scan, last_activity_time no longer matches and the row
		// is left active (no flip), so the sweep can't clobber a just-active device.
		res := api.RDB.DB(ctx).Model(&DeviceState{}).
			Where("id = ? AND active = ? AND last_activity_time = ?", row.ID, true, row.LastActivityTime).
			Updates(map[string]any{
				"active":                false,
				"last_disconnect_time":  sql.NullTime{Time: now, Valid: true},
				"inactivity_alarm_time": sql.NullTime{Time: now, Valid: true},
			})
		if res.Error != nil {
			return flipped, res.Error
		}
		flipped += res.RowsAffected
	}
	return flipped, nil
}

// isInactive reports whether a device whose last activity was at last (with the
// given per-device timeout in seconds) should be considered inactive at now. A
// device with no recorded activity is never flipped. A non-positive timeout is
// treated as the configured default.
func isInactive(last sql.NullTime, timeoutSecs int, now time.Time) bool {
	if !last.Valid {
		return false
	}
	if timeoutSecs <= 0 {
		timeoutSecs = config.DefaultInactivityTimeout
	}
	return now.Sub(last.Time) > time.Duration(timeoutSecs)*time.Second
}
