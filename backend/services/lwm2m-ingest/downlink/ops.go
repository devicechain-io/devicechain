// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
)

// The three generic path-addressed command keys L4a understands (ADR-075 L4a, scope decision:
// generic path-addressed, not per-resource semantic keys). The profile declares these as its
// CommandDefinitions; the LwM2M path (and, for write, the value) rides in the command PAYLOAD, so
// the adapter needs no per-device-type mapping and no device-management change. A command whose
// name is none of these is refused locally (it should never pass the ADR-043 enqueue gate, but the
// adapter fails closed rather than trusting that).
const (
	CommandRead    = "lwm2m.read"
	CommandWrite   = "lwm2m.write"
	CommandExecute = "lwm2m.execute"
)

// op labels for metrics — a bounded set (never the raw command name, ADR-023 cardinality).
const (
	labelRead    = "read"
	labelWrite   = "write"
	labelExecute = "execute"
	labelOther   = "other"
)

const (
	// maxWriteValueBytes bounds a Write value before it is put on the wire. The ADR-043 enqueue gate
	// types the payload but does not bound a STRING; a megabyte value into a CON exchange to a
	// constrained radio should fail fast locally, not stream to the device (ADR-075 L4a N4).
	maxWriteValueBytes = 8 << 10
	// maxReadPayloadBytes caps a Read response body carried back in the command response, so a
	// device returning a large (Block2-assembled) body cannot produce an oversized response message.
	maxReadPayloadBytes = 8 << 10
	// maxPathSegments and maxObjectID bound an LwM2M path: /objectId[/instanceId[/resourceId
	// [/resourceInstanceId]]], each a 16-bit unsigned id.
	maxPathSegments = 4
	maxObjectID     = 65535
)

// cmdPayload is the generic command payload the three keys share. Path is required; Value is the
// Write value (a JSON scalar); Args is the optional Execute argument string (LwM2M execute args,
// text/plain).
type cmdPayload struct {
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
	Args  string          `json:"args,omitempty"`
}

// Ops is the CoAP op executor: it maps a command (name + payload) to a Read/Write/Execute on a
// device conn and maps the CoAP response to command-delivery's success/payload/error shape. It is
// stateless; one instance is shared by every worker.
type Ops struct{}

// NewOps builds the executor.
func NewOps() *Ops { return &Ops{} }

// Execute performs one command's CoAP exchange and returns the mapped outcome. It never returns a
// bare error — every path (including a validation refusal, a device 4.xx/5.xx, and a transport
// failure) yields an OpResult the dispatcher turns into exactly one command response. The op label
// is always set for metrics.
func (o *Ops) Execute(ctx context.Context, conn mux.Conn, name string, payload []byte) OpResult {
	switch name {
	case CommandRead:
		return o.read(ctx, conn, payload)
	case CommandWrite:
		return o.write(ctx, conn, payload)
	case CommandExecute:
		return o.execute(ctx, conn, payload)
	default:
		return refuse(labelOther, fmt.Sprintf("unknown LwM2M command %q (expected %s/%s/%s)", name, CommandRead, CommandWrite, CommandExecute))
	}
}

// read issues a CoAP GET on the path and returns the device's response body (as text; base64 when
// not valid UTF-8). It sends NO Accept option: forcing SenML-JSON would make a conformant LwM2M
// 1.0-only client answer 4.06, and a Read must work for the presence-only 1.0 populations L1/L2
// keep — so we accept whatever content format the device returns and hand it back verbatim.
func (o *Ops) read(ctx context.Context, conn mux.Conn, payload []byte) OpResult {
	p, err := parsePayload(payload)
	if err != nil {
		return refuse(labelRead, err.Error())
	}
	resp, err := conn.Get(ctx, p.Path)
	if err != nil {
		return transportFail(ctx, labelRead, err)
	}
	if !isSuccess(resp.Code()) {
		return deviceFail(labelRead, resp.Code())
	}
	body, err := resp.ReadBody()
	if err != nil {
		return OpResult{Op: labelRead, Success: false, Err: strptr("read succeeded but its response body was unreadable")}
	}
	return OpResult{Op: labelRead, Success: true, Payload: strptr(encodeBody(body))}
}

// write issues a CoAP PUT (LwM2M Write, replace) of a single scalar resource value as text/plain —
// the LwM2M-universal single-resource representation for string/number/boolean. Multi-resource /
// object-instance writes (SenML/TLV) are out of L4a scope (a named boundary). A non-scalar value
// (object/array/null) or an oversized one is refused before the wire.
func (o *Ops) write(ctx context.Context, conn mux.Conn, payload []byte) OpResult {
	p, err := parsePayload(payload)
	if err != nil {
		return refuse(labelWrite, err.Error())
	}
	val, err := scalarText(p.Value)
	if err != nil {
		return refuse(labelWrite, err.Error())
	}
	resp, err := conn.Put(ctx, p.Path, message.TextPlain, bytes.NewReader([]byte(val)))
	if err != nil {
		return transportFail(ctx, labelWrite, err)
	}
	if !isSuccess(resp.Code()) {
		return deviceFail(labelWrite, resp.Code())
	}
	return OpResult{Op: labelWrite, Success: true}
}

