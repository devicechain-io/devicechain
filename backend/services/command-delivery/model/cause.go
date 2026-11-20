// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"math"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

const (
	// RejectCauseInvalid: the cause an automation supplied is malformed. REACT, the one caller
	// that supplies a cause, answers it by sending the command again without one, so a defective
	// cause never costs the actuation.
	RejectCauseInvalid RejectionCode = "CAUSE_INVALID"
)

// The cause kinds. ALARM: the rule that fired also targeted an alarm on this detection (its
// raiseAlarm action's guard passed and its key resolved); it does not assert that the alarm was
// created or applied. RULE: the rule targeted no alarm.
const (
	CauseKindAlarm = "ALARM"
	CauseKindRule  = "RULE"
)

// maxCauseText bounds the free-length cause strings (the rule id and the alarm key). A default alarm
// key is "{profileToken}/{ruleToken}" and a rule id "{tenant}/{profileToken}@{version}/{ruleToken}",
// each token up to 128 characters, so this admits every key and id the platform mints.
const maxCauseText = 1024

// maxDetectionKind is the width of cause_detection_kind.
const maxDetectionKind = 64

// validCause checks a supplied cause and returns it in its stored form. tenant is the request's
// tenant: a rule id names its owning tenant first, and a cause naming another tenant's rule is
// refused rather than stored (fail closed).
func validCause(ctx context.Context, in *CommandCauseInput) (storedCause, error) {
	var out storedCause
	switch in.Kind {
	case CauseKindAlarm:
		if in.AlarmKey == nil || *in.AlarmKey == "" {
			return out, rejected(RejectCauseInvalid, "a cause of kind ALARM must name the alarm key")
		}
	case CauseKindRule:
		if in.AlarmKey != nil {
			return out, rejected(RejectCauseInvalid, "a cause of kind RULE names no alarm key")
		}
	default:
		return out, rejected(RejectCauseInvalid, "cause kind %q is not ALARM or RULE", in.Kind)
	}
	tenant, _ := core.TenantFromContext(ctx)
	if tenant == "" || !strings.HasPrefix(in.RuleId, tenant+"/") || len(in.RuleId) == len(tenant)+1 {
		return out, rejected(RejectCauseInvalid, "the cause's rule id must name a rule of this tenant")
	}
	if len(in.RuleId) > maxCauseText {
		return out, rejected(RejectCauseInvalid, "the cause's rule id is longer than %d characters", maxCauseText)
	}
	if in.AlarmKey != nil && len(*in.AlarmKey) > maxCauseText {
		return out, rejected(RejectCauseInvalid, "the cause's alarm key is longer than %d characters", maxCauseText)
	}
	if in.DetectionKind == "" || len(in.DetectionKind) > maxDetectionKind {
		return out, rejected(RejectCauseInvalid, "the cause's detection kind must be 1 to %d characters", maxDetectionKind)
	}
	at, err := time.Parse(time.RFC3339Nano, in.DetectedAt)
	if err != nil {
		return out, rejected(RejectCauseInvalid, "the cause's detectedAt %q is not an RFC 3339 timestamp", in.DetectedAt)
	}
	if in.Value != nil && (math.IsNaN(*in.Value) || math.IsInf(*in.Value, 0)) {
		return out, rejected(RejectCauseInvalid, "the cause's value is not a finite number")
	}
	out.Kind = sql.NullString{String: in.Kind, Valid: true}
	out.RuleId = sql.NullString{String: in.RuleId, Valid: true}
	out.DetectionKind = sql.NullString{String: in.DetectionKind, Valid: true}
	out.DetectedAt = sql.NullString{String: at.UTC().Format(time.RFC3339Nano), Valid: true}
	if in.Value != nil {
		out.Value = sql.NullFloat64{Float64: *in.Value, Valid: true}
	}
	if in.AlarmKey != nil {
		out.AlarmKey = sql.NullString{String: *in.AlarmKey, Valid: true}
	}
	out.PositionDerived = sql.NullBool{Bool: in.PositionDerived, Valid: true}
	return out, nil
}

// storedCause is a validated cause in its column form.
type storedCause struct {
	Kind, RuleId, DetectionKind, DetectedAt, AlarmKey sql.NullString
	Value                                             sql.NullFloat64
	PositionDerived                                   sql.NullBool
}

// stamp writes the cause onto a command about to be inserted.
func (s storedCause) stamp(c *Command) {
	c.CauseKind, c.CauseRuleId, c.CauseDetectionKind = s.Kind, s.RuleId, s.DetectionKind
	c.CauseDetectedAt, c.CauseValue, c.CauseAlarmKey = s.DetectedAt, s.Value, s.AlarmKey
	c.CausePositionDerived = s.PositionDerived
}

// CommandCause is a command's recorded cause, as read back.
type CommandCause struct {
	Kind          string
	RuleId        string
	DetectionKind string
	DetectedAt    string
	Value         *float64
	AlarmKey      *string
	// PositionDerived is false only when the row says so: NULL reads as true.
	PositionDerived bool
}

// Cause returns the command's recorded cause, or nil when it has none.
func (c *Command) Cause() *CommandCause {
	if !c.CauseKind.Valid {
		return nil
	}
	out := &CommandCause{
		Kind:            c.CauseKind.String,
		RuleId:          c.CauseRuleId.String,
		DetectionKind:   c.CauseDetectionKind.String,
		DetectedAt:      c.CauseDetectedAt.String,
		PositionDerived: !c.CausePositionDerived.Valid || c.CausePositionDerived.Bool,
	}
	if c.CauseValue.Valid {
		v := c.CauseValue.Float64
		out.Value = &v
	}
	if c.CauseAlarmKey.Valid {
		k := c.CauseAlarmKey.String
		out.AlarmKey = &k
	}
	return out
}

// RuleTokens parses the profile token and rule token out of a minted rule id
// "{tenant}/{profileToken}@{version}/{ruleToken}", the grammar event-processing mints. ok is false
// for an id that does not parse into that shape.
func RuleTokens(ruleID string) (profileToken, ruleToken string, ok bool) {
	_, rest, ok := strings.Cut(ruleID, "/")
	if !ok {
		return "", "", false
	}
	pvt, ruleToken, ok := strings.Cut(rest, "/")
	if !ok || ruleToken == "" || strings.Contains(ruleToken, "/") {
		return "", "", false
	}
	profileToken, _, ok = strings.Cut(pvt, "@")
	if !ok || profileToken == "" {
		return "", "", false
	}
	return profileToken, ruleToken, true
}
