// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import "strings"

// QuoteIdentifier renders a SQL identifier as a double-quoted literal, doubling any
// embedded double quote per the SQL standard.
//
// This is not optional hygiene in this codebase, and it is not really about injection:
// a functional area's Postgres SCHEMA name is the area name verbatim, so it contains a
// hyphen — and `device-management.devices` unquoted parses as a subtraction, not as a
// name. Any statement this platform builds by string formatting against a schema has to
// go through here or it does not run at all.
//
// It lives in rdb because rdb is what creates those schemas (assurePostgresSchema) and
// what pins them on a connection, so it is the layer that knows their shape. It is
// exported because three places outside rdb had each hand-rolled the identical two
// lines: the restore drill, the load-test perturber, and the tenant purge. One of them
// getting it subtly wrong is a class of bug worth spending an exported function on.
//
// Note the deliberate asymmetry with QuoteDSNValue, which quotes a libpq DSN VALUE: same
// idea, different grammar, and using one where the other belongs produces a statement
// that is wrong in a way that still parses.
func QuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
