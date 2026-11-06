// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"regexp"

	"k8s.io/client-go/rest"
)

// An API server reports `unrecognized format "int32"` (or "int64") when it writes a CRD
// whose schema declares one of those formats on a type it does not accept them for. For
// a plain controller-gen integer (`type: integer`, `format: int32`) the warning is noise:
// those are exactly the formats an integer may declare, and there is nothing for an
// operator to do about it. Reaching the terminal of `dcctl install` as log-shaped lines,
// it reads like a problem with the install.
//
// Only that one warning, for only those two formats, is dropped, and it is matched by its
// text. The same text from a schema that really does misuse the format (an int32 on a
// boolean) is therefore dropped too; nothing here tells the two apart. Every other warning
// (a deprecated API, any other unrecognized format) goes where it went before.
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

// SilenceBenignFormatWarnings registers the filter for this process. It is called once, by
// the dcctl entry point, rather than from an init(): importing this package (a test, another
// binary) must not change how an unrelated client's warnings are delivered.
//
// It covers what dcctl and the Helm library do in-process. It does NOT reach the
// OpenTofu subprocess, whose providers print the server's warnings themselves.
func SilenceBenignFormatWarnings() { installWarningFilter(rest.WarningLogger{}) }
