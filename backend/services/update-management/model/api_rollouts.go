// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Refusal codes of the rollout plane.
const (
	CodeRolloutNotFound             = "ROLLOUT_NOT_FOUND"
	CodeRolloutExists               = "ROLLOUT_ALREADY_EXISTS"
	CodeRolloutStateConflict        = "ROLLOUT_STATE_CONFLICT"
	CodeInvalidRollout              = "INVALID_ROLLOUT"
	CodeRolloutGroupUnusable        = "ROLLOUT_GROUP_UNUSABLE"
	CodeRolloutGroupUnavailable     = "ROLLOUT_GROUP_LOOKUP_UNAVAILABLE"
	CodeRolloutTooLarge             = "ROLLOUT_TOO_LARGE"
	CodeRolloutThresholdReached     = "ROLLOUT_FAILURE_THRESHOLD_REACHED"
	CodeRolloutArtifactUnassignable = "ROLLOUT_ARTIFACT_NOT_ASSIGNABLE"
)

// Bounds on a rollout.
const (
	// MaxRolloutTargets is the largest target set a rollout freezes. A group that resolves to
	// more is REFUSED, never truncated: a rollout that silently covered the first ten thousand
	// devices of a larger group would report itself complete with the rest untouched.
	MaxRolloutTargets = 10_000
	// MaxRolloutInFlight bounds the concurrency cap.
	MaxRolloutInFlight = 1000
	// rolloutDispatchPerStep bounds how many devices one step looks up and dispatches, so the
	// rollout row is never held for a long run of device lookups.
	rolloutDispatchPerStep = 50
	// rolloutLookupSpare is how many targets past the free slots a step looks up, so devices the
	// assignment plane refuses do not leave a slot empty until the next step.
	rolloutLookupSpare = 10
	// rolloutGroupPage is how many members one group resolution call asks for (the owner's bound).
	rolloutGroupPage = 1000
	// maxBarrenGroupPages: consecutive group pages that add no device before the walk is an error.
	maxBarrenGroupPages = 5
	// maxRolloutsPerPass bounds how many RUNNING rollouts one driver pass steps.
	maxRolloutsPerPass = 200
)

// GroupTargetPage is one keyset page of a group's device members, as device-management answers it.
type GroupTargetPage struct {
	// Rejected: the group cannot be a target at all (it does not exist, collects something other
	// than devices, was never published). Code and Reason say why; nothing else is meaningful.
	Rejected bool
	Code     string
	Reason   string

	DeviceTokens []string
	// NextCursor is empty when the group is exhausted.
	NextCursor string
	// ResolvedVersion is the published group version the page resolved, nil for a static group.
	ResolvedVersion *int32
}

// GroupResolver resolves an entity group to its devices, one keyset page at a time, at
// device-management. An error means the resolution could not be PERFORMED, and the caller fails
// closed on it; a group that cannot be a target is a Rejected page, not an error.
type GroupResolver interface {
	ResolveGroupTargets(ctx context.Context, groupToken string, version *int32, afterCursor string,
		limit int) (*GroupTargetPage, error)
}

// RolloutStepResult is what one step of one rollout did.
type RolloutStepResult struct {
	Dispatched int
	Skipped    int
	// Transition is the state the step moved the rollout to ("" when it did not move it), and
	// Reason the pause reason when that state is PAUSED.
	Transition string
	Reason     string
}

// rolloutByToken reads the tenant's rollout with the given token, or nil.
func (api *Api) rolloutByToken(db *gorm.DB, token string) (*UpdateRollout, error) {
	var found []UpdateRollout
	if err := db.Where("token = ?", token).Limit(1).Find(&found).Error; err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &found[0], nil
}

// lockRollout reads a rollout FOR UPDATE inside tx (PostgreSQL). With skip, a row another
// transaction holds is not waited for: the read returns nothing and ok is false, which is how
// a driver replica learns another one is stepping this rollout. SQLite, which the unit suites
// run on, has no row locks; the clause is omitted there.
//
// 🔴 THIS LOCK IS THE WHOLE HA STORY. It is what makes the concurrency cap and pause/resume/abort
// exact: the in-flight count is taken and spent under it, and every operator transition takes
// it too, so a pause that has committed is seen by every later step and a step in progress
// finishes before the pause does. Lock order, everywhere: this row, then the rollout's target
// rows, then (per device) the assignment plane's rows.
func (api *Api) lockRollout(tx *gorm.DB, id uint, skip bool) (*UpdateRollout, bool, error) {
	q := tx
	if tx.Dialector.Name() == "postgres" && !api.noRolloutLock {
		lock := clause.Locking{Strength: clause.LockingStrengthUpdate}
		if skip {
			lock.Options = clause.LockingOptionsSkipLocked
		}
		q = tx.Clauses(lock)
	}
	var found []UpdateRollout
	if err := q.Where("id = ?", id).Limit(1).Find(&found).Error; err != nil {
		return nil, false, err
	}
	if len(found) == 0 {
		return nil, false, nil
	}
	return &found[0], true, nil
}

