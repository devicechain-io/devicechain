// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
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
	Database
	Connect(ctx context.Context) error
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
// # Deciding whether this instance has a telemetry store at all
//
// It has one only if event-management is deployed, and NOTHING IN THE CONFIGURATION SAYS
// SO. The telemetry cluster is provisioned unconditionally, its credentials are in the
// Secret every pod mounts, and there is no runtime area-capability discovery — a service
// knows its own functional area and nothing else. So the question is put to the database,
// which is the only participant that actually knows, and it is asked in two steps because
// there are two different ways the answer can be "no":
//
//   - SQLSTATE 3D000 — the server was reached, the credentials were accepted, and there is
//     no such database.
//   - the database is there, but carries no event-management schema. A functional-area
//     schema is created by its own service on first startup and by nothing else, so its
//     absence means the area has never run here.
//
// The second exists because the first is not enough, which this store learned the hard
// way: CNPG's initdb creates a database named by `timescale_database`, defaulting to the
// same value as the instance id, so on a profile without event-management the database is
// present and empty and the connect succeeds.
//
// # 🔴 Both answers are the most dangerous kind, so both are logged
//
// They let a purge complete without erasing anything, and the record they write is
// indistinguishable from "swept the cluster and found nothing". The same two conditions are
// also produced by a cluster that is up and not yet restored, which is NOT a correct reason
// to complete. Neither can be told apart from the correct case programmatically — so the
// conclusion goes where an operator can see it on every pass, and an operator running a
// profile that includes event-management who sees one is looking at a problem.
//
// What is NOT read this way: a database that has the schema but no tables in it. That is a
// migration or a restore in flight, and core's own empty-classification guard blocks it.
func (t *Telemetry) Erase(ctx context.Context, tenant string, epoch time.Time) (Outcome, error) {
	if err := t.guest.Connect(ctx); err != nil {
		if rdb.IsMissingDatabase(err) {
			return t.nothingHere(tenant, "the telemetry database does not exist"), nil
		}
		return Outcome{}, err
	}

	// 🔴 THE DATABASE EXISTING IS NOT EVIDENCE THE AREA IS DEPLOYED, and assuming it was
	// is a defect this store shipped with. The reasoning went: an instance that runs no
	// event-management never creates the instance database on the telemetry cluster, so
	// its absence is the signal and nothing else needs one. That is false — CNPG's own
	// initdb creates a database named by `timescale_database`, which defaults to
	// `devicechain`, the same default as the instance id. So on the `ingest-only` profile
	// the database is right there, empty, and the connection succeeds.
	//
	// Left at that, the empty classification core refuses (fail-closed, correctly) becomes
	// a failure on every pass forever: the store never reports clean, the purge never
	// completes, and the token is never released — on a shipped profile, permanently.
	//
	// So the question is asked directly instead. The schema is what the OWNING AREA
	// creates on its first startup; nothing else makes it. Absent, the area has never run
	// here and there is nothing of anyone's to erase. Present but empty, core's guard
	// still fires — that is a migration or a restore in flight, and blocking is right.
	exists, err := t.hasOwningSchema(ctx)
	if err != nil {
		return Outcome{}, err
	}
	if !exists {
		return t.nothingHere(tenant,
			"the telemetry database exists but holds no "+telemetrySchema+" schema"), nil
	}

	return t.rel.Erase(ctx, tenant, epoch)
}

// telemetrySchema is the functional-area schema event-management creates on the telemetry
// cluster. It is named here rather than derived because this store IS the one dedicated to
// that cluster — the schema's absence is the only runtime signal an instance gives that the
// area was never deployed, since the credentials, the cluster and now the database are all
// present regardless.
const telemetrySchema = "event-management"

// hasOwningSchema reports whether the owning area has ever migrated into this database.
//
// The catalog query itself lives in core/tenantpurge, not here, because it is the one
// piece of this decision that a unit test cannot check: get the catalog or column name
// wrong and this store answers "no schema" forever, i.e. reports clean forever, which is
// the precise fail-open the check exists to prevent. There it is exercised against a real
// migrated database by the purge drill, alongside the classification it belongs with.
func (t *Telemetry) hasOwningSchema(ctx context.Context) (bool, error) {
	exists, err := tenantpurge.SchemaExists(ctx, t.guest.DB(ctx), telemetrySchema)
	if err != nil {
		return false, fmt.Errorf("asking whether %s has ever run against the telemetry store: %w",
			telemetrySchema, err)
	}
	return exists, nil
}

// nothingHere reports a clean pass for a telemetry store that holds nothing, and says so
// where an operator can see it.
//
// 🔴 THE LOG LINE IS NOT DECORATION. This is the one conclusion in the subsystem that lets
// a purge complete without erasing anything, and the record it writes is byte-identical to
// "swept the cluster and found nothing" — Rows=0, Complete=true. The same reasoning also
// covers a cluster that is up but not yet restored, which is NOT a correct reason to
// complete. An operator running a profile that includes event-management and seeing this
// line is looking at a problem, so the line has to name what it concluded and why.
func (t *Telemetry) nothingHere(tenant, because string) Outcome {
	log.Warn().Str("tenant", tenant).Str("store", StoreTelemetry).
		Msgf("The telemetry store is reporting clean without erasing anything, because %s. "+
			"That is correct for an instance that runs no event-management. If this instance "+
			"DOES run event-management, the purge is about to record an erasure that did not "+
			"happen — check the telemetry cluster before trusting the deletion record.", because)
	return Outcome{}
}
