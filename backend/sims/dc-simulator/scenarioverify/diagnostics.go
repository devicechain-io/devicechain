// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

const diagnosticRows = 5
const diagnosticValueBytes = 48

type s7AlarmCounts struct{ before, after int }

// Only alarm identity/state fields are printable. Never format raw comparisons,
// response bodies, maps, errors, sessions or handshakes: those can hold OAuth or
// operator credentials. Quoting also prevents control characters forging log lines.
func shortDiagnostic(value string) string {
	suffix := ""
	if len(value) > diagnosticValueBytes {
		value, suffix = value[:diagnosticValueBytes], "..."
	}
	return strconv.QuoteToASCII(value) + suffix
}

func s7AlarmDiagnostic(read S7Comparison) string {
	var expected, actual []S7Alarm
	if json.Unmarshal(read.Expected, &expected) != nil || json.Unmarshal(read.Actual, &actual) != nil {
		return "alarm_diff=unavailable (non-alarm rows)"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "MCP=%d GraphQL=%d", len(actual), len(expected))
	if read.alarmCounts == nil {
		out.WriteString(" GraphQL_brackets=unavailable")
	} else {
		fmt.Fprintf(&out, " GraphQL_before=%d GraphQL_after=%d", read.alarmCounts.before, read.alarmCounts.after)
	}
	// Pair by full token, retaining duplicate occurrences. Truncation is for
	// presentation only; it cannot hide a difference or merge two identities.
	want, got := map[string][]S7Alarm{}, map[string][]S7Alarm{}
	for _, row := range expected {
		want[row.Token] = append(want[row.Token], row)
	}
	for _, row := range actual {
		got[row.Token] = append(got[row.Token], row)
	}
	tokens := []string{}
	for token := range want {
		tokens = append(tokens, token)
	}
	for token := range got {
		if _, ok := want[token]; !ok {
			tokens = append(tokens, token)
		}
	}
	sort.Strings(tokens)
	var details strings.Builder
	differing, shown := 0, 0
	for _, token := range tokens {
		left, right := want[token], got[token]
		for i := 0; i < max(len(left), len(right)); i++ {
			var a, b *S7Alarm
			if i < len(left) {
				a = &left[i]
			}
			if i < len(right) {
				b = &right[i]
			}
			if a != nil && b != nil && *a == *b {
				continue
			}
			differing++
			if shown >= diagnosticRows {
				continue
			}
			shown++
			fmt.Fprintf(&details, " {token=%s", shortDiagnostic(token))
			for _, field := range []struct {
				name  string
				value func(*S7Alarm) string
			}{
				{"originatorId", func(a *S7Alarm) string { return a.OriginatorID }},
				{"originatorToken", func(a *S7Alarm) string { return a.OriginatorToken }},
				{"alarmKey", func(a *S7Alarm) string { return a.AlarmKey }},
				{"state", func(a *S7Alarm) string { return a.State }},
				{"severity", func(a *S7Alarm) string { return a.Severity }},
			} {
				if a != nil && b != nil && field.value(a) == field.value(b) {
					continue
				}
				lv, rv := "<missing>", "<missing>"
				if a != nil {
					lv = shortDiagnostic(field.value(a))
				}
				if b != nil {
					rv = shortDiagnostic(field.value(b))
				}
				fmt.Fprintf(&details, " %s GraphQL=%s MCP=%s;", field.name, lv, rv)
			}
			details.WriteString("}")
		}
	}
	fmt.Fprintf(&out, " differing_alarms=%d shown=%d%s", differing, shown, details.String())
	if differing == 0 {
		out.WriteString(" (row order, null shape or unlisted fields differ)")
	}
	return out.String()
}

// A status is a closed platform vocabulary, not free-form diagnostic text.
func diagnosticCommandStatus(status string) string {
	switch status {
	case "QUEUED", "HELD", "SENT", "PARKED", "SUCCESSFUL", "FAILED", "TIMEOUT", "EXPIRED", "CANCELLED":
		return status
	default:
		return "<unknown>"
	}
}

func diagnosticTime(at time.Time) string {
	if at.IsZero() {
		return "<missing>"
	}
	return at.UTC().Format(time.RFC3339Nano)
}

func diagnosticTimestamp(raw string) string {
	if raw == "" {
		return "<missing>"
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "<invalid>"
	}
	return diagnosticTime(at)
}

func samplingCommandDiagnostic(o Observations, matched int) string {
	from, to := o.at(16.5), o.at(17)
	read, window, shown := 0, 0, 0
	var rows strings.Builder
	for _, row := range o.Commands {
		if row.Device != sim.HarborpulseSamplingGauge {
			continue
		}
		read++
		if inWindow(row.Queued, from, to) {
			window++
		}
		if shown >= diagnosticRows {
			continue
		}
		shown++
		fmt.Fprintf(&rows, "{status=%s createdAt=%s updatedAt=%s queuedTime=%s respondedTime=%s}",
			diagnosticCommandStatus(row.Status), diagnosticTimestamp(row.CreatedAt), diagnosticTimestamp(row.UpdatedAt), diagnosticTime(row.Queued), diagnosticTime(row.Responded))
	}
	// Include rows outside the queued-time window too: absent/late queuedTime can
	// explain zero matches even when the reader saw a command created in the window.
	return fmt.Sprintf("device_rows_read=%d window_rows=%d matched_commands=%d window=[%s,%s] rows=[%s] omitted=%d",
		read, window, matched, diagnosticTime(from), diagnosticTime(to), rows.String(), read-shown)
}
