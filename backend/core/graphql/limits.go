// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"os"
	"strconv"

	"github.com/rs/zerolog/log"
)

// ADR-029 request-shape ceilings. Every request is bounded by a maximum body
// size, a maximum raw query length, and a maximum selection-nesting depth, so a
// single hostile document cannot force unbounded ingress/validation/execution
// work. The query/depth ceilings are applied centrally in MustParseSchema and the
// body ceiling in the HTTP handler, so every service inherits them from one place.
// All three are operator-tunable per service via env, and can be tuned in either
// direction, but none can be weakened to unlimited: a missing, unparseable, or
// below-1 value falls back to the secure default rather than disabling the
// ceiling, mirroring rdb.EffectivePageSize and the never-unlimited governance rule
// (ADR-023). There is deliberately no env value that turns a ceiling off.
const (
	// EnvGraphQLMaxDepth overrides the maximum selection-set nesting depth.
	EnvGraphQLMaxDepth = "DC_GRAPHQL_MAX_DEPTH"
	// EnvGraphQLMaxQueryLength overrides the maximum raw query length in bytes.
	EnvGraphQLMaxQueryLength = "DC_GRAPHQL_MAX_QUERY_LENGTH"
	// EnvGraphQLMaxBodyBytes overrides the maximum HTTP request body size in bytes.
	EnvGraphQLMaxBodyBytes = "DC_GRAPHQL_MAX_BODY_BYTES"

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
	// DefaultGraphQLMaxBodyBytes caps the whole HTTP request body, which the query
	// length alone does not (the JSON envelope + variables are decoded before the
	// query string is length-checked). 4MB dwarfs any legitimate control-plane
	// mutation — dashboard definitions travel as opaque JSON variables and run to
	// tens of KB — while stopping a multi-hundred-MB body from being buffered.
	DefaultGraphQLMaxBodyBytes = 4 << 20
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

// maxBodyBytes resolves the effective HTTP request-body ceiling in bytes (see
// EnvGraphQLMaxBodyBytes).
func maxBodyBytes() int64 {
	return int64(envPositiveInt(EnvGraphQLMaxBodyBytes, DefaultGraphQLMaxBodyBytes))
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