// artifactAssignable is the gate assignArtifact applies to an artifact, as a refusal or nil.
func (api *Api) artifactAssignable(a *Artifact) error {
	if a.State != StateAvailable {
		return refuse(CodeArtifactNotAvailable, "artifact %q is %s; only an %s artifact can be assigned",
			a.Token, a.State, StateAvailable)
	}
	if a.Trust != TrustSigned && !api.AllowUnverified {
		return refuse(CodeUnverifiedRefused,
			"artifact %q is UNVERIFIED and this instance does not allow assigning unverified artifacts "+
				"(updates.allowUnverifiedArtifacts)", a.Token)
	}
	return nil
}

// CreateRollout declares a rollout in DRAFT. Nothing is resolved or dispatched until it starts.
func (api *Api) CreateRollout(ctx context.Context, req RolloutCreateRequest) (*UpdateRolloutView, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	if _, ok := core.TenantFromContext(ctx); !ok {
		return nil, errors.New("creating a rollout requires a tenant")
	}
	if err := core.ValidateToken(req.Token); err != nil {
		return nil, refuse(CodeInvalidRollout, "token: %v", err)
	}
	if err := core.ValidateToken(req.GroupToken); err != nil {
		return nil, refuse(CodeInvalidRollout, "groupToken: %v", err)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = req.Token
	}
	if len(name) > maxNameLen {
		return nil, refuse(CodeInvalidRollout, "name must be at most %d characters", maxNameLen)
	}
	if req.MaxInFlight < 1 || req.MaxInFlight > MaxRolloutInFlight {
		return nil, refuse(CodeInvalidRollout, "maxInFlight must be between 1 and %d (got %d)", MaxRolloutInFlight, req.MaxInFlight)
	}
	var threshold sql.NullInt32
	if req.FailureThreshold != nil {
		if *req.FailureThreshold < 1 || *req.FailureThreshold > MaxRolloutTargets {
			return nil, refuse(CodeInvalidRollout, "failureThreshold must be between 1 and %d (got %d)",
				MaxRolloutTargets, *req.FailureThreshold)
		}
		threshold = sql.NullInt32{Int32: int32(*req.FailureThreshold), Valid: true}
	}
	artifact, err := api.ArtifactByToken(ctx, req.ArtifactToken)
	if err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, ErrNotFound(req.ArtifactToken)
	}
	if err := api.artifactAssignable(artifact); err != nil {
		return nil, err
	}
	existing, err := api.rolloutByToken(api.RDB.DB(ctx), req.Token)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, refuse(CodeRolloutExists, "a rollout with token %q already exists", req.Token)
	}
	r := UpdateRollout{Token: req.Token, Name: name, ArtifactId: artifact.ID, GroupToken: req.GroupToken,
		MaxInFlight: req.MaxInFlight, FailureThreshold: threshold, State: RolloutDraft, CreatedBy: actorOf(ctx)}
	if err := api.RDB.DB(ctx).Create(&r).Error; err != nil {
		if again, qerr := api.rolloutByToken(api.RDB.DB(ctx), req.Token); qerr == nil && again != nil {
			return nil, refuse(CodeRolloutExists, "a rollout with token %q already exists", req.Token)
		}
		return nil, err
	}
	return &UpdateRolloutView{Rollout: r, Artifact: *artifact}, nil
}

// resolveRolloutGroup walks a group to its complete device set by keyset cursor (see command
// delivery's batch resolver, whose contract this follows): a device that stays in the group for
// the whole walk is visited exactly once, and what the walk saw is the frozen target set.
func (api *Api) resolveRolloutGroup(ctx context.Context, groupToken string) ([]string, *int32, error) {
	seen := map[string]struct{}{}
	var tokens []string
	var version *int32
	cursor, barren := "", 0
	for barren < maxBarrenGroupPages {
		page, err := api.Groups.ResolveGroupTargets(ctx, groupToken, nil, cursor, rolloutGroupPage)
		if err != nil {
			return nil, nil, refuse(CodeRolloutGroupUnavailable, "the group could not be resolved in device-management: %v", err)
		}
		if page.Rejected {
			return nil, nil, refuse(CodeRolloutGroupUnusable, "group %q cannot be a rollout target [%s]: %s",
				groupToken, page.Code, page.Reason)
		}
		if page.ResolvedVersion != nil {
			v := *page.ResolvedVersion
			version = &v
		}
		before := len(tokens)
		for _, t := range page.DeviceTokens {
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			tokens = append(tokens, t)
		}
		if len(tokens) > MaxRolloutTargets {
			return nil, nil, refuse(CodeRolloutTooLarge, "group %q resolves to more than %d devices; narrow the group or split the rollout",
				groupToken, MaxRolloutTargets)
		}
		if page.NextCursor == "" {
			return tokens, version, nil
		}
		if len(tokens) == before {
			barren++
		} else {
			barren = 0
		}
		cursor = page.NextCursor
	}
	return nil, nil, refuse(CodeRolloutGroupUnavailable, "the resolution of group %q did not terminate", groupToken)
}

