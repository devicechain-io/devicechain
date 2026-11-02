// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"unicode"

	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// 🔴 THIS FILE MUST NOT IMPORT THE EVENT-SOURCES PROTO PACKAGE. The model is linked by the
// calculated-metric backfill worker, which is held to not reaching the broker (see
// processor/backfill's deps test), and event-sources' package pulls the messaging layer in. The
// code that reads an archived event out of a failed-events record lives in the processor.

// ReasonName is the snake_case name of a FailureReason number, from the protobuf vocabulary
// itself so a value added there is named here without an edit. ok is false for a number this
// build does not know: it is a reason a NEWER writer added, and the caller serves it as
// "unknown" with its number rather than as any neighbour.
func ReasonName(code int32) (name string, ok bool) {
	n, found := resolvedevent.FailureReason_name[code]
	if !found {
		return "", false
	}
	return snakeCase(n), true
}

// KnownReasonCodes is every FailureReason number this build knows other than Unknown (0), which
// is "no reason given" and is served as UNKNOWN beside the numbers it does not know.
func KnownReasonCodes() []int32 {
	out := make([]int32, 0, len(resolvedevent.FailureReason_name))
	for code := range resolvedevent.FailureReason_name {
		if code != int32(resolvedevent.FailureReason_Unknown) {
			out = append(out, code)
		}
	}
	return out
}

func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// ClaimedDeviceToken makes a sender-claimed token storable: bounded to the column, valid
// UTF-8, and never equal to the overflow marker, which would let a sender's claim pose as the
// platform's own overflow row.
func ClaimedDeviceToken(claimed string) string {
	t := SanitizeFailedEventText(claimed, 128)
	if t == OverflowDeviceToken {
		return "(" + t + ")"
	}
	return t
}
