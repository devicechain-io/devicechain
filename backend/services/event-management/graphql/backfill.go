// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/integrity"
	"gorm.io/gorm"
)

// Calculated-metric backfill (ADR-082, S8-2b). Starting and cancelling a job is device:write,
// the authority that publishes a formula and so already decides every value it stores; reading a
// job is event:read. Both go through auth.Authorize, which refuses a customer-scoped caller
// outright: a backfill is a whole-tenant operation with no scope to apply.

// CalculatedMetricBackfillInput is the CalculatedMetricBackfillInput input object.
type CalculatedMetricBackfillInput struct {
	ProfileToken string
	Version      *int32
	MetricKeys   *[]string
	From         string
	To           string
	DeviceTokens *[]string
}

// StartCalculatedMetricBackfill queues a backfill job.
func (r *SchemaResolver) StartCalculatedMetricBackfill(ctx context.Context, args struct {
	Input CalculatedMetricBackfillInput
}) (*CalculatedMetricBackfillResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	in := args.Input
	from, err := time.Parse(time.RFC3339, in.From)
	if err != nil {
		return nil, integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("from is not an RFC 3339 timestamp: %v", err))
	}
	to, err := time.Parse(time.RFC3339, in.To)
	if err != nil {
		return nil, integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("to is not an RFC 3339 timestamp: %v", err))
	}
	req := model.StartBackfillRequest{ProfileToken: in.ProfileToken, From: from, To: to}
	if in.Version != nil {
		v := int(*in.Version)
		req.Version = &v
	}
	if in.MetricKeys != nil {
		req.MetricKeys = *in.MetricKeys
	}
	if in.DeviceTokens != nil {
		req.DeviceTokens = *in.DeviceTokens
	}
	job, err := r.GetApi(ctx).StartBackfill(ctx, req, callerName(ctx))
	if err != nil {
		return nil, err
	}
	return &CalculatedMetricBackfillResolver{M: job}, nil
}

