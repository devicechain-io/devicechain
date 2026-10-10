// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"errors"
	"regexp"
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

// ErrUnimplemented is returned by every stub body.
var ErrUnimplemented = errors.New("ota: not implemented")

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
	return Report{}, ErrUnimplemented
}

// Validate checks every field against the wire grammar. It is strict in both directions: a field
// that must be present is required, and a field that does not belong to the report's kind or stage
// is refused rather than ignored.
func (r Report) Validate() error {
	return ErrUnimplemented
}
