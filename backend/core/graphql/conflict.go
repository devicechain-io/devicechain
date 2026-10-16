// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"maps"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// CodeNotFound is the extensions.code of a resolver error whose chain holds
// gorm.ErrRecordNotFound and no code of its own: the record the request named does not
// exist (in the caller's tenant). See answerIntegrity.
const CodeNotFound = "NOT_FOUND"

// CodeForbidden is the extensions.code of a resolver error whose chain holds
// auth.ErrForbidden: the caller is authenticated but may not perform the operation —
// it lacks the authority, holds it on the wrong tier, or is a scoped member the
// operation has not opted in to (ADR-081). Such a refusal used to reach the client as a
// bare message with no code, so a client could tell it from a failure only by matching
// prose. When the refusal has a reason, it rides extensions.reason beside the code
// (auth.ForbiddenReason; today only SCOPE_UNSUPPORTED). See answerIntegrity.
const CodeForbidden = "FORBIDDEN"

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
// Ahead of everything else, an authorization refusal — auth.ErrForbidden in the chain,
// which includes auth.ErrScopeUnsupported — answers FORBIDDEN (CodeForbidden), with the
// refusal's reason, if it has one, as extensions.reason. It comes first because a caller
// who may not perform an operation is told only that, never what else the attempt ran
// into (a taken value, a missing record) on the way to being refused. A code a typed
// error already set is still kept, as for every code below. Otherwise, the code, in
// order:
//
//  0. a limit refusal (core/limit) in the chain answers LIMIT_EXCEEDED, ahead of the
//     integrity classes below (but still behind a code a typed error already set, step 1);
//  1. an extensions.code a typed error already set is kept: the outermost typed error
//     chose its code;
//  2. otherwise the first non-unique refusal in the chain — a service's
//     *integrity.Refusal, or a driver reference or invalid violation — answers with its
//     class's code;
//  3. only when there is none, a conflict (a service *conflict.Error or a driver unique
//     violation) answers CONFLICT. CONFLICT is the one code a client treats as "already
//     exists, carry on", so it is never served over a chain that also holds a refusal
//     re-running cannot fix;
//  4. only when there is none of those, a miss — gorm.ErrRecordNotFound in the chain —
//     answers NOT_FOUND (CodeNotFound). The message is left as the service wrote it.
//
// 🔑 THE MISS IS HERE BECAUSE IT IS THE SAME SHAPE OF GAP. Most services report "that record
// does not exist" by returning the store's own sentinel, wrapped or bare, and it reached the
// client with no code at all — while the services that wrap a miss in a typed refusal
// answered with one. The same failure therefore did and did not carry a code depending on
// which service a client happened to ask, and only prose distinguished "not there" from any
// other failure. Classifying it once, at the one place that reads every chain, gives every
// service the same answer without asking any of them to remember it. A typed error that
// chose a code for its own miss keeps that code (step 1).
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
		chosen, reason := "", ""
		if errors.Is(err, auth.ErrForbidden) {
			chosen, reason = CodeForbidden, auth.ForbiddenReason(err)
		} else if _, ok := limit.As(err); ok {
			chosen = limit.Code
		} else if class, ok := integrity.Refused(err); ok {
			chosen, _, _ = integrity.Answer(class)
		} else if conflict.Is(err) {
			chosen = conflict.Code
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			chosen = CodeNotFound
		}
		if chosen != "" {
			if _, set := qe.Extensions["code"]; !set {
				// A copy, never a write into the map the typed error returned: an
				// Extensions() that hands out a shared map would otherwise be mutated
				// by every request that reaches here.
				ext := make(map[string]any, len(qe.Extensions)+2)
				maps.Copy(ext, qe.Extensions)
				ext["code"] = chosen
				if _, has := ext["reason"]; reason != "" && !has {
					ext["reason"] = reason
				}
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
