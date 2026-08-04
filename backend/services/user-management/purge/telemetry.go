// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// Telemetry erases a tenant from the instance's telemetry database.
//
// The erasure itself is Relational's, unchanged — the telemetry cluster is one database
// with one owning role and one catalog, exactly like the relational one, and its
// hypertables and continuous-aggregate materialization are handled inside
// core/tenantpurge rather than here. What this type adds is everything about REACHING
// that database, which is where the two stores genuinely differ.
//
// # It is not this service's database, and it is not open at startup
//
// user-management holds a guest connection: no schema of its own, no migrations, no
// model callbacks (rdb.Guest says why at length). The connection is made on first use
// rather than at startup, so the login path for the whole instance does not acquire an
// availability dependency on a cluster it never otherwise touches. A pass that cannot
// connect returns the error, the ledger records it as retryable, and the coordinator —
// which is already a retry loop — comes back a minute later.
//
// # 🔴 There is no interlock inside the sweep, and that is a real weakening
//
// The relational store runs StillPurging inside its own deleting transaction, so the
// lifecycle is read under the same snapshot that does the erasing. Nothing here can do
// that: iam_tenants is in the OTHER database, so no statement on this connection can
// read it under this transaction. This store's interlock is the coordinator's own re-read
// before the pass, which proves the token was safe a moment ago rather than now. Saying
// so is better than implying an equivalence that does not hold.
type Telemetry struct {
	guest connector
	rel   *Relational
}

// connector is the guest connection narrowed to what this store uses. It is an interface
// so the branch below — the one that decides a store is clean without erasing anything —
// can be exercised without needing a Postgres that is missing a database. Whether a real
// driver's error still reads as a missing database after four layers of wrapping is a
// separate question, answered against a live server in core/rdb's integration test rather
// than assumed here.
type connector interface {
	Connect(ctx context.Context) error
	DB(ctx context.Context) *gorm.DB
}

// NewTelemetry builds the telemetry store over a guest connection.
func NewTelemetry(guest *rdb.Guest) *Telemetry {
	return &Telemetry{
		guest: guest,
		// No precondition: see the type doc. Passing nil is what makes the absence
		// explicit at the one place a reader would look for it.
		rel: NewRelational(StoreTelemetry, guest, nil),
	}
}

func (t *Telemetry) Name() string { return StoreTelemetry }

// Erase connects if needed and sweeps.
//
// # The one case where "nothing to erase" is a conclusion rather than an assumption
//
// An instance that runs no event-management has no telemetry database at all: the
// instance database on each cluster is created by whichever service owns it, and on the
// telemetry cluster that service is event-management alone. The `ingest-only` profile
// ships without it, so there is nothing there to connect to — while the cluster itself is
// provisioned unconditionally and its credentials reach every pod, so nothing about the
// configuration says the area is absent.
//
// Postgres answers that question directly: SQLSTATE 3D000 means the server was reached,
// the credentials were accepted, and there is no such database. A database that does not
// exist holds no rows, so the store is clean — but the conclusion is LOGGED at warn on
// every pass rather than passed over, because the same code is returned by a cluster
// that is up and not yet restored, and a purge completing against a telemetry store that
// was merely temporarily empty is exactly the false erasure record ADR-077 exists to
// prevent. An operator who sees this line while running a profile that includes
// event-management is looking at a problem.
func (t *Telemetry) Erase(ctx context.Context, tenant string, epoch time.Time) (Outcome, error) {
	if err := t.guest.Connect(ctx); err != nil {
		if rdb.IsMissingDatabase(err) {
			log.Warn().Str("tenant", tenant).Err(err).
				Msg("No telemetry database exists in this instance, so the telemetry store is " +
					"reporting clean without erasing anything. That is correct for an instance " +
					"that runs no event-management; if this instance DOES run event-management, " +
					"the purge is about to record an erasure that did not happen.")
			return Outcome{}, nil
		}
		return Outcome{}, err
	}
	return t.rel.Erase(ctx, tenant, epoch)
}
