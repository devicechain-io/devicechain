// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrationcodec"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"gorm.io/gorm"
)

// NameValue is one header or query parameter of a pasted webhook request.
type NameValue struct {
	Name  string
	Value string
}

// PayloadDecoderTestRequest is a decoder and one input to run it over. At most one of FrameHex,
// FrameBase64, JSON, WebhookBody and SampleId names the input; none runs only the saved vectors.
type PayloadDecoderTestRequest struct {
	ProfileToken string
	// Document is the decoder to test; nil tests the profile's saved draft.
	Document *string

	FrameHex    *string
	FrameBase64 *string
	JSON        *string
	// WebhookBody is a whole network-server or generic body, parsed by the SAME codec ingest uses.
	WebhookBody *string
	// IntegrationToken names the stored integration whose descriptor (type, identity, generic
	// settings, link metrics) parses a WebhookBody.
	IntegrationToken *string
	Headers          []NameValue
	Query            []NameValue
	MessageType      *string
	ContentType      *string

	FPort *int32
	FCnt  *int32
	// SampleId loads the input from a stored failure sample of this profile.
	SampleId *uint
}

// PayloadDecoderReading is one decoded value as it would be stored.
type PayloadDecoderReading struct {
	Key, Value string
}

// PayloadDecoderSkipped is a value the decode did not store, with the reason.
type PayloadDecoderSkipped struct {
	Key, Reason string
}

// PayloadDecoderFailure is why a test did not produce readings.
type PayloadDecoderFailure struct {
	Reason  string
	Path    string
	Message string
	// Offset is the byte offset a native refused, or nil.
	Offset *int32
}

// PayloadDecoderVectorOutcome is one saved vector's outcome.
type PayloadDecoderVectorOutcome struct {
	Name  string
	OK    bool
	Error string
	Cost  int32
}

// PayloadDecoderTestResult is what the decoder made of the input. A document that does not parse
// or compile is a Failure with no other field, not a transport error, so the panel can show it
// beside the editor.
type PayloadDecoderTestResult struct {
	OK       bool
	Ignored  bool
	Section  *string
	Readings []PayloadDecoderReading
	Skipped  []PayloadDecoderSkipped
	Failure  *PayloadDecoderFailure
	Cost     int32
	Vectors  []PayloadDecoderVectorOutcome
	// Uplinks is how many uplinks a webhook body held; the first is the one tested.
	Uplinks int32
}

func inputFailure(path, format string, args ...any) *PayloadDecoderFailure {
	return &PayloadDecoderFailure{Reason: string(payloaddecode.ReasonInputMalformed), Path: path,
		Message: fmt.Sprintf(format, args...)}
}

