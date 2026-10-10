// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/core"
)

const (
	// ReportVersion is the only envelope version this decoder accepts.
	ReportVersion = 1

	// KindProgress and KindInventory are the two device-report kinds this package decodes.
	KindProgress  = "update.progress"
	KindInventory = "update.inventory"

	// MaxReportBytes caps an encoded report; a larger body is rejected before parsing.
	MaxReportBytes = 4096

	maxComponentLen = 64
	maxIDLen        = 128
	maxVersionLen   = 64
	maxBootIDLen    = 128
	maxCodeLen      = 64
	maxDetailBytes  = 512
)

var (
	componentGrammar = regexp.MustCompile(`^[a-z0-9._/-]+$`)
	digestGrammar    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	codeGrammar      = regexp.MustCompile(`^[A-Z0-9_]+$`)
)

// Progress is download progress in bytes. Total 0 means the size is not known.
type Progress struct {
	Bytes int64 `json:"bytes"`
	Total int64 `json:"total"`
}

// Result is the device's failure code and human-readable detail.
type Result struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Running is what the device says it is running now.
type Running struct {
	Version string `json:"version"`
	Digest  string `json:"digest,omitempty"`
}

// Report is one device-originated message of kind update.progress or update.inventory. The device's
// identity is NOT a field: it comes from the broker-confined subject the report arrived on.
type Report struct {
	V              int         `json:"v"`
	Kind           string      `json:"kind"`
	AttemptID      string      `json:"attemptId,omitempty"`
	AssignmentID   string      `json:"assignmentId,omitempty"`
	Component      string      `json:"component"`
	ArtifactDigest string      `json:"artifactDigest,omitempty"`
	Seq            uint64      `json:"seq,omitempty"`
	Stage          ReportStage `json:"stage,omitempty"`
	Progress       *Progress   `json:"progress,omitempty"`
	Result         *Result     `json:"result,omitempty"`
	Running        *Running    `json:"running,omitempty"`
	BootID         string      `json:"bootId,omitempty"`
}

