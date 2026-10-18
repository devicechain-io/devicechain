// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/integrity"
)

// CalculatedInputInput is one input of a draft formula (the CalculatedInput input object).
type CalculatedInputInput struct {
	Key      string
	DataType string
}

// CalculatedConstantInput is one device constant a draft formula reads (the CalculatedConstant
// input object).
type CalculatedConstantInput struct {
	Key   string
	Value string
}

// CalculatedEvents evaluates a draft calculated-metric formula over one device's stored
// readings (ADR-082, technical spec §4a) — the console formula editor's history panel. It is a
// read under the caller's own event:read, through the same scoped measurement read as
// measurementEvents, and it writes nothing.
func (r *SchemaResolver) CalculatedEvents(ctx context.Context, args struct {
	DeviceToken string
	Expression  string
	DataType    string
	Inputs      []CalculatedInputInput
	MinValue    *float64
	MaxValue    *float64
	Constants   *[]CalculatedConstantInput
	From        string
	To          string
}) (*CalculatedEventsResultResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.EventRead); err != nil {
		return nil, err
	}
	from, err := time.Parse(time.RFC3339, args.From)
	if err != nil {
		return nil, integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("from is not an RFC 3339 timestamp: %v", err))
	}
	to, err := time.Parse(time.RFC3339, args.To)
	if err != nil {
		return nil, integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("to is not an RFC 3339 timestamp: %v", err))
	}
	inputs := make([]model.CalculatedInput, 0, len(args.Inputs))
	for _, in := range args.Inputs {
		inputs = append(inputs, model.CalculatedInput{Key: in.Key, DataType: in.DataType})
	}
	var constants []model.CalculatedConstant
	if args.Constants != nil {
		for _, c := range *args.Constants {
			constants = append(constants, model.CalculatedConstant{Key: c.Key, Value: c.Value})
		}
	}
	res, err := r.GetApi(ctx).CalculatedEvents(ctx, model.CalculatedEventsRequest{
		DeviceToken: args.DeviceToken, Expression: args.Expression, DataType: args.DataType,
		Inputs: inputs, MinValue: args.MinValue, MaxValue: args.MaxValue, Constants: constants, From: from, To: to,
	})
	if err != nil {
		return nil, err
	}
	return &CalculatedEventsResultResolver{M: res}, nil
}

// CalculatedEventsResultResolver resolves CalculatedEventsResult.
type CalculatedEventsResultResolver struct{ M *model.CalculatedEventsResult }

func (r *CalculatedEventsResultResolver) Samples() []*CalculatedSampleResolver {
	out := make([]*CalculatedSampleResolver, len(r.M.Samples))
	for i := range r.M.Samples {
		out[i] = &CalculatedSampleResolver{M: &r.M.Samples[i]}
	}
	return out
}

func (r *CalculatedEventsResultResolver) Computed() int32 { return int32(r.M.Computed) }

func (r *CalculatedEventsResultResolver) Skipped() []*CalculatedSkipCountResolver {
	out := make([]*CalculatedSkipCountResolver, len(r.M.Skipped))
	for i := range r.M.Skipped {
		out[i] = &CalculatedSkipCountResolver{M: r.M.Skipped[i]}
	}
	return out
}

// InputDecimalPlaces is how many decimal places a stored input keeps.
func (r *CalculatedEventsResultResolver) InputDecimalPlaces() int32 {
	return calcfield.StoredDecimals
}

// CalculatedSampleResolver resolves CalculatedSample.
type CalculatedSampleResolver struct{ M *model.CalculatedSample }

func (r *CalculatedSampleResolver) OccurredTime() string {
	return r.M.OccurredTime.UTC().Format(time.RFC3339Nano)
}
func (r *CalculatedSampleResolver) EventId() string { return hex.EncodeToString(r.M.EventId) }
func (r *CalculatedSampleResolver) Inputs() []*CalculatedInputValueResolver {
	out := make([]*CalculatedInputValueResolver, len(r.M.Inputs))
	for i := range r.M.Inputs {
		out[i] = &CalculatedInputValueResolver{M: r.M.Inputs[i]}
	}
	return out
}
func (r *CalculatedSampleResolver) Value() *string         { return r.M.Value }
func (r *CalculatedSampleResolver) SkippedReason() *string { return r.M.SkippedReason }

// CalculatedInputValueResolver resolves CalculatedInputValue.
type CalculatedInputValueResolver struct{ M model.CalculatedInputValue }

func (r *CalculatedInputValueResolver) Key() string   { return r.M.Key }
func (r *CalculatedInputValueResolver) Value() string { return r.M.Value }

// CalculatedSkipCountResolver resolves CalculatedSkipCount.
type CalculatedSkipCountResolver struct{ M model.CalculatedSkipCount }

func (r *CalculatedSkipCountResolver) Reason() string { return r.M.Reason }
func (r *CalculatedSkipCountResolver) Count() int32   { return int32(r.M.Count) }
