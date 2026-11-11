// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package review compares isolated evaluations over a single recorded-input list.
// It has no production sink, writer, consumer acknowledgement or service client.
package review

import (
	"context"
	"encoding/json"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/replayinput"
	"github.com/devicechain-io/dc-microservice/ruletest"
)

const (
	MaxDevices         = 50
	MaxWindow          = 24 * time.Hour
	MaxLeadIn          = 6 * time.Hour
	MaxExpectations    = 10
	MaxListedChanges   = 2000
	DefaultShiftWindow = 15 * time.Minute
	LeadInMargin       = 5 * time.Minute
)

type Role string

const (
	Incident Role = "INCIDENT"
	Normal   Role = "NORMAL"
)

type RuleEnvelope struct {
	Token              string          `json:"token"`
	Enabled            bool            `json:"enabled"`
	Definition         json.RawMessage `json:"definition"`
	EntityGroupToken   string          `json:"entityGroupToken"`
	EntityGroupVersion int             `json:"entityGroupVersion"`
}
type Window struct {
	Role  Role      `json:"role"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}
type Bounds struct {
	Role     Role   `json:"role"`
	Stream   string `json:"stream"`
	FirstSeq uint64 `json:"firstSeq"`
	LastSeq  uint64 `json:"lastSeq"`
}
type Expectation struct {
	Window Role            `json:"window"`
	Expect ruletest.Expect `json:"expect"`
}
type VersionRules func(context.Context, string) (*RuleEnvelope, bool, error)
type Params struct {
	Tenant, ProfileToken string
	Base, Draft          *RuleEnvelope
	Devices              []string
	Windows              []Window
	Expectations         []Expectation
	ShiftWindow          time.Duration
	StreamBounds         []Bounds
	ConstantMetrics      []string
	Lateness             time.Duration
	AppliedGuard         bool
	Source               replayinput.Opener
	Suffix               string
	MaxScan, MaxRead     int
	WindowBudget         time.Duration
	VersionRules         VersionRules
	Now                  time.Time
}
type InputError struct {
	Msg string
	// RuleRejected distinguishes a definition/compile refusal from malformed request input.
	RuleRejected bool
}

func (e *InputError) Error() string { return e.Msg }

type Fidelity struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
	Label   string   `json:"label"`
}
type State string

const (
	Unknown State = "UNKNOWN"
	Clear   State = "CLEAR"
	Raised  State = "RAISED"
)

type DeviceCoverage struct {
	Device         string              `json:"device"`
	Readings       map[string]int      `json:"readings"`
	MetricTypes    map[string][]string `json:"metricTypes"`
	LeadInReadings map[string]int      `json:"leadInReadings"`
	First          time.Time           `json:"first"`
	Last           time.Time           `json:"last"`
	Versions       map[string]int      `json:"versions"`
	LateBase       int                 `json:"lateBase"`
	LateDraft      int                 `json:"lateDraft"`
	StateBase      State               `json:"stateBase"`
	StateDraft     State               `json:"stateDraft"`
	Divergence     *time.Time          `json:"divergence"`
}
type ReadingRef struct {
	Device   string        `json:"device"`
	At       time.Time     `json:"at"`
	Value    float64       `json:"value"`
	HasValue bool          `json:"hasValue"`
	Span     time.Duration `json:"span"`
}
type ChangeKind string

const (
	Added          ChangeKind = "ADDED"
	Removed        ChangeKind = "REMOVED"
	Shifted        ChangeKind = "SHIFTED"
	ContentChanged ChangeKind = "CONTENT_CHANGED"
)

type Content struct {
	Severity       string        `json:"severity,omitempty"`
	Payload        string        `json:"payload,omitempty"`
	RelativeExpiry time.Duration `json:"relativeExpiry,omitempty"`
	ExpiryLabel    string        `json:"expiryLabel,omitempty"`
	BodyDigest     string        `json:"bodyDigest,omitempty"`
	Method         string        `json:"method,omitempty"`
}
type Change struct {
	Kind            ChangeKind    `json:"kind"`
	Device          string        `json:"device"`
	Subject         string        `json:"subject"`
	Discriminator   string        `json:"discriminator"`
	Base            *time.Time    `json:"base"`
	Draft           *time.Time    `json:"draft"`
	Shift           time.Duration `json:"shift"`
	BaseContent     *Content      `json:"baseContent"`
	DraftContent    *Content      `json:"draftContent"`
	ContentChanged  bool          `json:"contentChanged"`
	Reading         ReadingRef    `json:"reading"`
	AfterDivergence bool          `json:"afterDivergence"`
}
type Counts struct {
	Unchanged      int `json:"unchanged"`
	Added          int `json:"added"`
	Removed        int `json:"removed"`
	Shifted        int `json:"shifted"`
	ContentChanged int `json:"contentChanged"`
}
type WindowResult struct {
	Role               Role              `json:"role"`
	Start              time.Time         `json:"start"`
	End                time.Time         `json:"end"`
	LeadInStart        time.Time         `json:"leadInStart"`
	Bounds             Bounds            `json:"bounds"`
	InputDigest        string            `json:"inputDigest"`
	Degraded           []string          `json:"degraded"`
	Devices            []DeviceCoverage  `json:"devices"`
	BaseEdges          []evaluate.Edge   `json:"baseEdges"`
	DraftEdges         []evaluate.Edge   `json:"draftEdges"`
	BaseIntents        []evaluate.Intent `json:"baseIntents"`
	DraftIntents       []evaluate.Intent `json:"draftIntents"`
	LeadInBaseEdges    []evaluate.Edge   `json:"leadInBaseEdges"`
	LeadInDraftEdges   []evaluate.Edge   `json:"leadInDraftEdges"`
	LeadInBaseIntents  []evaluate.Intent `json:"leadInBaseIntents"`
	LeadInDraftIntents []evaluate.Intent `json:"leadInDraftIntents"`
	Changes            []Change          `json:"changes"`
	Counts             Counts            `json:"counts"`
	ChangesTruncated   bool              `json:"changesTruncated"`
}
type Verdict string

const (
	Pass         Verdict = "PASS"
	Fail         Verdict = "FAIL"
	Inconclusive Verdict = "INCONCLUSIVE"
)

type ExpectationResult struct {
	Index          int          `json:"index"`
	Name           string       `json:"name"`
	Window         Role         `json:"window"`
	Verdict        Verdict      `json:"verdict"`
	Baseline       Verdict      `json:"baseline"`
	Reason         string       `json:"reason"`
	BaselineReason string       `json:"baselineReason"`
	Evidence       []ReadingRef `json:"evidence"`
}
type Result struct {
	Fidelity     Fidelity            `json:"fidelity"`
	Windows      []WindowResult      `json:"windows"`
	Expectations []ExpectationResult `json:"expectations"`
	Outcome      string              `json:"outcome"`
	Incomplete   []string            `json:"incomplete"`
	InputDigest  string              `json:"inputDigest"`
}
