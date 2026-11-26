// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Dispatch, evidence and posting values the model writes.
const (
	FleetDispatchPlanning = string(ledger.DispatchPlanning)
	FleetDispatchReady    = string(ledger.DispatchReady)
	FleetEvidenceOpen     = string(ledger.EvidenceOpen)
	FleetPostingPlanned   = string(ledger.PostingPlanned)
	FleetPostingRefused   = string(ledger.PostingRefused)

	fleetTargetKindDeviceList = "DEVICE_LIST"
	fleetEventCreate          = "CREATE"
	fleetLinesPerInsert       = 1000
)

// fleetKindExecutable reports whether this build can execute a kind. It is a variable only so a
// test can open the door the engine's registry keeps shut until the engine ships: create refuses a
// kind the build cannot execute, and never accepts one on the strength of a plan alone.
var fleetKindExecutable = ledger.Executable

// fleetOpKey is the fence key of one operation (K_op): shared by posting transactions, exclusive
// for every control transition and for the planning step that inserts lines.
func fleetOpKey(tenant string, id uint) int64 {
	return rdb.AdvisoryLockKey(tenant + "\x00fleet-op\x00" + strconv.FormatUint(uint64(id), 10))
}

func fleetAdmissionTenantKey(tenant string) int64 {
	return rdb.AdvisoryLockKey(tenant + "\x00fleet-adm\x00")
}

func fleetAdmissionInstanceKey() int64 { return rdb.AdvisoryLockKey("\x00fleet-adm-instance\x00") }

// fleetLock takes a transaction-scoped advisory lock. PostgreSQL only: the SQLite unit fixtures have
// one writer at a time, so there is nothing to serialise.
func fleetLock(tx *gorm.DB, key int64, shared bool) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	// Two constant statements, so the SQL is readable statically (the raw-SQL guard needs that).
	if shared {
		return tx.Exec("SELECT pg_advisory_xact_lock_shared(?)", key).Error
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", key).Error
}

// fleetAdmissionLockTimeout bounds how long create waits for the admission locks.
const fleetAdmissionLockTimeout = "2s"

func fleetSetLockTimeout(tx *gorm.DB, d string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT set_config('lock_timeout', ?, true)", d).Error
}

// lockTimedOut reports a 55P03 (lock_not_available).
func lockTimedOut(err error) bool { return attributeSQLState(err) == "55P03" }

// CreateFleetOperation is the two-step create. It is idempotent on the operation token: the same
// request replays the original and writes nothing, a different request under the token is a
// CONFLICT, and a header a crashed earlier call left in PLANNING is finished by the replay.
//
//	Step 1, a short transaction under the admission locks: count the quotas, insert the header as
//	PLANNING with its target count, insert the runtime row. The reservation is what stops two
//	replicas both admitting the last slot.
//	Step 2, a second transaction under the operation's fence, with no admission lock: insert the
//	lines, the first contract version and the CREATE event, then set the digests and READY.
func (api *Api) CreateFleetOperation(ctx context.Context, req *FleetOperationCreateRequest, actor string) (*FleetOperation, error) {
	n, err := normalizeFleetRequest(req)
	if err != nil {
		return nil, err
	}
	if !fleetKindExecutable(n.kind) {
		return nil, fleetErr(FleetCodeKindNotExecutable, "kind %q cannot be executed by this build", n.kind)
	}
	if req.Token == "" || len(req.Token) > 128 || req.Name == "" || len(req.Name) > 128 || actor == "" {
		return nil, fleetErr(FleetCodeInvalidRequest, "token and name (1-128 characters) and an initiator are required")
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, errors.New("a fleet operation needs a tenant in the context")
	}

	// The replay probe comes first, before any target is resolved or any quota counted.
	existing, err := api.fleetOperationByToken(ctx, req.Token)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		plan, err := api.planNormalized(ctx, n)
		if err != nil {
			return nil, err
		}
		existing, err = api.fleetCreateStepOne(ctx, tenant, req, n, plan, actor)
		if err != nil {
			bulkMetrics.refused(err) // QUOTA_EXCEEDED and ADMISSION_BUSY come from this step
			return nil, err
		}
		return api.fleetCreateStepTwo(ctx, tenant, existing, plan, actor)
	}
	if existing.RequestDigest != n.requestDigest {
		return nil, fleetErr(FleetCodeConflict, "operation token %q is already used by a different request", req.Token)
	}
	if existing.Dispatch != FleetDispatchPlanning {
		return existing, nil
	}
	// The earlier call died between the steps: plan again (the digest proves it is the same request)
	// and finish.
	plan, err := api.planNormalized(ctx, n)
	if err != nil {
		return nil, err
	}
	return api.fleetCreateStepTwo(ctx, tenant, existing, plan, actor)
}

