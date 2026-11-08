// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/schedule"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Campaign rejection codes. Like every other code in this service they are verdicts about
// the request, returned as a rejection rather than a failure.
const (
	// RejectCampaignTriggerInvalid: the trigger document did not validate.
	RejectCampaignTriggerInvalid RejectionCode = "CAMPAIGN_TRIGGER_INVALID"
	// RejectCampaignStepsInvalid: the step list is empty, too long, or a step is malformed.
	RejectCampaignStepsInvalid RejectionCode = "CAMPAIGN_STEPS_INVALID"
	// RejectCampaignStepUnsupported: a step type that is in the vocabulary but is not
	// executed by this build. Recognised, not executable, and never silently dropped.
	RejectCampaignStepUnsupported RejectionCode = "CAMPAIGN_STEP_UNSUPPORTED"
	// RejectCampaignTargetInvalid: the target is missing or of a kind this build cannot resolve.
	RejectCampaignTargetInvalid RejectionCode = "CAMPAIGN_TARGET_INVALID"
	// RejectCampaignFieldInvalid: a numeric field is out of range, or a name is too long.
	RejectCampaignFieldInvalid RejectionCode = "CAMPAIGN_FIELD_INVALID"
)

// Campaign bounds and defaults.
const (
	MaxCampaignSteps = 10
	// MaxCampaignFiresPerDay bounds how often a trigger may fire in one local day.
	MaxCampaignFiresPerDay = 48
	// MaxCampaignTargets bounds the devices one cycle may plan. A cycle is rows rather than
	// one batch, so it may exceed MaxBatchDeviceTokens; the bound is still finite.
	MaxCampaignTargets = 100000
	// MaxCampaignStepDelaySeconds is the longest per-step delay: a day.
	MaxCampaignStepDelaySeconds = 24 * 60 * 60
	// maxCampaignTextLen bounds a campaign's name and group token, the width of their columns.
	maxCampaignTextLen = 128

	DefaultCampaignMaxLateness = 15 * time.Minute
	DefaultCampaignExpiresIn   = time.Hour
	MaxCampaignLateness        = 24 * time.Hour
	MaxCampaignExpiresIn       = 7 * 24 * time.Hour

	// CampaignPlanAhead is how far ahead of now cycles are planned. It must exceed a day by
	// the widest spread of a solar event inside one civil day (about 14h either side of the
	// zone's midnight at the extremes), so that a day is always planned before any of its
	// fixtures falls due.
	CampaignPlanAhead = 36 * time.Hour
	// campaignReplanMargin is how low the planned window may run before a pass plans again.
	// Planning resolves the group, so it is the expensive step; inside the window a pass only
	// fires what is due.
	campaignReplanMargin = time.Hour

	// campaignClaimPage bounds the elements one pass claims for one campaign, and so the
	// size of the biggest batch a pass creates.
	campaignClaimPage = 5000
	// campaignClaimStale is how long a CLAIMED element is left to the replica that claimed
	// it before any replica may finish its dispatch.
	campaignClaimStale = 2 * time.Minute
	campaignRowChunk   = 1000

	// CampaignHistoryRetention is how long finished cycles and their elements are kept, counted
	// from when the cycle was planned, and how long a deleted campaign's row is kept. A nightly
	// campaign over a large fleet writes a row per device per step per night, so the history has
	// to be bounded; the commands themselves follow the command retention.
	CampaignHistoryRetention = 30 * 24 * time.Hour
	campaignPruneChunk       = 5000
	// campaignPruneChunksPerPass bounds the element chunks one prune of one tenant removes.
	campaignPruneChunksPerPass = 20
	// campaignPruneCycles bounds the expired cycles one prune of one tenant walks.
	campaignPruneCycles = 500
)

// campaignTenantBudget bounds the time one pass spends firing one tenant's campaigns. A pass
// that runs over leaves the rest to the next pass, which starts with the oldest work, so one
// tenant's large fleet delays every other tenant by at most this. A variable so a test can
// exhaust it.
var campaignTenantBudget = 5 * time.Second

// Site is where a device is installed, for a solar trigger.
type Site struct {
	Latitude  float64
	Longitude float64
}

// SiteResolver reads devices' installed locations from their owner (device-management).
// A device with no location is absent from the map; an error means nothing was decided and
// the caller must not guess. Dependency-inverted like the other seams on Api.
type SiteResolver interface {
	DeviceSites(ctx context.Context, deviceTokens []string) (map[string]Site, error)
}

// CampaignStepInput is a step as the API receives it.
type CampaignStepInput struct {
	Type         string
	Name         *string
	Payload      *string
	DelaySeconds *int32
}

// CampaignCreateRequest defines a campaign.
type CampaignCreateRequest struct {
	Token string
	Name  *string
	// Trigger is a core/schedule Spec as a JSON document.
	Trigger string
	Steps   []CampaignStepInput
	// GroupToken names the entity group whose devices are targeted. TargetKind may be
	// omitted or GROUP.
	GroupToken         string
	TargetKind         *string
	MaxLatenessSeconds *int32
	ExpiresInSeconds   *int32
	Enabled            *bool
}

// DecodeTrigger parses a stored trigger document.
func DecodeTrigger(doc []byte) (*schedule.Trigger, error) {
	var spec schedule.Spec
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("%w: %v", schedule.ErrInvalid, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data after the trigger document", schedule.ErrInvalid)
	}
	return schedule.New(spec)
}

// DecodeSteps parses a stored step list.
func DecodeSteps(doc []byte) ([]CampaignStep, error) {
	var steps []CampaignStep
	if err := json.Unmarshal(doc, &steps); err != nil {
		return nil, err
	}
	return steps, nil
}

// boundFiresPerDay refuses a trigger that fires more than MaxCampaignFiresPerDay times in any one
// day of a whole (leap) year. A sample week is not enough: a cron restricted to a month, a
// day-of-month or a weekday the sample misses would pass it and then fire every minute. The
// scan walks day by day and stops at the first day over the bound, so a dense trigger costs
// one dense day, not a year of firings.
//
// The days are the trigger zone's LOCAL days, midnight to midnight, because that is the day a
// cron or a calendar names. Counted on UTC days instead, a dense local hour that straddles UTC
// midnight (05:00 in Kolkata, 00:00 in Berlin) is split across two days and slips under the
// bound on both.
func boundFiresPerDay(trigger *schedule.Trigger) error {
	zone := trigger.Zone()
	for d := 0; d < 366; d++ {
		// A leap year, so February 29 is seen. Both ends are local midnights, so a 23- or
		// 25-hour day at a DST change is counted whole.
		from := time.Date(2028, 1, 1+d, 0, 0, 0, 0, zone)
		to := time.Date(2028, 1, 2+d, 0, 0, 0, 0, zone)
		day, err := trigger.Cycles(from, to)
		if err != nil {
			return rejected(RejectCampaignTriggerInvalid, "%s", strings.TrimPrefix(err.Error(), "invalid schedule: "))
		}
		if len(day) > MaxCampaignFiresPerDay {
			return rejected(RejectCampaignTriggerInvalid,
				"the trigger fires %d times on %s (local); a campaign may fire at most %d times a day",
				len(day), from.Format("01-02"), MaxCampaignFiresPerDay)
		}
	}
	return nil
}