// DecodeReport parses and validates one report. It enforces the size cap first, rejects unknown
// fields and trailing data, and then runs Validate. A body shaped like anything else (a command
// response, say) fails here and can never reach the reducer.
func DecodeReport(b []byte) (Report, error) {
	if len(b) > MaxReportBytes {
		return Report{}, fmt.Errorf("ota: report is %d bytes, exceeds the %d byte limit", len(b), MaxReportBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var r Report
	if err := dec.Decode(&r); err != nil {
		return Report{}, fmt.Errorf("ota: decode report: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return Report{}, fmt.Errorf("ota: decode report: trailing data after the report")
	}
	if err := r.Validate(); err != nil {
		return Report{}, err
	}
	return r, nil
}

// Validate checks every field against the wire grammar. It is strict in both directions: a field
// that must be present is required, and a field that does not belong to the report's kind or stage
// is refused rather than ignored.
func (r Report) Validate() error {
	if r.V != ReportVersion {
		return fmt.Errorf("ota: unsupported report version %d (want %d)", r.V, ReportVersion)
	}
	switch r.Kind {
	case KindProgress, KindInventory:
	default:
		return fmt.Errorf("ota: unknown report kind %q", r.Kind)
	}
	if err := checkLen("component", r.Component, maxComponentLen); err != nil {
		return err
	}
	if !componentGrammar.MatchString(r.Component) {
		return fmt.Errorf("ota: component %q must match %s", r.Component, componentGrammar)
	}
	if err := checkBootID(r.BootID); err != nil {
		return err
	}
	if r.Kind == KindInventory {
		return r.validateInventory()
	}
	return r.validateProgress()
}

func (r Report) validateProgress() error {
	for _, f := range []struct{ name, v string }{{"attemptId", r.AttemptID}, {"assignmentId", r.AssignmentID}} {
		if err := checkLen(f.name, f.v, maxIDLen); err != nil {
			return err
		}
		if err := core.ValidateToken(f.v); err != nil {
			return fmt.Errorf("ota: %s: %w", f.name, err)
		}
	}
	if err := checkDigest("artifactDigest", r.ArtifactDigest, true); err != nil {
		return err
	}
	if r.Seq < 1 {
		return fmt.Errorf("ota: seq must be at least 1 on an update.progress report")
	}
	if r.Stage == "" {
		return fmt.Errorf("ota: stage is required on an update.progress report")
	}
	if !r.Stage.Valid() {
		return fmt.Errorf("ota: unknown stage %q", r.Stage)
	}
	if r.Progress != nil {
		p := r.Progress
		if p.Bytes < 0 || p.Total < 0 {
			return fmt.Errorf("ota: progress bytes and total must not be negative")
		}
		if p.Total > 0 && p.Bytes > p.Total {
			return fmt.Errorf("ota: progress bytes %d exceeds total %d", p.Bytes, p.Total)
		}
	}
	if r.Stage == StageFailed {
		if r.Result == nil {
			return fmt.Errorf("ota: result is required when stage is FAILED")
		}
		if err := checkLen("result.code", r.Result.Code, maxCodeLen); err != nil {
			return err
		}
		if !codeGrammar.MatchString(r.Result.Code) {
			return fmt.Errorf("ota: result.code %q must match %s", r.Result.Code, codeGrammar)
		}
		if len(r.Result.Detail) > maxDetailBytes {
			return fmt.Errorf("ota: result.detail is %d bytes, exceeds the %d byte limit", len(r.Result.Detail), maxDetailBytes)
		}
		if !utf8.ValidString(r.Result.Detail) {
			return fmt.Errorf("ota: result.detail is not valid UTF-8")
		}
	} else if r.Result != nil {
		return fmt.Errorf("ota: result is only allowed when stage is FAILED")
	}
	if r.Stage == StageRunning {
		return checkRunning(r.Running)
	}
	if r.Running != nil {
		return fmt.Errorf("ota: running is only allowed when stage is RUNNING")
	}
	return nil
}

func (r Report) validateInventory() error {
	switch {
	case r.AttemptID != "" || r.AssignmentID != "":
		return fmt.Errorf("ota: attemptId and assignmentId are not allowed on an update.inventory report")
	case r.Seq != 0:
		return fmt.Errorf("ota: seq is not allowed on an update.inventory report")
	case r.Stage != "":
		return fmt.Errorf("ota: stage is not allowed on an update.inventory report")
	case r.ArtifactDigest != "":
		return fmt.Errorf("ota: artifactDigest is not allowed on an update.inventory report")
	case r.Progress != nil:
		return fmt.Errorf("ota: progress is not allowed on an update.inventory report")
	case r.Result != nil:
		return fmt.Errorf("ota: result is not allowed on an update.inventory report")
	}
	return checkRunning(r.Running)
}

func checkRunning(run *Running) error {
	if run == nil {
		return fmt.Errorf("ota: running is required for this report")
	}
	if err := checkLen("running.version", run.Version, maxVersionLen); err != nil {
		return err
	}
	return checkDigest("running.digest", run.Digest, false)
}

func checkBootID(id string) error {
	if err := checkLen("bootId", id, maxBootIDLen); err != nil {
		return err
	}
	if !utf8.ValidString(id) {
		return fmt.Errorf("ota: bootId is not valid UTF-8")
	}
	for _, c := range id {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("ota: bootId contains a control character")
		}
	}
	return nil
}

func checkLen(name, v string, max int) error {
	if len(v) < 1 || len(v) > max {
		return fmt.Errorf("ota: %s must be 1-%d bytes, got %d", name, max, len(v))
	}
	return nil
}

func checkDigest(name, v string, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("ota: %s is required", name)
		}
		return nil
	}
	if !digestGrammar.MatchString(v) {
		return fmt.Errorf("ota: %s must be sha256: followed by 64 lowercase hex characters", name)
	}
	return nil
}