func (api *Api) fleetOperationByToken(ctx context.Context, token string) (*FleetOperation, error) {
	var op FleetOperation
	err := api.RDB.DB(ctx).Where("token = ?", token).First(&op).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &op, nil
}

func (api *Api) fleetCreateStepOne(ctx context.Context, tenant string, req *FleetOperationCreateRequest, n *fleetNormalized,
	plan *FleetPlan, actor string) (*FleetOperation, error) {
	var created *FleetOperation
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := fleetSetLockTimeout(tx, fleetAdmissionLockTimeout); err != nil {
			return err
		}
		// A reversal first holds its original's fence, shared: it is the lower lock in the one order, and
		// holding it until the header commits is what keeps retention and a resume off the original.
		var reversalOf sql.NullInt64
		if n.reversal != nil {
			if err := fleetReversalFenceOnTx(tx, tenant, n.reversal); err != nil {
				return err
			}
			reversalOf = sql.NullInt64{Int64: int64(n.reversal.original.ID), Valid: true}
		}
		// Tenant lock first, then instance: the one order everything takes them in.
		for _, key := range []int64{fleetAdmissionTenantKey(tenant), fleetAdmissionInstanceKey()} {
			if err := fleetLock(tx, key, false); err != nil {
				return err
			}
		}
		// A racing create of the same request may have finished while this one waited for the locks: it is
		// a replay and must be answered as one, not refused for the budget its twin just used.
		var twin FleetOperation
		if err := tx.Where("token = ?", req.Token).First(&twin).Error; err == nil {
			if twin.RequestDigest != n.requestDigest {
				return fleetErr(FleetCodeConflict, "operation token %q is already used by a different request", req.Token)
			}
			created = &twin
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := api.fleetAdmit(ctx, tx, len(plan.Lines)); err != nil {
			return err
		}
		op := &FleetOperation{
			Token: req.Token, Name: req.Name, Kind: n.kind, ReversalOf: reversalOf,
			Dispatch: FleetDispatchPlanning, Evidence: FleetEvidenceOpen,
			RequestDigest: n.requestDigest, Intent: n.intentJSON, ContractVersion: 1,
			// The protocol is decided in one place: nothing here uses a feature a protocol-2 engine defines.
			DriverProtocol: int32(ledger.RequiredProtocol(ledger.ProtocolFeatures{})),
			TargetKind:     fleetTargetKindDeviceList, TargetCount: int32(len(plan.Lines)),
			MaxPostsPerSecond: int32(*n.safety.MaxPostsPerSecond), EffPostsPerSecond: int32(*n.safety.MaxPostsPerSecond),
			FailureThreshold: nullInt32(n.safety.FailureThreshold), EffFailureThreshold: nullInt32(n.safety.FailureThreshold),
			AmbiguityThreshold: nullInt32(n.safety.AmbiguityThreshold), EffAmbiguityThreshold: nullInt32(n.safety.AmbiguityThreshold),
			SettleDeadlineS: int32(n.safety.SettleDeadlineSeconds), Initiator: actor,
			CreatedBy: sql.NullString{String: actor, Valid: true},
		}
		if err := tx.Create(op).Error; err != nil {
			return err
		}
		if err := tx.Create(&FleetOperationRuntime{OperationId: int64(op.ID)}).Error; err != nil {
			return err
		}
		created = op
		return nil
	})
	if err != nil {
		if lockTimedOut(err) {
			return nil, fleetErr(FleetCodeAdmissionBusy, "fleet operation admission is busy; retry")
		}
		// A racing create of the same token won the unique index: it is a replay, not an error.
		for _, v := range integrity.All(err) {
			if v.Class == integrity.ClassUnique {
				if again, perr := api.fleetOperationByToken(ctx, req.Token); perr == nil && again != nil {
					if again.RequestDigest != n.requestDigest {
						return nil, fleetErr(FleetCodeConflict, "operation token %q is already used by a different request", req.Token)
					}
					return again, nil
				}
			}
		}
		return nil, err
	}
	return created, nil
}

func nullInt32(v *int64) sql.NullInt32 {
	if v == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(*v), Valid: true}
}