// TestPayloadDecoder compiles the decoder (the supplied document, or the saved draft) against the
// profile's DRAFT metrics, runs it over the input through the same code the resolver will use, and
// runs the saved vectors. Nothing is stored. The caller's authority is checked by the resolver
// (DeviceRead); this method adds the one stricter bar, on samples.
func (api *Api) TestPayloadDecoder(ctx context.Context, req *PayloadDecoderTestRequest) (*PayloadDecoderTestResult, error) {
	sources := 0
	for _, set := range []bool{req.FrameHex != nil, req.FrameBase64 != nil, req.JSON != nil, req.WebhookBody != nil, req.SampleId != nil} {
		if set {
			sources++
		}
	}
	if sources > 1 {
		return nil, &InvalidArgumentError{Msg: "name at most one input: frameHex, frameBase64, json, webhookBody or sampleId"}
	}
	if req.WebhookBody != nil && req.IntegrationToken == nil {
		return nil, &InvalidArgumentError{Msg: "webhookBody needs integrationToken: the stored integration's type and settings decide how the body is parsed"}
	}
	if req.WebhookBody == nil && (req.IntegrationToken != nil || len(req.Headers) > 0 || len(req.Query) > 0 ||
		req.MessageType != nil || req.ContentType != nil) {
		return nil, &InvalidArgumentError{Msg: "integrationToken, headers, query, messageType and contentType apply only with webhookBody"}
	}
	for _, c := range []struct {
		what string
		got  *string
		max  int
	}{
		{"frameHex", req.FrameHex, 4 * payloaddecode.MaxFrameBytes}, // room for separators
		{"frameBase64", req.FrameBase64, 2 * payloaddecode.MaxFrameBytes},
		{"json", req.JSON, payloaddecode.MaxJSONBytes},
		{"document", req.Document, payloaddecode.MaxDocumentBytes},
		{"webhookBody", req.WebhookBody, integrationcodec.MaxBodyBytes},
	} {
		if c.got != nil && len(*c.got) > c.max {
			return nil, limit.Exceeded(c.what+" bytes", len(*c.got), c.max)
		}
	}

	profile, err := api.deviceProfileByToken(ctx, req.ProfileToken)
	if err != nil {
		return nil, err
	}

	// The sample is read BEFORE anything is compiled or run: a caller who may not read one learns
	// nothing about it, not even from a compile error.
	var sample *payloaddecode.VectorInput
	if req.SampleId != nil {
		if sample, err = api.payloadDecoderSampleInput(ctx, profile, *req.SampleId); err != nil {
			return nil, err
		}
	}

	defs, err := api.MetricDefinitionsByDeviceProfile(ctx, profile.ID)
	if err != nil {
		return nil, err
	}
	var raw []byte
	switch {
	case req.Document != nil:
		raw = []byte(*req.Document)
	case profile.DecoderDeclaration != nil && len(*profile.DecoderDeclaration) > 0:
		raw = *profile.DecoderDeclaration
	default:
		return nil, &InvalidArgumentError{Msg: "the profile has no saved decoder; pass a document to test"}
	}

	result := &PayloadDecoderTestResult{Readings: []PayloadDecoderReading{}, Skipped: []PayloadDecoderSkipped{},
		Vectors: []PayloadDecoderVectorOutcome{}}
	_, plan, err := compileDecoder("", raw, defs)
	if err != nil {
		reason := payloaddecode.ReasonCompileFailed
		if errors.Is(err, payloaddecode.ErrEnvUnsupported) {
			reason = payloaddecode.ReasonEnvUnsupported
		}
		result.Failure = &PayloadDecoderFailure{Reason: string(reason), Message: err.Error()}
		return result, nil
	}

	for _, v := range plan.RunVectors() {
		result.Vectors = append(result.Vectors, PayloadDecoderVectorOutcome{Name: v.Name, OK: v.OK, Error: v.Error, Cost: clampCost(v.Cost)})
	}
	if sources == 0 {
		result.OK = true
		return result, nil
	}

	up, link, fail, err := api.testInput(ctx, req, plan.InputKind(), sample)
	if err != nil {
		return nil, err
	}
	if fail != nil {
		result.Failure = fail
		return result, nil
	}
	result.Uplinks = up.uplinks
	result.Skipped = append(result.Skipped, up.skipped...)

	res, derr := plan.Decode(up.input)
	if derr != nil {
		result.Failure = &PayloadDecoderFailure{Reason: string(derr.Reason), Path: derr.Path, Message: derr.Message}
		if derr.Offset >= 0 {
			off := int32(derr.Offset)
			result.Failure.Offset = &off
		}
		result.Cost = clampCost(derr.Cost)
		return result, nil
	}
	result.OK = true
	result.Ignored = res.Ignored
	result.Cost = clampCost(res.Cost)
	if res.Section != "" {
		s := res.Section
		result.Section = &s
	}
	for _, r := range res.Readings {
		result.Readings = append(result.Readings, PayloadDecoderReading{Key: r.Key, Value: r.Value})
	}
	for _, s := range res.Skipped {
		result.Skipped = append(result.Skipped, PayloadDecoderSkipped{Key: s.Key, Reason: s.Reason})
	}
	// Link metrics are the platform's, appended after the decode by the integration's own setting;
	// the preview shows them so it matches what is stored.
	if link != nil && !res.Ignored {
		for _, kv := range link.readings() {
			result.Readings = append(result.Readings, PayloadDecoderReading{Key: kv[0], Value: kv[1]})
		}
	}
	return result, nil
}

func clampCost(c uint64) int32 {
	if c > 1<<31-1 {
		return 1<<31 - 1
	}
	return int32(c)
}

// testUplink is the input a test resolved, with what was learned on the way.
type testUplink struct {
	input   payloaddecode.Input
	uplinks int32
	skipped []PayloadDecoderSkipped
}

// linkReadings are the receive-link metrics an integration with linkMetrics set appends.
type linkReadings struct {
	link *integrationcodec.Link
}

func (l *linkReadings) readings() [][2]string {
	out := make([][2]string, 0, 3)
	if l.link.HasSignal {
		out = append(out, [2]string{integrationcodec.KeyRSSI, calcfield.FormatDouble(l.link.RSSI)},
			[2]string{integrationcodec.KeySNR, calcfield.FormatDouble(l.link.SNR)})
	}
	return append(out, [2]string{integrationcodec.KeyGateways, strconv.Itoa(l.link.Gateways)})
}

