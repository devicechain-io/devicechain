// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Connection strings are assembled here, and deliberately NOT with fmt.Sprintf.
//
// Adversarial review found that hand-formatting them had produced a whole class
// of defect that validating one field could not close (ADR-020 A2.1):
//
//   - A password containing a SPACE silently redirected the connection to a unix
//     socket. `password=hunter 2 host=dc-postgresql...` parses as password
//     "hunter" plus a runtime parameter named "2 host", leaving no host — so pgx
//     falls back to /tmp/.s.PGSQL.5432, the service dials itself, and the
//     operator sees a hostname that appears nowhere in their config. An EMPTY
//     password did the same thing. Passwords are operator-supplied, and nothing
//     constrained their character set.
//   - The URL form and the keyword form disagreed about what a password even IS.
//     `p%41ss` stayed literal in the DSN and percent-decoded to `pAss` in the
//     URL, so the connection that creates databases authenticated with a
//     different password than the one the service used afterwards.
//   - Values in trailing position could append parameters that WIN, because
//     libpq takes the last occurrence of a keyword. A functional area of
//     `usermgmt sslmode=disable search_path=usermgmt` turned a validated
//     `sslmode=verify-full` into a plaintext connection.
//
// The lesson generalised: validating `sslMode` removed one instance; escaping
// removes the class. Every value below is escaped for the form it lands in, so a
// value can never become syntax.

// postgresURL builds a `postgres://` connection URL.
//
// net/url does the escaping, which is the point: url.UserPassword percent-encodes
// the credentials on String(), and pgx percent-DECODES them, so the round trip is
// lossless for every byte a password may contain — including the `%` that made
// the two forms disagree. net.JoinHostPort brackets IPv6 literals, which naive
// formatting also got wrong.
func postgresURL(username, password, hostname string, port int32, database, sslMode string) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(username, password),
		Host:   net.JoinHostPort(hostname, strconv.Itoa(int(port))),
		Path:   "/" + database,
	}
	u.RawQuery = url.Values{"sslmode": []string{sslMode}}.Encode()
	return u.String()
}

// postgresKeywordDSN builds libpq's space-separated keyword/value form.
//
// Ordering is fixed and every value is quoted, so no value can introduce a
// keyword. `extra` carries runtime parameters (search_path) and is applied last.
func postgresKeywordDSN(username, password, hostname string, port int32, database, sslMode string,
	extra map[string]string) string {
	pairs := []string{
		"user=" + QuoteDSNValue(username),
		"password=" + QuoteDSNValue(password),
		"host=" + QuoteDSNValue(hostname),
		"port=" + QuoteDSNValue(strconv.Itoa(int(port))),
		"dbname=" + QuoteDSNValue(database),
		"sslmode=" + QuoteDSNValue(sslMode),
	}
	// Sorted so the rendered string is deterministic — an assertion against a
	// map-ordered string is a flake waiting to happen.
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		pairs = append(pairs, k+"="+QuoteDSNValue(extra[k]))
	}
	return strings.Join(pairs, " ")
}

// QuoteDSNValue renders a value as a libpq single-quoted literal.
//
// libpq's rule: a value may be single-quoted, and within the quotes a single
// quote or a backslash must be backslash-escaped. Quoting UNCONDITIONALLY rather
// than only-when-needed is deliberate — "does this value need quoting" is
// exactly the judgement that produced the space-in-password defect, and an
// always-quoted value has no such judgement to get wrong. An empty value renders
// as ” , which libpq reads as a genuinely empty value rather than as the next
// token.
func QuoteDSNValue(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('\'')
	for i := 0; i < len(value); i++ {
		if c := value[i]; c == '\'' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(value[i])
	}
	b.WriteByte('\'')
	return b.String()
}

// sortStrings is a tiny insertion sort so this file pulls in no more of the
// standard library than it needs; the slice is never more than a couple of keys.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
