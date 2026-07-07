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

// MustParseSchema parses a GraphQL schema with the ADR-029 secure defaults applied:
// a maximum selection depth, a maximum query length, and — unless dev tooling is
// explicitly enabled (see DevToolsEnabled) — no introspection. They are appended
// AFTER any caller-supplied opts so the secure defaults take precedence, and are
// centralized here so every service inherits them from one place rather than each
// remembering to pass them. It panics on a parse error, exactly like
// graphql.MustParseSchema, so it is a drop-in replacement.
func MustParseSchema(schema string, resolver interface{}, opts ...graphql.SchemaOpt) *graphql.Schema {
	opts = append(opts, graphql.MaxDepth(maxDepth()), graphql.MaxQueryLength(maxQueryLength()))
	if !DevToolsEnabled() {
		opts = append(opts, graphql.DisableIntrospection())
	}
	return graphql.MustParseSchema(schema, resolver, opts...)
}