func validateCampaignRequest(req *CampaignCreateRequest) ([]CampaignStep, int, int, error) {
	trigger, err := DecodeTrigger([]byte(req.Trigger))
	if err != nil {
		return nil, 0, 0, rejected(RejectCampaignTriggerInvalid, "%s", strings.TrimPrefix(err.Error(), "invalid schedule: "))
	}
	// A schedule that fires more than a few dozen times a day is not a schedule, it is a
	// stream, and every firing plans a row per device: bound it where it is defined.
	if err := boundFiresPerDay(trigger); err != nil {
		return nil, 0, 0, err
	}
	if len(req.Steps) == 0 || len(req.Steps) > MaxCampaignSteps {
		return nil, 0, 0, rejected(RejectCampaignStepsInvalid, "a campaign needs 1..%d steps", MaxCampaignSteps)
	}
	steps := make([]CampaignStep, 0, len(req.Steps))
	for i, in := range req.Steps {
		switch in.Type {
		case StepCommand:
		case StepArtifact:
			return nil, 0, 0, rejected(RejectCampaignStepUnsupported,
				"step %d: ARTIFACT steps are recognised but not executed by this build; use a COMMAND step", i+1)
		default:
			return nil, 0, 0, rejected(RejectCampaignStepsInvalid, "step %d: type %q is not COMMAND or ARTIFACT", i+1, in.Type)
		}
		if in.Name == nil || *in.Name == "" {
			return nil, 0, 0, rejected(RejectCampaignStepsInvalid, "step %d: a COMMAND step needs a name", i+1)
		}
		if err := validateCommandInputBounds(*in.Name, in.Payload, nil); err != nil {
			return nil, 0, 0, err
		}
		if in.Payload != nil && !json.Valid([]byte(*in.Payload)) {
			return nil, 0, 0, rejected(RejectPayloadNotJSON, "step %d: the payload is not valid JSON", i+1)
		}
		step := CampaignStep{Type: StepCommand, Name: *in.Name}
		if in.Payload != nil {
			step.Payload = *in.Payload
		}
		if in.DelaySeconds != nil {
			if *in.DelaySeconds < 0 || *in.DelaySeconds > MaxCampaignStepDelaySeconds {
				return nil, 0, 0, rejected(RejectCampaignStepsInvalid,
					"step %d: delaySeconds is outside 0..%d", i+1, MaxCampaignStepDelaySeconds)
			}
			step.DelaySeconds = int(*in.DelaySeconds)
		}
		steps = append(steps, step)
	}
	if req.TargetKind != nil && *req.TargetKind != "" && *req.TargetKind != CampaignTargetGroup {
		return nil, 0, 0, rejected(RejectCampaignTargetInvalid,
			"target kind %q is not supported by this build; target an entity group (a dynamic group can select an area's devices)", *req.TargetKind)
	}
	group := strings.TrimSpace(req.GroupToken)
	if group == "" {
		return nil, 0, 0, rejected(RejectCampaignTargetInvalid, "a campaign needs a groupToken")
	}
	if len(group) > maxCampaignTextLen {
		return nil, 0, 0, rejected(RejectCampaignTargetInvalid, "groupToken is longer than %d characters", maxCampaignTextLen)
	}
	if req.Name != nil && len(*req.Name) > maxCampaignTextLen {
		return nil, 0, 0, rejected(RejectCampaignFieldInvalid, "name is longer than %d characters", maxCampaignTextLen)
	}
	lateness, expires := int(DefaultCampaignMaxLateness/time.Second), int(DefaultCampaignExpiresIn/time.Second)
	if req.MaxLatenessSeconds != nil {
		if *req.MaxLatenessSeconds < 1 || time.Duration(*req.MaxLatenessSeconds)*time.Second > MaxCampaignLateness {
			return nil, 0, 0, rejected(RejectCampaignFieldInvalid, "maxLatenessSeconds is outside 1..%d", int(MaxCampaignLateness/time.Second))
		}
		lateness = int(*req.MaxLatenessSeconds)
	}
	if req.ExpiresInSeconds != nil {
		if *req.ExpiresInSeconds < 1 || time.Duration(*req.ExpiresInSeconds)*time.Second > MaxCampaignExpiresIn {
			return nil, 0, 0, rejected(RejectCampaignFieldInvalid, "expiresInSeconds is outside 1..%d", int(MaxCampaignExpiresIn/time.Second))
		}
		expires = int(*req.ExpiresInSeconds)
	}
	return steps, lateness, expires, nil
}

// CreateCampaign stores a campaign. Nothing fires until the scheduler plans it, and a
// campaign created disabled plans nothing.
//
// A campaign is never edited in place: changing one is deleting it and creating it again. The
// new campaign fires nothing that fell due before it was created, so recreating one inside its
// lateness window does not re-send a firing the old one already sent.
func (api *Api) CreateCampaign(ctx context.Context, req *CampaignCreateRequest) (*Campaign, error) {
	steps, lateness, expires, err := validateCampaignRequest(req)
	if err != nil {
		return nil, err
	}
	stepsDoc, _ := json.Marshal(steps)
	trigger := datatypes.JSON(req.Trigger)
	// Stored in its normalised form so what is read back is what was validated.
	if t, derr := DecodeTrigger([]byte(req.Trigger)); derr == nil {
		if norm, merr := json.Marshal(t.Spec()); merr == nil {
			trigger = datatypes.JSON(norm)
		}
	}
	var existing int64
	if err := api.RDB.DB(ctx).Model(&Campaign{}).Where("token = ?", req.Token).Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, rejected(RejectTokenInUse, "a campaign with token %q already exists", req.Token)
	}
	now := api.campaignClock()
	c := &Campaign{
		Model:              gorm.Model{CreatedAt: now},
		TokenReference:     rdb.TokenReference{Token: req.Token},
		Enabled:            req.Enabled == nil || *req.Enabled,
		Trigger:            trigger,
		Steps:              datatypes.JSON(stepsDoc),
		TargetKind:         CampaignTargetGroup,
		GroupToken:         strings.TrimSpace(req.GroupToken),
		MaxLatenessSeconds: lateness,
		ExpiresInSeconds:   expires,
	}
	if c.Enabled {
		c.EnabledAt = sql.NullTime{Time: now, Valid: true}
	}
	if req.Name != nil {
		c.Name = *req.Name
	}
	if campaignCreateHook != nil {
		campaignCreateHook()
	}
	if err := api.RDB.DB(ctx).Create(c).Error; err != nil {
		// The probe above holds no lock, so a concurrent create of the same token can pass it
		// too. The unique index decides, and the loser is told what the probe would have said.
		if isUniqueViolation(err) {
			return nil, rejected(RejectTokenInUse, "a campaign with token %q already exists", req.Token)
		}
		return nil, err
	}
	return c, nil
}

// isUniqueViolation recognises a unique-index refusal from Postgres, or from the SQLite the
// unit tests run on.
func isUniqueViolation(err error) bool {
	return rdb.IsUniqueViolation(err) || errors.Is(err, gorm.ErrDuplicatedKey) ||
		strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// CampaignsByToken finds campaigns by token.
func (api *Api) CampaignsByToken(ctx context.Context, tokens []string) ([]*Campaign, error) {
	found := make([]*Campaign, 0)
	if len(tokens) == 0 {
		return found, nil
	}
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", tokens); err != nil {
		return nil, err
	}
	return found, nil
}

// ListCampaigns returns the tenant's campaigns, newest first.
func (api *Api) ListCampaigns(ctx context.Context, limit int) ([]*Campaign, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	var out []*Campaign
	err := api.RDB.DB(ctx).Order(Campaign{}.DefaultOrder()).Limit(limit).Find(&out).Error
	return out, err
}

