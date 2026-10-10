// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import "strings"

// likeEscaper neutralizes the LIKE/ILIKE wildcards in user-supplied filter text
// so it is matched literally, with backslash as the escape character.
var likeEscaper = strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`)

// ContainsPattern turns free filter text into a LIKE/ILIKE pattern matching it as
// a literal substring: the text's own `%`, `_` and `\` are escaped, then the whole is
// wrapped in %…%.
//
// The pattern escapes with backslash, which is Postgres's default but NOT SQLite's
// (SQLite has no default escape character), so a caller writes the clause with an
// explicit `ESCAPE '\'` — e.g. `name LIKE ? ESCAPE '\'` — to mean the same thing on
// every dialect the services run or test against.
func ContainsPattern(s string) string {
	return "%" + likeEscaper.Replace(s) + "%"
}
