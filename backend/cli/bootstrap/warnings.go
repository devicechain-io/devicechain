// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"regexp"

	"k8s.io/client-go/rest"
)

// Some API servers warn "unrecognized format "int32"" on every CRD create or update
// whose schema carries a plain controller-gen integer (`type: integer`, `format: int32`).
// The warning is wrong, not informative: int32 and int64 are exactly the formats a CRD
// integer may declare, the server ignores nothing, and there is nothing for an operator
// to do about it. It reached the terminal of every `dcctl install` as a stream of
// log-shaped lines, which reads like a problem with the install.
//
// Only that one warning, for only those two formats, is dropped. Every other warning the
// server sends (a deprecated API, a genuinely unrecognized format such as a typo'd
// string format) still goes where it went before.
var benignFormatWarning = regexp.MustCompile(`^(?:.*: )?unrecognized format "(?:int32|int64)"$`)

// formatWarningFilter wraps the handler client-go would otherwise use.
type formatWarningFilter struct {
	next rest.WarningHandlerWithContext
}

func (f formatWarningFilter) HandleWarningHeaderWithContext(ctx context.Context, code int, agent, text string) {
	if benignFormatWarning.MatchString(text) {
		return
	}
	f.next.HandleWarningHeaderWithContext(ctx, code, agent, text)
}

// installWarningFilter makes the filter the process-wide default handler, delegating to
// next. The default is what every client without its own handler uses, which includes the
// ones Helm builds for itself, so one registration covers the whole command.
func installWarningFilter(next rest.WarningHandlerWithContext) {
	rest.SetDefaultWarningHandlerWithContext(formatWarningFilter{next: next})
}

func init() { installWarningFilter(rest.WarningLogger{}) }
