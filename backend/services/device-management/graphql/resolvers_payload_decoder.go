// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
)

// PayloadDecoderResolver resolves a stored decoder document. The kind, version and input come
// from a strict parse of the document; the computed fields (maxReadings, estimatedCost) from a
// compile that needs no metrics, so reading a profile never touches the database for them.
//
// A document that does not parse or compile is an ERROR on the field that needed it, never a zero
// value: a decoder reported as costing nothing is a decoder nobody should trust.
type PayloadDecoderResolver struct {
	raw json.RawMessage

	once sync.Once
	doc  *payloaddecode.Document
	plan *payloaddecode.Plan
	err  error
}

// newPayloadDecoderResolver is nil for no document.
func newPayloadDecoderResolver(raw json.RawMessage) *PayloadDecoderResolver {
	if len(raw) == 0 {
		return nil
	}
	return &PayloadDecoderResolver{raw: raw}
}

func (r *PayloadDecoderResolver) load() {
	r.once.Do(func() {
		if r.doc, r.err = payloaddecode.ParseDocument(r.raw); r.err != nil {
			return
		}
		// Metrics are not needed for the figures this resolver serves, and a nil Metrics skips
		// the strict-key check, which is the publish gate's job against the right metric set.
		r.plan, r.err = payloaddecode.Compile(r.doc, nil)
		if errors.Is(r.err, payloaddecode.ErrUnsupportedDecoderKind) || errors.Is(r.err, payloaddecode.ErrEnvUnsupported) {
			// The document is readable; only the figures are not.
			r.plan = nil
		}
	})
}

// head parses the document alone: kind, envVersion and input need no compile.
func (r *PayloadDecoderResolver) head() (*payloaddecode.Document, error) {
	r.load()
	if r.doc == nil {
		return nil, r.err
	}
	return r.doc, nil
}

func (r *PayloadDecoderResolver) Kind() (string, error) {
	d, err := r.head()
	if err != nil {
		return "", err
	}
	return string(d.Kind), nil
}

func (r *PayloadDecoderResolver) EnvVersion() (int32, error) {
	d, err := r.head()
	if err != nil {
		return 0, err
	}
	return int32(d.EnvVersion), nil
}

func (r *PayloadDecoderResolver) Input() (string, error) {
	d, err := r.head()
	if err != nil {
		return "", err
	}
	return string(d.Input), nil
}

func (r *PayloadDecoderResolver) Document() string { return string(r.raw) }

func (r *PayloadDecoderResolver) compiled() (*payloaddecode.Plan, error) {
	r.load()
	if r.plan == nil {
		if r.err == nil {
			return nil, errors.New("the decoder cannot be compiled")
		}
		return nil, r.err
	}
	return r.plan, nil
}

func (r *PayloadDecoderResolver) MaxReadings() (int32, error) {
	p, err := r.compiled()
	if err != nil {
		return 0, err
	}
	return int32(p.MaxReadings()), nil
}

func (r *PayloadDecoderResolver) EstimatedCost() (int32, error) {
	p, err := r.compiled()
	if err != nil {
		return 0, err
	}
	c := p.EstimatedCost()
	if c > 1<<31-1 {
		c = 1<<31 - 1
	}
	return int32(c), nil
}

// Decoder is the profile's DRAFT decoder, or null.
func (r *DeviceProfileResolver) Decoder() *PayloadDecoderResolver {
	if r.M.DecoderDeclaration == nil {
		return nil
	}
	return newPayloadDecoderResolver(json.RawMessage(*r.M.DecoderDeclaration))
}

// Decoder is the decoder frozen into this version, or null. A snapshot that cannot be decoded is
// an error, not "no decoder": a missing decoder changes how every uplink of the profile resolves.
func (r *DeviceProfileVersionResolver) Decoder() (*PayloadDecoderResolver, error) {
	snap, err := model.ParseProfileSnapshot(r.M.Snapshot)
	if err != nil {
		return nil, err
	}
	return newPayloadDecoderResolver(snap.Decoder), nil
}