// StartRollout freezes the group's current membership as the rollout's target set and moves the
// rollout to RUNNING, then takes the first step at once. A device that joins the group afterwards
// is not part of this rollout, and one that leaves it stays a target.
func (api *Api) StartRollout(ctx context.Context, token string) (*UpdateRolloutView, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	if err := api.requireUpdatePlane(); err != nil {
		return nil, err
	}
	if api.Groups == nil {
		return nil, refuse(CodeRolloutGroupUnavailable,
			"groups cannot be resolved: the device-management endpoint or service secret is not configured")
	}
	r, err := api.rolloutByToken(api.RDB.DB(ctx), token)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, refuse(CodeRolloutNotFound, "no rollout with token %q", token)
	}
	if r.State != RolloutDraft {
		return nil, refuse(CodeRolloutStateConflict, "rollout %q is %s; only a %s rollout can be started", token, r.State, RolloutDraft)
	}
	artifact, err := api.artifactByID(ctx, r.ArtifactId)
	if err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, fmt.Errorf("rollout %q names an artifact that does not exist", token)
	}
	if err := api.artifactAssignable(artifact); err != nil {
		return nil, err
	}
	devices, version, err := api.resolveRolloutGroup(ctx, r.GroupToken)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, refuse(CodeInvalidRollout, "group %q has no devices", r.GroupToken)
	}
	now := api.now()
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		cur, ok, err := api.lockRollout(tx, r.ID, false)
		if err != nil {
			return err
		}
		if !ok || cur.State != RolloutDraft {
			return refuse(CodeRolloutStateConflict, "rollout %q was started or aborted at the same moment", token)
		}
		targets := make([]UpdateRolloutTarget, 0, len(devices))
		for _, d := range devices {
			targets = append(targets, UpdateRolloutTarget{RolloutId: r.ID, DeviceToken: d, Status: TargetPending})
		}
		if err := tx.CreateInBatches(&targets, 500).Error; err != nil {
			return err
		}
		cols := map[string]any{
			"state": RolloutRunning, "target_count": len(devices), "started_at": now,
			"state_changed_by": actorOf(ctx), "state_changed_at": now, "updated_at": now,
		}
		if version != nil {
			cols["group_version"] = sql.NullInt32{Int32: *version, Valid: true}
		}
		return tx.Model(&UpdateRollout{ID: r.ID, Token: r.Token}).Where("id = ?", r.ID).Updates(cols).Error
	})
	if err != nil {
		return nil, err
	}
	api.stepInline(ctx, r.ID)
	return api.RolloutByToken(ctx, token)
}

// stepInline takes one step right after an operator started or resumed a rollout, so the first
// devices go out at once instead of at the driver's next pass. A failure is only logged: the
// driver retries it.
func (api *Api) stepInline(ctx context.Context, id uint) {
	res, err := api.StepRollout(ctx, id)
	if api.OnRolloutStep != nil {
		api.OnRolloutStep(res, err)
	}
	if err != nil {
		log.Warn().Err(err).Uint("rollout", id).Msg("The first step of a rollout failed; the rollout driver retries it.")
	}
}

// transition applies an operator transition under the rollout's lock. decide returns the columns
// to write, nil for "already there" (idempotent), or a refusal.
func (api *Api) transition(ctx context.Context, token string,
	decide func(tx *gorm.DB, r *UpdateRollout) (map[string]any, error)) (*UpdateRollout, error) {
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return nil, err
	}
	var out *UpdateRollout
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		r, err := api.rolloutByToken(tx, token)
		if err != nil {
			return err
		}
		if r == nil {
			return refuse(CodeRolloutNotFound, "no rollout with token %q", token)
		}
		cur, ok, err := api.lockRollout(tx, r.ID, false)
		if err != nil {
			return err
		}
		if !ok {
			return refuse(CodeRolloutNotFound, "no rollout with token %q", token)
		}
		cols, err := decide(tx, cur)
		if err != nil || cols == nil {
			out = cur
			return err
		}
		now := api.now()
		cols["state_changed_by"] = actorOf(ctx)
		cols["state_changed_at"] = now
		cols["updated_at"] = now
		if err := tx.Model(&UpdateRollout{ID: cur.ID, Token: cur.Token}).Where("id = ?", cur.ID).Updates(cols).Error; err != nil {
			return err
		}
		out = cur
		return nil
	})
	return out, err
}

// PauseRollout stops a RUNNING rollout from dispatching to more devices. Devices already working
// on the update carry on: pausing does not recall them. Pausing a PAUSED rollout is a no-op.
func (api *Api) PauseRollout(ctx context.Context, token string) (*UpdateRolloutView, error) {
	_, err := api.transition(ctx, token, func(_ *gorm.DB, r *UpdateRollout) (map[string]any, error) {
		switch r.State {
		case RolloutPaused:
			return nil, nil
		case RolloutRunning:
			return map[string]any{"state": RolloutPaused, "pause_reason": PauseOperator}, nil
		}
		return nil, refuse(CodeRolloutStateConflict, "rollout %q is %s; only a %s rollout can be paused", token, r.State, RolloutRunning)
	})
	if err != nil {
		return nil, err
	}
	return api.RolloutByToken(ctx, token)
}

