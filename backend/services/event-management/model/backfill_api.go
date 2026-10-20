// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"gorm.io/gorm"
)

// The API of calculated-metric backfill (ADR-082, S8-2b): start, cancel, read and list. The
// job record and its guarded transitions are calculated_backfill.go, the worker that runs a job
// is processor/backfill; this file is the door between them, and the only thing that creates a
// job.

// The bounds of a request. They are constants of the platform, not settings: the span is raised
// after the load measurement of S8-4, not by an operator.
const (
	// MaxBackfillSpan is the longest range one job may cover.
	MaxBackfillSpan = 90 * 24 * time.Hour
	// MaxBackfillDevices is the most devices a request may name.
	MaxBackfillDevices = 1000
	// DefaultBackfillPageSize and MaxBackfillPageSize bound the job list.
	DefaultBackfillPageSize = 25
	MaxBackfillPageSize     = 100
	// CodeUnavailable is the extensions.code of a refusal that is the instance's state, not the
	// request's: retrying the same request later may succeed, and no edit to it will.
	CodeUnavailable = "UNAVAILABLE"
)

// BackfillUnavailable is the refusal of a request the instance cannot serve right now: the
// worker is off, or the service it needs is not reachable. It answers UNAVAILABLE.
type BackfillUnavailable struct{ Reason string }

func (e *BackfillUnavailable) Error() string {
	return "calculated-metric backfill is unavailable: " + e.Reason
}

// Extensions gives the refusal its wire code.
func (e *BackfillUnavailable) Extensions() map[string]any {
	return map[string]any{"code": CodeUnavailable}
}

// BackfillPlanSource is where a job's plan comes from: device-management's plan and roster doors.
type BackfillPlanSource interface {
	// Plan returns the published version's plan, decoded and verbatim (the verbatim document is
	// what a job freezes). version nil is the active version. A profile or version that does not
	// exist is an error wrapping gorm.ErrRecordNotFound; a source that cannot be reached is a
	// *BackfillUnavailable.
	Plan(ctx context.Context, tenant, profileToken string, version *int) (*CalculatedPlanDoc, json.RawMessage, error)
	// CheckDevices refuses, naming it, a device token that is not a device of the profile.
	CheckDevices(ctx context.Context, tenant, profileToken string, deviceTokens []string) error
}

// BackfillRuntime is what the API needs from the instance: whether the worker runs here, and the
// sources and policy a start is checked against. Main builds it once; a nil runtime on the Api
// answers every start UNAVAILABLE.
type BackfillRuntime struct {
	// Runs is whether the worker runs on this instance (CalculatedBackfillConfiguration.Runs), and
	// WhyNot says why not in words for the caller when it does not. The caller is any tenant user
	// with event:read (the availability query), so WhyNot names the lever and never the
	// instance's settings or sizing; those go to the startup log.
	Runs   bool
	WhyNot string
	// Plans is nil when device-management is not configured; a start then is UNAVAILABLE.
	Plans BackfillPlanSource
	// RetentionDays and ChunkInterval give the retention floor of a range: the worker will not
	// write within two chunks of the retention horizon. RetentionDays 0 keeps everything.
	RetentionDays int
	ChunkInterval time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

func (r *BackfillRuntime) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// StartBackfillRequest is the input of StartBackfill.
type StartBackfillRequest struct {
	ProfileToken string
	Version      *int
	MetricKeys   []string
	From, To     time.Time
	DeviceTokens []string
}

func invalid(format string, args ...any) error {
	return integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf(format, args...))
}

// BackfillAvailability is the one decision about whether this instance can run a backfill at
// all: nil when it can, a *BackfillUnavailable naming why when it cannot. StartBackfill refuses
// with exactly this error, and the availability query reports exactly its reason, so the console
// can disable the control up front with the words a refused start would have carried.
func (a *Api) BackfillAvailability() *BackfillUnavailable {
	rt := a.Backfill
	if rt == nil {
		return &BackfillUnavailable{Reason: "this instance has no backfill configuration"}
	}
	if !rt.Runs {
		reason := rt.WhyNot
		if reason == "" {
			reason = "the worker is not running on this instance"
		}
		return &BackfillUnavailable{Reason: "the backfill worker is not running: " + reason}
	}
	if rt.Plans == nil {
		return &BackfillUnavailable{Reason: "device-management is not configured, so the profile's plan cannot be read"}
	}
	return nil
}