func (api *Api) campaignByToken(ctx context.Context, token string) (*Campaign, error) {
	var c Campaign
	if err := api.RDB.DB(ctx).Where("token = ?", token).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// SetCampaignEnabled turns a campaign on or off. Turning it off stops planning and firing;
// what was planned stays, and falls due as MISSED if the campaign is still off by then.
// Turning it on again starts from that instant: what fell due while it was off is skipped as
// DISABLED, never released in a burst.
func (api *Api) SetCampaignEnabled(ctx context.Context, token string, enabled bool) (*Campaign, error) {
	c, err := api.campaignByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	db := api.RDB.DB(ctx)
	if enabled {
		// Only an off-to-on change moves enabled_at: switching on a campaign that is already on
		// must not skip what is due right now.
		now := api.campaignClock()
		res := db.Model(c).Where("id = ? AND enabled = ?", c.ID, false).
			Updates(map[string]any{"enabled": true, "enabled_at": now})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected > 0 {
			c.EnabledAt = sql.NullTime{Time: now, Valid: true}
		}
	} else if err := db.Transaction(func(tx *gorm.DB) error {
		// Under the tenant batch lock, for the reason DeleteCampaign gives: once this returns, no
		// batch of this campaign can still be created.
		if err := lockTenantForBatch(ctx, tx); err != nil {
			return err
		}
		return tx.Model(c).Where("id = ?", c.ID).Update("enabled", false).Error
	}); err != nil {
		return nil, err
	}
	c.Enabled = enabled
	return c, nil
}

// DeleteCampaign removes a campaign and cancels what it had planned but not yet sent.
// Commands already created are untouched; they belong to their batches.
//
// 🔴 A CLAIM WITH A BATCH TOKEN IS NOT CANCELLED HERE. Its batch may exist and its commands may
// have been sent, and calling those elements CANCELLED would record as unsent a command that
// reached a device. They stay CLAIMED and the scheduler settles them: from their batch if it
// exists, as CANCELLED if it does not, decided under the tenant batch lock that the batch's
// creation also holds, so a pass creating that batch at this moment cannot race the settle. A
// claim with no token has no batch and can never get one once it is not CLAIMED, so it is
// cancelled here.
func (api *Api) DeleteCampaign(ctx context.Context, token string) error {
	c, err := api.campaignByToken(ctx, token)
	if err != nil {
		return err
	}
	now := api.campaignClock()
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// Under the tenant batch lock, so a batch being created for this campaign either
		// committed before the delete (and its claims settle from it) or sees the campaign gone
		// when it takes the lock: a command cannot be created after this call returns.
		if err := lockTenantForBatch(ctx, tx); err != nil {
			return err
		}
		if err := tx.Model(&CampaignElement{}).
			Where("campaign_id = ? AND (status = ? OR (status = ? AND batch_token IS NULL))",
				c.ID, string(ElementPending), string(ElementClaimed)).
			Updates(map[string]any{"status": string(ElementSkipped), "reason": SkipCancelled, "updated_at": now}).Error; err != nil {
			return err
		}
		// Stamped from the scheduler's clock, like every other campaign instant, because the
		// pruner removes the row once deleted_at is past the retention.
		return tx.Model(c).Where("id = ?", c.ID).Update("deleted_at", now).Error
	})
}

// SkipCancelled marks planned elements a deleted campaign never sent.
const SkipCancelled = "CANCELLED"

// campaignClock is the scheduler's clock: time.Now unless a test has set one.
func (api *Api) campaignClock() time.Time {
	if api.campaignNow != nil {
		return api.campaignNow()
	}
	return time.Now()
}

// ---------------------------------------------------------------------------------------
// Ticking
// ---------------------------------------------------------------------------------------