// ResumeRollout moves a PAUSED rollout back to RUNNING and takes a step at once. A rollout paused
// because it reached its failure threshold stays paused unless the caller raises the threshold
// past the failures counted: the accounting is never reset by resuming. failureThreshold, when
// given, replaces the rollout's threshold (journaled with the resume).
func (api *Api) ResumeRollout(ctx context.Context, token string, failureThreshold *int) (*UpdateRolloutView, error) {
	if err := api.requireUpdatePlane(); err != nil {
		return nil, err
	}
	if failureThreshold != nil && (*failureThreshold < 1 || *failureThreshold > MaxRolloutTargets) {
		return nil, refuse(CodeInvalidRollout, "failureThreshold must be between 1 and %d (got %d)", MaxRolloutTargets, *failureThreshold)
	}
	// The artifact is read before the rollout is locked: a read on a second connection while the
	// lock's transaction holds the first would queue behind it on a one-connection pool.
	pre, err := api.rolloutByToken(api.RDB.DB(ctx), token)
	if err != nil {
		return nil, err
	}
	if pre == nil {
		return nil, refuse(CodeRolloutNotFound, "no rollout with token %q", token)
	}
	artifact, err := api.artifactByID(ctx, pre.ArtifactId)
	if err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, fmt.Errorf("rollout %q names an artifact that does not exist", token)
	}
	var resumed *UpdateRollout
	_, err = api.transition(ctx, token, func(tx *gorm.DB, r *UpdateRollout) (map[string]any, error) {
		switch r.State {
		case RolloutRunning:
			return nil, nil
		case RolloutPaused:
		default:
			return nil, refuse(CodeRolloutStateConflict, "rollout %q is %s; only a %s rollout can be resumed", token, r.State, RolloutPaused)
		}
		cols := map[string]any{"state": RolloutRunning, "pause_reason": ""}
		threshold := r.FailureThreshold
		if failureThreshold != nil {
			threshold = sql.NullInt32{Int32: int32(*failureThreshold), Valid: true}
			cols["failure_threshold"] = threshold
		}
		if threshold.Valid {
			p, err := api.progressIn(tx, r.ID)
			if err != nil {
				return nil, err
			}
			if p.Failures() >= int(threshold.Int32) {
				return nil, refuse(CodeRolloutThresholdReached,
					"rollout %q has %d failed or unknown devices, which reaches its failure threshold of %d; "+
						"pass a higher failureThreshold to resume", token, p.Failures(), threshold.Int32)
			}
		}
		if r.ArtifactId != artifact.ID {
			return nil, fmt.Errorf("rollout %q changed its artifact", token)
		}
		if err := api.artifactAssignable(artifact); err != nil {
			return nil, refuse(CodeRolloutArtifactUnassignable, "rollout %q cannot resume: %v", token, err)
		}
		resumed = r
		return cols, nil
	})
	if err != nil {
		return nil, err
	}
	if resumed != nil {
		api.stepInline(ctx, resumed.ID)
	}
	return api.RolloutByToken(ctx, token)
}

// AbortRollout ends a rollout for good. Devices not yet dispatched are CANCELLED; devices already
// working on the update are LEFT TO FINISH, and the progress keeps reporting them, because the
// platform cannot claim to have stopped an installer it did not stop. Cancel any one of them with
// cancelUpdate. Aborting an ABORTED rollout is a no-op; a COMPLETED one cannot be aborted.
func (api *Api) AbortRollout(ctx context.Context, token string) (*UpdateRolloutView, error) {
	_, err := api.transition(ctx, token, func(tx *gorm.DB, r *UpdateRollout) (map[string]any, error) {
		switch r.State {
		case RolloutAborted:
			return nil, nil
		case RolloutDraft, RolloutRunning, RolloutPaused:
		default:
			return nil, refuse(CodeRolloutStateConflict, "rollout %q is %s and cannot be aborted", token, r.State)
		}
		if err := tx.Model(&UpdateRolloutTarget{}).Where("rollout_id = ? AND status = ?", r.ID, TargetPending).
			Updates(map[string]any{"status": TargetCancelled, "updated_at": api.now()}).Error; err != nil {
			return nil, err
		}
		return map[string]any{"state": RolloutAborted, "pause_reason": "", "finished_at": api.now()}, nil
	})
	if err != nil {
		return nil, err
	}
	return api.RolloutByToken(ctx, token)
}

// progressRow is one group of the progress aggregate.
type progressRow struct {
	RolloutId    uint
	Status       string
	AttemptState sql.NullString
	N            int
}

// progressQuery joins a rollout's targets to the attempts their assignments name. The attempt is
// the device's outcome; the rollout never copies it.
func progressQuery(db *gorm.DB, ids []uint) *gorm.DB {
	return db.Model(&UpdateRolloutTarget{}).
		Select("update_rollout_targets.rollout_id AS rollout_id, update_rollout_targets.status AS status, "+
			"update_attempts.state AS attempt_state, COUNT(*) AS n").
		Joins("LEFT JOIN update_attempts ON update_attempts.assignment_id = update_rollout_targets.assignment_id "+
			"AND update_attempts.tenant_id = update_rollout_targets.tenant_id").
		Where("update_rollout_targets.rollout_id IN ?", ids).
		Group("update_rollout_targets.rollout_id, update_rollout_targets.status, update_attempts.state")
}