// fleetAdmit counts the admission budgets inside step 1, under the tenant and instance locks.
// A PLANNING header already counts its lines (its target count), so a created-then-abandoned
// operation cannot be used to run past the retained-lines budget.
func (api *Api) fleetAdmit(ctx context.Context, tx *gorm.DB, lines int) error {
	exceeded := func(quota string, format string, args ...any) error {
		e := fleetErr(FleetCodeQuotaExceeded, format, args...)
		if strings.HasPrefix(quota, "instance.") {
			// What the instance as a whole holds is other tenants' business: say which limit, not how full.
			e.Message = "the instance-wide limit " + strings.TrimPrefix(quota, "instance.") + " is reached; retry later"
		}
		e.Quota = quota
		return e
	}
	type scope struct {
		name                                     string
		db                                       *gorm.DB
		unstarted, unposted, retained, hour, day int
	}
	sysCtx := core.WithSystemContext(ctx)
	scopes := []scope{
		{"tenant", tx, ledger.TenantUnstartedOperations, ledger.TenantUnpostedLines, ledger.TenantRetainedLines, ledger.TenantCreatesPerHour, ledger.TenantCreatesPerDay},
		// Instance counts select counts only, under a system context: no row of any tenant is read. They run
		// on the SAME transaction as the tenant counts, so the connection that holds the admission locks
		// never waits for a second one from the pool.
		{"instance", tx.WithContext(sysCtx), ledger.InstanceUnstartedOperations, ledger.InstanceUnpostedLines, ledger.InstanceRetainedLines, 0, 0},
	}
	for _, s := range scopes {
		db := s.db
		var unstarted int64
		if err := db.Model(&FleetOperation{}).Where("dispatch IN ?", []string{FleetDispatchPlanning, FleetDispatchReady}).Count(&unstarted).Error; err != nil {
			return err
		}
		if int(unstarted)+1 > s.unstarted {
			return exceeded(s.name+".unstartedOperations", "the %s already has %d operations waiting to start (limit %d)", s.name, unstarted, s.unstarted)
		}
		var planning struct{ N sql.NullInt64 }
		if err := db.Model(&FleetOperation{}).Select("COALESCE(SUM(target_count), 0) AS n").Where("dispatch = ?", FleetDispatchPlanning).Scan(&planning).Error; err != nil {
			return err
		}
		var unposted, retained int64
		if err := db.Model(&FleetOperationLine{}).Where("posting = ?", FleetPostingPlanned).Count(&unposted).Error; err != nil {
			return err
		}
		if err := db.Model(&FleetOperationLine{}).Count(&retained).Error; err != nil {
			return err
		}
		reserved := planning.N.Int64
		if int(unposted+reserved)+lines > s.unposted {
			return exceeded(s.name+".unpostedLines", "the %s already has %d lines waiting to post (limit %d)", s.name, unposted+reserved, s.unposted)
		}
		if int(retained+reserved)+lines > s.retained {
			return exceeded(s.name+".retainedLines", "the %s already retains %d operation lines (limit %d)", s.name, retained+reserved, s.retained)
		}
		if s.hour > 0 {
			now := time.Now()
			for _, w := range []struct {
				since time.Duration
				limit int
				name  string
			}{{time.Hour, s.hour, "createsPerHour"}, {24 * time.Hour, s.day, "createsPerDay"}} {
				var created int64
				if err := db.Model(&FleetOperation{}).Where("created_at > ?", now.Add(-w.since)).Count(&created).Error; err != nil {
					return err
				}
				if int(created) >= w.limit {
					return exceeded(s.name+"."+w.name, "the %s created %d operations in the last %s (limit %d)", s.name, created, w.since, w.limit)
				}
			}
		}
	}
	return nil
}

