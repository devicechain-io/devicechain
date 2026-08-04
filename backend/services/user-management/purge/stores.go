// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/messaging"
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
// about, and its absence looks exactly like a store that reported clean. That is why a
// storage system with no erasure yet is REGISTERED as Pending rather than left out; see the
// Pending type for the argument.
//
// Every store here now erases, so nothing is Pending today. That is the goal state, not a
// reason to delete the type: the next storage system the platform grows starts as one, and
// the discipline only works if registering it is easier than remembering it.
//
// The order is deliberate. The relational database comes first because it is the only
// store that can fail the whole purge closed — a table it cannot classify stops the pass
// before a row is touched anywhere — so it is better to learn that before the others have
// deleted anything.
func DefaultStores(db *rdb.RdbManager, tsdb *rdb.Guest, broker messaging.Connection,
	instanceId string, objects blob.Store, tenants tenantReader) []Store {
	return []Store{
		NewRelational(StoreRelational, db, StillPurging),
		NewTelemetry(tsdb),
		NewBroker(broker, instanceId),
		NewKeyValue(broker, instanceId),

		NewObject(objects, tenants),
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
