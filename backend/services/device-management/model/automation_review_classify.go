// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/calcfield"
)

type ProfileChange struct {
	Area       string `json:"area"`
	Key        string `json:"key"`
	Change     string `json:"change"`
	Automation bool   `json:"automation"`
}

type reviewClassification struct {
	Kind        string
	RuleToken   string
	Base, Draft *RuleEnvelope
	Changes     []ProfileChange
	Reasons     []string
}

func ruleEnvelope(r *DetectionRule) (*RuleEnvelope, error) {
	if r == nil {
		return nil, nil
	}
	v, err := reviewJSON(r.Definition)
	if err != nil {
		return nil, err
	}
	b, err := reviewCanonical(v)
	if err != nil {
		return nil, err
	}
	return &RuleEnvelope{Token: r.Token, Enabled: r.Enabled, Definition: string(b),
		EntityGroupToken: r.EntityGroupToken, EntityGroupVersion: r.EntityGroupVersion}, nil
}

// Scope absence is equivalent to an empty token and version zero in an envelope.
func comparableEnvelope(e *RuleEnvelope) any {
	if e == nil {
		return nil
	}
	token, version := "", int32(0)
	if e.EntityGroupToken != nil {
		token = *e.EntityGroupToken
	}
	if e.EntityGroupVersion != nil {
		version = *e.EntityGroupVersion
	}
	return map[string]any{"token": e.Token, "enabled": e.Enabled, "definition": e.Definition,
		"entityGroupToken": token, "entityGroupVersion": version}
}

func snapshotRules(s *ProfileSnapshot) map[string]*DetectionRule {
	out := map[string]*DetectionRule{}
	for _, r := range s.Rules {
		out[r.Token] = r
	}
	return out
}