// ---------------------------
// Test result resolvers
// ---------------------------

type PayloadDecoderTestResultResolver struct {
	M model.PayloadDecoderTestResult
}

func (r *PayloadDecoderTestResultResolver) Ok() bool         { return r.M.OK }
func (r *PayloadDecoderTestResultResolver) Ignored() bool    { return r.M.Ignored }
func (r *PayloadDecoderTestResultResolver) Section() *string { return r.M.Section }
func (r *PayloadDecoderTestResultResolver) Cost() int32      { return r.M.Cost }
func (r *PayloadDecoderTestResultResolver) Uplinks() int32   { return r.M.Uplinks }

func (r *PayloadDecoderTestResultResolver) Readings() []*PayloadDecoderReadingResolver {
	out := make([]*PayloadDecoderReadingResolver, 0, len(r.M.Readings))
	for _, x := range r.M.Readings {
		out = append(out, &PayloadDecoderReadingResolver{M: x})
	}
	return out
}

func (r *PayloadDecoderTestResultResolver) Skipped() []*PayloadDecoderSkipResolver {
	out := make([]*PayloadDecoderSkipResolver, 0, len(r.M.Skipped))
	for _, x := range r.M.Skipped {
		out = append(out, &PayloadDecoderSkipResolver{M: x})
	}
	return out
}

func (r *PayloadDecoderTestResultResolver) Error() *PayloadDecoderErrorResolver {
	if r.M.Failure == nil {
		return nil
	}
	return &PayloadDecoderErrorResolver{M: *r.M.Failure}
}

func (r *PayloadDecoderTestResultResolver) Vectors() []*PayloadDecoderVectorResultResolver {
	out := make([]*PayloadDecoderVectorResultResolver, 0, len(r.M.Vectors))
	for _, x := range r.M.Vectors {
		out = append(out, &PayloadDecoderVectorResultResolver{M: x})
	}
	return out
}

type PayloadDecoderReadingResolver struct{ M model.PayloadDecoderReading }

func (r *PayloadDecoderReadingResolver) Key() string   { return r.M.Key }
func (r *PayloadDecoderReadingResolver) Value() string { return r.M.Value }

type PayloadDecoderSkipResolver struct{ M model.PayloadDecoderSkipped }

func (r *PayloadDecoderSkipResolver) Key() string    { return r.M.Key }
func (r *PayloadDecoderSkipResolver) Reason() string { return r.M.Reason }

type PayloadDecoderErrorResolver struct{ M model.PayloadDecoderFailure }

func (r *PayloadDecoderErrorResolver) Reason() string  { return r.M.Reason }
func (r *PayloadDecoderErrorResolver) Path() string    { return r.M.Path }
func (r *PayloadDecoderErrorResolver) Message() string { return r.M.Message }
func (r *PayloadDecoderErrorResolver) Offset() *int32  { return r.M.Offset }

type PayloadDecoderVectorResultResolver struct {
	M model.PayloadDecoderVectorOutcome
}

func (r *PayloadDecoderVectorResultResolver) Name() string { return r.M.Name }
func (r *PayloadDecoderVectorResultResolver) Ok() bool     { return r.M.OK }
func (r *PayloadDecoderVectorResultResolver) Cost() int32  { return r.M.Cost }
func (r *PayloadDecoderVectorResultResolver) Error() *string {
	if r.M.Error == "" {
		return nil
	}
	return &r.M.Error
}

type PayloadDecoderFunctionResolver struct{ M payloaddecode.FunctionDoc }

func (r *PayloadDecoderFunctionResolver) Name() string        { return r.M.Name }
func (r *PayloadDecoderFunctionResolver) Signature() string   { return r.M.Signature }
func (r *PayloadDecoderFunctionResolver) Description() string { return r.M.Doc }
