// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"os"
	"strconv"

	"github.com/rs/zerolog/log"
)

// ADR-029 query-shape ceilings. Every request is bounded by a maximum selection
// nesting depth and a maximum raw query length, so a single hostile document
// cannot force unbounded validation/execution work. Both are applied centrally in
// MustParseSchema, so every service inherits them from one place, and both are
// operator-tunable per service via env — but only UPWARD in effect: a missing,
// unparseable, or below-1 value falls back to the secure default rather than
// disabling the ceiling, mirroring rdb.EffectivePageSize and the never-unlimited
// governance rule (ADR-023). There is deliberately no env value that turns a
// ceiling off.
const (
	// EnvGraphQLMaxDepth overrides the maximum selection-set nesting depth.
	EnvGraphQLMaxDepth = "DC_GRAPHQL_MAX_DEPTH"
	// EnvGraphQLMaxQueryLength overrides the maximum raw query length in bytes.
	EnvGraphQLMaxQueryLength = "DC_GRAPHQL_MAX_QUERY_LENGTH"

	// DefaultGraphQLMaxDepth caps selection nesting. The deepest legitimate operation
	// the platform issues is depth ~4; the canonical schema-introspection query
	// (reachable only when dev tools are enabled) reaches ~13, so the default clears
	// both with headroom while blocking pathologically deep documents. Do not lower
	// it below the introspection depth or /graphiql breaks when dev tools are on.
	DefaultGraphQLMaxDepth = 15
	// DefaultGraphQLMaxQueryLength caps the raw query string. Real operations are a
	// few KB (the introspection query ~5KB); 100KB leaves generous room while
	// rejecting a multi-megabyte alias-amplified document before it is parsed.
	DefaultGraphQLMaxQueryLength = 100_000
)

// maxDepth resolves the effective selection-depth ceiling (see EnvGraphQLMaxDepth).
func maxDepth() int {
	return envPositiveInt(EnvGraphQLMaxDepth, DefaultGraphQLMaxDepth)
}

// maxQueryLength resolves the effective query-length ceiling in bytes (see
// EnvGraphQLMaxQueryLength).
func maxQueryLength() int {
	return envPositiveInt(EnvGraphQLMaxQueryLength, DefaultGraphQLMaxQueryLength)
}

// envPositiveInt reads a positive integer from env, falling back to def when the
// variable is unset, unparseable, or below 1. It never returns a value that would
// weaken the ceiling below the secure default's intent — a set-but-invalid value
// warns and uses the default (fail-safe), and there is no way to request 0/unlimited.
func envPositiveInt(env string, def int) int {
	v := os.Getenv(env)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Warn().Str("value", v).Str("env", env).Int("default", def).
			Msg("Invalid GraphQL limit; using the secure default (fail-safe).")
		return def
	}
	if n < 1 {
		log.Warn().Int("value", n).Str("env", env).Int("default", def).
			Msg("Non-positive GraphQL limit would disable a ceiling; using the secure default instead.")
		return def
	}
	return n
}
