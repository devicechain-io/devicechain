// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/maintwindow"
)

var ErrInvalidAlarmSuppression = integrity.NewRefusal(integrity.ClassInvalid, "invalid alarm suppression")

func suppressionInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidAlarmSuppression, fmt.Sprintf(format, args...))
}

// ValidateAlarmSuppression is pure and is also used when compiling stored rows.
func ValidateAlarmSuppression(w *AlarmSuppressionWindow) error {
	if w == nil {
		return suppressionInvalid("window is required")
	}
	if err := core.ValidateToken(w.Token); err != nil {
		return suppressionInvalid("%v", err)
	}
	if strings.TrimSpace(w.Reason) == "" || utf8.RuneCountInString(w.Reason) > 1024 {
		return suppressionInvalid("reason is required, at most 1024 characters")
	}
	if utf8.RuneCountInString(w.Name.String) > 128 || utf8.RuneCountInString(w.Description.String) > 1024 {
		return suppressionInvalid("name or description is too long")
	}
	if len(w.Targets) < 1 || len(w.Targets) > 20 {
		return suppressionInvalid("targets must contain 1..20 entries")
	}
	seen := map[AlarmSuppressionTarget]bool{}
	for _, t := range w.Targets {
		switch t.Type {
		case "DEVICE", "DEVICE_TYPE", "ENTITY_GROUP", "AREA", "ASSET", "CUSTOMER":
			if err := core.ValidateToken(t.Token); err != nil {
				return suppressionInvalid("target token: %v", err)
			}
		case "TENANT":
			if t.Token != "" || len(w.Targets) != 1 {
				return suppressionInvalid("TENANT must be alone and have no token")
			}
		default:
			return suppressionInvalid("unknown target type %q", t.Type)
		}
		if seen[t] {
			return suppressionInvalid("duplicate target")
		}
		seen[t] = true
	}
	if len(w.AlarmKeys) > 20 {
		return suppressionInvalid("at most 20 alarm keys")
	}
	keys := map[string]bool{}
	for _, key := range w.AlarmKeys {
		if strings.TrimSpace(key) == "" || utf8.RuneCountInString(key) > 256 || keys[key] {
			return suppressionInvalid("alarm keys must be nonempty, unique and at most 256 characters")
		}
		keys[key] = true
	}
	if w.BypassSeverity.Valid && !AlarmSeverity(w.BypassSeverity.String).Valid() {
		return suppressionInvalid("unknown bypass severity")
	}
	switch w.Kind {
	case SuppressionOneShot:
		if w.StartTime == nil || w.EndTime == nil || !w.StartTime.Before(*w.EndTime) || w.EndTime.Sub(*w.StartTime) > 31*24*time.Hour {
			return suppressionInvalid("one-shot requires start < end, at most 31 days")
		}
		if w.Timezone.Valid || w.Slots != nil || w.ValidFrom != nil || w.ValidUntil != nil {
			return suppressionInvalid("one-shot cannot have recurring fields")
		}
	case SuppressionRecurring:
		if w.StartTime != nil || w.EndTime != nil {
			return suppressionInvalid("recurring cannot have start/end")
		}
		if w.ValidFrom != nil && w.ValidUntil != nil && !w.ValidFrom.Before(*w.ValidUntil) {
			return suppressionInvalid("valid_from must precede valid_until")
		}
		if _, err := suppressionSchedule(w); err != nil {
			return err
		}
	default:
		return suppressionInvalid("unknown kind %q", w.Kind)
	}
	return nil
}

func suppressionSchedule(w *AlarmSuppressionWindow) (*maintwindow.Schedule, error) {
	if !w.Timezone.Valid {
		return nil, suppressionInvalid("timezone is required")
	}
	dec := json.NewDecoder(bytes.NewReader(w.Slots))
	dec.DisallowUnknownFields()
	var slots []maintwindow.Slot
	if err := dec.Decode(&slots); err != nil {
		return nil, suppressionInvalid("slots: %v", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, suppressionInvalid("slots has trailing content")
	}
	schedule, err := maintwindow.New(w.Timezone.String, slots)
	if err != nil {
		return nil, suppressionInvalid("%v", err)
	}
	// Quiet-hours policy rules, applied on wall-clock minutes including the week seam.
	// The shared calculator deliberately permits overlaps and a full week for command
	// windows; this stricter policy is local because this slice cannot change core.
	type span struct{ start, end int }
	spans := make([]span, 0, len(slots))
	total := 0
	for _, s := range slots {
		t, _ := time.Parse("15:04", s.Start)
		start := slices.Index(maintwindow.Days, s.Day)*1440 + t.Hour()*60 + t.Minute()
		spans = append(spans, span{start, start + s.DurationMinutes})
		total += s.DurationMinutes
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i, s := range spans {
		next := spans[(i+1)%len(spans)].start
		if i == len(spans)-1 {
			next += 10080
		}
		if len(spans) > 1 && s.end > next {
			return nil, suppressionInvalid("slots overlap")
		}
	}
	if total >= 10080 {
		return nil, suppressionInvalid("slots cover the whole week")
	}
	return schedule, nil
}
