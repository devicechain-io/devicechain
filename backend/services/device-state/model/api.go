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
	// demotions publishes operator presence demotions onto the inbound-events stream.
	// It is wired during startup rather than at construction because it needs an
	// initialized NatsManager, which does not exist when the Api is built; nil means
	// the process cannot emit, and DemoteAssertedPresence fails closed on it.
	demotions *DemotionEmitter
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
	AssertedStatesForDemotion(ctx context.Context, source string, tokens *[]string, afterId uint64, limit int) ([]*DeviceState, error)
	DemoteAssertedPresence(ctx context.Context, source string, tokens *[]string, afterId uint64, limit int, actor, reason string) (*PresenceDemotionResult, error)
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
	if pt != nil {
		// 🔴 THE GUARD IS POSITIVE, AND IT HAS TO BE. Row creation is the ONE presence path
		// that never consults presence.Decide, so nothing here refuses a claim on its own
		// terms: whatever this branch admits becomes an ASSERTED row with a fabricated
		// connectivity edge, and command-delivery HOLDS commands for an asserted-dead
		// device. Naming only the claims that are refused leaves every claim that is merely
		// NOT ClaimConnected reading as a death — which is the exact bool-shaped misreading
		// the Claim type exists to forbid, and which the zero value walks straight into.
		//
		// So a DEMOTED claim (custody released for a device the projection never knew) and
		// an INVALID one (a producer defect) both fall through to the inert row, as does any
		// claim added later. Nothing authoritative has been said about this device; the row
		// is created knowing nothing, and its next data event populates it the ordinary
		// inferred way.
		if pt.Claim != presence.ClaimConnected && pt.Claim != presence.ClaimDisconnected {
			return ds
		}
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
// pt is an authoritative presence claim (ADR-067) and can move the row in either
// direction: a CONNECTED/DISCONNECTED claim promotes the device to ASSERTED and drives
// Active under the monotonic (SessionId, OccurredTime) guard, while a DEMOTED claim
// returns it to INFERRED without touching Active at all. A nil pt is a plain data
// event: for an INFERRED device it is an implicit heartbeat (unchanged); for an
// ASSERTED device it advances activity but NEVER flips Active — a stray data event
// can't resurrect a device the platform knows is dead.
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
			// neverAsserted is the FIRST-authoritative-word promotion: no source has ever
			// spoken authoritatively about this device, so this StateChange establishes the
			// authoritative baseline and must record its edge even without a state flip — an
			// authoritative death time supersedes a synthetic swept one.
			//
			// It reads PresenceTime rather than PresenceSource, and the difference is
			// load-bearing once a demotion exists: a DEMOTED row is INFERRED again but KEEPS
			// the ordering stamp of the transition that demoted it. Spelled the old way, every
			// transition arriving after a demotion would look like a first authoritative word,
			// and a late higher-session DISCONNECT would overwrite a real LastDisconnectTime.
			// The two fields are written together on every promotion (below, and in
			// newDeviceState), so on a row that has never been demoted the two spellings are
			// the same predicate.
			neverAsserted := !found.PresenceTime.Valid
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
				if d.Demoted {
					// The source has released custody of this device: it is no longer willing to
					// speak for the device's connectivity, and it is asserting NOTHING about
					// whether the device is up or down. So the row returns to INFERRED — which
					// hands it back to the inactivity sweep and to the implicit-heartbeat path,
					// the two mechanisms that can repair it without any further word from the
					// source — and the ordering stamp advances so a late echo from the released
					// session cannot re-assert it.
					//
					// SessionId, Active, LastConnectTime, LastDisconnectTime, LastActivityTime
					// and InactivityAlarmTime are deliberately untouched. A demotion is a
					// statement about who has custody, never about what the device is doing;
					// rewriting Active here would fabricate a connectivity edge out of an
					// administrative one, and DETECT — which keys off the same predicate —
					// would raise an offline alarm for every demoted device. SessionId stays so
					// the released session remains named: it is what acceptsDemotion matched,
					// and it is what a re-assertion must beat.
					found.PresenceSource = PresenceSourceInferred
					found.PresenceTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
				} else {
					found.PresenceSource = PresenceSourceAsserted
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
						if d.Flipped || d.NewSession || neverAsserted {
							found.LastConnectTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
							found.InactivityAlarmTime = sql.NullTime{}
						}
					} else if d.Flipped || neverAsserted {
						// A true CONNECTED→dead flip, or the first authoritative word over an
						// inferred-dead device, records the disconnect time. A higher-session
						// DISCONNECT over an ALREADY-ASSERTED-dead device is a late echo —
						// first-known-dead wins (the S3a history table retains the later row for audit).
						found.LastDisconnectTime = sql.NullTime{Time: pt.OccurredAt, Valid: true}
					}
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

		// A data event OLDER than the activity already recorded is evidence about the past,
		// not the present. The activity advance has always been guarded that way; the
		// resurrect was not, so a redelivered or store-and-forward event from before the
		// silence could bring a swept device back to life and then immediately be swept
		// again. Both effects now read the SAME test — a stale event is stale for every
		// purpose, not just for the timestamp.
		//
		// 🔴 THE TEST IS "NOT OLDER" RATHER THAN "NEWER", AND THE BOUNDARY CASE IS WHY. A
		// device with a frozen clock stamps every event identically — broken, but still a
		// device SENDING DATA. Under a strict After() its first sweep would be its last
		// state change: every later event compares equal, never resurrects, and the row
		// reads inactive forever while telemetry keeps arriving. Trading a flap for a
		// permanent lie is not a repair.
		//
		// It deliberately does not close every re-sweep. The sweep leaves LastActivityTime
		// alone, so an event newer than the last recorded activity but still from inside the
		// silence resurrects and is swept again next pass. That device really did produce
		// data at that time; whether it is alive NOW is the sweep's question, not this one's.
		fresh := !found.LastActivityTime.Valid || !occurredAt.Before(found.LastActivityTime.Time)
		if fresh && found.PresenceSource != PresenceSourceAsserted && !found.Active {
			found.Active = true
			found.LastConnectTime = sql.NullTime{Time: occurredAt, Valid: true}
			found.InactivityAlarmTime = sql.NullTime{}
		}
		if fresh {
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

// AssertedStatesForDemotion returns ONE PAGE of the calling tenant's ASSERTED device
// states for one event source — the set demoteAssertedPresence releases (ADR-067
// demotion). It is the sibling of AssertedDeviceStates and shares its keyset walk and
// its refusal to serve an unusable page, for the same reasons recorded there.
//
// It differs from that query in two ways, both deliberate. There is no activeOnly:
// a stopped presence tap freezes rows in BOTH directions — the ones it left connected
// and the ones it left dead — and one demotion repairs both, so a demotion that could
// only reach half the set would leave the louder half wedged. And it takes an optional
// device-token narrowing, because an operator repairing one machine should not have to
// aim a fleet-wide write to do it.
//
// 🔴 tokens IS THREE-STATE, AND THE EMPTY STATE IS THE ONE THAT MATTERS:
//
//   - nil — no narrowing. The whole source.
//   - a NON-EMPTY slice — only those devices, ANDed with the source. It narrows
//     WITHIN the source and can never reach past it, so a token belonging to another
//     source is simply not found rather than demoted.
//   - an EMPTY slice — NO DEVICES. Zero rows, deliberately and without touching the
//     database.
//
// The empty case is the opposite of the choice CommandSearchCriteria.Statuses makes
// with the same Go shape, and the divergence is the point. That one is a READ filter,
// where a caller who built a list and ended up with none of them almost always means
// "no preference", and the other reading turns that into a silently empty page. This
// is a WRITE whose unnarrowed blast radius is an entire event source's fleet, so the
// same slip has to fail in the direction that does nothing rather than the direction
// that does everything. `deviceTokens: []` demotes nothing.
//
// ⚠️ THE SHORT-CIRCUIT BELOW IS NOT WHAT MAKES THE EMPTY CASE SAFE TODAY, and saying
// otherwise would be the comment-asserts-the-invariant trap. Measured, not inferred:
// gorm renders `device_token IN ?` over an empty slice as a predicate that matches
// nothing, so deleting the short-circuit changes no result — a mutation pass reports it
// as an equivalent mutant, correctly. The trap that DOES exist is gorm's INLINE-condition
// form, `Find(&out, ids)`, which drops an empty slice and degrades into an unfiltered
// select; core/rdb.FindByIds owns that case and pins it. The two forms sit side by side
// across this codebase and behave oppositely on the same input, which is exactly why the
// distinction is written down here rather than assumed.
//
// It is kept for two reasons that do not depend on gorm: it makes "an empty list demotes
// nothing" a property of THIS code rather than of a rendering decision in a dependency —
// the one place a version bump could silently invert the answer for a WRITE — and it
// spares a round trip for a request that cannot return anything.
func (api *Api) AssertedStatesForDemotion(ctx context.Context, source string, tokens *[]string,
	afterId uint64, limit int) ([]*DeviceState, error) {
	if limit < 1 || limit > MaxAssertedPageSize {
		return nil, fmt.Errorf("limit must be between 1 and %d, got %d", MaxAssertedPageSize, limit)
	}
	if tokens != nil && len(*tokens) == 0 {
		return nil, nil
	}
	found := make([]*DeviceState, 0, limit)
	db := api.RDB.DB(ctx).
		Where("presence_source = ? AND source = ?", PresenceSourceAsserted, source).
		Where("id > ?", afterId).
		Order("id ASC").
		Limit(limit)
	if tokens != nil {
		db = db.Where("device_token IN ?", *tokens)
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
// tenant_id. It marks every active device whose last activity is older than its
// inactivity timeout as inactive and returns how many were flipped.
//
// 🔴 THIS IS ONE STATEMENT ON PURPOSE, AND THE REASON IS NOT ONLY SPEED. It used to
// be a cross-tenant Find() of every active row into a Go slice, followed by one
// round-trip UPDATE per candidate — so a fleet-sized sweep materialised the fleet in
// memory and issued a query per device. The stale-ONLINE repair is exactly what hands
// it a whole fleet at once, so the worst input arrives precisely when the instance is
// least able to absorb it.
//
// 🔑 Collapsing it also DELETES a race rather than guarding it. The old loop carried a
// `last_activity_time = <snapshot>` precondition on each UPDATE so a MergeDeviceState
// landing between the scan and the write could not be clobbered. That precondition
// existed only because the read and the write were separate statements; here they are
// the same statement, so there is no snapshot that can go stale.
//
// 🔴 There is NO index on (active, presence_source, last_activity_time) — device_states
// carries only the (tenant_id, device_token) unique index — so this is still a
// sequential scan. It is one scan inside the database instead of a full table read into
// this process, which is the part that was unbounded. Adding the index is a migration
// and is deliberately NOT part of this change.
func (api *Api) SweepInactive(ctx context.Context, now time.Time) (int64, error) {
	db := api.RDB.DB(ctx)
	frag, args, err := inactivitySQL(db.Dialector.Name(), now)
	if err != nil {
		return 0, err
	}
	// Only INFERRED devices are swept: an ASSERTED device's offline is an
	// authoritative DEATH/LWT, never a data-silence timeout (ADR-067 decision 6), so
	// the sweep must not flip it. The predicate is applied in the statement so an
	// asserted device is never even considered.
	res := db.Model(&DeviceState{}).
		Where("active = ? AND presence_source <> ?", true, PresenceSourceAsserted).
		Where(frag, args...).
		Updates(map[string]any{
			"active":                false,
			"last_disconnect_time":  sql.NullTime{Time: now, Valid: true},
			"inactivity_alarm_time": sql.NullTime{Time: now, Valid: true},
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// inactivitySQL is the SQL twin of isInactive: it returns the fragment and args
// selecting rows whose last activity is older than their own inactivity timeout at
// now. The two are pinned together by TestSweepAgreesWithIsInactive, which runs one
// case table through both — a rule with two implementations is only safe while
// something fails when they disagree.
//
// 🔴 THE DIALECTS ARE NOT INTERCHANGEABLE AND THE OBVIOUS SQLITE FORM IS WRONG.
// Measured on the pinned driver (glebarez/modernc), a timestamp column holds TEXT like
// "2026-06-24T12:00:00Z", and the julianday() form —
// (julianday(now) - julianday(last)) * 86400.0 > timeout — reports a device inactive
// at EXACTLY its timeout, because the float product lands a hair above the integer.
// It is correct everywhere except the boundary, which is the one place a sweep
// decides. unixepoch() compares whole seconds as integers and gets the boundary right.
// datetime() also measured correct, but it compares TEXT against a differently
// formatted TEXT literal and only works through affinity rules subtle enough that a
// future reader could not check it by inspection; unixepoch()'s mechanism is legible.
//
// Postgres multiplies an integer by INTERVAL '1 second', which is exact integer
// interval arithmetic — no float, no EXTRACT() version differences.
//
// Both forms are strict, and they are the same comparison written from opposite sides:
// `last < now - timeout` is `now - last > timeout`.
//
// 🔴 NOTHING IN CI EXECUTES THE POSTGRES STRING — the unit tests run on SQLite and
// hack/migration-diff.sh compares schema only, no rows. It was therefore verified by
// hand against a real PostgreSQL 16: correct at +599s, at EXACTLY the timeout, at
// +601s and at +2h, with the zero/negative fallback flipping the right rows, and with
// a deliberately wrong interval as the negative control so the run could fail. Redo
// that by hand if this string changes; a green suite says nothing about it.
func inactivitySQL(dialect string, now time.Time) (string, []any, error) {
	// `last_activity_time IS NOT NULL` is REDUNDANT and kept on purpose. SQL's
	// three-valued logic already drops a NULL row — the comparison yields NULL, not
	// true — and that was confirmed on both engines, so no test can kill this clause
	// and none should be written that appears to. It is here because isInactive makes
	// the same refusal explicitly (`if !last.Valid`), and because a later edit that
	// wraps either side in COALESCE would silently start sweeping devices that have
	// never reported at all.
	switch dialect {
	case "postgres":
		return "last_activity_time IS NOT NULL AND last_activity_time < " +
				"?::timestamptz - (CASE WHEN inactivity_timeout > 0 THEN inactivity_timeout ELSE ? END) * INTERVAL '1 second'",
			[]any{now, config.DefaultInactivityTimeout}, nil
	case "sqlite":
		return "last_activity_time IS NOT NULL AND " +
				"(unixepoch(?) - unixepoch(last_activity_time)) > (CASE WHEN inactivity_timeout > 0 THEN inactivity_timeout ELSE ? END)",
			[]any{now, config.DefaultInactivityTimeout}, nil
	}
	// Fail closed. A dialect nobody wrote a predicate for would otherwise fall through
	// to a sweep with no time bound at all, which flips the entire fleet offline.
	return "", nil, fmt.Errorf("inactivity sweep has no predicate for dialect %q", dialect)
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
