// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file holds the shapes of the three read-only doors event-processing reconciles its
// detection projections against (api_detect_reconcile.go). The rule, roster and attribute facts
// this service emits are at-most-once: a fact that never reaches the stream is not replayed. What
// repairs it is event-processing comparing its copy with the state these doors return — at the
// start of every leadership term and every five minutes — so the doors return exactly what the
// facts would have said, built by the same code.
//
// The page ceilings are named here, once, because both sides need them: the doors refuse a larger
// page (fail loudly rather than clamp) and event-processing asks for exactly this much.
const (
	// MaxActiveProfileRulesPageSize bounds one page of active profiles. A page carries every
	// enabled rule of each profile it lists, so it is the one page whose byte size is not a
	// function of its row count; the reader halves it when a response exceeds the service-client
	// cap, down to a single profile.
	MaxActiveProfileRulesPageSize = 50
	// MaxRosterPageSize bounds one page of the device roster. A row is two tokens and a time.
	MaxRosterPageSize = 1000
	// MaxThresholdAttributePageSize bounds one page of threshold attributes. A row is a device
	// token, a scope, a key, a number and a time.
	MaxThresholdAttributePageSize = 1000
)

// ActiveProfileRules is one published profile's ACTIVE version as the detection engine should
// hold it: the version token its rules are keyed on, the instant that version became active, and
// its enabled rules — the same three things the detection-rules-published fact for that version
// carries.
type ActiveProfileRules struct {
	// ProfileId is the row id, the page cursor. It is not part of the fact.
	ProfileId    uint
	ProfileToken string
	// VersionToken is "{profileToken}@{version}".
	VersionToken string
	// ActiveSince is the stored activation instant (see DeviceProfile.ActiveSince), resolved
	// from the version rows when the column is NULL.
	ActiveSince time.Time
	// Rules are the version's ENABLED rules, exactly as the fact carries them.
	Rules []PublishedDetectionRule
}

// ActiveProfileRulesPage is one keyset page of ActiveProfileRules. NextCursor is the last row id
// the page scanned when the page was full, and nil when the walk is complete.
type ActiveProfileRulesPage struct {
	Entries    []ActiveProfileRules
	NextCursor *uint64
}

// DeviceRosterEntry is one device as the roster fact describes it: its token, the STABLE token of
// the profile its type adopts ("" when none), and when that membership began.
type DeviceRosterEntry struct {
	// DeviceId is the row id, the page cursor. It is not part of the fact.
	DeviceId      uint
	DeviceToken   string
	ProfileToken  string
	ExpectedSince time.Time
}

// DeviceRosterPage is one keyset page of the device roster.
type DeviceRosterPage struct {
	Entries    []DeviceRosterEntry
	NextCursor *uint64
}

// DeviceThresholdAttribute is one attribute a dynamic detection threshold can read — a numeric
// SHARED or SERVER attribute of a device — as the device-attribute fact describes it.
type DeviceThresholdAttribute struct {
	// Id is the attribute row id, the page cursor. It is not part of the fact.
	Id          uint
	DeviceToken string
	Scope       string
	AttrKey     string
	Value       float64
	UpdatedAt   time.Time
}

// DeviceThresholdAttributePage is one keyset page of threshold attributes. A page can hold fewer
// entries than it scanned rows (a non-numeric row is scanned and skipped), so an empty page with
// a cursor is NOT the end of the walk; only a nil cursor is.
type DeviceThresholdAttributePage struct {
	Entries    []DeviceThresholdAttribute
	NextCursor *uint64
}

// SameRuleDefinition reports whether two detection-rule definitions are the same rule.
//
// 🔴 IT IS NOT A BYTE COMPARISON, AND ON POSTGRES A BYTE COMPARISON IS ALWAYS WRONG. A profile
// version's snapshot is stored as jsonb, which re-renders a document on the way out — its own key
// order, ": " and ", " separators, numbers in numeric's spelling — while a definition that went
// through encoding/json before it was stored is compact. The same rule therefore arrives in two
// byte forms depending on which road it took, and every consumer that asked "did this rule
// change?" by comparing bytes would answer yes for a rule nobody touched: the detection engine
// would drop the rule's running state (its duration holds, windows and absence timers) and count
// the "change" as a repair.
//
// So both documents are decoded and compared as values: object keys unordered, whitespace
// ignored, and numbers compared EXACTLY as rationals — 1, 1.0 and 1e0 are one number, and a
// 19-digit integer is not rounded through a float on the way. A definition that is not a single
// JSON document is compared byte-for-byte, which is the conservative answer: it can report a
// change that is not one, never miss one that is.
//
// It is the one definition of "the rule changed" — device-management's reconcile door and
// event-processing's registry both ask it — so the two cannot disagree.
func SameRuleDefinition(a, b string) bool {
	if a == b {
		return true
	}
	ca, okA := canonicalJSON(a)
	cb, okB := canonicalJSON(b)
	if !okA || !okB {
		return false
	}
	return ca == cb
}

// maxCanonicalExponent bounds the decimal exponent canonicalJSON will expand into an exact
// rational. big.Rat materialises 10^exp, so an author-supplied "1e999999999" would cost
// gigabytes; a number beyond this bound is compared by its spelling instead (still exact, merely
// not normalised), which no real threshold approaches.
const maxCanonicalExponent = 1000

// canonicalJSON renders a JSON document as a canonical string: keys sorted, no whitespace, and
// every number as its exact rational. ok is false when the text is not exactly one JSON value.
func canonicalJSON(doc string) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", false // trailing content: not a single document
	}
	var sb strings.Builder
	writeCanonical(&sb, v)
	return sb.String(), true
}

func writeCanonical(sb *strings.Builder, v any) {
	switch t := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		sb.WriteString(strconv.FormatBool(t))
	case string:
		sb.WriteString(strconv.Quote(t))
	case json.Number:
		sb.WriteString(canonicalNumber(string(t)))
	case []any:
		sb.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeCanonical(sb, e)
		}
		sb.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(strconv.Quote(k))
			sb.WriteByte(':')
			writeCanonical(sb, t[k])
		}
		sb.WriteByte('}')
	}
}

// canonicalNumber renders a JSON number as its exact rational ("n:3/2", "n:100"), or by its
// spelling ("s:...") when its exponent is too large to expand safely.
func canonicalNumber(num string) string {
	if i := strings.IndexAny(num, "eE"); i >= 0 {
		exp, err := strconv.Atoi(strings.TrimPrefix(num[i+1:], "+"))
		if err != nil || exp > maxCanonicalExponent || exp < -maxCanonicalExponent {
			return "s:" + num
		}
	}
	r, ok := new(big.Rat).SetString(num)
	if !ok {
		return "s:" + num
	}
	return "n:" + r.RatString()
}