func (api *Api) progressOf(db *gorm.DB, ids []uint) (map[uint]RolloutProgress, error) {
	out := map[uint]RolloutProgress{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []progressRow
	if err := progressQuery(db, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		p := out[row.RolloutId]
		p.add(outcomeOf(row.Status, row.AttemptState), row.N)
		out[row.RolloutId] = p
	}
	return out, nil
}

func (api *Api) progressIn(tx *gorm.DB, id uint) (RolloutProgress, error) {
	m, err := api.progressOf(tx, []uint{id})
	if err != nil {
		return RolloutProgress{}, err
	}
	return m[id], nil
}

// viewsOfRollouts joins rollouts to their artifacts and progress with two reads.
func (api *Api) viewsOfRollouts(ctx context.Context, rs []UpdateRollout) ([]UpdateRolloutView, error) {
	ids := make([]uint, 0, len(rs))
	artIDs := make([]uint, 0, len(rs))
	for _, r := range rs {
		ids = append(ids, r.ID)
		artIDs = append(artIDs, r.ArtifactId)
	}
	var artifacts []Artifact
	if len(artIDs) > 0 {
		if err := rdb.FindByKeys(api.RDB.DB(ctx), &artifacts, "id", artIDs); err != nil {
			return nil, err
		}
	}
	ptrs := make([]*Artifact, len(artifacts))
	for i := range artifacts {
		ptrs[i] = &artifacts[i]
	}
	if err := api.fillTrust(ctx, ptrs...); err != nil {
		return nil, err
	}
	byID := make(map[uint]Artifact, len(artifacts))
	for _, a := range artifacts {
		byID[a.ID] = a
	}
	progress, err := api.progressOf(api.RDB.DB(ctx), ids)
	if err != nil {
		return nil, err
	}
	out := make([]UpdateRolloutView, 0, len(rs))
	for _, r := range rs {
		out = append(out, UpdateRolloutView{Rollout: r, Artifact: byID[r.ArtifactId], Progress: progress[r.ID]})
	}
	return out, nil
}

// RolloutByToken returns the tenant's rollout with its artifact and progress, or nil.
func (api *Api) RolloutByToken(ctx context.Context, token string) (*UpdateRolloutView, error) {
	r, err := api.rolloutByToken(api.RDB.DB(ctx), token)
	if err != nil || r == nil {
		return nil, err
	}
	views, err := api.viewsOfRollouts(ctx, []UpdateRollout{*r})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// Rollouts searches the tenant's rollouts, newest first.
func (api *Api) Rollouts(ctx context.Context, c RolloutSearchCriteria) (*RolloutSearchResults, error) {
	if c.State != nil && !slices.Contains(RolloutStates, *c.State) {
		return nil, refuse(CodeInvalidRollout, "state must be one of %s", strings.Join(RolloutStates, ", "))
	}
	results := make([]UpdateRollout, 0)
	db, pag := api.RDB.ListOf(ctx, &UpdateRollout{}, func(q *gorm.DB) *gorm.DB {
		if c.State != nil {
			q = q.Where("state = ?", *c.State)
		}
		return q
	}, c.Pagination)
	if err := db.Find(&results).Error; err != nil {
		return nil, err
	}
	views, err := api.viewsOfRollouts(ctx, results)
	if err != nil {
		return nil, err
	}
	return &RolloutSearchResults{Results: views, Pagination: pag}, nil
}

// outcomeFilter is the SQL predicate (over the targets joined to their attempts) for one outcome.
func outcomeFilter(q *gorm.DB, outcome string) *gorm.DB {
	finished := []string{string(ota.StateUpdated), string(ota.StateFailed), string(ota.StateTimedOut),
		string(ota.StateCancelled), string(ota.StateUnknown)}
	dispatched := "update_rollout_targets.status = '" + TargetDispatched + "'"
	switch outcome {
	case OutcomePending:
		return q.Where("update_rollout_targets.status = ?", TargetPending)
	case OutcomeSkipped:
		return q.Where("update_rollout_targets.status = ?", TargetSkipped)
	case OutcomeCancelled:
		return q.Where("update_rollout_targets.status = ? OR ("+dispatched+" AND update_attempts.state = ?)",
			TargetCancelled, string(ota.StateCancelled))
	case OutcomeSucceeded:
		return q.Where(dispatched+" AND update_attempts.state = ?", string(ota.StateUpdated))
	case OutcomeFailed:
		return q.Where(dispatched+" AND update_attempts.state IN ?", []string{string(ota.StateFailed), string(ota.StateTimedOut)})
	case OutcomeUnknown:
		return q.Where(dispatched+" AND update_attempts.state = ?", string(ota.StateUnknown))
	default: // OutcomeInFlight
		return q.Where(dispatched+" AND (update_attempts.state IS NULL OR update_attempts.state NOT IN ?)", finished)
	}
}

type targetRow struct {
	UpdateRolloutTarget
	AttemptState    sql.NullString
	AssignmentToken sql.NullString
}

// RolloutTargets pages a rollout's targets in the order they were frozen, optionally by outcome.
// It answers nil for a rollout the tenant does not have.
func (api *Api) RolloutTargets(ctx context.Context, token string, c RolloutTargetCriteria) (*RolloutTargetResults, error) {
	if c.Outcome != nil && !slices.Contains(Outcomes, *c.Outcome) {
		return nil, refuse(CodeInvalidRollout, "outcome must be one of %s", strings.Join(Outcomes, ", "))
	}
	r, err := api.rolloutByToken(api.RDB.DB(ctx), token)
	if err != nil || r == nil {
		return nil, err
	}
	rows := make([]targetRow, 0)
	db, pag := api.RDB.ListOf(ctx, &UpdateRolloutTarget{}, func(q *gorm.DB) *gorm.DB {
		q = q.Select("update_rollout_targets.*, update_attempts.state AS attempt_state, update_assignments.token AS assignment_token").
			Joins("LEFT JOIN update_attempts ON update_attempts.assignment_id = update_rollout_targets.assignment_id "+
				"AND update_attempts.tenant_id = update_rollout_targets.tenant_id").
			Joins("LEFT JOIN update_assignments ON update_assignments.id = update_rollout_targets.assignment_id "+
				"AND update_assignments.tenant_id = update_rollout_targets.tenant_id").
			Where("update_rollout_targets.rollout_id = ?", r.ID)
		if c.Outcome != nil {
			q = outcomeFilter(q, *c.Outcome)
		}
		return q
	}, c.Pagination)
	if err := db.Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]RolloutTargetView, 0, len(rows))
	for _, row := range rows {
		out = append(out, RolloutTargetView{Target: row.UpdateRolloutTarget, Outcome: outcomeOf(row.Status, row.AttemptState),
			AssignmentToken: row.AssignmentToken.String, AttemptState: row.AttemptState.String})
	}
	return &RolloutTargetResults{Results: out, Pagination: pag}, nil
}

