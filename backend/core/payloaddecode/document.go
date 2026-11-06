// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/devicechain-io/dc-microservice/payloaddecode/plancost"
)

// Limits. They are part of the function-library contract: raising one is compatible, lowering
// one is not (a stored document that fit would stop fitting).
const (
	// EnvVersion is the function-library version this build compiles against.
	EnvVersion = 1

	MaxFrameBytes      = plancost.MaxFrameBytes // the raw frame an uplink may carry
	MaxJSONBytes       = plancost.MaxJSONBytes  // the canonical JSON of `json` or `decoded`
	MaxJSONDepth       = plancost.MaxJSONDepth  // nesting past this is refused before the value is carried
	MaxDocumentBytes   = 32 << 10               // the whole decoder document
	MaxExpressionBytes = 2048                   // one expression's source
	MaxLets            = 16                     // let bindings
	MaxSections        = 8                      // sections
	MaxVectors         = 20                     // saved vectors
	MaxDecoderReadings = 64                     // readings one section may emit, scalar rows plus map maxKeys
	MaxTextBytes       = 8192                   // text a hex.decode / base64.decode call accepts
	MaxInKeys          = 16                     // literals in one constant `in` list
	MaxNameBytes       = 64                     // a let, section or vector name
	MaxKeyBytes        = 128                    // an output key or prefix
	ExprCostCeiling    = 10_000                 // static cost of one expression
	DecoderCostCeiling = 50_000                 // static cost of a whole decoder
	ReservedKeyPrefix  = "lorawan."             // link metrics, appended by the platform, never a decoder output
)

// Kind is a decoder's engine.
type Kind string

const (
	// KindCEL is the CEL engine.
	KindCEL Kind = "CEL"
	// KindWASM is reserved. A document naming it is refused with ErrUnsupportedDecoderKind.
	KindWASM Kind = "WASM"
)

// InputKind names which input an uplink must carry for the decoder to run.
type InputKind string

const (
	InputFrame   InputKind = "FRAME"
	InputJSON    InputKind = "JSON"
	InputDecoded InputKind = "DECODED"
)

// Document is the decoder as stored: on the profile draft and in the published snapshot.
type Document struct {
	Kind       Kind      `json:"kind"`
	EnvVersion int       `json:"envVersion"`
	Input      InputKind `json:"input"`
	Let        []Let     `json:"let,omitempty"`
	Sections   []Section `json:"sections"`
	Vectors    []Vector  `json:"vectors,omitempty"`
	// Table is the console's field-table form, round-tripped verbatim. It is advisory: the
	// compiler never reads it.
	Table json.RawMessage `json:"table,omitempty"`
}

// Let is a named intermediate, evaluated once per uplink in order.
type Let struct {
	Name string `json:"name"`
	Expr string `json:"expr"`
}

// Section is a branch of the decoder; the first whose When holds runs.
type Section struct {
	Name string `json:"name"`
	// When is the guard; empty always matches.
	When string `json:"when,omitempty"`
	// Ignore accepts the uplink and stores nothing, counted as ignored.
	Ignore  bool     `json:"ignore,omitempty"`
	Outputs []Output `json:"outputs,omitempty"`
}

// Output is one row: a scalar (Key + Expr) or a map (Map + Prefix + MaxKeys).
type Output struct {
	Key  string `json:"key,omitempty"`
	Expr string `json:"expr,omitempty"`
	// When guards the row; false skips it with no count.
	When    string `json:"when,omitempty"`
	Map     string `json:"map,omitempty"`
	Prefix  string `json:"prefix,omitempty"`
	MaxKeys int    `json:"maxKeys,omitempty"`
}

// Vector is a saved input with its expected readings. Every vector must pass to publish.
type Vector struct {
	Name   string                 `json:"name"`
	Input  VectorInput            `json:"input"`
	Expect map[string]json.Number `json:"expect"`
}

// VectorInput is a vector's uplink, in the forms a person pastes.
type VectorInput struct {
	// FrameHex and FrameBase64 are pointers because a present empty string is an empty frame
	// (an empty-payload uplink), which Decode tells apart from an absent one.
	FrameHex    *string            `json:"frameHex,omitempty"`
	FrameBase64 *string            `json:"frameBase64,omitempty"`
	JSON        json.RawMessage    `json:"json,omitempty"`
	Decoded     json.RawMessage    `json:"decoded,omitempty"`
	FPort       *int               `json:"fPort,omitempty"`
	FCnt        *int               `json:"fCnt,omitempty"`
	Link        map[string]float64 `json:"link,omitempty"`
}

// ParseDocument reads a stored document. It fails closed: an unknown key, trailing data or a
// document over MaxDocumentBytes is an error, so a typo is never a silently ignored field.
func ParseDocument(raw []byte) (*Document, error) {
	if len(raw) > MaxDocumentBytes {
		return nil, &CompileError{Issues: []Issue{{Message: fmt.Sprintf("the decoder document is %d bytes, above the maximum of %d", len(raw), MaxDocumentBytes)}}}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var d Document
	if err := dec.Decode(&d); err != nil {
		return nil, &CompileError{Issues: []Issue{{Message: "the decoder document does not parse: " + err.Error()}}}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, &CompileError{Issues: []Issue{{Message: "the decoder document has trailing data"}}}
	}
	// The length checks above are on the bytes sent. The store re-renders the document, so the
	// cap is also checked on the rendering's upper bound (see CanonicalSize).
	size, err := CanonicalSize(raw)
	if err != nil {
		return nil, &CompileError{Issues: []Issue{{Message: "the decoder document cannot be stored: " + err.Error()}}}
	}
	if size > MaxDocumentBytes {
		return nil, &CompileError{Issues: []Issue{{Message: fmt.Sprintf("the decoder document is %d bytes once stored (numbers are written out in full, separators gain a space), above the maximum of %d", size, MaxDocumentBytes)}}}
	}
	// table is the console's field table: advisory, never compiled, but stored and returned on
	// every read, so it is held to a shape: one object.
	if len(d.Table) > 0 && !bytes.HasPrefix(bytes.TrimSpace(d.Table), []byte("{")) {
		return nil, &CompileError{Issues: []Issue{{Path: "table", Message: "table must be an object"}}}
	}
	return &d, nil
}

// toInput builds the uplink a vector describes.
func (v VectorInput) toInput() (Input, error) {
	var in Input
	if v.FrameHex != nil && v.FrameBase64 != nil {
		return in, errors.New("a vector names both frameHex and frameBase64")
	}
	switch {
	case v.FrameHex != nil:
		b, err := hex.DecodeString(*v.FrameHex)
		if err != nil {
			return in, fmt.Errorf("frameHex: %w", err)
		}
		in.Frame = append([]byte{}, b...)
	case v.FrameBase64 != nil:
		b, err := base64.StdEncoding.DecodeString(*v.FrameBase64)
		if err != nil {
			return in, fmt.Errorf("frameBase64: %w", err)
		}
		in.Frame = append([]byte{}, b...)
	}
	if len(v.JSON) > 0 {
		in.JSON = []byte(v.JSON)
	}
	if len(v.Decoded) > 0 {
		in.Decoded = []byte(v.Decoded)
	}
	if v.FPort != nil || v.FCnt != nil {
		in.LoRa = &LoRa{FPort: -1, FCnt: -1}
		if v.FPort != nil {
			in.LoRa.FPort = int64(*v.FPort)
		}
		if v.FCnt != nil {
			in.LoRa.FCnt = int64(*v.FCnt)
		}
	}
	in.Link = v.Link
	return in, nil
}