func (api *Api) fleetCreateStepTwo(ctx context.Context, tenant string, op *FleetOperation, plan *FleetPlan, actor string) (*FleetOperation, error) {
	var out *FleetOperation
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// The fence, exclusive: nothing posts or controls this operation while its lines go in.
		if err := fleetFence(tx, fleetOpKey(tenant, op.ID), false, "CREATE"); err != nil {
			return err
		}
		var current FleetOperation
		if err := tx.First(&current, op.ID).Error; err != nil {
			return err
		}
		if current.Dispatch != FleetDispatchPlanning {
			// A concurrent finisher (or the abandoned-planning sweep) got here first.
			out = &current
			return nil
		}
		lines := make([]FleetOperationLine, 0, len(plan.Lines))
		for _, l := range plan.Lines {
			row := FleetOperationLine{OperationId: int64(op.ID), Ordinal: l.Ordinal, Posting: FleetPostingPlanned, OverrideChanged: l.OverrideChanged}
			if l.TargetId != nil {
				// A refused input of a create has no target, so it stores no identifier: the token is
				// returned only in the plan and create responses, and the operator's own file maps the
				// ordinal back to it. A later duplicate is refused here too, so the unique index on the
				// device is never surfaced. A reversal's refused line names the original's device.
				row.TargetId = sql.NullInt64{Int64: int64(*l.TargetId), Valid: true}
				row.TargetKey = l.Token
			}
			if l.ReversesLineId != nil {
				row.ReversesLineId = sql.NullInt64{Int64: int64(*l.ReversesLineId), Valid: true}
			}
			if l.Code != "" {
				row.Posting, row.Code, row.RefusedAtPlan = FleetPostingRefused, l.Code, true
				if l.Posting != "" {
					row.Posting = l.Posting
				}
			}
			lines = append(lines, row)
		}
		// INSERTs participate in lock ordering too. These rows all have zero IDs:
		// the sequence allocates increasing fresh IDs, and this transaction never
		// locks an existing line afterwards. The exclusive operation fence keeps
		// another creator out of its ordinal/target unique keys; there are no
		// line foreign keys that could acquire locks on older journal rows.
		if err := tx.CreateInBatches(&lines, fleetLinesPerInsert).Error; err != nil {
			return err
		}
		// expires_at is what the expiry sweep and a start compare with the database's clock.
		now, err := fleetDBNow(tx)
		if err != nil {
			return err
		}
		if err := tx.Create(&FleetOperationContract{
			OperationId: int64(op.ID), Version: 1, Digest: plan.ContractDigest, Body: plan.ContractBody,
			ApprovalKind: FleetApprovalHumanDirect, PolicyVersion: 1, ProposedBy: actor,
			ExpiresAt: sql.NullTime{Time: now.Add(ledger.ReadyExpiry), Valid: true},
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&FleetOperationEvent{
			OperationId: int64(op.ID), At: now, Kind: fleetEventCreate, Actor: sql.NullString{String: actor, Valid: true},
			ContractVersion: sql.NullInt32{Int32: 1, Valid: true},
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&FleetOperation{ID: op.ID}).Where("dispatch = ?", FleetDispatchPlanning).Updates(map[string]any{
			"dispatch": FleetDispatchReady, "plan_digest": plan.PlanDigest, "contract_digest": plan.ContractDigest,
			"expires_at":       sql.NullTime{Time: now.Add(ledger.ReadyExpiry), Valid: true},
			"state_changed_by": actor, "state_changed_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.First(&current, op.ID).Error
	})
	if err != nil {
		return nil, err
	}
	if out != nil {
		return out, nil
	}
	return api.fleetOperationByToken(ctx, op.Token)
}

// FleetOperationByToken reads one operation, or nil when the token names none in the tenant.
func (api *Api) FleetOperationByToken(ctx context.Context, token string) (*FleetOperation, error) {
	return api.fleetOperationByToken(ctx, token)
}

// FleetOperationSearchCriteria filters the operation list.
type FleetOperationSearchCriteria struct {
	rdb.Pagination
	Dispatch *string
	Evidence *string
	Kind     *string
}

// FleetOperationSearchResults is one page of operations.
type FleetOperationSearchResults struct {
	Results    []FleetOperation
	Pagination rdb.SearchResultsPagination
}

func (api *Api) FleetOperations(ctx context.Context, c FleetOperationSearchCriteria) (*FleetOperationSearchResults, error) {
	results := make([]FleetOperation, 0)
	db, pag := api.RDB.ListOf(ctx, &FleetOperation{}, func(r *gorm.DB) *gorm.DB {
		if c.Dispatch != nil {
			r = r.Where("dispatch = ?", *c.Dispatch)
		}
		if c.Evidence != nil {
			r = r.Where("evidence = ?", *c.Evidence)
		}
		if c.Kind != nil {
			r = r.Where("kind = ?", *c.Kind)
		}
		return r
	}, c.Pagination)
	if err := db.Find(&results).Error; err != nil {
		return nil, err
	}
	return &FleetOperationSearchResults{Results: results, Pagination: pag}, nil
}

// FleetLineSearchCriteria filters one operation's lines.
type FleetLineSearchCriteria struct {
	rdb.Pagination
	Posting    *string
	Settlement *string
	Code       *string
}

// FleetLineSearchResults is one page of lines.
type FleetLineSearchResults struct {
	Results    []FleetOperationLine
	Pagination rdb.SearchResultsPagination
}

// FleetOperationLines pages one operation's lines in ordinal order.
func (api *Api) FleetOperationLines(ctx context.Context, token string, c FleetLineSearchCriteria) (*FleetLineSearchResults, error) {
	op, err := api.fleetOperationByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if op == nil {
		return nil, gorm.ErrRecordNotFound
	}
	results := make([]FleetOperationLine, 0)
	db, pag := api.RDB.ListOf(ctx, &FleetOperationLine{}, func(r *gorm.DB) *gorm.DB {
		r = r.Where("operation_id = ?", op.ID)
		if c.Posting != nil {
			r = r.Where("posting = ?", *c.Posting)
		}
		if c.Settlement != nil {
			r = r.Where("settlement = ?", *c.Settlement)
		}
		if c.Code != nil {
			r = r.Where("code = ?", *c.Code)
		}
		return r
	}, c.Pagination)
	if err := db.Find(&results).Error; err != nil {
		return nil, err
	}
	return &FleetLineSearchResults{Results: results, Pagination: pag}, nil
}

// FleetOperationProgress is the grouped count of one operation's lines.
type FleetOperationProgress struct {
	Planned, Posted, Refused, Skipped, Cancelled       int32
	Confirmed, Failed, Superseded, Ambiguous, Resolved int32
	AwaitingEvidence                                   int32
}

// FleetOperationProgressOf counts the lines of an operation by state with one grouped query.
func (api *Api) FleetOperationProgressOf(ctx context.Context, op *FleetOperation) (*FleetOperationProgress, error) {
	var rows []struct {
		Posting    string
		Settlement sql.NullString
		N          int64
	}
	if err := api.RDB.DB(ctx).Model(&FleetOperationLine{}).Select("posting, settlement, count(*) AS n").
		Where("operation_id = ?", op.ID).Group("posting, settlement").Scan(&rows).Error; err != nil {
		return nil, err
	}
	p := &FleetOperationProgress{}
	for _, r := range rows {
		n := int32(r.N)
		switch ledger.Posting(r.Posting) {
		case ledger.PostingPlanned:
			p.Planned += n
		case ledger.PostingRefused:
			p.Refused += n
		case ledger.PostingSkipped:
			p.Skipped += n
		case ledger.PostingCancelled:
			p.Cancelled += n
		case ledger.PostingPosted:
			p.Posted += n
			switch ledger.Settlement(r.Settlement.String) {
			case ledger.SettlementConfirmed:
				p.Confirmed += n
			case ledger.SettlementFailed:
				p.Failed += n
			case ledger.SettlementSuperseded:
				p.Superseded += n
			case ledger.SettlementAmbiguous:
				p.Ambiguous += n
			case ledger.SettlementResolvedConfirmed, ledger.SettlementResolvedFailed:
				p.Resolved += n
			default:
				p.AwaitingEvidence += n
			}
		default:
			return nil, fmt.Errorf("fleet operation %d has a line in an unknown posting state %q", op.ID, r.Posting)
		}
	}
	return p, nil
}

// FleetOperationContractBody returns the canonical JSON of the contract version in force.
func (api *Api) FleetOperationContractBody(ctx context.Context, op *FleetOperation) (string, error) {
	var c FleetOperationContract
	if err := api.RDB.DB(ctx).Where("operation_id = ? AND version = ?", op.ID, op.ContractVersion).First(&c).Error; err != nil {
		return "", err
	}
	return c.Body, nil
}

// FleetOperationRuntimeOf returns the runtime row of an operation, or nil when it has none.
func (api *Api) FleetOperationRuntimeOf(ctx context.Context, op *FleetOperation) (*FleetOperationRuntime, error) {
	var r FleetOperationRuntime
	err := api.RDB.DB(ctx).Where("operation_id = ?", op.ID).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// OpenFleetKindsForTest replaces the build's "can this kind be executed" test and returns the
// function that restores it. It exists for tests of the served surface in other packages, which
// cannot reach the unexported variable: the engine's registry keeps every kind shut until the
// engine ships, and create must refuse a kind the build cannot execute.
func OpenFleetKindsForTest(executable func(kind string) bool) (restore func()) {
	prev := fleetKindExecutable
	fleetKindExecutable = executable
	return func() { fleetKindExecutable = prev }
}