// StartBackfill validates a request, freezes the plan it names and queues the job. The caller has
// authorized the principal; createdBy is its name. Nothing is queued unless every check passes,
// and an instance whose worker does not run refuses the request instead of queueing a job that
// nothing would run.
func (a *Api) StartBackfill(ctx context.Context, req StartBackfillRequest, createdBy string) (*CalculatedBackfill, error) {
	if unavailable := a.BackfillAvailability(); unavailable != nil {
		return nil, unavailable
	}
	rt := a.Backfill
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, errors.New("no tenant in the request context")
	}

	// 1. The request itself, before any call out.
	if strings.TrimSpace(req.ProfileToken) == "" {
		return nil, invalid("profileToken is required")
	}
	if req.Version != nil && *req.Version < 1 {
		return nil, invalid("version must be a published version number, 1 or more")
	}
	if err := rt.checkRange(req.From, req.To); err != nil {
		return nil, err
	}
	devices, err := checkDeviceTokens(req.DeviceTokens)
	if err != nil {
		return nil, err
	}

	// 2. The plan, and what the request selects out of it.
	doc, raw, err := rt.Plans.Plan(ctx, tenant, req.ProfileToken, req.Version)
	if err != nil {
		return nil, err
	}
	keys, err := selectBackfillKeys(doc, req.MetricKeys)
	if err != nil {
		return nil, err
	}
	plan, err := doc.Compile()
	if err != nil {
		return nil, invalid("the plan of %s cannot be compiled: %v", doc.VersionToken, err)
	}
	// 🔴 A key whose formula, or any formula it reads, cannot run is refused here: the job would
	// otherwise be accepted and fail at its first batch.
	if _, err := plan.Closure(keys); err != nil {
		return nil, invalid("%v", err)
	}
	warnings, err := backfillWarnings(doc, plan, keys)
	if err != nil {
		return nil, err
	}
	if len(devices) > 0 {
		if err := rt.Plans.CheckDevices(ctx, tenant, req.ProfileToken, devices); err != nil {
			return nil, err
		}
	}

	// 3. Queue it. The partial unique index is the arbiter of "one active job per tenant".
	token, err := newBackfillToken()
	if err != nil {
		return nil, err
	}
	job := &CalculatedBackfill{
		Token: token, ProfileToken: req.ProfileToken, ProfileVersionToken: doc.VersionToken,
		ProfileVersion: doc.Version, RangeFrom: req.From.UTC(), RangeTo: req.To.UTC(),
	}
	job.TenantId = tenant
	job.CreatedAt = rt.now()
	job.UpdatedAt = job.CreatedAt
	if createdBy != "" {
		job.CreatedBy = &createdBy
	}
	if job.MetricKeys, err = json.Marshal(keys); err != nil {
		return nil, err
	}
	job.Plan = raw
	if job.Warnings, err = json.Marshal(warnings); err != nil {
		return nil, err
	}
	if len(devices) > 0 {
		if job.DeviceFilter, err = json.Marshal(devices); err != nil {
			return nil, err
		}
	}
	if err := CreateBackfill(ctx, a.RDB.Database, job); err != nil {
		var active *ErrBackfillActive
		if errors.As(err, &active) {
			if active.ActiveToken == "" {
				return nil, conflict.New("the tenant already has an active calculated-metric backfill")
			}
			return nil, conflict.Errorf("the tenant already has an active calculated-metric backfill, %q; "+
				"cancel it or wait for it to finish", active.ActiveToken)
		}
		return nil, err
	}
	return job, nil
}

