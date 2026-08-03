// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import "github.com/devicechain-io/dc-microservice/rdb"

// Store names, as they appear in the deletion record's ledger. They are PERSISTED, so a
// rename orphans the history of every purge that used the old one.
const (
	StoreRelational = "rdb"
	StoreTelemetry  = "tsdb"
	StoreBroker     = "broker"
	StoreKeyValue   = "kv"
	StoreObject     = "blob"
)

// DefaultStores is the complete set of places a tenant's data lives, in the order a pass
// works them.
//
// 🔴 THE SET IS THE COORDINATOR'S WHOLE COVERAGE CLAIM. A store missing from here is not
// a gap that shows up somewhere as a warning — it is a store the coordinator never asks
// about, and its absence looks exactly like a store that reported clean. That is why the
// ones that have no erasure yet are REGISTERED as Pending rather than left out: each puts
// a line in every purge's ledger naming what it still holds, and makes completion
// impossible until it is replaced by the real thing.
//
// The order is deliberate. The relational database comes first because it is the only
// store that can fail the whole purge closed — a table it cannot classify stops the pass
// before a row is touched anywhere — so it is better to learn that before the others have
// deleted anything.
func DefaultStores(db *rdb.RdbManager) []Store {
	return []Store{
		NewRelational(StoreRelational, db),

		Pending{
			StoreName: StoreTelemetry,
			Holds: "the event store still holds this tenant's telemetry — the raw measurements, " +
				"locations and alerts in the hypertables, and separately the per-device, per-metric " +
				"aggregate buckets in the measurement rollups' materialization, which a delete against " +
				"the raw tables does not reach and whose refresh window would never have removed",
		},
		Pending{
			StoreName: StoreBroker,
			Holds: "the broker still holds this tenant's retained messages — its per-tenant subjects on " +
				"the platform streams until the stream age limit expires them, and its MQTT session and " +
				"retained-message state, which is deliberately age-unbounded and so does not expire at " +
				"all. A successor at a reused token would be delivered its predecessor's events, alarms " +
				"and MQTT messages",
		},
		Pending{
			StoreName: StoreKeyValue,
			Holds: "the key-value store still holds this tenant's cached resolutions — device-by-token, " +
				"relationships, group memberships and the per-tenant gate entries — keyed by an encoded " +
				"tenant-and-token pair, so they expire only as their TTLs run out",
		},
		Pending{
			StoreName: StoreObject,
			Holds: "the object store still holds this tenant's uploaded assets, which today means its " +
				"branding logos",
		},
	}
}
