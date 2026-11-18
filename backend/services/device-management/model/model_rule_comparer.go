// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"
)

// RuleComparer is the recorded-input comparison boundary owned by event-processing.
type RuleComparer interface {
	CompareRuleVersions(context.Context, CompareRequest) (*CompareResponse, error)
}

type RuleEnvelope struct {
	Token              string  `json:"token"`
	Enabled            bool    `json:"enabled"`
	Definition         string  `json:"definition"`
	EntityGroupToken   *string `json:"entityGroupToken"`
	EntityGroupVersion *int32  `json:"entityGroupVersion"`
}

type ReviewWindow struct {
	Role  string    `json:"role"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type ReviewExpectation struct {
	Window string `json:"window"`
	Name   string `json:"name"`
	Expect string `json:"expect"`
}

type CompareRequest struct {
	ProfileToken       string              `json:"profileToken"`
	Base               *RuleEnvelope       `json:"base"`
	Draft              *RuleEnvelope       `json:"draft"`
	DeviceTokens       []string            `json:"deviceTokens"`
	Windows            []ReviewWindow      `json:"windows"`
	Expectations       []ReviewExpectation `json:"expectations"`
	ShiftWindowSeconds *int32              `json:"shiftWindowSeconds"`
	ConstantMetrics    []string            `json:"constantMetrics"`
}

type ReviewFidelity struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
	Label   string   `json:"label"`
}

type ReviewExpectationResult struct {
	Index          int    `json:"index"`
	Name           string `json:"name"`
	Window         string `json:"window"`
	Verdict        string `json:"verdict"`
	Baseline       string `json:"baseline"`
	Reason         string `json:"reason"`
	BaselineReason string `json:"baselineReason"`
}

type ReviewChangeCounts struct {
	Added          int `json:"added"`
	Removed        int `json:"removed"`
	Unchanged      int `json:"unchanged"`
	Shifted        int `json:"shifted"`
	ContentChanged int `json:"contentChanged"`
}

// Sequence bounds remain decimal strings, as served by EP, without uint64 narrowing.
type ReviewStreamBounds struct {
	Role     string `json:"role"`
	Stream   string `json:"stream"`
	FirstSeq string `json:"firstSeq"`
	LastSeq  string `json:"lastSeq"`
}

type ReviewWindowResult struct {
	Role             string             `json:"role"`
	Start            time.Time          `json:"start"`
	End              time.Time          `json:"end"`
	LeadInStart      time.Time          `json:"leadInStart"`
	Stream           string             `json:"stream"`
	FirstSeq         string             `json:"firstSeq"`
	LastSeq          string             `json:"lastSeq"`
	InputDigest      string             `json:"inputDigest"`
	Degraded         []string           `json:"degraded"`
	Counts           ReviewChangeCounts `json:"counts"`
	ChangesTruncated bool               `json:"changesTruncated"`
}

type CompareResponse struct {
	Ok             bool                      `json:"ok"`
	RefusalReason  string                    `json:"refusalReason"`
	RefusalMessage string                    `json:"refusalMessage"`
	Outcome        string                    `json:"outcome"`
	Fidelity       ReviewFidelity            `json:"fidelity"`
	Expectations   []ReviewExpectationResult `json:"expectations"`
	Windows        []ReviewWindowResult      `json:"windows"`
	InputDigest    string                    `json:"inputDigest"`
	EngineBuild    string                    `json:"engineBuild"`
	Result         string                    `json:"result"`
	ResultDigest   string                    `json:"resultDigest"`
}
