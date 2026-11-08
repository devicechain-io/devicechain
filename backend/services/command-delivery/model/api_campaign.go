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
	// RejectCampaignFieldInvalid: a numeric field is out of range.
	RejectCampaignFieldInvalid RejectionCode = "CAMPAIGN_FIELD_INVALID"
)

// Campaign bounds and defaults.
const (
	MaxCampaignSteps = 10
	// MaxCampaignFiresPerDay bounds how often a trigger may fire, averaged over a week.
	MaxCampaignFiresPerDay = 48
	// MaxCampaignTargets bounds the devices one cycle may plan. A cycle is rows rather than
	// one batch, so it may exceed MaxBatchDeviceTokens; the bound is still finite.
	MaxCampaignTargets = 100000
	// MaxCampaignStepDelaySeconds is the longest per-step delay: a day.
	MaxCampaignStepDelaySeconds = 24 * 60 * 60

	DefaultCampaignMaxLateness = 15 * time.Minute
	DefaultCampaignExpiresIn   = time.Hour
	MaxCampaignLateness        = 24 * time.Hour
	MaxCampaignExpiresIn       = 7 * 24 * time.Hour

	// CampaignPlanAhead is how far ahead of now cycles are planned. It must exceed a day by
	// the widest spread of a solar event inside one civil day (about 14h either side of the
	// zone's midnight at the extremes), so that a day is always planned before any of its
	// fixtures falls due.
	CampaignPlanAhead = 36 * time.Hour

	// campaignClaimPage bounds the elements one pass claims for one campaign, and so the
	// size of the biggest batch a pass creates.
	campaignClaimPage = 5000
	// campaignClaimStale is how long a CLAIMED element is left to the replica that claimed
	// it before any replica may finish its dispatch.
	campaignClaimStale = 2 * time.Minute
	campaignRowChunk   = 1000

	// CampaignHistoryRetention is how long finished cycles and their elements are kept. A
	// nightly campaign over a large fleet writes a row per device per step per night, so the
	// history has to be bounded; the commands themselves follow the command retention.
	CampaignHistoryRetention = 30 * 24 * time.Hour
	campaignPruneChunk       = 5000
	// campaignPruneChunksPerPass bounds the work one tenant costs one pass.
	campaignPruneChunksPerPass = 20
)

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
func boundFiresPerDay(trigger *schedule.Trigger) error {
	start := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC) // a leap year, so February 29 is seen
	for d := 0; d < 366; d++ {
		from := start.AddDate(0, 0, d)
		day, err := trigger.Cycles(from, from.Add(24*time.Hour))
		if err != nil {
			return rejected(RejectCampaignTriggerInvalid, "%s", strings.TrimPrefix(err.Error(), "invalid schedule: "))
		}
		if len(day) > MaxCampaignFiresPerDay {
			return rejected(RejectCampaignTriggerInvalid,
				"the trigger fires %d times on %s; a campaign may fire at most %d times a day",
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
	if strings.TrimSpace(req.GroupToken) == "" {
		return nil, 0, 0, rejected(RejectCampaignTargetInvalid, "a campaign needs a groupToken")
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
	c := &Campaign{
		TokenReference:     rdb.TokenReference{Token: req.Token},
		Enabled:            req.Enabled == nil || *req.Enabled,
		Trigger:            trigger,
		Steps:              datatypes.JSON(stepsDoc),
		TargetKind:         CampaignTargetGroup,
		GroupToken:         strings.TrimSpace(req.GroupToken),
		MaxLatenessSeconds: lateness,
		ExpiresInSeconds:   expires,
	}
	if req.Name != nil {
		c.Name = *req.Name
	}
	if err := api.RDB.DB(ctx).Create(c).Error; err != nil {
		return nil, err
	}
	return c, nil
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
func (api *Api) SetCampaignEnabled(ctx context.Context, token string, enabled bool) (*Campaign, error) {
	c, err := api.campaignByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := api.RDB.DB(ctx).Model(c).Where("id = ?", c.ID).Update("enabled", enabled).Error; err != nil {
		return nil, err
	}
	c.Enabled = enabled
	return c, nil
}

// DeleteCampaign removes a campaign and cancels what it had planned but not yet sent.
// Commands already created are untouched; they belong to their batches.
func (api *Api) DeleteCampaign(ctx context.Context, token string) error {
	c, err := api.campaignByToken(ctx, token)
	if err != nil {
		return err
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// CLAIMED as well as PENDING: once the campaign row is gone no tick loads it again, so a
		// claim a dead replica left would otherwise stay CLAIMED for good, and pruning keeps
		// CLAIMED rows. A live pass that already fenced and is mid-dispatch then finds its
		// settle a no-op; the commands it created stand, and the deleted campaign's elements
		// read CANCELLED, which is what deleting it asked for.
		if err := tx.Model(&CampaignElement{}).
			Where("campaign_id = ? AND status IN ?", c.ID, []string{string(ElementPending), string(ElementClaimed)}).
			Updates(map[string]any{"status": string(ElementSkipped), "reason": SkipCancelled}).Error; err != nil {
			return err
		}
		return tx.Delete(&Campaign{}, c.ID).Error
	})
}

// SkipCancelled marks planned elements a deleted campaign never sent.
const SkipCancelled = "CANCELLED"

// ---------------------------------------------------------------------------------------
// Ticking
// ---------------------------------------------------------------------------------------

// TickCampaigns runs one scheduler pass over every tenant with an enabled campaign: plan the
// cycles that are coming, expire what is too late to send, claim what is due, and create the
// batches. The caller passes a context WITHOUT a tenant (the pass lists tenants itself under
// a system context) and a fresh pass id per call.
//
// 🔑 IT IS SAFE TO RUN IN EVERY REPLICA AT ONCE, with no leader. Each step is a
// compare-and-set the database arbitrates, in the manner of the notification escalation
// scheduler: a cycle is a row under a unique key, so two replicas planning it produce one;
// an element is claimed by an UPDATE conditional on it still being PENDING, so two replicas
// claiming it produce one claim; and a batch is created under a token derived from its
// elements, so a replica finishing a dead one's work replays rather than duplicates. The
// failure this is built against is a street light commanded twice.
//
// The first error does not stop the pass: one tenant's unreachable group must not stop every
// other tenant's lights. Errors are joined and returned.
func (api *Api) TickCampaigns(ctx context.Context, now time.Time, pass string) error {
	// Every tenant that has a campaign or any campaign history: a tenant whose last campaign
	// was switched off or deleted still has pending work to expire and history to prune.
	var withCampaigns, withHistory []string
	// 🔑 THE SYSTEM CONTEXT ONLY ENUMERATES TENANTS. It reads two DISTINCT tenant_id lists and
	// nothing else; every campaign, cycle and element of a tenant is then read and written under
	// core.WithTenant below, so the tenant predicate applies to all of the actual work.
	sys := api.RDB.DB(core.WithSystemContext(ctx))
	if err := sys.Unscoped().Model(&Campaign{}).Distinct().Pluck("tenant_id", &withCampaigns).Error; err != nil {
		return fmt.Errorf("listing tenants with campaigns: %w", err)
	}
	if err := sys.Model(&CampaignCycle{}).Distinct().Pluck("tenant_id", &withHistory).Error; err != nil {
		return fmt.Errorf("listing tenants with campaign history: %w", err)
	}
	seen := map[string]struct{}{}
	var tenants []string
	for _, t := range append(withCampaigns, withHistory...) {
		if _, dup := seen[t]; !dup {
			seen[t] = struct{}{}
			tenants = append(tenants, t)
		}
	}
	var errs []error
	for _, tenant := range tenants {
		tctx := core.WithTenant(ctx, tenant)
		var campaigns []*Campaign
		if err := api.RDB.DB(tctx).Order("id ASC").Find(&campaigns).Error; err != nil {
			errs = append(errs, fmt.Errorf("tenant %s: %w", tenant, err))
			continue
		}
		for _, c := range campaigns {
			if !c.Enabled {
				// Off plans and sends nothing, but what it left pending still goes stale.
				if err := api.expireLateElements(tctx, c, now); err != nil {
					errs = append(errs, fmt.Errorf("tenant %s campaign %s: %w", tenant, c.Token, err))
				}
				continue
			}
			if err := api.tickCampaign(tctx, c, now, pass); err != nil {
				errs = append(errs, fmt.Errorf("tenant %s campaign %s: %w", tenant, c.Token, err))
			}
		}
		if err := api.pruneCampaignHistory(tctx, now); err != nil {
			errs = append(errs, fmt.Errorf("tenant %s: pruning campaign history: %w", tenant, err))
		}
	}
	return errors.Join(errs...)
}

func (api *Api) tickCampaign(ctx context.Context, c *Campaign, now time.Time, pass string) error {
	trigger, err := DecodeTrigger(c.Trigger)
	if err != nil {
		return api.recordCampaignError(ctx, c, fmt.Errorf("stored trigger no longer validates: %w", err))
	}
	steps, err := DecodeSteps(c.Steps)
	if err != nil || len(steps) == 0 {
		return api.recordCampaignError(ctx, c, fmt.Errorf("stored steps are unreadable"))
	}
	var errs []error
	if err := api.planCampaign(ctx, c, trigger, steps, now); err != nil {
		errs = append(errs, api.recordCampaignError(ctx, c, err))
	} else if c.LastError.Valid {
		if uerr := api.RDB.DB(ctx).Model(c).Where("id = ?", c.ID).
			Update("last_error", sql.NullString{}).Error; uerr != nil {
			errs = append(errs, uerr)
		}
	}
	if err := api.expireLateElements(ctx, c, now); err != nil {
		errs = append(errs, err)
	}
	if err := api.fireDueElements(ctx, c, steps, now, pass); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// pruneCampaignHistory removes one chunk of elements that are finished and older than the
// retention, and the cycles left with none. A pending or claimed element is never removed,
// however old: it is work, not history.
func (api *Api) pruneCampaignHistory(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-CampaignHistoryRetention)
	db := api.RDB.DB(ctx)
	// Several chunks a pass, so history can be removed faster than one chunk a tick allows;
	// the trigger-rate bound at create is what keeps it from being produced faster still.
	for i := 0; i < campaignPruneChunksPerPass; i++ {
		var ids []uint
		if err := db.Model(&CampaignElement{}).
			Where("due_at < ? AND status NOT IN ?", cutoff,
				[]string{string(ElementPending), string(ElementClaimed)}).
			Order("id ASC").Limit(campaignPruneChunk).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		if err := db.Where("id IN ?", ids).Delete(&CampaignElement{}).Error; err != nil {
			return err
		}
		if len(ids) < campaignPruneChunk {
			break
		}
	}
	var cycles []uint
	if err := db.Model(&CampaignCycle{}).
		Where("created_at < ? AND NOT EXISTS (SELECT 1 FROM campaign_elements e WHERE e.cycle_id = campaign_cycles.id)", cutoff).
		Limit(campaignPruneChunk).Pluck("id", &cycles).Error; err != nil {
		return err
	}
	if len(cycles) == 0 {
		return nil
	}
	return db.Where("id IN ?", cycles).Delete(&CampaignCycle{}).Error
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
		// A solar day is planned from its local midnight, so the day a campaign is created
		// on is not lost; any fixture already past its time falls out as MISSED.
		// One day earlier still: a fixture's event for the previous solar date can fall after
		// now (the west of the world dims after midnight UTC); anything already past is
		// recorded as MISSED rather than lost.
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

	cycle := &CampaignCycle{
		CampaignId:   c.ID,
		CycleKey:     cy.Key,
		Day:          sql.NullString{String: cy.Day, Valid: cy.Day != ""},
		At:           sql.NullTime{Time: cy.At, Valid: !cy.At.IsZero()},
		GroupVersion: targets.groupVersion,
		Resolved:     len(targets.deviceTokens),
	}
	now := time.Now()
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

func (api *Api) expireLateElements(ctx context.Context, c *Campaign, now time.Time) error {
	cutoff := now.Add(-time.Duration(c.MaxLatenessSeconds) * time.Second)
	if err := api.RDB.DB(ctx).Model(&CampaignElement{}).
		Where("campaign_id = ? AND status = ? AND due_at < ?", c.ID, string(ElementPending), cutoff).
		Updates(map[string]any{"status": string(ElementSkipped), "reason": SkipMissed, "updated_at": now}).Error; err != nil {
		return err
	}
	// A claim a dead replica left on a switched-off campaign: past its lateness bound, stale, and
	// never given a batch token, so no batch can exist for it and it is simply MISSED. A claim
	// WITH a token is left for the tick that recovers it once the campaign is on again, because
	// its batch may exist and its commands may have run.
	return api.RDB.DB(ctx).Model(&CampaignElement{}).
		Where("campaign_id = ? AND status = ? AND due_at < ? AND claimed_at < ? AND batch_token IS NULL",
			c.ID, string(ElementClaimed), cutoff, now.Add(-campaignClaimStale)).
		Updates(map[string]any{"status": string(ElementSkipped), "reason": SkipMissed, "updated_at": now}).Error
}

// campaignClaimHook, when set, runs between a pass reading the due elements and claiming
// them: the window in which another replica can take some. It exists only so a test can be
// that other replica deterministically; it is nil in the service.
var campaignClaimHook func()

// campaignClaimedHook runs after a pass has loaded the elements it will dispatch and before
// it fences, tokens and creates batches: the window in which another replica can finish,
// re-token or cancel them. Nil in the service; a test is the other replica.
var campaignClaimedHook func()

// fireDueElements claims what is due and turns it into batches.
func (api *Api) fireDueElements(ctx context.Context, c *Campaign, steps []CampaignStep, now time.Time, pass string) error {
	db := api.RDB.DB(ctx)

	var ids []uint
	if err := db.Model(&CampaignElement{}).
		Where("campaign_id = ? AND status = ? AND due_at <= ?", c.ID, string(ElementPending), now).
		Order("due_at ASC, id ASC").Limit(campaignClaimPage).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if campaignClaimHook != nil {
		campaignClaimHook()
	}
	if len(ids) > 0 {
		// The compare-and-set: only rows still PENDING are taken, and the database decides
		// between replicas racing for the same rows.
		if err := db.Model(&CampaignElement{}).
			Where("id IN ? AND status = ?", ids, string(ElementPending)).
			Updates(map[string]any{
				"status": string(ElementClaimed), "claimed_by": pass,
				"claimed_at": now, "updated_at": now,
			}).Error; err != nil {
			return err
		}
	}

	// Everything claimed by this pass, plus claims a dead replica left behind.
	var claimed []CampaignElement
	if err := db.Where("campaign_id = ? AND status = ? AND (claimed_by = ? OR claimed_at < ?)",
		c.ID, string(ElementClaimed), pass, now.Add(-campaignClaimStale)).
		Order("cycle_id ASC, step ASC, id ASC").Find(&claimed).Error; err != nil {
		return err
	}
	if len(claimed) == 0 {
		return nil
	}

	if campaignClaimedHook != nil {
		campaignClaimedHook()
	}

	// A claim recovered from a dead replica is held to the same lateness bound as a fresh one:
	// work that went stale while nobody finished it is MISSED, never sent late.
	//
	// Except a late claim whose batch ALREADY EXISTS: a replica created it and died before
	// settling the elements, so its commands were sent on time and may have run. Calling those
	// MISSED would drop them from tracking and from retries; they go on to the replay below,
	// which creates nothing and settles them from the batch's own commands.
	cutoff := now.Add(-time.Duration(c.MaxLatenessSeconds) * time.Second)
	var onTime []CampaignElement
	var lateIDs []uint
	batchExists := map[string]bool{}
	for _, e := range claimed {
		if !e.DueAt.Before(cutoff) {
			onTime = append(onTime, e)
			continue
		}
		if e.BatchToken.Valid {
			exists, seen := batchExists[e.BatchToken.String]
			if !seen {
				_, found, err := api.liveBatchByToken(ctx, e.BatchToken.String)
				if err != nil {
					return err
				}
				exists = found
				batchExists[e.BatchToken.String] = found
			}
			if exists {
				onTime = append(onTime, e)
				continue
			}
		}
		lateIDs = append(lateIDs, e.ID)
	}
	if err := api.settleElements(ctx, lateIDs, ElementSkipped, SkipMissed, now); err != nil {
		return err
	}
	claimed = onTime
	if len(claimed) == 0 {
		return nil
	}

	// Fence: the campaign may have been switched off or deleted since this pass loaded it.
	// Deleted cancels what this pass claimed; off leaves it claimed, and it goes MISSED if it
	// is not finished in time.
	var live Campaign
	switch err := db.Where("id = ?", c.ID).First(&live).Error; {
	case errors.Is(err, gorm.ErrRecordNotFound):
		ids := make([]uint, 0, len(claimed))
		for _, e := range claimed {
			ids = append(ids, e.ID)
		}
		return api.settleElements(ctx, ids, ElementSkipped, SkipCancelled, now)
	case err != nil:
		return err
	case !live.Enabled:
		return nil
	}

	if err := api.assignBatchTokens(ctx, c, claimed, pass); err != nil {
		return err
	}

	// The tokens, and who is in each batch, are what the DATABASE says. This pass's own copy
	// can be stale: another replica recovering the same claims may have assigned a token first,
	// and the update that would have given this pass its own was then a no-op.
	claimedIDs := make([]uint, 0, len(claimed))
	for _, e := range claimed {
		claimedIDs = append(claimedIDs, e.ID)
	}
	tokenSet := map[string]struct{}{}
	var order []string
	for start := 0; start < len(claimedIDs); start += campaignRowChunk {
		end := min(start+campaignRowChunk, len(claimedIDs))
		var rows []CampaignElement
		if err := db.Select("id", "batch_token").Where("id IN ? AND status = ? AND batch_token IS NOT NULL",
			claimedIDs[start:end], string(ElementClaimed)).Order("id ASC").Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			if _, ok := tokenSet[r.BatchToken.String]; !ok {
				tokenSet[r.BatchToken.String] = struct{}{}
				order = append(order, r.BatchToken.String)
			}
		}
	}
	var errs []error
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
	batch, err := api.CreateCommandBatch(ctx, req)
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

	admitted := map[string]struct{}{}
	var rows []Command
	if err := api.RDB.DB(ctx).Select("device_token").Where("batch_token = ?", batch.Token).Find(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		admitted[r.DeviceToken] = struct{}{}
	}
	refusalCode := map[string]string{}
	if batch.Refusals != nil {
		var sample []BatchDeviceRefusal
		if json.Unmarshal(*batch.Refusals, &sample) == nil {
			for _, r := range sample {
				refusalCode[r.DeviceToken] = string(r.Code)
			}
		}
	}
	var ok, refused []uint
	reasons := map[uint]string{}
	for _, m := range members {
		if _, in := admitted[m.DeviceToken]; in {
			ok = append(ok, m.ID)
			continue
		}
		refused = append(refused, m.ID)
		reason := refusalCode[m.DeviceToken]
		if reason == "" {
			reason = "NOT_ADMITTED"
		}
		reasons[m.ID] = reason
	}
	if err := api.settleElements(ctx, ok, ElementDispatched, "", now); err != nil {
		return err
	}
	byReason := map[string][]uint{}
	for _, id := range refused {
		byReason[reasons[id]] = append(byReason[reasons[id]], id)
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