// TickCampaigns runs one scheduler pass over every tenant with a campaign or an open claim:
// plan the cycles that are coming, expire what is too late to send, claim what is due, create
// the batches, and settle claims left behind. The caller passes a context WITHOUT a tenant (the
// pass lists tenants itself under a system context), `now` (the instant against which work is
// due), and a fresh pass id per call. Pruning history is PruneCampaignHistory, on its own,
// slower cadence.
//
// 🔑 IT IS SAFE TO RUN IN EVERY REPLICA AT ONCE, with no leader and no lease. This is a
// deliberate departure from core/lease: each step is a compare-and-set the database arbitrates,
// in the manner of the notification escalation scheduler. A cycle is a row under a unique key,
// so two replicas planning it produce one; an element is claimed by an UPDATE conditional on it
// still being PENDING, so two replicas claiming it produce one claim; a batch is created under a
// token derived from its elements, so a replica finishing a dead one's work replays rather than
// duplicates; and whether a tokened claim's batch exists is decided under the tenant batch lock
// that creating the batch also takes. A lease would add failure modes (a stuck holder, a skewed
// clock) without removing the need for any of those. The failure this is built against is a
// street light commanded twice.
//
// The first error does not stop the pass: one tenant's unreachable group must not stop every
// other tenant's lights. Errors are joined and returned.
func (api *Api) TickCampaigns(ctx context.Context, now time.Time, pass string) error {
	// 🔑 THE SYSTEM CONTEXT ONLY ENUMERATES TENANTS. It reads two DISTINCT tenant_id lists and
	// nothing else; every campaign, cycle and element of a tenant is then read and written under
	// core.WithTenant below, so the tenant predicate applies to all of the actual work.
	//
	// A tenant is in the pass while it has a live campaign (enabled ones fire; disabled ones
	// still have pending work to expire), or a claim not yet settled (a deleted campaign's
	// claims are settled from their batch). A tenant whose campaigns are all deleted and settled
	// drops out; its history is the pruner's.
	sys := api.RDB.DB(core.WithSystemContext(ctx))
	var withCampaigns, withClaims []string
	if err := sys.Model(&Campaign{}).Distinct().Pluck("tenant_id", &withCampaigns).Error; err != nil {
		return fmt.Errorf("listing tenants with campaigns: %w", err)
	}
	if err := sys.Model(&CampaignElement{}).Where("status = ?", string(ElementClaimed)).
		Distinct().Pluck("tenant_id", &withClaims).Error; err != nil {
		return fmt.Errorf("listing tenants with open campaign claims: %w", err)
	}
	var errs []error
	for _, tenant := range dedupeStrings(append(withCampaigns, withClaims...)) {
		if err := api.tickTenant(core.WithTenant(ctx, tenant), now, pass); err != nil {
			errs = append(errs, fmt.Errorf("tenant %s: %w", tenant, err))
		}
	}
	return errors.Join(errs...)
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, dup := seen[s]; !dup {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// tickTenant is one pass over one tenant. Every query is tenant-wide or for a campaign that
// has work: a tenant with a hundred quiet campaigns costs a handful of statements a pass, not
// a handful per campaign.
func (api *Api) tickTenant(ctx context.Context, now time.Time, pass string) error {
	deadline := api.campaignClock().Add(campaignTenantBudget)
	db := api.RDB.DB(ctx)
	var campaigns []*Campaign
	if err := db.Order("id ASC").Find(&campaigns).Error; err != nil {
		return err
	}
	live := make(map[uint]*Campaign, len(campaigns))
	for _, c := range campaigns {
		live[c.ID] = c
	}
	var errs []error

	// Plan, only where the planned window runs low.
	for _, c := range campaigns {
		if !c.Enabled {
			continue
		}
		if c.PlannedThrough.Valid && !c.PlannedThrough.Time.Before(now.Add(CampaignPlanAhead-campaignReplanMargin)) {
			continue
		}
		if err := api.planOrRecord(ctx, c, now); err != nil {
			errs = append(errs, fmt.Errorf("campaign %s: %w", c.Token, err))
		}
	}

	if err := api.expireLateElements(ctx, campaigns, now); err != nil {
		errs = append(errs, err)
	}

	// What has work: due PENDING elements of enabled campaigns, and claims gone stale (of any
	// campaign, deleted ones included, since a claim must always be settled).
	// Oldest due first, so a pass that runs out of budget leaves the newest work, not the same
	// campaigns every time.
	var due []uint
	if err := db.Model(&CampaignElement{}).
		Where("status = ? AND due_at <= ?", string(ElementPending), now).
		Group("campaign_id").Order("MIN(due_at) ASC, campaign_id ASC").Pluck("campaign_id", &due).Error; err != nil {
		return errors.Join(append(errs, err)...)
	}
	var stale []uint
	if err := db.Model(&CampaignElement{}).Where("status = ? AND claimed_at < ?",
		string(ElementClaimed), api.campaignClock().Add(-campaignClaimStale)).
		Distinct().Pluck("campaign_id", &stale).Error; err != nil {
		return errors.Join(append(errs, err)...)
	}
	// Stale claims first: they are the oldest work there is.
	sort.Slice(stale, func(i, j int) bool { return stale[i] < stale[j] })
	queued := map[uint]bool{}
	var queue []uint
	for _, id := range stale {
		queued[id] = true
		queue = append(queue, id)
	}
	for _, id := range due {
		if c, ok := live[id]; !ok || !c.Enabled || queued[id] {
			continue // a disabled campaign's due work only expires
		}
		queued[id] = true
		queue = append(queue, id)
	}

	for i, id := range queue {
		// Always at least one, so a tenant whose first campaign alone overruns still progresses.
		if i > 0 && api.campaignClock().After(deadline) {
			break
		}
		c, ok := live[id]
		var err error
		switch {
		case !ok:
			err = api.settleOrphanedClaims(ctx, id, nil, now)
		case !c.Enabled:
			err = api.settleOrphanedClaims(ctx, id, c, now)
		default:
			err = api.fireCampaign(ctx, c, now, pass)
		}
		if err != nil {
			label := fmt.Sprint(id)
			if ok {
				label = c.Token
			}
			errs = append(errs, fmt.Errorf("campaign %s: %w", label, err))
		}
	}
	return errors.Join(errs...)
}

// planOrRecord plans one campaign and records the outcome on it.
func (api *Api) planOrRecord(ctx context.Context, c *Campaign, now time.Time) error {
	trigger, err := DecodeTrigger(c.Trigger)
	if err != nil {
		return api.recordCampaignError(ctx, c, fmt.Errorf("stored trigger no longer validates: %w", err))
	}
	steps, err := DecodeSteps(c.Steps)
	if err != nil || len(steps) == 0 {
		return api.recordCampaignError(ctx, c, fmt.Errorf("stored steps are unreadable"))
	}
	if err := api.planCampaign(ctx, c, trigger, steps, now); err != nil {
		return api.recordCampaignError(ctx, c, err)
	}
	if c.LastError.Valid {
		return api.RDB.DB(ctx).Model(c).Where("id = ?", c.ID).
			Update("last_error", sql.NullString{}).Error
	}
	return nil
}

func (api *Api) recordCampaignError(ctx context.Context, c *Campaign, cause error) error {
	msg := cause.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_ = api.RDB.DB(ctx).Model(c).Where("id = ?", c.ID).
		Update("last_error", sql.NullString{String: msg, Valid: true}).Error
	return cause
}

func (api *Api) planCampaign(ctx context.Context, c *Campaign, trigger *schedule.Trigger,
	steps []CampaignStep, now time.Time) error {
	horizon := now.Add(CampaignPlanAhead)
	var from time.Time
	switch {
	case c.PlannedThrough.Valid:
		from = c.PlannedThrough.Time
		// A campaign off for a long time has nothing worth planning in the past.
		if floor := now.Add(-26 * time.Hour); from.Before(floor) {
			from = floor
		}
	case trigger.PerDevice():
		// A solar day is planned from the local midnight BEFORE today's: a fixture's event for
		// the previous solar date can still lie ahead (the west of the world dims after
		// midnight UTC). What that reaches back to before the campaign existed is recorded,
		// as PREDATES_CAMPAIGN, and never sent (see materializeCycle).
		l := now.In(trigger.Zone())
		from = time.Date(l.Year(), l.Month(), l.Day()-1, 0, 0, 0, 0, trigger.Zone())
	default:
		from = now
	}
	if !horizon.After(from) {
		return nil
	}
	cycles, err := trigger.Cycles(from, horizon)
	if err != nil {
		return err
	}
	for _, cy := range cycles {
		if err := api.materializeCycle(ctx, c, trigger, steps, cy); err != nil {
			return err
		}
	}
	// Forward only: a replica that planned the same window a moment later changes nothing.
	return api.RDB.DB(ctx).Model(c).
		Where("id = ? AND (planned_through IS NULL OR planned_through < ?)", c.ID, horizon).
		Update("planned_through", horizon).Error
}

func (api *Api) materializeCycle(ctx context.Context, c *Campaign, trigger *schedule.Trigger,
	steps []CampaignStep, cy schedule.Cycle) error {
	var already int64
	if err := api.RDB.DB(ctx).Model(&CampaignCycle{}).
		Where("campaign_id = ? AND cycle_key = ?", c.ID, cy.Key).Count(&already).Error; err != nil {
		return err
	}
	if already > 0 {
		return nil
	}
	if api.GroupTargetResolver == nil {
		return errors.New("cannot plan: group resolution is unavailable")
	}
	group := c.GroupToken
	targets, err := api.resolveGroupTargetsUpTo(ctx, &CommandBatchCreateRequest{GroupToken: &group}, MaxCampaignTargets)
	if err != nil {
		return err
	}
	var sites map[string]Site
	if trigger.PerDevice() {
		if api.SiteResolver == nil {
			return errors.New("cannot plan a solar trigger: device locations are unavailable")
		}
		if sites, err = api.SiteResolver.DeviceSites(ctx, targets.deviceTokens); err != nil {
			return unavailableOr(err, "cannot plan a solar trigger: device locations are unavailable")
		}
	}

	now := api.campaignClock()
	cycle := &CampaignCycle{
		CreatedAt:    now,
		CampaignId:   c.ID,
		CycleKey:     cy.Key,
		Day:          sql.NullString{String: cy.Day, Valid: cy.Day != ""},
		At:           sql.NullTime{Time: cy.At, Valid: !cy.At.IsZero()},
		GroupVersion: targets.groupVersion,
		Resolved:     len(targets.deviceTokens),
	}
	elements := make([]CampaignElement, 0, len(targets.deviceTokens)*len(steps))
	for _, device := range targets.deviceTokens {
		var fire schedule.Fire
		if trigger.PerDevice() {
			site, ok := sites[device]
			if !ok {
				fire = schedule.Fire{Skipped: SkipNoSite}
			} else if fire, err = trigger.SolarFire(cy, site.Latitude, site.Longitude); err != nil {
				fire = schedule.Fire{Skipped: SkipNoSite}
			}
		} else {
			fire = schedule.Fire{At: cy.At}
		}
		for i, step := range steps {
			e := CampaignElement{
				CampaignId: c.ID, Step: i, DeviceToken: device,
				UpdatedAt: now, Status: string(ElementPending),
			}
			if fire.Skipped != "" {
				e.Status = string(ElementSkipped)
				e.Reason = sql.NullString{String: fire.Skipped, Valid: true}
				e.DueAt = now
			} else {
				e.DueAt = fire.At.Add(time.Duration(step.DelaySeconds) * time.Second)
				// 🔴 NOTHING THAT FELL DUE BEFORE THE CAMPAIGN EXISTED IS EVER PENDING. A plan
				// reaches back (a solar plan to the previous local midnight), and an element
				// created PENDING in that past would fire at the first pass if it is within the
				// lateness bound: yesterday's dusk for a fresh campaign with a long lateness, or
				// today's dusk a second time after a delete and recreate. The same holds for
				// what fell due while the campaign was switched off.
				switch {
				case e.DueAt.Before(c.CreatedAt):
					e.Status = string(ElementSkipped)
					e.Reason = sql.NullString{String: SkipPredatesCampaign, Valid: true}
				case c.EnabledAt.Valid && e.DueAt.Before(c.EnabledAt.Time):
					e.Status = string(ElementSkipped)
					e.Reason = sql.NullString{String: SkipWhileDisabled, Valid: true}
				}
			}
			elements = append(elements, e)
		}
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(cycle)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // another replica planned it first
		}
		for i := range elements {
			elements[i].CycleId = cycle.ID
		}
		return tx.CreateInBatches(elements, campaignRowChunk).Error
	})
}