// candidate is a PENDING target looked up before the step takes the rollout's lock.
type candidate struct {
	target UpdateRolloutTarget
	facts  DeviceFacts
	found  bool
}

// errTargetTaken: another writer moved the target out of PENDING first.
var errTargetTaken = errors.New("the rollout target is no longer pending")

// StepRollout advances one rollout by one step. ctx carries the rollout's tenant.
//
//  1. Outside any lock: read the rollout and some of its PENDING targets, and look each device up
//     in device-management. A lookup that fails leaves its target PENDING for the next step.
//  2. In one transaction, holding the rollout row (SKIP LOCKED: a replica that cannot take it
//     returns at once, because another is stepping): re-check RUNNING, count the targets in flight
//     and failed from their attempts, auto-pause at the failure threshold, pause if the artifact is
//     no longer assignable, dispatch the looked-up targets that are STILL PENDING up to the free
//     slots, and complete the rollout when nothing is pending or in flight.
//  3. After commit: put each new assignment on its device's desired channel, as assignArtifact does.
//
// Every write of a step is in that one transaction, so a replica that dies mid-step leaves
// nothing half-done, and the next step by any replica recomputes from the rows.
func (api *Api) StepRollout(ctx context.Context, id uint) (RolloutStepResult, error) {
	var res RolloutStepResult
	if err := api.RefuseDeletedTenant(ctx); err != nil {
		return res, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return res, errors.New("stepping a rollout requires a tenant")
	}
	if err := api.requireUpdatePlane(); err != nil {
		return res, err
	}
	var pre []UpdateRollout
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &pre, "id", []uint{id}); err != nil {
		return res, err
	}
	if len(pre) == 0 || pre[0].State != RolloutRunning {
		return res, nil
	}
	p, err := api.progressIn(api.RDB.DB(ctx), id)
	if err != nil {
		return res, err
	}
	// The free slots and a few spare, so a run of refusals still fills them; none when there are none.
	want := 0
	if free := pre[0].MaxInFlight - p.Occupied(); free > 0 {
		want = min(free+rolloutLookupSpare, rolloutDispatchPerStep)
	}
	var cands []candidate
	if want > 0 {
		var pending []UpdateRolloutTarget
		if err := api.RDB.DB(ctx).Where("rollout_id = ? AND status = ?", id, TargetPending).
			Order("id ASC").Limit(want).Find(&pending).Error; err != nil {
			return res, err
		}
		for _, t := range pending {
			facts, found, err := api.Devices.DeviceOf(ctx, tenant, t.DeviceToken)
			if err != nil {
				log.Warn().Err(err).Str("device", t.DeviceToken).Uint("rollout", id).
					Msg("A rollout target could not be looked up; it is tried again at the next step.")
				continue
			}
			cands = append(cands, candidate{target: t, facts: facts, found: found})
		}
	}

	// Read before the lock for the same reason as the lookups; the assignment plane re-checks the
	// trust under its own share lock on the signer, and a loss there pauses the rollout below.
	artifact, err := api.artifactByID(ctx, pre[0].ArtifactId)
	if err != nil {
		return res, err
	}

	var toPublish []*UpdateAssignmentView
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		r, locked, err := api.lockRollout(tx, id, true)
		if err != nil || !locked || r.State != RolloutRunning {
			return err
		}
		pause := func(reason string) error {
			res.Transition, res.Reason = RolloutPaused, reason
			return tx.Model(&UpdateRollout{ID: r.ID, Token: r.Token}).Where("id = ?", r.ID).
				Updates(map[string]any{"state": RolloutPaused, "pause_reason": reason, "updated_at": api.now(),
					"state_changed_by": "", "state_changed_at": api.now()}).Error
		}
		p, err := api.progressIn(tx, r.ID)
		if err != nil {
			return err
		}
		if r.FailureThreshold.Valid && p.Failures() >= int(r.FailureThreshold.Int32) {
			return pause(PauseFailureThreshold)
		}
		if p.Pending == 0 && p.Occupied() == 0 {
			res.Transition = RolloutCompleted
			return tx.Model(&UpdateRollout{ID: r.ID, Token: r.Token}).Where("id = ?", r.ID).
				Updates(map[string]any{"state": RolloutCompleted, "finished_at": api.now(), "updated_at": api.now(),
					"state_changed_by": "", "state_changed_at": api.now()}).Error
		}
		slots := r.MaxInFlight - p.Occupied()
		if api.beforeDispatch != nil {
			api.beforeDispatch()
		}
		if slots <= 0 || len(cands) == 0 {
			return nil
		}
		if artifact == nil || artifact.ID != r.ArtifactId || api.artifactAssignable(artifact) != nil {
			return pause(PauseArtifactNotAssignable)
		}
		var compatible []string
		if err := json.Unmarshal(artifact.CompatibleProfiles, &compatible); err != nil {
			return fmt.Errorf("artifact %q has an unreadable compatibleProfiles document: %w", artifact.Token, err)
		}
		for _, c := range cands {
			if slots == 0 {
				break
			}
			v, skipCode, _, err := api.dispatchOne(ctx, tx, artifact, compatible, c)
			switch {
			case errors.Is(err, errTargetTaken):
				continue
			case err != nil:
				var re *RefusalError
				if errors.As(err, &re) && re.Code == CodeUnverifiedRefused {
					// The trust the gate read was derived outside the savepoint; the signer behind it
					// was revoked in between. The target stays PENDING and the rollout pauses.
					return pause(PauseArtifactNotAssignable)
				}
				return err
			case skipCode != "":
				res.Skipped++
			default:
				res.Dispatched++
				slots--
				if v != nil {
					toPublish = append(toPublish, v)
				}
			}
		}
		left, err := api.progressIn(tx, r.ID)
		if err != nil {
			return err
		}
		if left.Pending == 0 && left.Occupied() == 0 {
			res.Transition = RolloutCompleted
			return tx.Model(&UpdateRollout{ID: r.ID, Token: r.Token}).Where("id = ?", r.ID).
				Updates(map[string]any{"state": RolloutCompleted, "finished_at": api.now(), "updated_at": api.now(),
					"state_changed_by": "", "state_changed_at": api.now()}).Error
		}
		return nil
	})
	if err != nil {
		return RolloutStepResult{}, err
	}
	for _, v := range toPublish {
		api.publishAssignment(ctx, v)
	}
	return res, nil
}

