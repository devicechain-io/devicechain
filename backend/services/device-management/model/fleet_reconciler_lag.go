// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
)

// The reconciler lag gauge (bulk_change_reconciler_lag_seconds): how long the oldest device report
// that could decide an applied bulk change line has gone undecided, across the whole instance.
//
// 🔴 THE GAUGE NEVER READS HEALTHY WITHOUT A MEASUREMENT BEHIND IT. Its value is exactly one of:
// the result of the last measurement, if that measurement succeeded; or NaN, from construction
// until the first measurement succeeds and after any measurement that fails. There is no retained
// sample, no per-tenant state and no second writer, so each way an earlier design went wrong has
// nothing to happen to:
//
//   - a failed measurement cannot leave an old value standing: it WRITES NaN;
//   - no tenant can be forgotten: one statement reads every tenant's awaiting lines, so there is no
//     rotation, sample map or eviction for a lagging tenant to fall out of;
//   - the cadence cannot be bypassed: MeasureFleetReconcilerLag is the gauge's only writer, and its
//     only caller is one periodic task, whose passes run one after another on one goroutine;
//   - a replica on which every measurement fails reads NaN, not the 0 a gauge is born with.
//
// NaN is "not measured", and Prometheus treats it that way where it matters: max() across replicas
// takes a measured replica's value over a NaN one, and the alert reading it fires on a NaN that every
// replica agrees on (see prometheusrule-bulk-changes.yaml), so an instance that cannot measure its
// lag at all is reported, not hidden.

// FleetReconcilerLagEvery is how often each replica measures the lag: the cadence of the other
// bulk-change gauges.
const FleetReconcilerLagEvery = 15 * time.Second

// fleetReconcilerLagTimeout bounds one measurement, so one that hangs ends as a failure (NaN)
// rather than leaving the previous value standing while it waits. A variable only for a test.
var fleetReconcilerLagTimeout = 10 * time.Second

// MeasureFleetReconcilerLag measures the lag and publishes it, or publishes NaN when the measurement
// fails. It is the gauge's only writer; the bulk-change-reconciler-lag task is its only caller.
func (api *Api) MeasureFleetReconcilerLag(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, fleetReconcilerLagTimeout)
	defer cancel()
	lag, err := api.FleetReconcilerLag(ctx)
	if err != nil {
		bulkMetrics.reconcilerLag.Set(math.NaN())
		return err
	}
	bulkMetrics.reconcilerLag.Set(lag.Seconds())
	return nil
}

var errFleetReconcilerLagSharded = errors.New("the bulk-change reconciler lag is not measured across tenant shard groups")

// FleetReconcilerLag reads, across every tenant, the age of the oldest device report that could
// decide an applied line and has not: a report at or after the line's posted revision, for a line
// still awaiting evidence, of an operation this release drives. 0 means nothing is waiting, which
// is a measurement, not a default. Age is on the database clock.
//
// ONE STATEMENT UNDER THE SYSTEM CONTEXT, deliberately. The gauge is an instance-wide aggregate
// with no tenant label, so the instance-wide question is the one it asks; asked one tenant at a
// time it needs a rotation and remembered samples, which is what went wrong before. Every join is
// bound to the line's own tenant (o.tenant_id = l.tenant_id, r.tenant_id = l.tenant_id) and the
// statement returns one timestamp, never row content.
//
// IT IS DRIVEN FROM THE AWAITING LINES, not from the reports. The lines awaiting evidence are a
// partial index (posted, no evidence, not redacted), and each one costs one probe of the report
// log's (device_id, revision) index for the reports at or after its posted revision. The report
// log is retained history and grows with every report, decided or not; a read driven from it costs
// what the instance has retained. The per-line lookup is a correlated subquery so the plan is that
// probe whatever the planner estimates for a join.
//
// 🔴 IT READS THE HOME DATABASE, so it refuses to answer once tenant sharding is on: tenants placed
// in another shard group would be missing from the minimum, and the lag would read healthy for
// exactly the tenants it cannot see. Answering across shard groups is not built; until it is, the
// gauge reads NaN there, which the alert reports.
func (api *Api) FleetReconcilerLag(ctx context.Context) (time.Duration, error) {
	if api.RDB.ShardingEnabled() {
		return 0, errFleetReconcilerLagSharded
	}
	db := api.RDB.DB(core.WithSystemContext(ctx))
	r, l, o := fleetSettlementTable(db, "DeviceConfigurationReport"), fleetSettlementTable(db, "FleetOperationLine"), fleetSettlementTable(db, "FleetOperation")
	// The age is computed in the statement, against the clock of the statement that read the
	// reports. SQLite (the unit fixtures) has no interval type and its fixture clock is injected.
	age, args := "EXTRACT(EPOCH FROM clock_timestamp() - min(x.created_at))", []any{}
	if db.Dialector.Name() != "postgres" {
		age, args = "(julianday(?) - julianday(min(x.created_at))) * 86400.0", []any{fleetClock().UTC()}
	}
	var seconds sql.NullFloat64
	err := db.Raw(`SELECT `+age+` AS age FROM (SELECT (SELECT min(r.created_at) FROM `+r+` r
 WHERE r.device_id=l.target_id AND r.tenant_id=l.tenant_id AND r.revision>=l.posted_revision) AS created_at
 FROM `+l+` l JOIN `+o+` o ON o.id=l.operation_id AND o.tenant_id=l.tenant_id
 WHERE l.posting='POSTED' AND l.evidence_revision IS NULL AND l.redacted_at IS NULL AND (`+fleetSettlementProtocol+`)) x`,
		append(args, ledger.SupportedDriverProtocol)...).Scan(&seconds).Error
	if err != nil {
		return 0, err
	}
	if !seconds.Valid {
		// min() over no waiting report: nothing is waiting.
		return 0, nil
	}
	return max(time.Duration(seconds.Float64*float64(time.Second)), 0), nil
}