// expireLateElements settles, tenant-wide, what has gone past its campaign's lateness bound
// without being sent: PENDING elements, and claims that never got a batch token (no batch can
// exist for them). One statement per distinct lateness, not per campaign. A claim WITH a token
// is not touched here: its batch may exist, and settling it is settleTokenedClaims's, under the
// batch lock.
//
// It also skips, as DISABLED, the pending elements of a campaign switched back on that fell due
// while it was off.
func (api *Api) expireLateElements(ctx context.Context, campaigns []*Campaign, now time.Time) error {
	db := api.RDB.DB(ctx)
	byLateness := map[int][]uint{}
	for _, c := range campaigns {
		byLateness[c.MaxLatenessSeconds] = append(byLateness[c.MaxLatenessSeconds], c.ID)
	}
	stale := api.campaignClock().Add(-campaignClaimStale)
	missed := map[string]any{"status": string(ElementSkipped), "reason": SkipMissed, "updated_at": now}
	for lateness, ids := range byLateness {
		cutoff := now.Add(-time.Duration(lateness) * time.Second)
		if err := db.Model(&CampaignElement{}).
			Where("campaign_id IN ? AND status = ? AND due_at < ?", ids, string(ElementPending), cutoff).
			Updates(missed).Error; err != nil {
			return err
		}
		if err := db.Model(&CampaignElement{}).
			Where("campaign_id IN ? AND status = ? AND due_at < ? AND claimed_at < ? AND batch_token IS NULL",
				ids, string(ElementClaimed), cutoff, stale).
			Updates(missed).Error; err != nil {
			return err
		}
	}
	for _, c := range campaigns {
		// Only a campaign switched on within the longest lateness can still hold such an
		// element: anything older has been expired above.
		if !c.Enabled || !c.EnabledAt.Valid || c.EnabledAt.Time.Before(now.Add(-MaxCampaignLateness)) {
			continue
		}
		// A tokenless claim left from before the switch-off counts too: it has no batch and can
		// get none once it is not CLAIMED.
		if err := db.Model(&CampaignElement{}).
			Where("campaign_id = ? AND due_at < ? AND (status = ? OR (status = ? AND batch_token IS NULL AND claimed_at < ?))",
				c.ID, c.EnabledAt.Time, string(ElementPending), string(ElementClaimed), stale).
			Updates(map[string]any{"status": string(ElementSkipped), "reason": SkipWhileDisabled, "updated_at": now}).Error; err != nil {
			return err
		}
	}
	return nil
}

// campaignClaimHook, when set, runs between a pass reading the due elements and claiming
// them: the window in which another replica can take some. It exists only so a test can be
// that other replica deterministically; it is nil in the service.
var campaignClaimHook func()

// campaignClaimedHook runs after a pass has loaded the elements it will dispatch and before
// it fences, tokens and creates batches: the window in which another replica can finish,
// re-token or cancel them. Nil in the service; a test is the other replica.
var campaignClaimedHook func()

// campaignDispatchHook runs just before a pass creates a batch for tokened claims: the window
// in which a campaign can be deleted or switched off, or its claims settled, by someone else.
// Nil in the service; a test is that someone.
var campaignDispatchHook func()

// campaignCreateHook runs between CreateCampaign's token probe and its insert, where a
// concurrent create of the same token can land. Nil in the service.
var campaignCreateHook func()