// dispatchOne decides one target inside the step's transaction. It returns the assignment to
// publish (nil when an existing one was adopted), or a skip code and detail, or errTargetTaken.
//
// The dispatch runs in a savepoint, so a refusal from the assignment plane rolls back only this
// target's writes and the step carries on with the next. Inside it the target row is taken FIRST
// (UPDATE … WHERE status = PENDING), so the lock order is the rollout row, the target row, then the
// assignment plane's rows.
func (api *Api) dispatchOne(ctx context.Context, tx *gorm.DB, artifact *Artifact, compatible []string,
	c candidate) (*UpdateAssignmentView, string, string, error) {
	skip := func(code, detail string) (*UpdateAssignmentView, string, string, error) {
		return nil, code, detail, api.markSkipped(tx, c.target, code, detail)
	}
	switch {
	case !c.found:
		return skip(CodeDeviceNotFound, fmt.Sprintf("no device with token %q", c.target.DeviceToken))
	case c.facts.Disabled:
		return skip(CodeDeviceDisabled, fmt.Sprintf("device %q is disabled", c.target.DeviceToken))
	case c.facts.Profile == "" || !slices.Contains(compatible, c.facts.Profile):
		return skip(CodeIncompatibleProfile, fmt.Sprintf("device %q (profile %q) is not on artifact %q's compatible profile list",
			c.target.DeviceToken, c.facts.Profile, artifact.Token))
	}
	now := api.now()
	var view *UpdateAssignmentView
	err := tx.Transaction(func(sp *gorm.DB) error {
		res := sp.Model(&UpdateRolloutTarget{ID: c.target.ID, DeviceToken: c.target.DeviceToken}).
			Where("id = ? AND status = ?", c.target.ID, TargetPending).
			Updates(map[string]any{"status": TargetDispatched, "dispatched_at": now, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errTargetTaken
		}
		assignmentID, err := api.adoptCurrent(sp, c.target.DeviceToken, artifact)
		if err != nil {
			return err
		}
		if assignmentID == 0 {
			v, err := api.assignInTx(ctx, sp, c.target.DeviceToken, artifact, c.facts.Anchors)
			if err != nil {
				return err
			}
			view, assignmentID = v, v.Assignment.ID
		}
		return sp.Model(&UpdateRolloutTarget{ID: c.target.ID, DeviceToken: c.target.DeviceToken}).Where("id = ?", c.target.ID).
			Update("assignment_id", sql.NullInt64{Int64: int64(assignmentID), Valid: true}).Error
	})
	if err == nil || errors.Is(err, errTargetTaken) {
		return view, "", "", err
	}
	var re *RefusalError
	if errors.As(err, &re) {
		if re.Code == CodeUnverifiedRefused {
			return nil, "", "", err
		}
		return skip(re.Code, re.Msg)
	}
	// On PostgreSQL a unique-index loss to a concurrent assign of the same device and component
	// is a raw error inside the savepoint (the assignment plane's own re-read cannot run in an
	// aborted statement block). The savepoint is rolled back, so it can be asked here.
	var current []UpdateAssignment
	if q := tx.Where("device_token = ? AND component = ? AND superseded_at IS NULL", c.target.DeviceToken, artifact.Component).
		Limit(1).Find(&current); q.Error == nil && len(current) == 1 {
		return skip(CodeUpdateInProgress, fmt.Sprintf("device %q was assigned an update at the same moment", c.target.DeviceToken))
	}
	return nil, "", "", err
}

func (api *Api) markSkipped(tx *gorm.DB, target UpdateRolloutTarget, code, detail string) error {
	if len(detail) > 512 {
		detail = detail[:512]
	}
	res := tx.Model(&UpdateRolloutTarget{ID: target.ID, DeviceToken: target.DeviceToken}).
		Where("id = ? AND status = ?", target.ID, TargetPending).
		Updates(map[string]any{"status": TargetSkipped, "skip_code": code, "skip_detail": detail, "updated_at": api.now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errTargetTaken
	}
	return nil
}

// adoptCurrent returns the id of the device's current assignment for the artifact's component when
// it already names THIS artifact and is either still live or ended UPDATED: the device is already
// on (or on its way to) the rollout's target, so no second assignment is made. 0 means none.
func (api *Api) adoptCurrent(tx *gorm.DB, deviceToken string, artifact *Artifact) (uint, error) {
	var current []UpdateAssignment
	if err := tx.Where("device_token = ? AND component = ? AND superseded_at IS NULL", deviceToken, artifact.Component).
		Limit(1).Find(&current).Error; err != nil {
		return 0, err
	}
	if len(current) == 0 || current[0].ArtifactId != artifact.ID {
		return 0, nil
	}
	var at []UpdateAttempt
	if err := tx.Where("assignment_id = ?", current[0].ID).Limit(1).Find(&at).Error; err != nil {
		return 0, err
	}
	if len(at) == 0 {
		return 0, fmt.Errorf("update assignment %q has no attempt row", current[0].Token)
	}
	st := ota.State(at[0].State)
	if !st.Terminal() || st == ota.StateUpdated {
		return current[0].ID, nil
	}
	return 0, nil
}

// AdvanceResult counts what one driver pass did.
type AdvanceResult struct {
	Stepped    int
	Dispatched int
	Skipped    int
	Failed     int
}

// AdvanceRollouts steps every RUNNING rollout once. It reads which rollouts are RUNNING across
// every tenant under a system context, then steps each under its own tenant's context, so every
// write a step makes carries the tenant predicate. Safe on every replica at once: a step skips a
// rollout another replica is stepping.
func (api *Api) AdvanceRollouts(ctx context.Context) (AdvanceResult, error) {
	var out AdvanceResult
	// 🔴 A TENANT-ISOLATION BYPASS, and it is one SELECT of ids and tenants, as the deadline sweep's.
	// Which rollouts are running is a question across every tenant; every change is made by
	// StepRollout under core.WithTenant(that rollout's own tenant).
	var running []UpdateRollout
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Select("id", "tenant_id").Where("state = ?", RolloutRunning).
		Order("id ASC").Limit(maxRolloutsPerPass).Find(&running).Error; err != nil {
		return out, err
	}
	for _, r := range running {
		if api.TenantDeleted != nil && api.TenantDeleted(r.TenantId) {
			continue
		}
		res, err := api.StepRollout(core.WithTenant(ctx, r.TenantId), r.ID)
		if api.OnRolloutStep != nil {
			api.OnRolloutStep(res, err)
		}
		out.Stepped++
		if err != nil {
			out.Failed++
			log.Warn().Err(err).Uint("rollout", r.ID).Msg("A rollout step failed; it is retried at the next pass.")
			continue
		}
		out.Dispatched += res.Dispatched
		out.Skipped += res.Skipped
		if res.Transition == RolloutPaused {
			log.Warn().Uint("rollout", r.ID).Str("reason", res.Reason).Msg("A rollout paused itself.")
		}
	}
	return out, nil
}