// testInput builds the decoder input from the request. A failure to read the input is a result
// failure (the panel shows it), not an error; err is for the refusals that are the caller's
// request to fix or its authority.
func (api *Api) testInput(ctx context.Context, req *PayloadDecoderTestRequest, kind payloaddecode.InputKind,
	sample *payloaddecode.VectorInput) (*testUplink, *linkReadings, *PayloadDecoderFailure, error) {
	out := &testUplink{uplinks: 1}
	switch {
	case sample != nil:
		in, err := inputFromVector(*sample)
		if err != nil {
			return nil, nil, inputFailure("sample", "%s", err.Error()), nil
		}
		out.input = in
	case req.FrameHex != nil:
		b, err := hex.DecodeString(strings.Join(strings.Fields(*req.FrameHex), ""))
		if err != nil {
			return nil, nil, inputFailure("frameHex", "the frame is not valid hex: %s", err.Error()), nil
		}
		out.input.Frame = b
	case req.FrameBase64 != nil:
		b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(*req.FrameBase64), ""))
		if err != nil {
			return nil, nil, inputFailure("frameBase64", "the frame is not valid base64: %s", err.Error()), nil
		}
		out.input.Frame = b
	case req.JSON != nil:
		// The same bytes feed whichever JSON input the decoder declared.
		if kind == payloaddecode.InputDecoded {
			out.input.Decoded = []byte(*req.JSON)
		} else {
			out.input.JSON = []byte(*req.JSON)
		}
	case req.WebhookBody != nil:
		return api.webhookInput(ctx, req)
	}
	if req.FPort != nil || req.FCnt != nil {
		if out.input.LoRa == nil {
			out.input.LoRa = &payloaddecode.LoRa{FPort: -1, FCnt: -1}
		}
		if req.FPort != nil {
			out.input.LoRa.FPort = int64(*req.FPort)
		}
		if req.FCnt != nil {
			out.input.LoRa.FCnt = int64(*req.FCnt)
		}
	}
	return out, nil, nil, nil
}

