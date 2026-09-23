// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"os"
	"strconv"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/rs/zerolog/log"
)

// EnvGraphQLDevTools toggles GraphQL developer tooling — schema introspection and
// the /graphiql UI. It is read per service at startup.
const EnvGraphQLDevTools = "DC_GRAPHQL_DEV_TOOLS"

// DevToolsEnabled reports whether GraphQL developer tooling (schema introspection
// + the /graphiql explorer) is enabled. It is SECURE BY DEFAULT (disabled): a
// production deploy that sets nothing exposes neither the introspection surface nor
// the explorer UI (ADR-029). Enable it on a dev instance only, with
// DC_GRAPHQL_DEV_TOOLS=true. A set-but-unparseable value fails closed (disabled)
// with a warning rather than guessing — the fail-closed config convention.
func DevToolsEnabled() bool {
	v := os.Getenv(EnvGraphQLDevTools)
	if v == "" {
		return false
	}
	enabled, err := strconv.ParseBool(v)
	if err != nil {
		log.Warn().Str("value", v).Str("env", EnvGraphQLDevTools).
			Msg("Invalid GraphQL dev-tools flag; treating as disabled (fail-closed).")
		return false
	}
	return enabled
}

// MustParseSchema parses a GraphQL schema with the ADR-029 secure defaults applied,
// and returns it wrapped in the work limit (see Schema).
//
// The graphql-go options are a maximum selection depth, a maximum query length, and —
// unless dev tooling is explicitly enabled (see DevToolsEnabled) — no introspection.
// They are appended AFTER any caller-supplied opts so the secure defaults take
// precedence. On top of those, the returned Schema refuses an operation that selects
// more ROOT fields than DC_GRAPHQL_MAX_QUERY_ROOT_FIELDS / _MUTATION_ROOT_FIELDS allow,
// before any resolver runs. That bounds the serial work one request can buy through
// aliased mutations and its root fan-out; it does not bound nested aliasing (see
// Schema's doc comment for what it leaves open). Every execution also carries a
// credential-check budget of DC_GRAPHQL_MAX_CREDENTIAL_CHECKS, which bounds the
// bcrypt compares one request can reach however its document is written.
//
// Everything is centralized here so every service inherits it from one place rather
// than each remembering to pass it, and the handler constructors accept only the
// wrapped type, so a handler cannot be built over a schema that skips the limit. It
// panics on a parse error, exactly like graphql.MustParseSchema.
func MustParseSchema(schema string, resolver interface{}, opts ...graphql.SchemaOpt) *Schema {
	maxLen := maxQueryLength()
	opts = append(opts, graphql.MaxDepth(maxDepth()), graphql.MaxQueryLength(maxLen))
	if !DevToolsEnabled() {
		opts = append(opts, graphql.DisableIntrospection())
	}
	return &Schema{
		inner:            graphql.MustParseSchema(schema, resolver, opts...),
		maxQueryLength:   maxLen,
		maxQueryRoots:    maxQueryRootFields(),
		maxMutationRoots: maxMutationRootFields(),
		maxCredChecks:    maxCredentialChecks(),
	}
}