// checkRange refuses a range that is empty, reaches into the future, spans more than
// MaxBackfillSpan, or starts where retention may already have dropped the data. A range is
// refused, never clamped: a silently shorter fill is a fill the operator did not ask for.
func (r *BackfillRuntime) checkRange(from, to time.Time) error {
	now := r.now()
	if from.IsZero() || to.IsZero() {
		return invalid("from and to are required")
	}
	if !from.Before(to) {
		return invalid("from must be before to")
	}
	if to.After(now) {
		return invalid("to is in the future; the latest it can be is now")
	}
	if to.Sub(from) > MaxBackfillSpan {
		return invalid("the range spans %d days, and a job covers at most %d days; split it into several jobs",
			int(to.Sub(from).Hours()/24), int(MaxBackfillSpan.Hours()/24))
	}
	if r.RetentionDays > 0 {
		floor := now.Add(-time.Duration(r.RetentionDays) * 24 * time.Hour).Add(2 * r.ChunkInterval)
		if from.Before(floor) {
			return invalid("from is older than retention allows: readings are kept for %d days and the oldest "+
				"time that can be filled is %s", r.RetentionDays, floor.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

func checkDeviceTokens(in []string) ([]string, error) {
	if len(in) > MaxBackfillDevices {
		return nil, invalid("deviceTokens names %d devices, and a job takes at most %d; omit it to fill every device of the profile",
			len(in), MaxBackfillDevices)
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		if strings.TrimSpace(t) == "" {
			return nil, invalid("deviceTokens holds an empty token")
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// selectBackfillKeys resolves the keys a job fills. Omitted, they are the keys NEW in the
// version (absent from the previous published one); named, each must be a calculated metric of the
// version.
func selectBackfillKeys(doc *CalculatedPlanDoc, requested []string) ([]string, error) {
	if len(requested) == 0 {
		var keys []string
		for _, m := range doc.Calculated {
			if m.NewInVersion {
				keys = append(keys, m.Key)
			}
		}
		if len(keys) == 0 {
			return nil, invalid("no calculated metric is new in %s, so there is no default to fill; "+
				"name the metricKeys to fill", doc.VersionToken)
		}
		return keys, nil
	}
	seen := map[string]bool{}
	var keys, unknown []string
	for _, k := range requested {
		if seen[k] {
			continue
		}
		seen[k] = true
		if _, ok := doc.Metric(k); !ok {
			unknown = append(unknown, k)
			continue
		}
		keys = append(keys, k)
	}
	if len(unknown) > 0 {
		return nil, invalid("%s does not calculate: %s", doc.VersionToken, strings.Join(unknown, ", "))
	}
	return keys, nil
}

// backfillWarnings are the notes a job carries about what its fill will and will not do (D4, D9).
func backfillWarnings(doc *CalculatedPlanDoc, plan *calcfield.Plan, keys []string) ([]string, error) {
	selected := map[string]bool{}
	for _, k := range keys {
		selected[k] = true
	}
	out := []string{}
	for _, k := range keys {
		if m, _ := doc.Metric(k); m != nil && m.ChangedSinceEarlier {
			out = append(out, fmt.Sprintf("%s was calculated by an earlier version under a different formula: this fills "+
				"readings from before %s with this formula, and readings that already hold a stored value keep it", k, doc.VersionToken))
		}
	}
	for _, k := range keys {
		closure, err := plan.Closure([]string{k})
		if err != nil {
			return nil, invalid("%v", err)
		}
		var stale []string
		for _, up := range closure {
			if up == k || selected[up] {
				continue
			}
			if m, _ := doc.Metric(up); m != nil && m.ChangedSinceEarlier {
				stale = append(stale, up)
			}
		}
		if len(stale) > 0 {
			sort.Strings(stale)
			out = append(out, fmt.Sprintf("%s reads %s, which is not selected and may be stored under an earlier formula; "+
				"the fill computes %s from this version's formula for it, not from the stored values",
				k, strings.Join(stale, ", "), k))
		}
	}
	return out, nil
}

func newBackfillToken() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("minting a backfill token: %w", err)
	}
	return "bf-" + hex.EncodeToString(b), nil
}

// CancelBackfill cancels a job: a QUEUED one at once, a RUNNING one at its next commit. A job
// that does not exist in the tenant is NOT_FOUND, and one that already finished is a CONFLICT.
func (a *Api) CancelBackfill(ctx context.Context, token string) (*CalculatedBackfill, error) {
	job, err := CancelBackfill(a.RDB.Database.WithContext(ctx), token)
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, ErrBackfillLost) {
		return nil, err
	}
	cur, gerr := a.GetBackfill(ctx, token)
	if gerr != nil {
		return nil, gerr
	}
	return nil, conflict.Errorf("the backfill %q already finished (%s); there is nothing to cancel", token, cur.Status)
}

// GetBackfill reads one job of the tenant; NOT_FOUND when there is none.
func (a *Api) GetBackfill(ctx context.Context, token string) (*CalculatedBackfill, error) {
	var job CalculatedBackfill
	if err := a.RDB.Database.WithContext(ctx).Where("token = ?", token).Take(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("no calculated-metric backfill %q: %w", token, gorm.ErrRecordNotFound)
		}
		return nil, err
	}
	return &job, nil
}

// ListBackfillsRequest is the input of ListBackfills.
type ListBackfillsRequest struct {
	ProfileToken string
	Statuses     []string
	First        int
	// After is the NextCursor of the previous page, "" for the first.
	After string
}

// BackfillPage is one page of jobs, newest first.
type BackfillPage struct {
	Jobs []CalculatedBackfill
	// NextCursor is "" on the last page.
	NextCursor string
}

// ListBackfills pages the tenant's jobs, newest first. The order is the job id, which is
// assigned in creation order, so a page boundary never skips or repeats a job that is created
// while the list is walked.
func (a *Api) ListBackfills(ctx context.Context, req ListBackfillsRequest) (*BackfillPage, error) {
	first := req.First
	if first == 0 {
		first = DefaultBackfillPageSize
	}
	if first < 1 || first > MaxBackfillPageSize {
		return nil, invalid("first must be between 1 and %d", MaxBackfillPageSize)
	}
	for _, s := range req.Statuses {
		switch s {
		case BackfillQueued, BackfillRunning, BackfillSucceeded, BackfillCancelled, BackfillFailed:
		default:
			return nil, invalid("%q is not a backfill status", s)
		}
	}
	q := a.RDB.Database.WithContext(ctx).Model(&CalculatedBackfill{})
	if req.After != "" {
		id, err := strconv.ParseInt(req.After, 10, 64)
		if err != nil || id < 1 {
			return nil, invalid("after is not a cursor this list gave")
		}
		q = q.Where("id < ?", id)
	}
	if req.ProfileToken != "" {
		q = q.Where("profile_token = ?", req.ProfileToken)
	}
	if len(req.Statuses) > 0 {
		q = q.Where("status IN ?", req.Statuses)
	}
	var jobs []CalculatedBackfill
	if err := q.Order("id DESC").Limit(first + 1).Find(&jobs).Error; err != nil {
		return nil, err
	}
	page := &BackfillPage{}
	if len(jobs) > first {
		jobs = jobs[:first]
		page.NextCursor = strconv.FormatInt(jobs[first-1].Id, 10)
	}
	page.Jobs = jobs
	return page, nil
}