// webhookInput parses a whole pasted body with the stored integration's descriptor, through the
// codecs ingest runs, and builds the decoder input from the first uplink exactly as ingest will.
func (api *Api) webhookInput(ctx context.Context, req *PayloadDecoderTestRequest) (*testUplink, *linkReadings, *PayloadDecoderFailure, error) {
	found, err := api.IntegrationsByToken(ctx, []string{*req.IntegrationToken})
	if err != nil {
		return nil, nil, nil, err
	}
	if len(found) == 0 {
		return nil, nil, nil, gorm.ErrRecordNotFound
	}
	integ := found[0]
	desc := &integrationcodec.Descriptor{Token: integ.Token, Type: integrationcodec.Type(integ.Type),
		Enabled: integ.Enabled, Identity: integ.Identity, LinkMetrics: integ.LinkMetrics}
	if integ.Generic != nil {
		g := string(*integ.Generic)
		desc.Generic = &g
	}
	if err := desc.Prepare(); err != nil {
		return nil, nil, inputFailure("integrationToken", "the integration cannot parse a body: %s", err.Error()), nil
	}
	hdr := http.Header{}
	for _, h := range req.Headers {
		hdr.Add(h.Name, h.Value)
	}
	qry := url.Values{}
	for _, q := range req.Query {
		qry.Add(q.Name, q.Value)
	}
	r := &integrationcodec.Request{Body: []byte(*req.WebhookBody), Header: hdr, Query: qry,
		MessageType: deref(req.MessageType), ContentType: deref(req.ContentType), ReceivedAt: time.Now().UTC()}
	res, err := integrationcodec.Decode(desc, r)
	if err != nil {
		reason := string(payloaddecode.ReasonInputMalformed)
		if errors.Is(err, integrationcodec.ErrUnsupportedEncoding) {
			reason = "unsupported_encoding"
		}
		return nil, nil, &PayloadDecoderFailure{Reason: reason, Path: "webhookBody", Message: err.Error()}, nil
	}
	if res.Ignored || len(res.Uplinks) == 0 {
		return nil, nil, &PayloadDecoderFailure{Reason: "ignored_message", Path: "webhookBody",
			Message: "the integration ignores this message type: it holds no uplink to decode"}, nil
	}
	up := res.Uplinks[0]
	out := &testUplink{uplinks: int32(len(res.Uplinks))}
	if up.FrameErr != nil {
		return nil, nil, inputFailure("frame", "the frame is not valid %s: %s", up.FrameEncoding, up.FrameErr.Error()), nil
	}
	if up.DepthErr != nil {
		return nil, nil, inputFailure("decoded", "%s", up.DepthErr.Error()), nil
	}
	if up.MetaErr != nil {
		out.skipped = append(out.skipped, PayloadDecoderSkipped{Key: "fPort/fCnt", Reason: "invalid: " + up.MetaErr.Error()})
	}
	out.input.Frame = up.Frame
	if up.Decoded != nil {
		b, err := json.Marshal(up.Decoded)
		if err != nil {
			return nil, nil, inputFailure("decoded", "%s", err.Error()), nil
		}
		// A generic item is the decoder's `json`; a network server's decoded object is `decoded`.
		if desc.Type == integrationcodec.GenericJSON {
			out.input.JSON = b
		} else {
			out.input.Decoded = b
		}
	}
	if up.FPort != integrationcodec.NoLoRaWAN || up.FCnt != integrationcodec.NoLoRaWAN {
		out.input.LoRa = &payloaddecode.LoRa{FPort: int64(up.FPort), FCnt: up.FCnt}
	}
	var link *linkReadings
	if up.Link != nil {
		out.input.Link = map[string]float64{"gateways": float64(up.Link.Gateways)}
		if up.Link.HasSignal {
			out.input.Link["rssi"], out.input.Link["snr"] = up.Link.RSSI, up.Link.SNR
		}
		link = &linkReadings{link: up.Link}
	}
	return out, link, nil, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// inputFromVector builds the uplink a VectorInput describes: the form a stored sample and a saved
// vector share.
func inputFromVector(v payloaddecode.VectorInput) (payloaddecode.Input, error) {
	var in payloaddecode.Input
	if v.FrameHex != "" && v.FrameBase64 != "" {
		return in, errors.New("the sample names both frameHex and frameBase64")
	}
	switch {
	case v.FrameHex != "":
		b, err := hex.DecodeString(v.FrameHex)
		if err != nil {
			return in, errors.New("the sample's frameHex is not valid hex")
		}
		in.Frame = b
	case v.FrameBase64 != "":
		b, err := base64.StdEncoding.DecodeString(v.FrameBase64)
		if err != nil {
			return in, errors.New("the sample's frameBase64 is not valid base64")
		}
		in.Frame = b
	}
	if len(v.JSON) > 0 {
		in.JSON = []byte(v.JSON)
	}
	if len(v.Decoded) > 0 {
		in.Decoded = []byte(v.Decoded)
	}
	if v.FPort != nil || v.FCnt != nil {
		in.LoRa = &payloaddecode.LoRa{FPort: -1, FCnt: -1}
		if v.FPort != nil {
			in.LoRa.FPort = int64(*v.FPort)
		}
		if v.FCnt != nil {
			in.LoRa.FCnt = int64(*v.FCnt)
		}
	}
	in.Link = v.Link
	return in, nil
}

// payloadDecoderSampleInput loads one stored failure sample of the profile.
//
// 🔴 THIS IS THE SAMPLE BOUNDARY. Running a decoder over a sample reveals its bytes
// (`double(u8(frame, i))` reads one a call), so evaluating a sample is reading it, and it is held
// to the bar of reading it directly: DeviceWrite, the tenant-admin bar the failed-events API
// already uses, which also refuses a caller limited to part of the tenant. DeviceRead is NOT
// enough. The check is here, at the one place a sample's bytes are read, and not only in the
// resolver, so a second caller of this method cannot forget it.
//
// The lookup is also bound to the caller's tenant and to THIS profile, on top of the tenant
// predicate the database layer adds to every tenant-scoped query: a sample id belonging to another
// tenant, or to another profile of this one, is "not found" and indistinguishable from an id that
// never existed.
func (api *Api) payloadDecoderSampleInput(ctx context.Context, profile *DeviceProfile, id uint) (*payloaddecode.VectorInput, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, errors.New("no tenant in the request context")
	}
	var s ProfileDecoderSample
	err := api.RDB.DB(ctx).
		Where("id = ? AND tenant_id = ? AND device_profile_id = ?", id, tenant, profile.ID).
		First(&s).Error
	if err != nil {
		return nil, err
	}
	var v payloaddecode.VectorInput
	if err := json.Unmarshal(s.Input, &v); err != nil {
		return nil, errors.New("the stored sample cannot be read")
	}
	return &v, nil
}
