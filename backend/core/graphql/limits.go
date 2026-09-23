// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"os"
	"strconv"

	"github.com/rs/zerolog/log"
)

// ADR-029 request-shape ceilings. Every request is bounded by a maximum body size, a
// maximum raw query length, a maximum selection-nesting depth, and a maximum number of
// ROOT fields per operation (tightest for mutations). The query/depth/root-field
// ceilings are applied centrally in MustParseSchema and the body ceiling in the HTTP
// handler, so every service inherits them from one place.
//
// What each one bounds, because they are easy to over-read: the body and length
// ceilings bound bytes, the depth ceiling bounds nesting, and the root-field ceilings
// bound how many resolvers an operation starts at its root — which is what bounds the
// SERIAL work one mutation request can buy through aliases. None of them bounds
// aliasing of a nested field; see Schema.
//
// All of them are operator-tunable per service via env, and can be tuned in either
// direction, but none can be weakened to unlimited: a missing, unparseable, or below-1
// value falls back to the secure default rather than disabling the ceiling, mirroring
// rdb.EffectivePageSize and the never-unlimited governance rule (ADR-023). There is
// deliberately no env value that turns a ceiling off.
const (
	// EnvGraphQLMaxDepth overrides the maximum selection-set nesting depth.
	EnvGraphQLMaxDepth = "DC_GRAPHQL_MAX_DEPTH"
	// EnvGraphQLMaxQueryLength overrides the maximum raw query length in bytes.
	EnvGraphQLMaxQueryLength = "DC_GRAPHQL_MAX_QUERY_LENGTH"
	// EnvGraphQLMaxBodyBytes overrides the maximum HTTP request body size in bytes.
	EnvGraphQLMaxBodyBytes = "DC_GRAPHQL_MAX_BODY_BYTES"
	// EnvGraphQLMaxQueryRootFields overrides the maximum distinct root fields in a query.
	EnvGraphQLMaxQueryRootFields = "DC_GRAPHQL_MAX_QUERY_ROOT_FIELDS"
	// EnvGraphQLMaxMutationRootFields overrides the maximum distinct root fields in a
	// mutation.
	EnvGraphQLMaxMutationRootFields = "DC_GRAPHQL_MAX_MUTATION_ROOT_FIELDS"
	// EnvGraphQLMaxCredentialChecks overrides how many credential checks (password or
	// client-secret compares) one request may make.
	EnvGraphQLMaxCredentialChecks = "DC_GRAPHQL_MAX_CREDENTIAL_CHECKS"

	// DefaultGraphQLMaxDepth caps selection nesting. The deepest legitimate operation
	// the platform issues is depth ~4; the canonical schema-introspection query
	// (reachable only when dev tools are enabled) reaches ~13, so the default clears
	// both with headroom while blocking pathologically deep documents. Do not lower
	// it below the introspection depth or /graphiql breaks when dev tools are on.
	DefaultGraphQLMaxDepth = 15
	// DefaultGraphQLMaxQueryLength caps the raw query string. Real operations are a
	// few KB (the introspection query ~5KB); 100KB leaves generous room while
	// rejecting a multi-megabyte document before it is parsed. It does NOT bound alias
	// amplification: 100KB holds well over a thousand aliased mutations, which is what
	// the root-field ceilings below are for.
	DefaultGraphQLMaxQueryLength = 100_000
	// DefaultGraphQLMaxBodyBytes caps the whole HTTP request body, which the query
	// length alone does not (the JSON envelope + variables are decoded before the
	// query string is length-checked). 4MB dwarfs any legitimate control-plane
	// mutation — dashboard definitions travel as opaque JSON variables and run to
	// tens of KB — while stopping a multi-hundred-MB body from being buffered.
	DefaultGraphQLMaxBodyBytes = 4 << 20
	// DefaultGraphQLMaxQueryRootFields caps the distinct root fields (response keys,
	// so aliases count) one QUERY operation may select. The largest first-party query
	// selects 2; 20 is ten times that. Query root fields run in parallel under
	// graphql-go's parallelism limit, so this bounds fan-out rather than duration.
	DefaultGraphQLMaxQueryRootFields = 20
	// DefaultGraphQLMaxMutationRootFields caps the distinct root fields one MUTATION
	// may select. Every first-party mutation selects exactly 1. Mutation root fields
	// run one after another, so this is the ceiling that bounds the serial work one
	// request can buy — before it, one request carried ~1,500 aliased logins.
	DefaultGraphQLMaxMutationRootFields = 5
	// DefaultGraphQLMaxCredentialChecks caps the credential checks one request may make
	// — in practice, how many `login` mutations it can have evaluated. Every first-party
	// client (console, dashboard app, SDKs, dcctl, the simulators) sends exactly one
	// sign-in per request, and the OAuth endpoints outside GraphQL make exactly one check
	// per request by their shape, so 1 makes "one credential check per request" true on
	// every path. Any allowance above it would be headroom only a guesser uses. Unlike
	// the root-field ceilings it does not read the document, so it holds even for a
	// document the root-field count misreads (see credential.WithRequestBudget).
	DefaultGraphQLMaxCredentialChecks = 1
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

// maxQueryRootFields resolves the effective query root-field ceiling (see
// EnvGraphQLMaxQueryRootFields).
func maxQueryRootFields() int {
	return envPositiveInt(EnvGraphQLMaxQueryRootFields, DefaultGraphQLMaxQueryRootFields)
}

// maxMutationRootFields resolves the effective mutation root-field ceiling (see
// EnvGraphQLMaxMutationRootFields).
func maxMutationRootFields() int {
	return envPositiveInt(EnvGraphQLMaxMutationRootFields, DefaultGraphQLMaxMutationRootFields)
}

// maxCredentialChecks resolves the effective per-request credential-check budget (see
// EnvGraphQLMaxCredentialChecks).
func maxCredentialChecks() int {
	return envPositiveInt(EnvGraphQLMaxCredentialChecks, DefaultGraphQLMaxCredentialChecks)
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
