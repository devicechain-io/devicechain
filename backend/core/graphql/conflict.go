// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"maps"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// answerIntegrity gives every resolver error whose chain holds an integrity refusal its
// code — CONFLICT for a taken unique value, REFERENCE_VIOLATION for a refused reference
// between records, INVALID_VALUE for any other refused value — and removes any database
// wording from its message. It mutates errs in place.
//
// 🔴 WHY HERE, AND WHY IT CANNOT BE LEFT TO graphql-go. graphql-go copies a resolver
// error's Extensions() only by DIRECT type assertion on the error the resolver returned,
// so a refusal wrapped with %w — which is how nearly every service returns one — would
// lose its code. And a raw driver violation has no code to lose: before this, its text
// ("duplicate key value violates unique constraint "uix_…" (SQLSTATE 23505)", or
// "update or delete on table … violates foreign key constraint … (SQLSTATE 23503)") was
// served as the message. The resolver error is kept on the QueryError, chain intact, so
// this is the one place that can read the whole chain for every service. It is called
// from Schema.Exec and from the subscription pump, which between them serve every
// response.
//
// The code, in order:
//
//  1. an extensions.code a typed error already set is kept: the outermost typed error
//     chose its code;
//  2. otherwise the first non-unique refusal in the chain — a service's
//     *integrity.Refusal, or a driver reference or invalid violation — answers with its
//     class's code;
//  3. only when there is none, a conflict (a service *conflict.Error or a driver unique
//     violation) answers CONFLICT. CONFLICT is the one code a client treats as "already
//     exists, carry on", so it is never served over a chain that also holds a refusal
//     re-running cannot fix.
//
// The redaction applies regardless of which code was chosen, and to EVERY driver
// violation in the chain, so a service error joined with a driver error cannot shield
// the driver's text. Under REFERENCE_VIOLATION or INVALID_VALUE no unique violation's
// text is replaced by conflict.Message: the message never says "unique" over a code that
// is not CONFLICT. Every violation is logged, whichever code was served.
func answerIntegrity(errs []*gqlerrors.QueryError) {
	for _, qe := range errs {
		if qe == nil || qe.ResolverError == nil {
			continue
		}
		err := qe.ResolverError
		chosen := ""
		if _, ok := limit.As(err); ok {
			chosen = limit.Code
		} else if class, ok := integrity.Refused(err); ok {
			chosen, _, _ = integrity.Answer(class)
		} else if conflict.Is(err) {
			chosen = conflict.Code
		}
		if chosen != "" {
			if _, set := qe.Extensions["code"]; !set {
				// A copy, never a write into the map the typed error returned: an
				// Extensions() that hands out a shared map would otherwise be mutated
				// by every request that reaches here.
				ext := make(map[string]any, len(qe.Extensions)+1)
				maps.Copy(ext, qe.Extensions)
				ext["code"] = chosen
				qe.Extensions = ext
			}
		}
		served, _ := qe.Extensions["code"].(string)
		violations := integrity.All(err)
		for _, v := range violations {
			logViolation(v)
		}
		for _, v := range violations {
			redacted, whole := integrity.Redact(qe.Message, v, replacement(served, v.Class))
			if whole {
				// A fragment survived: the whole message goes, replaced by the sentence
				// of the code actually served, so the message never contradicts it.
				qe.Message = sentenceForCode(served, v.Class)
				break
			}
			qe.Message = redacted
		}
	}
}

// sentence is the neutral sentence of a class.
func sentence(c integrity.Class) string {
	if _, msg, ok := integrity.Answer(c); ok {
		return msg
	}
	return conflict.Message
}

// replacement is the sentence that stands in for a violation's full text. It is the
// violation's own sentence, except that a unique violation in a chain served
// REFERENCE_VIOLATION or INVALID_VALUE takes the served code's sentence: conflict.Message
// deliberately says "unique", which a client from before CONFLICT existed reads as
// "already exists, carry on", and that must not be said over a refusal re-running
// cannot fix.
func replacement(served string, c integrity.Class) string {
	if c == integrity.ClassUnique && (served == integrity.CodeReference || served == integrity.CodeInvalid) {
		return sentenceForCode(served, c)
	}
	return sentence(c)
}

// sentenceForCode is the neutral sentence of the code served, or of the violation's own
// class when the served code is not an integrity code (a typed error chose its own).
func sentenceForCode(code string, fallback integrity.Class) string {
	switch code {
	case conflict.Code:
		return conflict.Message
	case integrity.CodeReference:
		return integrity.MessageReference
	case integrity.CodeInvalid:
		return integrity.MessageInvalid
	}
	return sentence(fallback)
}

// logViolation records a driver violation for the operator, since the client no longer
// sees the database's wording. A unique violation is routine (a repeated create) and is
// logged at Debug. A reference or invalid violation is logged at Warn: every reference
// the API protects has a check before the write, and every value it stores is validated,
// so the database refusing one means a lost race or a server bug — a missed check or a
// missed cascade that can leave a record permanently undeletable. The detail is never
// logged: it repeats the values sent.
func logViolation(v integrity.Violation) {
	level := zerolog.WarnLevel
	if v.Class == integrity.ClassUnique {
		level = zerolog.DebugLevel
	}
	log.WithLevel(level).
		Str("class", v.Class.String()).
		Str("constraint", v.Constraint).
		Str("table", v.Table).
		Str("column", v.Column).
		Str("sqlstate", v.SQLState).
		Msg("database integrity violation answered with a neutral message")
}
