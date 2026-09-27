// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
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

// IdleInTransactionTimeout is how long a connection this package opens may sit inside an
// open transaction without sending the database anything, before the database ends the
// session and rolls the transaction back.
//
// 🔴 IT EXISTS FOR THE ERASURE FENCE'S EXPOSURE ARGUMENT (tenant_fence.go, the memo
// paragraph): a transaction that read the fence clear before a purge planted it can still
// commit, and the purge only catches that write if it commits inside the settle window.
// The realistic way a writer's transaction outlives that window is by going quiet — a
// frozen pod, a partition between the service and the database, client code blocked
// mid-transaction — and on the server every one of those is "idle in transaction". A
// COMMIT held up in a partition cannot arrive later than this either: the server has
// ended the session first.
//
// It is NOT a bound on a transaction that keeps the server busy; tenant_fence.go states
// what stays unbounded. statement_timeout, lock_timeout and transaction_timeout were
// rejected because they would kill three things that are long by design: the migration
// advisory-lock wait, migration DDL, and the tenant purge sweep, which is one transaction
// (and transaction_timeout does not exist on PostgreSQL 16, which refuses a startup packet
// that names it). idle_session_timeout is rejected too, and it is the one most likely to
// be reached for next: TryAdvisoryLock and WithAdvisoryLock hold a SESSION-level lock on a
// connection that sits idle — not in a transaction — while their work runs on other pooled
// connections, so it would drop the migration lease and the purge coordinator's lease
// mid-pass. This parameter cannot touch those leases for exactly that reason: a session
// holding one is idle, not idle in transaction. A move to pg_advisory_xact_lock would
// change that and has to be weighed against this bound.
//
// It must stay well inside the purge settle floor; user-management's configuration test
// TestTheSettleFloorOutlastsAnIdleTransaction fails if it does not.
const IdleInTransactionTimeout = 60 * time.Second

// sessionParameters are the runtime parameters every connection string built here
// carries. Both builders apply them AFTER a caller's extra, so no call site can weaken
// them. The unit is spelled out: a bare number is read as MILLISECONDS by this parameter.
func sessionParameters() map[string]string {
	return map[string]string{
		"idle_in_transaction_session_timeout": strconv.Itoa(int(IdleInTransactionTimeout/time.Second)) + "s",
	}
}

// postgresURL builds a `postgres://` connection URL.
//
// net/url does the escaping, which is the point: url.UserPassword percent-encodes
// the credentials on String(), and pgx percent-DECODES them, so the round trip is
// lossless for every byte a password may contain — including the `%` that made
// the two forms disagree. net.JoinHostPort brackets IPv6 literals, which naive
// formatting also got wrong.
//
// `extra` carries runtime parameters alongside sslmode, mirroring the keyword form
// below so the two builders stay the same shape. url.Values.Encode sorts by key, so
// the rendered query is deterministic. The session parameters (sessionParameters) are
// set after extra, so extra cannot weaken one.
func postgresURL(username, password, hostname string, port int32, database, sslMode string,
	extra map[string]string) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(username, password),
		Host:   net.JoinHostPort(hostname, strconv.Itoa(int(port))),
		Path:   "/" + database,
	}
	q := url.Values{"sslmode": []string{sslMode}}
	for k, v := range extra {
		q.Set(k, v)
	}
	for k, v := range sessionParameters() {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// postgresKeywordDSN builds libpq's space-separated keyword/value form.
//
// Ordering is fixed and every value is quoted, so no value can introduce a
// keyword. `extra` carries runtime parameters (search_path) and is applied last,
// merged under the session parameters (sessionParameters), which win: extra cannot
// weaken one. Each key is rendered once, so which occurrence libpq honours never
// comes into it.
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
	merged := make(map[string]string, len(extra)+1)
	for k, v := range extra {
		merged[k] = v
	}
	for k, v := range sessionParameters() {
		merged[k] = v
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		pairs = append(pairs, k+"="+QuoteDSNValue(merged[k]))
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