// execute issues a CoAP POST (LwM2M Execute) on the path with the optional argument string as
// text/plain (the LwM2M execute-arguments format). An empty Args is a bare Execute.
func (o *Ops) execute(ctx context.Context, conn mux.Conn, payload []byte) OpResult {
	p, err := parsePayload(payload)
	if err != nil {
		return refuse(labelExecute, err.Error())
	}
	if len(p.Args) > maxWriteValueBytes {
		return refuse(labelExecute, fmt.Sprintf("execute args exceed %d bytes", maxWriteValueBytes))
	}
	resp, err := conn.Post(ctx, p.Path, message.TextPlain, bytes.NewReader([]byte(p.Args)))
	if err != nil {
		return transportFail(ctx, labelExecute, err)
	}
	if !isSuccess(resp.Code()) {
		return deviceFail(labelExecute, resp.Code())
	}
	return OpResult{Op: labelExecute, Success: true}
}

// parsePayload decodes and validates the shared command payload. The path is validated for shape
// here — an operator-supplied path is refused locally with a clear error rather than turned into an
// opaque CoAP error at the device.
func parsePayload(payload []byte) (cmdPayload, error) {
	var p cmdPayload
	if len(payload) == 0 {
		return p, errors.New("command payload is required")
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return p, fmt.Errorf("command payload is not valid JSON: %w", err)
	}
	if err := validatePath(p.Path); err != nil {
		return p, err
	}
	return p, nil
}

// validatePath checks an LwM2M path is well-formed: a leading '/', 1..4 non-empty numeric segments,
// each a 16-bit unsigned id. This both rejects a nonsense path early and keeps a stray '.'/'*'/'>'
// or empty segment from producing a malformed CoAP request.
func validatePath(path string) error {
	if path == "" || path[0] != '/' {
		return errors.New("path must be an absolute LwM2M path (e.g. /3/0/9)")
	}
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segs) == 0 || len(segs) > maxPathSegments {
		return fmt.Errorf("path must have 1 to %d segments", maxPathSegments)
	}
	for _, s := range segs {
		if !isCanonicalID(s) {
			return fmt.Errorf("path segment %q is not a canonical LwM2M id (a plain decimal 0-%d, no sign or leading zero)", s, maxObjectID)
		}
	}
	return nil
}

// isCanonicalID reports whether s is a canonical LwM2M id: plain ASCII digits, no sign or leading
// zero (except "0" itself), within the 16-bit id range. strconv.Atoi would accept "+3"/"03"/"-1"
// and forward them to the device (a guaranteed 4.04); rejecting them locally keeps the failure a
// clear, wire-free FAILED.
func isCanonicalID(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(s)
	return err == nil && n <= maxObjectID
}

// scalarText renders a JSON scalar Write value as its LwM2M text/plain representation. A boolean is
// "1"/"0" (LwM2M text/plain boolean); a number is its exact literal (json.Number, so a large
// integer is not lossily floated); a string is itself. An object/array/null/absent value, or one
// past the size bound, is refused.
func scalarText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("write requires a scalar value")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("write value is not valid JSON: %w", err)
	}
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case json.Number:
		s = t.String()
	case bool:
		if t {
			s = "1"
		} else {
			s = "0"
		}
	default:
		return "", errors.New("write value must be a scalar (string, number, or boolean); object/array/null and multi-resource writes are not supported")
	}
	if len(s) > maxWriteValueBytes {
		return "", fmt.Errorf("write value exceeds %d bytes", maxWriteValueBytes)
	}
	return s, nil
}

// encodeBody renders a Read response body for the command response: as text when it is valid UTF-8
// (SenML/JSON/plain), else base64 (an opaque resource). It is capped so a large device body cannot
// produce an oversized response message.
func encodeBody(body []byte) string {
	if len(body) > maxReadPayloadBytes {
		body = body[:maxReadPayloadBytes]
	}
	if utf8.Valid(body) {
		return string(body)
	}
	return base64.StdEncoding.EncodeToString(body)
}

// isSuccess reports whether a CoAP code is in the 2.xx success class.
func isSuccess(code codes.Code) bool {
	return uint8(code)>>5 == 2
}

// refuse builds a validation-refusal result (the command never reached the wire).
func refuse(op, reason string) OpResult {
	return OpResult{Op: op, Success: false, Err: strptr(reason)}
}

// deviceFail builds a result for a device that answered a non-2.xx CoAP code.
func deviceFail(op string, code codes.Code) OpResult {
	return OpResult{Op: op, Success: false, Err: strptr("device returned " + code.String())}
}

// transportFail builds a result for a CoAP exchange that did not complete. A context deadline is
// reported distinctly (the device did not answer in time) from other transport errors.
func transportFail(ctx context.Context, op string, err error) OpResult {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return OpResult{Op: op, Success: false, Err: strptr("device did not respond (timeout)")}
	}
	return OpResult{Op: op, Success: false, Err: strptr("transport error contacting the device")}
}

// strptr returns a pointer to s (responseEnvelope's Payload/Error are *string).
func strptr(s string) *string { return &s }