// fireCampaign coalesces, re-checks membership, claims what is due, and turns it into batches.
func (api *Api) fireCampaign(ctx context.Context, c *Campaign, now time.Time, pass string) error {
	steps, err := DecodeSteps(c.Steps)
	if err != nil || len(steps) == 0 {
		return api.recordCampaignError(ctx, c, fmt.Errorf("stored steps are unreadable"))
	}
	db := api.RDB.DB(ctx)
	var errs []error

	if err := api.supersedeOlderFirings(ctx, c, now); err != nil {
		return err
	}
	blocked, err := api.recheckDueMembership(ctx, c, now)
	if err != nil {
		errs = append(errs, api.recordCampaignError(ctx, c, err))
	}

	q := db.Model(&CampaignElement{}).
		Where("campaign_id = ? AND status = ? AND due_at <= ?", c.ID, string(ElementPending), now)
	if len(blocked) > 0 {
		q = q.Where("cycle_id NOT IN ?", blocked)
	}
	var ids []uint
	if err := q.Order("due_at ASC, id ASC").Limit(campaignClaimPage).Pluck("id", &ids).Error; err != nil {
		return errors.Join(append(errs, err)...)
	}
	if campaignClaimHook != nil {
		campaignClaimHook()
	}
	if len(ids) > 0 {
		// The compare-and-set: only rows still PENDING are taken, and the database decides
		// between replicas racing for the same rows. Stamped with the clock at the UPDATE, not
		// the start of the pass: a pass that took minutes to get here must not hand other
		// replicas a claim that already looks stale.
		taken := api.campaignClock()
		if err := db.Model(&CampaignElement{}).
			Where("id IN ? AND status = ?", ids, string(ElementPending)).
			Updates(map[string]any{
				"status": string(ElementClaimed), "claimed_by": pass,
				"claimed_at": taken, "updated_at": taken,
			}).Error; err != nil {
			return errors.Join(append(errs, err)...)
		}
	}

	// Everything claimed by this pass, plus claims a dead replica left behind.
	var claimed []CampaignElement
	if err := db.Where("campaign_id = ? AND status = ? AND (claimed_by = ? OR claimed_at < ?)",
		c.ID, string(ElementClaimed), pass, api.campaignClock().Add(-campaignClaimStale)).
		Order("cycle_id ASC, step ASC, id ASC").Find(&claimed).Error; err != nil {
		return errors.Join(append(errs, err)...)
	}
	if len(claimed) == 0 {
		return errors.Join(errs...)
	}

	if campaignClaimedHook != nil {
		campaignClaimedHook()
	}

	// A claim recovered from a dead replica is held to the same lateness bound as a fresh one:
	// work that went stale while nobody finished it is MISSED, never sent late.
	//
	// A late claim WITH a token is decided under the batch lock: if its batch exists, a replica
	// created it and died before settling, its commands went out on time, and it is settled from
	// the batch; if not, it is MISSED, and a pass still creating that batch finds its claims
	// gone when it takes the lock, and creates nothing.
	cutoff := now.Add(-time.Duration(c.MaxLatenessSeconds) * time.Second)
	var lateTokenless []uint
	lateTokens := map[string]struct{}{}
	for _, e := range claimed {
		if !e.DueAt.Before(cutoff) {
			continue
		}
		if e.BatchToken.Valid {
			lateTokens[e.BatchToken.String] = struct{}{}
		} else {
			lateTokenless = append(lateTokenless, e.ID)
		}
	}
	if err := api.settleTokenless(ctx, lateTokenless, SkipMissed, now); err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, token := range sortedKeys(lateTokens) {
		if err := api.settleTokenedClaims(ctx, c.ID, token, SkipMissed, dueBefore(cutoff), now); err != nil {
			errs = append(errs, err)
		}
	}

	// Fence: the campaign may have been switched off or deleted since this pass loaded it.
	// Deleted cancels what this pass claimed (a tokened claim through its batch, as above); off
	// leaves it claimed, and it goes MISSED if it is not finished in time.
	var live Campaign
	switch err := db.Where("id = ?", c.ID).First(&live).Error; {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return errors.Join(append(errs, api.settleOrphanedClaims(ctx, c.ID, nil, now))...)
	case err != nil:
		return errors.Join(append(errs, err)...)
	case !live.Enabled:
		return errors.Join(errs...)
	}

	// The restart boundary and the burst rule hold for claims as for pending work: a claim
	// left over from before the campaign was last switched on is DISABLED, and of several
	// claims for one device and step only the newest is sent.
	if err := api.settleClaimsBeforeEnable(ctx, &live, claimed, now); err != nil {
		return errors.Join(append(errs, err)...)
	}
	if err := api.supersedeOlderClaims(ctx, c.ID, claimed, now); err != nil {
		return errors.Join(append(errs, err)...)
	}

	claimedIDs := make([]uint, 0, len(claimed))
	for _, e := range claimed {
		claimedIDs = append(claimedIDs, e.ID)
	}
	if err := api.assignBatchTokens(ctx, c, claimed, pass); err != nil {
		return errors.Join(append(errs, err)...)
	}

	// The tokens, and who is in each batch, are what the DATABASE says. This pass's own copy
	// can be stale: another replica recovering the same claims may have assigned a token first,
	// and the update that would have given this pass its own was then a no-op. Only what is
	// still CLAIMED counts: a late claim settled above is no longer this pass's to send.
	tokenSet := map[string]struct{}{}
	var order []string
	for start := 0; start < len(claimedIDs); start += campaignRowChunk {
		end := min(start+campaignRowChunk, len(claimedIDs))
		var rows []CampaignElement
		if err := db.Select("id", "batch_token").Where("id IN ? AND status = ? AND batch_token IS NOT NULL",
			claimedIDs[start:end], string(ElementClaimed)).Order("id ASC").Find(&rows).Error; err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, r := range rows {
			if _, ok := tokenSet[r.BatchToken.String]; !ok {
				tokenSet[r.BatchToken.String] = struct{}{}
				order = append(order, r.BatchToken.String)
			}
		}
	}
	for _, token := range order {
		var members []CampaignElement
		if err := db.Where("campaign_id = ? AND batch_token = ? AND status = ?", c.ID, token,
			string(ElementClaimed)).Order("id ASC").Find(&members).Error; err != nil {
			errs = append(errs, err)
			continue
		}
		if len(members) == 0 {
			continue // settled by another replica in the meantime
		}
		step := steps[0]
		if members[0].Step >= 0 && members[0].Step < len(steps) {
			step = steps[members[0].Step]
		}
		if err := api.dispatchCampaignBatch(ctx, c, step, token, members, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func dueBefore(t time.Time) func(*gorm.DB) *gorm.DB {
	return func(q *gorm.DB) *gorm.DB { return q.Where("due_at < ?", t) }
}

func idsIn(ids []uint) func(*gorm.DB) *gorm.DB {
	return func(q *gorm.DB) *gorm.DB { return q.Where("id IN ?", ids) }
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// settleClaimsBeforeEnable settles, as DISABLED, claims due before the campaign was last
// switched on: a firing tokened or claimed before a switch-off is not released by switching the
// campaign back on. A tokened one goes through the batch lock like every tokened settle, so one
// whose batch was in fact created is recorded from it.
func (api *Api) settleClaimsBeforeEnable(ctx context.Context, live *Campaign, claimed []CampaignElement, now time.Time) error {
	if !live.EnabledAt.Valid {
		return nil
	}
	var tokenless []uint
	tokens := map[string]struct{}{}
	for _, e := range claimed {
		if !e.DueAt.Before(live.EnabledAt.Time) {
			continue
		}
		if e.BatchToken.Valid {
			tokens[e.BatchToken.String] = struct{}{}
		} else {
			tokenless = append(tokenless, e.ID)
		}
	}
	if err := api.settleTokenless(ctx, tokenless, SkipWhileDisabled, now); err != nil {
		return err
	}
	for _, token := range sortedKeys(tokens) {
		if err := api.settleTokenedClaims(ctx, live.ID, token, SkipWhileDisabled, dueBefore(live.EnabledAt.Time), now); err != nil {
			return err
		}
	}
	return nil
}

// supersedeOlderClaims applies the burst rule to what a pass holds: of several claims for one
// device and one step (a dead replica's stale claim of an earlier firing beside a fresh claim of
// the newest), only the newest is sent and the rest are SUPERSEDED. A tokened one is settled
// under the batch lock, so one whose batch was in fact created is recorded from it instead.
func (api *Api) supersedeOlderClaims(ctx context.Context, campaignID uint, claimed []CampaignElement, now time.Time) error {
	type key struct {
		device string
		step   int
	}
	newest := map[key]CampaignElement{}
	for _, e := range claimed {
		k := key{e.DeviceToken, e.Step}
		if n, ok := newest[k]; !ok || e.DueAt.After(n.DueAt) || (e.DueAt.Equal(n.DueAt) && e.ID > n.ID) {
			newest[k] = e
		}
	}
	var tokenless []uint
	byToken := map[string][]uint{}
	for _, e := range claimed {
		if newest[key{e.DeviceToken, e.Step}].ID == e.ID {
			continue
		}
		if e.BatchToken.Valid {
			byToken[e.BatchToken.String] = append(byToken[e.BatchToken.String], e.ID)
		} else {
			tokenless = append(tokenless, e.ID)
		}
	}
	if err := api.settleTokenless(ctx, tokenless, SkipSuperseded, now); err != nil {
		return err
	}
	for token, ids := range byToken {
		if err := api.settleTokenedClaims(ctx, campaignID, token, SkipSuperseded, idsIn(ids), now); err != nil {
			return err
		}
	}
	return nil
}

// supersedeOlderFirings collapses what is due for one device and one step to the newest firing.
// After an outage (or under a long lateness), several cycles can be due at once; sending each
// would replay hours of schedule into a device in seconds. The newest is what the schedule wants
// now; the older ones are recorded as SUPERSEDED.
func (api *Api) supersedeOlderFirings(ctx context.Context, c *Campaign, now time.Time) error {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return errors.New("superseding campaign firings: no tenant in context")
	}
	return api.RDB.DB(ctx).Model(&CampaignElement{}).
		Where(`status = ? AND id IN (SELECT ranked.id FROM (
			SELECT e.id, ROW_NUMBER() OVER (PARTITION BY e.device_token, e.step ORDER BY e.due_at DESC, e.id DESC) AS rn
			FROM campaign_elements e
			WHERE e.tenant_id = ? AND e.campaign_id = ? AND e.status = ? AND e.due_at <= ?) ranked
			WHERE ranked.rn > 1)`,
			string(ElementPending), tenant, c.ID, string(ElementPending), now).
		Updates(map[string]any{"status": string(ElementSkipped), "reason": SkipSuperseded, "updated_at": now}).Error
}

// recheckDueMembership resolves the group again for every cycle that has just started to fall
// due, and skips, as NOT_IN_GROUP, the pending elements of devices that have left it since the
// cycle was planned (up to CampaignPlanAhead earlier). A cycle is checked once, when its first
// element falls due. It returns the cycles it could NOT check: those are not sent this pass,
// because commanding a device that may have left the group is the failure the check exists for.
//
// A device that JOINED the group after planning is not added to a cycle already planned; it is
// in the next cycle planned. That is a documented limitation.
func (api *Api) recheckDueMembership(ctx context.Context, c *Campaign, now time.Time) ([]uint, error) {
	db := api.RDB.DB(ctx)
	var cycleIDs []uint
	if err := db.Model(&CampaignElement{}).
		Where("campaign_id = ? AND status = ? AND due_at <= ?", c.ID, string(ElementPending), now).
		Distinct().Pluck("cycle_id", &cycleIDs).Error; err != nil {
		return nil, err
	}
	if len(cycleIDs) == 0 {
		return nil, nil
	}
	var unchecked []uint
	if err := db.Model(&CampaignCycle{}).Where("id IN ? AND membership_checked_at IS NULL", cycleIDs).
		Order("id ASC").Pluck("id", &unchecked).Error; err != nil {
		return cycleIDs, err
	}
	if len(unchecked) == 0 {
		return nil, nil
	}
	if api.GroupTargetResolver == nil {
		return unchecked, errors.New("cannot re-check the group before firing: group resolution is unavailable")
	}
	group := c.GroupToken
	targets, err := api.resolveGroupTargetsUpTo(ctx, &CommandBatchCreateRequest{GroupToken: &group}, MaxCampaignTargets)
	if err != nil {
		return unchecked, fmt.Errorf("re-checking the group before firing: %w", err)
	}
	members := make(map[string]struct{}, len(targets.deviceTokens))
	for _, d := range targets.deviceTokens {
		members[d] = struct{}{}
	}
	checkedAt := api.campaignClock()
	for _, cycleID := range unchecked {
		var after uint
		for {
			var rows []CampaignElement
			if err := db.Select("id", "device_token").
				Where("cycle_id = ? AND status = ? AND id > ?", cycleID, string(ElementPending), after).
				Order("id ASC").Limit(campaignRowChunk).Find(&rows).Error; err != nil {
				return unchecked, err
			}
			if len(rows) == 0 {
				break
			}
			var gone []uint
			for _, r := range rows {
				if _, in := members[r.DeviceToken]; !in {
					gone = append(gone, r.ID)
				}
			}
			if len(gone) > 0 {
				if err := db.Model(&CampaignElement{}).Where("id IN ? AND status = ?", gone, string(ElementPending)).
					Updates(map[string]any{"status": string(ElementSkipped), "reason": SkipNotInGroup, "updated_at": now}).Error; err != nil {
					return unchecked, err
				}
			}
			after = rows[len(rows)-1].ID
			if len(rows) < campaignRowChunk {
				break
			}
		}
		if err := db.Model(&CampaignCycle{ID: cycleID}).Where("id = ? AND membership_checked_at IS NULL", cycleID).
			Update("membership_checked_at", checkedAt).Error; err != nil {
			return unchecked, err
		}
	}
	return nil, nil
}

// settleOrphanedClaims settles the stale claims of a campaign that will not dispatch them:
// deleted (c is nil) or switched off. A claim with no token has no batch: a deleted campaign's
// is CANCELLED, a switched-off campaign's goes MISSED in expireLateElements. A claim with a
// token is settled from its batch if it exists; if not, a deleted campaign's is CANCELLED and a
// switched-off one's is MISSED once all of it is past the lateness bound.
func (api *Api) settleOrphanedClaims(ctx context.Context, campaignID uint, c *Campaign, now time.Time) error {
	db := api.RDB.DB(ctx)
	staleBefore := api.campaignClock().Add(-campaignClaimStale)
	if c == nil {
		if err := db.Model(&CampaignElement{}).
			Where("campaign_id = ? AND status = ? AND batch_token IS NULL", campaignID, string(ElementClaimed)).
			Updates(map[string]any{"status": string(ElementSkipped), "reason": SkipCancelled, "updated_at": now}).Error; err != nil {
			return err
		}
	}
	var tokens []string
	if err := db.Model(&CampaignElement{}).
		Where("campaign_id = ? AND status = ? AND batch_token IS NOT NULL AND claimed_at < ?",
			campaignID, string(ElementClaimed), staleBefore).
		Distinct().Pluck("batch_token", &tokens).Error; err != nil {
		return err
	}
	sort.Strings(tokens)
	var errs []error
	for _, token := range tokens {
		reason := SkipCancelled
		if c != nil {
			reason = SkipMissed
			late := now.Add(-time.Duration(c.MaxLatenessSeconds) * time.Second)
			var onTime int64
			if err := db.Model(&CampaignElement{}).
				Where("campaign_id = ? AND batch_token = ? AND status = ? AND due_at >= ?",
					campaignID, token, string(ElementClaimed), late).
				Count(&onTime).Error; err != nil {
				errs = append(errs, err)
				continue
			}
			if onTime > 0 {
				// Not all of it is late. Still settle it from its batch if the batch exists; leave
				// it claimed otherwise, in case the campaign is switched back on in time.
				reason = ""
			}
		}
		if err := api.settleTokenedClaims(ctx, campaignID, token, reason, nil, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// settleTokenless settles claims that have no batch token, and only while they have none: a
// claim another pass tokened in the meantime is that pass's to finish.
func (api *Api) settleTokenless(ctx context.Context, ids []uint, reason string, now time.Time) error {
	for start := 0; start < len(ids); start += campaignRowChunk {
		end := min(start+campaignRowChunk, len(ids))
		if err := api.RDB.DB(ctx).Model(&CampaignElement{}).
			Where("id IN ? AND status = ? AND batch_token IS NULL", ids[start:end], string(ElementClaimed)).
			Updates(map[string]any{"status": string(ElementSkipped), "reason": reason, "updated_at": now}).Error; err != nil {
			return err
		}
	}
	return nil
}

// settleTokenedClaims decides a tokened claim that will not be dispatched as it stands.
//
// 🔴 WHETHER ITS BATCH EXISTS IS DECIDED UNDER THE TENANT BATCH LOCK, the lock the batch's
// creation holds for its whole transaction, and so is the settle when it does not. Probed
// without it, the answer "no batch" can be overtaken by a pass creating that batch at this
// moment: the elements would read MISSED or CANCELLED while their commands went to the devices.
// Under it, either the batch is committed (and is settled from), or the creating pass takes the
// lock after this settle, finds its claims no longer CLAIMED, and creates nothing.
//
// reason empty leaves a batch-less claim alone; narrow, when set, limits a batch-less settle
// to some of the token's members (those due before a cutoff, or named ones).
func (api *Api) settleTokenedClaims(ctx context.Context, campaignID uint, token, reason string,
	narrow func(*gorm.DB) *gorm.DB, now time.Time) error {
	var batch CommandBatch
	found := false
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenantForBatch(ctx, tx); err != nil {
			return err
		}
		res := tx.Where("token = ?", token).Limit(1).Find(&batch)
		if res.Error != nil {
			return res.Error
		}
		if found = res.RowsAffected == 1; found || reason == "" {
			return nil
		}
		q := tx.Model(&CampaignElement{}).
			Where("campaign_id = ? AND batch_token = ? AND status = ?", campaignID, token, string(ElementClaimed))
		if narrow != nil {
			q = narrow(q)
		}
		return q.Updates(map[string]any{"status": string(ElementSkipped), "reason": reason, "updated_at": now}).Error
	})
	if err != nil || !found {
		return err
	}
	var members []CampaignElement
	if err := api.RDB.DB(ctx).Where("campaign_id = ? AND batch_token = ? AND status = ?", campaignID, token,
		string(ElementClaimed)).Order("id ASC").Find(&members).Error; err != nil {
		return err
	}
	return api.settleFromBatch(ctx, &batch, members, nil, now)
}

// assignBatchTokens gives claimed elements that have no batch token one, in chunks of at most
// campaignClaimPage per cycle and step. The token carries the assigning pass's id, so two
// replicas recovering the same claims can never mint the same token for different members; each
// element takes whichever token reaches it first, and what an element's token IS is read back
// from the database by the caller, never assumed from this call.
func (api *Api) assignBatchTokens(ctx context.Context, c *Campaign, claimed []CampaignElement, pass string) error {
	type key struct {
		cycle uint
		step  int
	}
	groups := map[key][]uint{}
	var order []key
	for _, e := range claimed {
		if e.BatchToken.Valid {
			continue
		}
		k := key{e.CycleId, e.Step}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e.ID)
	}
	for _, k := range order {
		ids := groups[k]
		for start := 0; start < len(ids); start += campaignClaimPage {
			chunk := ids[start:min(start+campaignClaimPage, len(ids))]
			token := fmt.Sprintf("cmp-%d-%d-%d-%d-%s", c.ID, k.cycle, k.step, chunk[0], pass)
			// 🔴 ONE TRANSACTION PER TOKEN. A token's membership must appear all at once: written
			// in several statements, another replica recovering the same claims could read the
			// members so far, create the batch from them, and this pass's replay of that token
			// would then return the smaller batch and refuse the members it left out.
			if err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
				for from := 0; from < len(chunk); from += campaignRowChunk {
					part := chunk[from:min(from+campaignRowChunk, len(chunk))]
					if err := tx.Model(&CampaignElement{}).
						Where("id IN ? AND status = ? AND batch_token IS NULL", part, string(ElementClaimed)).
						Update("batch_token", token).Error; err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Why a campaign batch was not created: the work stopped being this pass's to send between
// loading it and taking the batch lock.
var (
	errCampaignGone    = errors.New("the campaign was deleted before its batch was created")
	errCampaignOff     = errors.New("the campaign was switched off before its batch was created")
	errCampaignSettled = errors.New("the batch's claims were settled before it was created")
)

func (api *Api) dispatchCampaignBatch(ctx context.Context, c *Campaign, step CampaignStep,
	token string, members []CampaignElement, now time.Time) error {
	devices := make([]string, 0, len(members))
	ids := make([]uint, 0, len(members))
	for _, m := range members {
		devices = append(devices, m.DeviceToken)
		ids = append(ids, m.ID)
	}
	expires := now.Add(time.Duration(c.ExpiresInSeconds) * time.Second).UTC().Format(time.RFC3339)
	meta, _ := json.Marshal(map[string]any{"campaign": c.Token, "cycle": members[0].CycleId, "step": members[0].Step})
	metaStr := string(meta)
	req := &CommandBatchCreateRequest{
		Token:        token,
		Name:         step.Name,
		ExpiresAt:    &expires,
		Metadata:     &metaStr,
		DeviceTokens: &devices,
		AllowPartial: true, // the elements ARE the per-device record; one bad device blocks no other
	}
	if step.Payload != "" {
		p := step.Payload
		req.Payload = &p
	}
	if campaignDispatchHook != nil {
		campaignDispatchHook()
	}
	// 🔴 THE FENCE THAT MATTERS RUNS UNDER THE BATCH LOCK, in the transaction that writes the
	// commands. The fence before tokening is a cheap early exit; this one is the proof. Whoever
	// settles a tokened claim (a delete, a late recovery, a switched-off campaign's settle) does
	// it under the same lock, so either they see this batch committed, or this sees their settle
	// and sends nothing.
	fence := func(tx *gorm.DB) error {
		var live Campaign
		res := tx.Where("id = ?", c.ID).Limit(1).Find(&live)
		switch {
		case res.Error != nil:
			return res.Error
		case res.RowsAffected == 0:
			return errCampaignGone
		case !live.Enabled:
			return errCampaignOff
		}
		// Switched off and on again since these were claimed: what is due before the switch-on
		// is not this batch's to send (the next pass settles it as DISABLED).
		if live.EnabledAt.Valid {
			for _, m := range members {
				if m.DueAt.Before(live.EnabledAt.Time) {
					return errCampaignOff
				}
			}
		}
		var still int64
		if err := tx.Model(&CampaignElement{}).Where("campaign_id = ? AND batch_token = ? AND status = ?",
			c.ID, token, string(ElementClaimed)).Count(&still).Error; err != nil {
			return err
		}
		if int(still) != len(members) {
			return errCampaignSettled
		}
		return nil
	}
	var refusals []BatchDeviceRefusal
	acct := batchAccounting{targetKind: requestTargetKindLabel(req)}
	batch, err := api.decideCommandBatch(ctx, req, &acct, nil, &batchOptions{fence: fence, refusals: &refusals})
	switch {
	case errors.Is(err, errCampaignGone):
		// Nothing was written; settle the claims as the delete asked, under the lock as always.
		return api.settleTokenedClaims(ctx, c.ID, token, SkipCancelled, nil, now)
	case errors.Is(err, errCampaignOff), errors.Is(err, errCampaignSettled):
		// Off: left claimed, MISSED if still off past the lateness bound. Settled: someone
		// else's record already stands. Neither is a batch outcome to count.
		return nil
	}
	api.recordBatchOutcome(&acct, err)
	if err != nil {
		var enq *EnqueueRejected
		var bat *BatchRejected
		switch {
		case errors.As(err, &bat):
			return api.settleElements(ctx, ids, ElementRefused, string(bat.Code), now)
		case errors.As(err, &enq):
			return api.settleElements(ctx, ids, ElementRefused, string(enq.Code), now)
		}
		// Not decided (a dependency is down). The elements stay CLAIMED and any replica
		// finishes them once the claim goes stale; the token makes that a replay.
		return fmt.Errorf("batch %s: %w", token, err)
	}
	if acct.replay {
		refusals = nil // a replay's refusals were decided by whoever created it
	}
	return api.settleFromBatch(ctx, batch, members, refusals, now)
}

// settleFromBatch settles claimed elements from the batch that carried them: DISPATCHED where
// the batch wrote the device a command, REFUSED with the device's own code where it did not.
// full is the complete refusal list when this process decided the batch; without it (a replay)
// the batch record's sample is the best evidence, and a device beyond it reads NOT_ADMITTED.
func (api *Api) settleFromBatch(ctx context.Context, batch *CommandBatch, members []CampaignElement,
	full []BatchDeviceRefusal, now time.Time) error {
	admitted := map[string]struct{}{}
	var rows []Command
	if err := api.RDB.DB(ctx).Select("device_token").Where("batch_token = ?", batch.Token).Find(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		admitted[r.DeviceToken] = struct{}{}
	}
	if full == nil && batch.Refusals != nil {
		_ = json.Unmarshal(*batch.Refusals, &full)
	}
	refusalCode := make(map[string]string, len(full))
	for _, r := range full {
		refusalCode[r.DeviceToken] = string(r.Code)
	}
	var ok []uint
	byReason := map[string][]uint{}
	for _, m := range members {
		if _, in := admitted[m.DeviceToken]; in {
			ok = append(ok, m.ID)
			continue
		}
		reason := refusalCode[m.DeviceToken]
		if reason == "" {
			reason = "NOT_ADMITTED"
		}
		byReason[reason] = append(byReason[reason], m.ID)
	}
	if err := api.settleElements(ctx, ok, ElementDispatched, "", now); err != nil {
		return err
	}
	for reason, rids := range byReason {
		if err := api.settleElements(ctx, rids, ElementRefused, reason, now); err != nil {
			return err
		}
	}
	return nil
}

func (api *Api) settleElements(ctx context.Context, ids []uint, status CampaignElementStatus, reason string, now time.Time) error {
	for start := 0; start < len(ids); start += campaignRowChunk {
		end := min(start+campaignRowChunk, len(ids))
		set := map[string]any{"status": string(status), "updated_at": now}
		if reason != "" {
			set["reason"] = reason
		}
		if err := api.RDB.DB(ctx).Model(&CampaignElement{}).
			Where("id IN ? AND status = ?", ids[start:end], string(ElementClaimed)).
			Updates(set).Error; err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------------------
// Pruning
// ---------------------------------------------------------------------------------------

// PruneCampaignHistory removes, for every tenant, the finished elements of cycles planned
// before the retention, the cycles left with none, and deleted campaigns whose history is
// gone. It runs on its own cadence, slower than the tick: history is not urgent, and a bounded
// amount per tenant per run keeps it from competing with firing. A pending or claimed element is
// never removed, however old: it is work, not history.
func (api *Api) PruneCampaignHistory(ctx context.Context, now time.Time) error {
	var tenants []string
	sys := api.RDB.DB(core.WithSystemContext(ctx))
	if err := sys.Unscoped().Model(&Campaign{}).Distinct().Pluck("tenant_id", &tenants).Error; err != nil {
		return fmt.Errorf("listing tenants with campaigns: %w", err)
	}
	var errs []error
	for _, tenant := range tenants {
		if err := api.pruneTenantCampaigns(core.WithTenant(ctx, tenant), now); err != nil {
			errs = append(errs, fmt.Errorf("tenant %s: pruning campaign history: %w", tenant, err))
		}
	}
	return errors.Join(errs...)
}

func (api *Api) pruneTenantCampaigns(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-CampaignHistoryRetention)
	db := api.RDB.DB(ctx)
	var old []uint
	if err := db.Model(&CampaignCycle{}).Where("created_at < ?", cutoff).
		Order("created_at ASC").Limit(campaignPruneCycles).Pluck("id", &old).Error; err != nil {
		return err
	}
	if len(old) > 0 {
		// Several chunks a run, so history can be removed faster than it is written; the
		// trigger-rate bound at create is what keeps it from being produced faster still.
		for i := 0; i < campaignPruneChunksPerPass; i++ {
			var ids []uint
			if err := db.Model(&CampaignElement{}).
				Where("cycle_id IN ? AND status NOT IN ?", old,
					[]string{string(ElementPending), string(ElementClaimed)}).
				Limit(campaignPruneChunk).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			for start := 0; start < len(ids); start += campaignRowChunk {
				end := min(start+campaignRowChunk, len(ids))
				if err := db.Where("id IN ?", ids[start:end]).Delete(&CampaignElement{}).Error; err != nil {
					return err
				}
			}
			if len(ids) < campaignPruneChunk {
				break
			}
		}
		if err := db.Where(`id IN ? AND NOT EXISTS (SELECT 1 FROM campaign_elements e
			WHERE e.tenant_id = campaign_cycles.tenant_id AND e.cycle_id = campaign_cycles.id)`, old).
			Delete(&CampaignCycle{}).Error; err != nil {
			return err
		}
	}
	// A deleted campaign's row goes once its history has: until then its cycles still name it.
	// Removing it takes its tenant out of the pruner's listing for good.
	return db.Unscoped().Where(`deleted_at IS NOT NULL AND deleted_at < ? AND NOT EXISTS (SELECT 1 FROM campaign_cycles cy
		WHERE cy.tenant_id = campaigns.tenant_id AND cy.campaign_id = campaigns.id)`, cutoff).
		Delete(&Campaign{}).Error
}
