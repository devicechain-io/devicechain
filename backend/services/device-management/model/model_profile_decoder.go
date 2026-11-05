// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// ProfileDecoderHealth is one replica's tally of decodes for one version of a profile over one
// minute. Its values are ABSOLUTE for (replica, window), never increments: the flusher writes the
// replica's cumulative count with SET, so a flush whose commit succeeded but whose response was
// lost re-applies to the same value. Readers sum across replicas.
//
// The table is created by the profile decoders migration, which declares its own snapshot of this
// shape. This type is the live model; the writer and the reader of the rows arrive with the
// accounting slice.
type ProfileDecoderHealth struct {
	ID uint `gorm:"primaryKey"`
	rdb.TenantScoped

	DeviceProfileId uint      `gorm:"not null"`
	ProfileVersion  int32     `gorm:"not null"`
	WindowStart     time.Time `gorm:"not null"`
	Replica         string    `gorm:"not null;size:128"`

	Decoded    int64 `gorm:"not null;default:0"`
	Ignored    int64 `gorm:"not null;default:0"`
	Failed     int64 `gorm:"not null;default:0"`
	Suppressed int64 `gorm:"not null;default:0"`
	ChargeSkew int64 `gorm:"not null;default:0"`

	LastFailureAt *time.Time
	LastError     string `gorm:"size:512"`
}

// ProfileDecoderSample is one stored failing uplink of a profile, kept so the decoder test panel
// can replay it. Input is the canonical JSON of a payloaddecode.VectorInput (the shape a vector
// uses, so a sample becomes a vector unchanged).
//
// 🔴 A SAMPLE IS TENANT-ADMIN DATA. Running a decoder over a sample reveals its bytes
// (`double(u8(frame, i))`), so evaluating one is reading it: both paths to a sample's bytes require
// DeviceWrite from a caller of the owning tenant (see payloadDecoderSampleInput).
type ProfileDecoderSample struct {
	ID uint `gorm:"primaryKey"`
	rdb.TenantScoped

	DeviceProfileId uint      `gorm:"not null"`
	ProfileVersion  int32     `gorm:"not null"`
	CreatedAt       time.Time `gorm:"not null"`
	DeviceToken     string    `gorm:"not null;size:128"`
	Reason          string    `gorm:"not null;size:32"`
	Error           string    `gorm:"size:512"`
	Input           []byte    `gorm:"type:bytea;not null"`
}

// Wire codes of a decoder refusal (extensions.code).
const (
	// CodeInvalidDecoder is a decoder document that does not parse, does not compile, names a
	// calculated, undeclared or reserved metric, or fails a vector at publish.
	CodeInvalidDecoder = "INVALID_DECODER"
	// CodeUnsupportedDecoderKind is a decoder kind that is a valid vocabulary member but is not
	// executable in this build (WASM). It is refused loudly, never skipped.
	CodeUnsupportedDecoderKind = "UNSUPPORTED_DECODER_KIND"
	// CodeRateLimited is a request over the per-tenant rate of a compile-on-demand door.
	CodeRateLimited = "RATE_LIMITED"
)

// DecoderRefusalError is the typed refusal of a decoder: it carries a wire code and a message that
// names the failing expression or vector. Returned unwrapped so graphql-go serves the code.
type DecoderRefusalError struct {
	Code string
	Msg  string
	// Cause is the compile or parse error behind the refusal, for errors.Is.
	Cause error
}

func (e *DecoderRefusalError) Error() string { return e.Msg }

// Unwrap exposes the cause.
func (e *DecoderRefusalError) Unwrap() error { return e.Cause }

// Extensions gives the refusal its wire code.
func (e *DecoderRefusalError) Extensions() map[string]any { return map[string]any{"code": e.Code} }

// ErrRateLimited is the refusal of a request over a per-tenant rate.
var ErrRateLimited error = &DecoderRefusalError{Code: CodeRateLimited,
	Msg: "too many requests: the decoder test door allows 10 per second per tenant; retry shortly"}

// refuseDecoder turns a compile or parse error into the typed refusal. prefix leads the
// message ("cannot publish device profile").
func refuseDecoder(prefix string, err error) error {
	code := CodeInvalidDecoder
	if errors.Is(err, payloaddecode.ErrUnsupportedDecoderKind) {
		code = CodeUnsupportedDecoderKind
	}
	msg := err.Error()
	if prefix != "" {
		msg = prefix + ": " + msg
	}
	return &DecoderRefusalError{Code: code, Msg: msg, Cause: err}
}

// decoderMetrics is the profile's declared metrics as the decoder compiler sees them: the same
// projection (ResolvedMetric) and the same validation (ValidateMetricValue) the resolver uses.
// It implements payloaddecode.Metrics.
//
// 🔴 A CALCULATED METRIC IS NOT AN OUTPUT. The resolver drops a value a device sends under a
// calculated key (the computed value is authoritative), so a decoder writing one would be
// accepted, run, and silently lose every reading. MetricType answers false for it, which is what
// makes the compiler refuse the row at save and at publish.
type decoderMetrics struct {
	byKey map[string]ResolvedMetric
}

// newDecoderMetrics projects the definitions. A key declared twice resolves to its last
// declaration, as ProfileResolution does.
func newDecoderMetrics(defs []*MetricDefinition) decoderMetrics {
	m := decoderMetrics{byKey: make(map[string]ResolvedMetric, len(defs))}
	for _, d := range defs {
		m.byKey[d.MetricKey] = d.Resolved()
	}
	return m
}

// MetricType is the type of a declared, measured metric; a calculated, STRING or undeclared key
// reports false.
func (m decoderMetrics) MetricType(key string) (calcfield.Type, bool) {
	r, ok := m.byKey[key]
	if !ok || r.Calculated() {
		return 0, false
	}
	return calcfield.ParseType(r.DataType())
}

// ValidateMeasurement is the resolver's own validation of one stored value.
func (m decoderMetrics) ValidateMeasurement(key, value string) error {
	r, ok := m.byKey[key]
	if !ok || r.Calculated() {
		return nil
	}
	return ValidateMetricValue(&r, value)
}
