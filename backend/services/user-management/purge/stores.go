// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/devicechain-io/dc-user-management/iam"
)

// Store names, as they appear in the deletion record's ledger — see Store.Name.
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
// ones with no erasure yet are REGISTERED as Pending rather than left out; see the Pending
// type for the argument.
//
// The order is deliberate. The relational database comes first because it is the only
// store that can fail the whole purge closed — a table it cannot classify stops the pass
// before a row is touched anywhere — so it is better to learn that before the others have
// deleted anything.
func DefaultStores(db *rdb.RdbManager, tsdb *rdb.Guest) []Store {
	return []Store{
		NewRelational(StoreRelational, db, StillPurging),
		NewTelemetry(tsdb),

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

// StillPurging builds the Precondition the relational sweep runs inside its own
// transaction: the token must still name a tenant whose purge has begun.
//
// 🔴 THIS IS THE ONLY UN-RACEABLE FORM OF THE INTERLOCK, and the coordinator's own check
// is not a substitute for it. The coordinator holds a Postgres advisory lock while a pass
// runs, but that is a session LEASE, not a fence: if the connection holding it dies the
// lock is released while the pass carries on issuing statements over other pooled
// connections, and a peer replica is then free to start its own pass. A lifecycle read
// taken before the transaction proves the token was safe at some earlier moment. Read
// under the deleting transaction's own snapshot, it proves it is safe now — which is the
// claim the sweep actually needs, because the sweep erases every schema at once and
// commits.
func StillPurging(tenant string) tenantpurge.Precondition {
	return func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&iam.Tenant{}).
			Where("token = ? AND purge_state = ?", tenant, iam.PurgePurging).
			Count(&n).Error; err != nil {
			return fmt.Errorf("reading the lifecycle of %q inside the sweep: %w", tenant, err)
		}
		if n != 1 {
			return fmt.Errorf("%q does not name exactly one tenant in %s (found %d) — it may have "+
				"been completed by a peer and the token reused since this pass began",
				tenant, iam.PurgePurging, n)
		}
		return nil
	}
}
