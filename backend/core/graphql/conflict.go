// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"maps"

	"github.com/devicechain-io/dc-microservice/conflict"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"github.com/rs/zerolog/log"
)

// answerConflicts gives every resolver error whose chain holds a uniqueness conflict the
// CONFLICT code, and removes any database wording from its message. It mutates errs in
// place.
//
// 🔴 WHY HERE, AND WHY IT CANNOT BE LEFT TO graphql-go. graphql-go copies a resolver
// error's Extensions() only by DIRECT type assertion on the error the resolver returned,
// so a conflict wrapped with %w — which is how nearly every service returns one — would
// lose its code. And a raw driver unique violation has no code to lose: before this, its
// text ("duplicate key value violates unique constraint "uix_…" (SQLSTATE 23505)") was
// served as the message. The resolver error is kept on the QueryError, chain intact, so
// this is the one place that can read the whole chain for every service. It is called
// from Schema.Exec and from the subscription pump, which between them serve every
// response.
//
// An extensions.code a typed error already set is kept: the outermost typed error chose
// its code. The redaction applies regardless.
func answerConflicts(errs []*gqlerrors.QueryError) {
	for _, qe := range errs {
		if qe == nil || qe.ResolverError == nil {
			continue
		}
		if !conflict.Is(qe.ResolverError) {
			continue
		}
		if _, set := qe.Extensions["code"]; !set {
			// A copy, never a write into the map the typed error returned: an
			// Extensions() that hands out a shared map would otherwise be mutated by
			// every request that reaches here.
			ext := make(map[string]any, len(qe.Extensions)+1)
			maps.Copy(ext, qe.Extensions)
			ext["code"] = conflict.Code
			qe.Extensions = ext
		}
		if redacted, constraint, changed := conflict.Redact(qe.Message, qe.ResolverError); changed {
			qe.Message = redacted
			log.Debug().Str("constraint", constraint).Msg("unique violation answered as CONFLICT")
		}
	}
}
