// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog/log"
)

// The tool-call audit record is one structured log line per tools/call, written by this
// server. Its field names and values are a published contract (docs/docs/concepts/mcp.md,
// "What is recorded"), so an operator's log pipeline can select and parse it; change them
// only by bumping auditSchemaVersion and saying so in the release notes.
//
// 🔴 WHY THIS EXISTS: NOTHING DOWNSTREAM RECORDS A READ. A tool call is a GraphQL query
// under the caller's token, and the platform's audit journal records entity MUTATIONS
// (the core/rdb write callbacks) and sign-in events — never a query. graphql.go used to
// say every downstream read "inherits the user's … audit trail", which was a guarantee
// with nothing behind it: an assistant could read a tenant's whole inventory, telemetry
// and position history and leave no record anywhere. This line is that record.
//
// 🔴 WHY A LOG LINE AND NOT THE JOURNAL. The journal lives in each area's own database
// and is written in the same transaction as the mutation it describes, so it commits iff
// the change does. This service has no database and a read has no transaction, so a
// journal row here would mean a new database for this area or a new write API on another
// one — neither is a log line's size, and the second would give this server a write path
// it is deliberately built without. The consequence is stated in the docs rather than
// left implicit: the record is in the instance's logs, which an operator reads, not in
// the Audit page, which a tenant administrator reads.
//
// 🔑 IT IS WRITTEN WITHOUT A LEVEL, SO THE CONFIGURED LOG LEVEL CANNOT SILENCE IT.
// infrastructure.logging.level is a verbosity knob: an operator who sets it to warn to
// quiet a noisy service is not choosing to stop auditing. zerolog drops an event whose
// level is below the global level, and every real level is below some setting; an event
// with no level (zerolog.NoLevel) sits above all of them, so this line is written at every
// configurable level. It therefore carries no "level" field — select it by "audit".
const (
	auditField         = "audit"
	auditKindToolCall  = "mcp_tool_call"
	auditSchemaVersion = 1
)

// Outcomes recorded on the line. They are the three things a tools/call can come back as,
// told apart by what the client receives, not by why:
//
//   - ok: the tool ran and returned a result.
//   - error: a tool result came back flagged as an error. That covers arguments that
//     failed validation (the tool never ran), a tool that refused its input, and a
//     downstream refusal or failure — including the caller lacking the permission, which
//     is the same refusal a person gets.
//   - rejected: the call was refused as a protocol error before any tool ran — a tool
//     name this server does not serve, or malformed parameters.
const (
	auditOutcomeOK       = "ok"
	auditOutcomeError    = "error"
	auditOutcomeRejected = "rejected"
)

// auditUnregisteredTool is recorded in place of a tool name the server does not serve.
// The name a client sends is free text bounded only by the 1 MiB request body; copying it
// would let any authenticated caller write arbitrary content of arbitrary size into the
// instance's logs. Only a registered name is ever written.
const auditUnregisteredTool = "(unregistered)"

// auditToolCalls is the receiving middleware that writes the record. It is installed on
// the one server newServer builds, so it sees every tools/call that server answers — the
// ones that never reach a handler (an unknown tool, arguments that fail validation)
// included — and a tool registered by any path is recorded without being told to be.
//
// registered is the set of tool names the server serves; see auditUnregisteredTool.
func auditToolCalls(registered map[string]bool) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			call, ok := req.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok {
				return next(ctx, method, req)
			}
			start := time.Now()
			res, err := next(ctx, method, req)
			writeToolCallRecord(call, registered, res, err, time.Since(start))
			return res, err
		}
	}
}

// writeToolCallRecord writes the line for one call.
func writeToolCallRecord(req *mcp.CallToolRequest, registered map[string]bool, res mcp.Result,
	err error, elapsed time.Duration) {
	tool, args := auditUnregisteredTool, json.RawMessage(nil)
	if req.Params != nil {
		if registered[req.Params.Name] {
			tool = req.Params.Name
		}
		args = req.Params.Arguments
	}

	outcome := auditOutcomeOK
	if err != nil {
		outcome = auditOutcomeRejected
	} else if r, ok := res.(*mcp.CallToolResult); !ok || r == nil || r.IsError {
		outcome = auditOutcomeError
	}

	id := auditIdentityOf(req)
	// log.Logger is read here, at each call, rather than captured when the server is
	// built: core replaces it during startup to stamp the instance and area on every line.
	log.Logger.Log().
		Str(auditField, auditKindToolCall).
		Int("schema", auditSchemaVersion).
		Str("tenant", id.tenant).
		Str("actor", id.actor).
		Str("client_id", id.clientID).
		Bool("sudo", id.sudo).
		Str("tool", tool).
		Str("args_sha256", argumentsDigest(args)).
		Str("outcome", outcome).
		Int64("duration_ms", elapsed.Milliseconds()).
		Msg("MCP tool call")
}

// auditIdentity is who made a call, as the verified token says — never as the client says.
type auditIdentity struct {
	tenant, actor, clientID string
	sudo                    bool
}

// auditIdentityOf reads the caller from the TokenInfo the bearer middleware attached. A
// request without one is recorded with every field empty rather than skipped: the
// middleware refuses such a request before it can get here, so a record with no actor is
// a sign that it did not, which is exactly what an audit trail is for.
func auditIdentityOf(req *mcp.CallToolRequest) auditIdentity {
	if req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.Extra == nil {
		return auditIdentity{}
	}
	x := req.Extra.TokenInfo.Extra
	id := auditIdentity{}
	id.tenant, _ = x[extraTenantKey].(string)
	id.actor, _ = x[extraActorKey].(string)
	id.clientID, _ = x[extraClientKey].(string)
	id.sudo, _ = x[extraSudoKey].(bool)
	return id
}

// argumentsDigest is the SHA-256 of a call's arguments in a canonical form — re-encoded
// with object keys sorted and no insignificant whitespace — so two calls that asked for
// the same thing share a digest however the client spelled the JSON. Arguments that are
// not valid JSON are digested as sent. No arguments at all digest as the empty string's
// digest, not as an empty field, so the field is always 64 hex characters.
//
// 🔴 THE DIGEST IS A CORRELATION KEY, NOT A SECRECY CONTROL, and it would be dishonest to
// let its shape suggest otherwise. What it buys is that the line has a fixed size and
// copies nothing a client wrote. It does not make the arguments unrecoverable: they are
// device tokens, filters and time windows, which are enumerable, so anyone holding the
// tenant's device list can test a guess against it. No argument of any tool here carries
// a credential — the caller's token rides the Authorization header, never the arguments —
// so there is nothing secret for it to protect; were a tool ever to take one, the
// argument would need leaving out of the digest's input, not hashing.
func argumentsDigest(raw json.RawMessage) string {
	canonical := []byte(raw)
	// UseNumber keeps a number's own digits, so two arguments that differ only past
	// float64's precision do not collapse onto one digest.
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if len(raw) > 0 && dec.Decode(&v) == nil && !dec.More() {
		if b, err := json.Marshal(v); err == nil {
			canonical = b
		}
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
