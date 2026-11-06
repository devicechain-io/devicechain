// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"strconv"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
	gql "github.com/graph-gophers/graphql-go"
)

// decoderTestLimiter bounds testPayloadDecoder per tenant: 10 a second, burst 20. The door
// compiles a document on every call, so it is the one read here a caller can use to spend CPU.
// A variable so a test can hold it to a small bucket.
var decoderTestLimiter = core.NewTenantRateLimiter(core.StaticCeiling(10, 20))

// HeaderInput is one header of a pasted webhook request.
type HeaderInput struct {
	Name  string
	Value string
}

// QueryParamInput is one query parameter of a pasted webhook request.
type QueryParamInput struct {
	Name  string
	Value string
}

// PayloadDecoderTestInput is the argument of testPayloadDecoder.
type PayloadDecoderTestInput struct {
	ProfileToken     string
	Document         *string
	FrameHex         *string
	FrameBase64      *string
	Json             *string
	WebhookBody      *string
	IntegrationToken *string
	Headers          *[]HeaderInput
	Query            *[]QueryParamInput
	MessageType      *string
	ContentType      *string
	FPort            *int32
	FCnt             *int32
	SampleId         *gql.ID
}

// TestPayloadDecoder runs a decoder over one input. An authoring tool, so a caller limited to part
// of the tenant is refused (Authorize, not AuthorizeCatalogue).
//
// 🔴 A SAMPLE IS READ ONLY BY DeviceWrite. Evaluating a decoder over a sample reveals its bytes, so
// sampleId needs the bar that reading them directly needs; DeviceRead alone is refused. The model
// enforces the same bar where the sample is read, so this check is the early refusal and not the
// only one.
func (r *SchemaResolver) TestPayloadDecoder(ctx context.Context, args struct {
	Input PayloadDecoderTestInput
}) (*PayloadDecoderTestResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	in := args.Input
	if in.SampleId != nil {
		if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
			return nil, err
		}
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, errors.New("no tenant in the request context")
	}
	if !decoderTestLimiter.Allow(tenant) {
		return nil, model.ErrRateLimited
	}

	req := &model.PayloadDecoderTestRequest{
		ProfileToken: in.ProfileToken, Document: in.Document, FrameHex: in.FrameHex, FrameBase64: in.FrameBase64,
		JSON: in.Json, WebhookBody: in.WebhookBody, IntegrationToken: in.IntegrationToken,
		MessageType: in.MessageType, ContentType: in.ContentType, FPort: in.FPort, FCnt: in.FCnt,
	}
	if in.Headers != nil {
		for _, h := range *in.Headers {
			req.Headers = append(req.Headers, model.NameValue{Name: h.Name, Value: h.Value})
		}
	}
	if in.Query != nil {
		for _, q := range *in.Query {
			req.Query = append(req.Query, model.NameValue{Name: q.Name, Value: q.Value})
		}
	}
	if in.SampleId != nil {
		id, err := strconv.ParseUint(string(*in.SampleId), 10, strconv.IntSize)
		if err != nil {
			return nil, &model.InvalidArgumentError{Msg: "sampleId is not a sample id"}
		}
		uid := uint(id)
		req.SampleId = &uid
	}
	res, err := r.GetApi(ctx).TestPayloadDecoder(ctx, req)
	if err != nil {
		return nil, err
	}
	return &PayloadDecoderTestResultResolver{M: *res}, nil
}

// PayloadDecoderFunctions serves the decoder function library: the table that drives the editor's
// help and the generated reference.
func (r *SchemaResolver) PayloadDecoderFunctions(ctx context.Context) ([]*PayloadDecoderFunctionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	fns := payloaddecode.Functions()
	out := make([]*PayloadDecoderFunctionResolver, 0, len(fns))
	for _, f := range fns {
		out = append(out, &PayloadDecoderFunctionResolver{M: f})
	}
	return out, nil
}

// PayloadDecoderHealth serves a profile's decode tally. DeviceWrite, enforced again by the model.
func (r *SchemaResolver) PayloadDecoderHealth(ctx context.Context, args struct {
	ProfileToken string
	Minutes      *int32
}) (*PayloadDecoderHealthResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	minutes := 0
	if args.Minutes != nil {
		minutes = int(*args.Minutes)
	}
	h, err := r.GetApi(ctx).PayloadDecoderHealth(ctx, args.ProfileToken, minutes)
	if err != nil {
		return nil, err
	}
	return &PayloadDecoderHealthResolver{M: *h}, nil
}

// PayloadDecoderSamples lists a profile's stored failing uplinks, without their bytes. DeviceWrite,
// enforced again by the model.
func (r *SchemaResolver) PayloadDecoderSamples(ctx context.Context, args struct {
	ProfileToken string
	Limit        *int32
}) ([]*PayloadDecoderSampleResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	limit := 0
	if args.Limit != nil {
		limit = int(*args.Limit)
	}
	rows, err := r.GetApi(ctx).PayloadDecoderSamples(ctx, args.ProfileToken, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*PayloadDecoderSampleResolver, 0, len(rows))
	for _, s := range rows {
		out = append(out, &PayloadDecoderSampleResolver{M: s})
	}
	return out, nil
}