// CancelCalculatedMetricBackfill cancels a backfill job.
func (r *SchemaResolver) CancelCalculatedMetricBackfill(ctx context.Context, args struct {
	Token string
}) (*CalculatedMetricBackfillResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	job, err := r.GetApi(ctx).CancelBackfill(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &CalculatedMetricBackfillResolver{M: job}, nil
}

// CalculatedMetricBackfill reads one job; null when the tenant has none with the token.
func (r *SchemaResolver) CalculatedMetricBackfill(ctx context.Context, args struct {
	Token string
}) (*CalculatedMetricBackfillResolver, error) {
	if err := auth.Authorize(ctx, auth.EventRead); err != nil {
		return nil, err
	}
	job, err := r.GetApi(ctx).GetBackfill(ctx, args.Token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &CalculatedMetricBackfillResolver{M: job}, nil
}

// CalculatedMetricBackfills lists the tenant's jobs, newest first.
func (r *SchemaResolver) CalculatedMetricBackfills(ctx context.Context, args struct {
	ProfileToken *string
	Statuses     *[]string
	First        *int32
	After        *string
}) (*CalculatedMetricBackfillConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.EventRead); err != nil {
		return nil, err
	}
	req := model.ListBackfillsRequest{}
	if args.ProfileToken != nil {
		req.ProfileToken = *args.ProfileToken
	}
	if args.Statuses != nil {
		req.Statuses = *args.Statuses
	}
	if args.First != nil {
		req.First = int(*args.First)
		if req.First == 0 {
			// An explicit 0 is not "the default": the model reads 0 as unset.
			return nil, integrity.NewRefusal(integrity.ClassInvalid,
				fmt.Sprintf("first must be between 1 and %d", model.MaxBackfillPageSize))
		}
	}
	if args.After != nil {
		req.After = *args.After
	}
	page, err := r.GetApi(ctx).ListBackfills(ctx, req)
	if err != nil {
		return nil, err
	}
	return &CalculatedMetricBackfillConnectionResolver{M: page}, nil
}

// callerName is who a job is attributed to.
func callerName(ctx context.Context) string {
	if claims, ok := auth.ClaimsFromContext(ctx); ok {
		if claims.Email != "" {
			return claims.Email
		}
		return claims.Username
	}
	return ""
}

// CalculatedMetricBackfillConnectionResolver resolves CalculatedMetricBackfillConnection.
type CalculatedMetricBackfillConnectionResolver struct{ M *model.BackfillPage }

func (r *CalculatedMetricBackfillConnectionResolver) Nodes() []*CalculatedMetricBackfillResolver {
	out := make([]*CalculatedMetricBackfillResolver, len(r.M.Jobs))
	for i := range r.M.Jobs {
		out[i] = &CalculatedMetricBackfillResolver{M: &r.M.Jobs[i]}
	}
	return out
}

func (r *CalculatedMetricBackfillConnectionResolver) NextCursor() *string {
	if r.M.NextCursor == "" {
		return nil
	}
	return &r.M.NextCursor
}

// CalculatedMetricBackfillResolver resolves CalculatedMetricBackfill. Counters are float64: a
// GraphQL Int is 32 bits, and a long job's counters pass it.
type CalculatedMetricBackfillResolver struct{ M *model.CalculatedBackfill }

func (r *CalculatedMetricBackfillResolver) Token() string        { return r.M.Token }
func (r *CalculatedMetricBackfillResolver) Status() string       { return r.M.Status }
func (r *CalculatedMetricBackfillResolver) ProfileToken() string { return r.M.ProfileToken }
func (r *CalculatedMetricBackfillResolver) ProfileVersion() int32 {
	return int32(r.M.ProfileVersion)
}

func (r *CalculatedMetricBackfillResolver) MetricKeys() ([]string, error) {
	return decodeStrings(r.M.MetricKeys, "metric keys")
}

func (r *CalculatedMetricBackfillResolver) Warnings() ([]string, error) {
	return decodeStrings(r.M.Warnings, "warnings")
}

func (r *CalculatedMetricBackfillResolver) From() string { return stamp(r.M.RangeFrom) }
func (r *CalculatedMetricBackfillResolver) To() string   { return stamp(r.M.RangeTo) }

func (r *CalculatedMetricBackfillResolver) DevicesTotal() *float64 {
	if r.M.DevicesTotal == nil {
		return nil
	}
	v := float64(*r.M.DevicesTotal)
	return &v
}

func (r *CalculatedMetricBackfillResolver) DevicesDone() float64 { return float64(r.M.DevicesDone) }

// Progress is devices done as a fraction of the total, capped at 1: a device added to the
// profile mid-job can make the total read low.
func (r *CalculatedMetricBackfillResolver) Progress() *float64 {
	if r.M.DevicesTotal == nil {
		return nil
	}
	var p float64
	if *r.M.DevicesTotal > 0 {
		p = min(1, float64(r.M.DevicesDone)/float64(*r.M.DevicesTotal))
	} else if r.M.Status == model.BackfillSucceeded {
		p = 1
	}
	return &p
}

func (r *CalculatedMetricBackfillResolver) Samples() float64  { return float64(r.M.Samples) }
func (r *CalculatedMetricBackfillResolver) Computed() float64 { return float64(r.M.Computed) }
func (r *CalculatedMetricBackfillResolver) Written() float64  { return float64(r.M.Written) }
func (r *CalculatedMetricBackfillResolver) AlreadyPresent() float64 {
	return float64(r.M.AlreadyPresent)
}
func (r *CalculatedMetricBackfillResolver) WindowsCompressed() float64 {
	return float64(r.M.WindowsCompressed)
}
func (r *CalculatedMetricBackfillResolver) WindowsBeyondRetention() float64 {
	return float64(r.M.WindowsBeyondRetention)
}
func (r *CalculatedMetricBackfillResolver) WindowsRefreshed() float64 {
	return float64(r.M.WindowsRefreshed)
}

// Skipped lists the skip counts by reason, in reason order so the answer is stable.
func (r *CalculatedMetricBackfillResolver) Skipped() ([]*CalculatedMetricBackfillSkipCountResolver, error) {
	counts, err := r.M.SkippedCounts()
	if err != nil {
		return nil, err
	}
	reasons := make([]string, 0, len(counts))
	for reason, n := range counts {
		if n > 0 {
			reasons = append(reasons, reason)
		}
	}
	sort.Strings(reasons)
	out := make([]*CalculatedMetricBackfillSkipCountResolver, len(reasons))
	for i, reason := range reasons {
		out[i] = &CalculatedMetricBackfillSkipCountResolver{R: reason, N: counts[reason]}
	}
	return out, nil
}

func (r *CalculatedMetricBackfillResolver) StoredInputPrecision() string {
	return fmt.Sprintf("%d decimal places", calcfield.StoredDecimals)
}

func (r *CalculatedMetricBackfillResolver) Failure() *string   { return r.M.Failure }
func (r *CalculatedMetricBackfillResolver) CreatedAt() string  { return stamp(r.M.CreatedAt) }
func (r *CalculatedMetricBackfillResolver) CreatedBy() *string { return r.M.CreatedBy }
func (r *CalculatedMetricBackfillResolver) StartedAt() *string { return stampPtr(r.M.StartedAt) }
func (r *CalculatedMetricBackfillResolver) FinishedAt() *string {
	return stampPtr(r.M.FinishedAt)
}
func (r *CalculatedMetricBackfillResolver) CancelRequestedAt() *string {
	return stampPtr(r.M.CancelRequestedAt)
}

// CalculatedMetricBackfillSkipCountResolver resolves CalculatedMetricBackfillSkipCount.
type CalculatedMetricBackfillSkipCountResolver struct {
	R string
	N int64
}

func (r *CalculatedMetricBackfillSkipCountResolver) Reason() string { return r.R }

func (r *CalculatedMetricBackfillSkipCountResolver) Count() float64 { return float64(r.N) }

// decodeStrings reads a stored jsonb array of strings; an unreadable one is an error, not an
// empty list.
func decodeStrings(raw []byte, what string) ([]string, error) {
	out := []string{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decoding the %s of a backfill: %w", what, err)
	}
	return out, nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func stampPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := stamp(*t)
	return &s
}