// reviewDependencies scans decoded, canonical definitions so JSON escapes cannot
// hide keys. CEL, escapes and templates can assemble keys at runtime, so they make
// every metric and command a dependency: edits to them are UNREVIEWABLE rather
// than reviewable. This scan approximates direct and calculated dependencies;
// whole-event validation and decoder changes are guarded separately whenever an
// enabled rule exists, including rules that read no metric key.
func reviewDependencies(snapshots ...*ProfileSnapshot) map[string]bool {
	deps := map[string]bool{}
	keys := map[string]bool{}
	var definitions []string
	all := false
	var metrics []*MetricDefinition
	for _, s := range snapshots {
		metrics = append(metrics, s.Metrics...)
		for _, m := range s.Metrics {
			keys[m.MetricKey] = true
		}
		for _, c := range s.Commands {
			keys[c.CommandKey] = true
		}
		for _, r := range s.Rules {
			value, err := reviewJSON(r.Definition)
			if err != nil {
				all = true
				continue
			}
			canonical, err := reviewCanonical(value)
			if err != nil {
				all = true
				continue
			}
			definition := string(canonical)
			definitions = append(definitions, definition)
			all = all || reviewHasCEL(value) || strings.ContainsAny(definition, `\`) || strings.Contains(definition, "{{")
		}
	}
	for k := range keys {
		if all {
			deps[k] = true
		}
		for _, d := range definitions {
			if k != "" && strings.Contains(d, k) {
				deps[k] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, m := range metrics {
			if !deps[m.MetricKey] || !m.Expression.Valid {
				continue
			}
			for _, other := range metrics {
				k := other.MetricKey
				if k != "" && !deps[k] && strings.Contains(m.Expression.String, k) {
					deps[k], changed = true, true
				}
			}
		}
	}
	return deps
}

// CEL may be nested in conditions or actions, including arrays.
func reviewHasCEL(value any) bool {
	switch x := value.(type) {
	case map[string]any:
		for key, v := range x {
			if text, ok := v.(string); key == "cel" && ok && text != "" {
				return true
			}
			if reviewHasCEL(v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if reviewHasCEL(v) {
				return true
			}
		}
	}
	return false
}

// classifyReviewRules keeps envelope changes separate from authoring annotations.
func classifyReviewRules(base, draft *ProfileSnapshot, out *reviewClassification) ([]string, error) {
	br, dr := snapshotRules(base), snapshotRules(draft)
	all := map[string]bool{}
	for k := range br {
		all[k] = true
	}
	for k := range dr {
		all[k] = true
	}
	var changedRules []string
	for k := range all {
		be, err := ruleEnvelope(br[k])
		if err != nil {
			return nil, err
		}
		de, err := ruleEnvelope(dr[k])
		if err != nil {
			return nil, err
		}
		b, err := reviewCanonical(comparableEnvelope(be))
		if err != nil {
			return nil, err
		}
		d, err := reviewCanonical(comparableEnvelope(de))
		if err != nil {
			return nil, err
		}
		automation := !bytes.Equal(b, d)
		b, err = reviewElement(br[k])
		if err != nil {
			return nil, err
		}
		d, err = reviewElement(dr[k])
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(b, d) {
			out.Changes = append(out.Changes, ProfileChange{Area: "RULE", Key: k,
				Change: reviewChangeType(br[k] == nil, dr[k] == nil), Automation: automation})
		}
		if automation {
			changedRules = append(changedRules, k)
			out.RuleToken, out.Base, out.Draft = k, be, de
		}
	}
	return changedRules, nil
}

// reviewMetricValidation excludes only known labels: unknown fields fail closed,
// since any invalid declared metric dead-letters the entire measurement event.
func reviewMetricValidation(element []byte) ([]byte, error) {
	value, err := reviewJSON(element)
	if err != nil {
		return nil, err
	}
	if fields, ok := value.(map[string]any); ok {
		for key := range fields {
			switch key {
			case "Name", "Description", "Metadata", "Descriptor", "Unit":
				delete(fields, key)
			default:
				if strings.HasPrefix(key, "Semantic") {
					delete(fields, key)
				}
			}
		}
	}
	return reviewCanonical(value)
}

// reviewDefinitionChanges preserves dependency-based labels while guarding whole-event
// validation for enabled automation, even when a rule reads a different metric.
func reviewDefinitionChanges(area string, base, draft map[string]any, deps map[string]bool, enabled bool) ([]ProfileChange, error) {
	var changes []ProfileChange
	keys := map[string]bool{}
	for k := range base {
		keys[k] = true
	}
	for k := range draft {
		keys[k] = true
	}
	for k := range keys {
		b, err := reviewElement(base[k])
		if err != nil {
			return nil, err
		}
		d, err := reviewElement(draft[k])
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(b, d) {
			automation := deps[k]
			if area == "METRIC" && enabled {
				bv, err := reviewMetricValidation(b)
				if err != nil {
					return nil, err
				}
				dv, err := reviewMetricValidation(d)
				if err != nil {
					return nil, err
				}
				automation = automation || base[k] == nil || draft[k] == nil || !bytes.Equal(bv, dv)
			}
			changes = append(changes, ProfileChange{Area: area, Key: k,
				Change: reviewChangeType(base[k] == nil, draft[k] == nil), Automation: automation})
		}
	}
	return changes, nil
}

func classifyAutomationReview(base, draft *ProfileSnapshot) (*reviewClassification, error) {
	out := &reviewClassification{Kind: ReviewKindNoAutomationChange, Changes: []ProfileChange{}, Reasons: []string{}}
	changedRules, err := classifyReviewRules(base, draft, out)
	if err != nil {
		return nil, err
	}
	deps := reviewDependencies(base, draft)
	enabled := false
	for _, snapshot := range []*ProfileSnapshot{base, draft} {
		for _, rule := range snapshot.Rules {
			enabled = enabled || rule.Enabled
		}
	}
	bm, dm, bc, dc := map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}
	for _, m := range base.Metrics {
		bm[m.MetricKey] = m
	}
	for _, m := range draft.Metrics {
		dm[m.MetricKey] = m
	}
	for _, c := range base.Commands {
		bc[c.CommandKey] = c
	}
	for _, c := range draft.Commands {
		dc[c.CommandKey] = c
	}
	for _, pair := range []struct {
		area string
		b, d map[string]any
	}{{"METRIC", bm, dm}, {"COMMAND", bc, dc}} {
		changes, err := reviewDefinitionChanges(pair.area, pair.b, pair.d, deps, enabled)
		if err != nil {
			return nil, err
		}
		out.Changes = append(out.Changes, changes...)
	}
	for _, p := range []struct {
		area string
		b, d any
	}{
		{"DECODER", base.Decoder, draft.Decoder}, {"LOCATION", base.Location, draft.Location},
		{"CONFIGURATION", base.Configuration, draft.Configuration}, {"SEMANTICS", base.SemanticPackVersions, draft.SemanticPackVersions},
	} {
		b, err := reviewCanonical(p.b)
		if err != nil {
			return nil, err
		}
		d, err := reviewCanonical(p.d)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(b, d) {
			out.Changes = append(out.Changes, ProfileChange{Area: p.area, Change: reviewChangeType(string(b) == "null", string(d) == "null"), Automation: p.area == "DECODER" && enabled})
		}
	}
	sort.Slice(out.Changes, func(i, j int) bool {
		a, b := out.Changes[i], out.Changes[j]
		if a.Area != b.Area {
			return a.Area < b.Area
		}
		return a.Key < b.Key
	})
	sort.Strings(changedRules)
	if len(changedRules) == 1 {
		out.Kind = ReviewKindRuleChange
	}
	if len(changedRules) > 1 {
		out.Kind = ReviewKindUnreviewable
		out.Reasons = append(out.Reasons, "rules "+strings.Join(changedRules, " and ")+" both change")
	}
	for _, c := range out.Changes {
		if c.Automation && c.Area != "RULE" {
			out.Kind = ReviewKindUnreviewable
			if c.Area == "DECODER" {
				out.Reasons = append(out.Reasons, "decoder, which produces the values rules read, changes")
				continue
			}
			if c.Area == "METRIC" && !deps[c.Key] {
				out.Reasons = append(out.Reasons, fmt.Sprintf("metric %s, which affects whole-event validation, changes", c.Key))
				continue
			}
			verb := "reads"
			if c.Area == "COMMAND" {
				verb = "sends"
			}
			out.Reasons = append(out.Reasons, fmt.Sprintf("%s %s, which a rule %s, changes", strings.ToLower(c.Area), c.Key, verb))
		}
	}
	if out.Kind == ReviewKindUnreviewable && len(changedRules) == 1 {
		out.Reasons = append([]string{"rule " + changedRules[0] + " changes"}, out.Reasons...)
	}
	if out.Kind != ReviewKindRuleChange {
		out.RuleToken, out.Base, out.Draft = "", nil, nil
	}
	return out, nil
}

func reviewChangeType(baseAbsent, draftAbsent bool) string {
	if baseAbsent {
		return "ADDED"
	}
	if draftAbsent {
		return "REMOVED"
	}
	return "MODIFIED"
}

// constantMetrics includes broken formulas, since their constant dependencies are
// unknown, and closes transitively across both versions before replay eligibility.
func constantMetrics(snapshots ...*ProfileSnapshot) []string {
	found := map[string]bool{}
	var metrics []*MetricDefinition
	for _, s := range snapshots {
		metrics = append(metrics, s.Metrics...)
		p := newProfileFormulas(s.Metrics)
		var defs []calcfield.Definition
		for _, m := range p.calculated {
			t, ok := calcfield.ParseType(m.DataType)
			if !ok {
				found[m.MetricKey] = true
				continue
			}
			defs = append(defs, calcfield.Definition{Key: m.MetricKey, Expression: m.Expression.String,
				Type: t, Min: nullFloatPtr(m.MinValue), Max: nullFloatPtr(m.MaxValue)})
		}
		plan, err := p.plan(defs)
		if err != nil {
			for _, m := range p.calculated {
				found[m.MetricKey] = true
			}
			continue
		}
		for i := 0; i < plan.Len(); i++ {
			step := plan.Step(i)
			if step.Err != nil || step.Formula == nil || len(step.Formula.ConstantKeys()) > 0 {
				found[step.Key] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, m := range metrics {
			if !m.Expression.Valid || found[m.MetricKey] {
				continue
			}
			for k := range found {
				if strings.Contains(m.Expression.String, k) {
					found[m.MetricKey], changed = true, true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// nullFloatPtr reads an optional bound the way the published calculation plan does.
func nullFloatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}
