// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"regexp"
	"sort"
)

var reviewAttrWord = regexp.MustCompile(`\battr\b`)

func reviewReadsAttributes(v any) bool {
	switch x := v.(type) {
	case string:
		return reviewAttrWord.MatchString(x)
	case map[string]any:
		for key, value := range x {
			if key == "thresholdAttr" {
				if text, ok := value.(string); !ok || text != "" {
					return true
				}
			}
			if reviewReadsAttributes(value) {
				return true
			}
		}
	case []any:
		for _, value := range x {
			if reviewReadsAttributes(value) {
				return true
			}
		}
	}
	return false
}

var reviewAnchors = regexp.MustCompile(`\banchors\s*\[`)
var reviewPosition = regexp.MustCompile(`\b(?:geo|inFence)\b`)

// Scan keys as well as all nested string values, including action expressions.
func reviewReadsRuleInput(v any, anchors bool) bool {
	switch x := v.(type) {
	case string:
		if anchors {
			return reviewAnchors.MatchString(x)
		}
		return reviewPosition.MatchString(x)
	case map[string]any:
		for key, value := range x {
			if anchors {
				if key == "anchors" {
					return true
				}
				if key == "anchorType" {
					if text, ok := value.(string); !ok || text != "" {
						return true
					}
				}
			} else {
				switch key {
				case "geofence", "fence", "fenceToken", "inFence":
					return true
				}
			}
			if reviewReadsRuleInput(key, anchors) || reviewReadsRuleInput(value, anchors) {
				return true
			}
		}
	case []any:
		for _, value := range x {
			if reviewReadsRuleInput(value, anchors) {
				return true
			}
		}
	}
	return false
}

// protectionIneligibility conservatively approximates dependencies; only event-processing
// parses executable rules. Unknown definitions and formulas fail closed.
func protectionIneligibility(snap *ProfileSnapshot) []string {
	var reasons []string
	constants := constantMetrics(snap)
	for _, rule := range snap.Rules {
		if !rule.Enabled {
			continue
		}
		if rule.EntityGroupToken != nil || rule.EntityGroupVersion != nil {
			reasons = append(reasons, fmt.Sprintf("rule %s is group-scoped", rule.Token))
		}
		value, err := reviewJSON(rule.Definition)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("rule %s has an unreadable definition", rule.Token))
		} else {
			if reviewReadsAttributes(value) {
				reasons = append(reasons, fmt.Sprintf("rule %s reads device attributes", rule.Token))
			}
			if reviewReadsRuleInput(value, true) {
				reasons = append(reasons, fmt.Sprintf("rule %s correlates by relationship anchors", rule.Token))
			}
			if reviewReadsRuleInput(value, false) {
				reasons = append(reasons, fmt.Sprintf("rule %s reads geofence position", rule.Token))
			}
		}
		deps := reviewDependencies(&ProfileSnapshot{Metrics: snap.Metrics, Commands: snap.Commands, Rules: []*DetectionRule{rule}})
		for _, key := range constants {
			if deps[key] {
				reasons = append(reasons, fmt.Sprintf("rule %s depends on constant-reading calculated metric %s", rule.Token, key))
			}
		}
	}
	sort.Strings(reasons)
	return reasons
}
