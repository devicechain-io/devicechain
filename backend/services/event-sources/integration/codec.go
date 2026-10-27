// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/devicealias"
	"github.com/devicechain-io/dc-microservice/eventtime"
)

const (
	// MaxBodyBytes caps an integration request body. It is read under this cap, so a sender
	// cannot make the route buffer more.
	MaxBodyBytes = 256 << 10
	// MaxDecodedBytes caps the decoded object (or one generic item) a sender may carry.
	MaxDecodedBytes = 64 << 10
	// MaxDepth is the deepest nesting flatten walks; a value below it is skipped and counted.
	MaxDepth = 32
	// MaxItems caps a generic JSON array body. A longer one is refused whole.
	MaxItems = 100
	// MaxSampleBytes is how much of an undecoded frame is kept on the integration.
	MaxSampleBytes = 512
)

// ErrMalformed is the root of every refusal of a body: the sender's request cannot be read as
// the envelope its integration is registered for. The route answers 400.
var ErrMalformed = errors.New("malformed integration request")

// ErrUnsupportedEncoding means the body is in an encoding this build does not parse, which the
// sender can fix in its own settings (ChirpStack's Protobuf integration encoding). The route
// answers 415.
var ErrUnsupportedEncoding = errors.New("unsupported integration encoding")

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

// Request is one HTTP request as the codecs see it.
type Request struct {
	Body []byte
	// Header, Query and ContentType carry what a generic sender may name its identifier in.
	Header      http.Header
	Query       url.Values
	ContentType string
	// MessageType is the optional path segment after the integration's token. The Things Stack
	// posts each message type to a path of its own.
	MessageType string
	// ReceivedAt is when the platform received the request.
	ReceivedAt time.Time
}

// Link is the receive-link metadata a network server reports, kept only when the integration
// asks for link metrics.
type Link struct {
	RSSI     float64
	SNR      float64
	Gateways int
	// HasSignal says RSSI and SNR are set. Gateways is set whenever the list was present.
	HasSignal bool
}

// Uplink is one device's message, decoded from the envelope and not yet resolved to a device.
type Uplink struct {
	// Identifier is the sender's name for the device, in the canonical spelling of Scheme.
	Identifier string
	Scheme     string
	// AltId is the network server's own dedup key, prefixed by codec. It is also the inbound
	// message id.
	AltId string
	// Time is the envelope's time: the sender's, or receipt when the sender gives none.
	Time time.Time
	// SenderTime says Time came from the sender. It is false for a generic sender without a
	// timePointer, whose retries are therefore not deduplicated.
	SenderTime bool
	// Decoded is the network server's decoded object (or a generic item), nil when there is
	// none. Numbers keep their written form (json.Number).
	Decoded map[string]any
	Link    *Link
	// Frame is the raw frame when the sender supplied one, kept only to sample an undecoded
	// uplink.
	Frame []byte
}

// SampleHex is the frame's hex encoding, cut to MaxSampleBytes.
func (u *Uplink) SampleHex() string {
	f := u.Frame
	if len(f) > MaxSampleBytes {
		f = f[:MaxSampleBytes]
	}
	return hex.EncodeToString(f)
}

// Result is what a codec made of a request.
type Result struct {
	// Ignored means the request is a well-formed message of a kind this integration does not
	// turn into events (a join, a status, a downlink acknowledgement). The route answers 204.
	Ignored bool
	Uplinks []Uplink
}

// Decode parses a request by the integration's registered type. Nothing is sniffed.
func Decode(d *Descriptor, r *Request) (*Result, error) {
	switch d.Type {
	case ChirpStackV4:
		return decodeChirpStack(d, r)
	case TTSV3:
		return decodeTTS(d, r)
	case GenericJSON:
		return decodeGenericJSON(d, r)
	case GenericBinary:
		return decodeGenericBinary(d, r)
	}
	return nil, fmt.Errorf("integration %q has the unknown type %q", d.Token, d.Type)
}

// parseDocument decodes a JSON document keeping numbers in their written form.
func parseDocument(body []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(into); err != nil {
		return malformed("the body is not valid JSON: %v", err)
	}
	if dec.More() {
		return malformed("the body holds more than one JSON value")
	}
	return nil
}

// objectOf parses a decoded-object field. Absent, null and an empty object are all "no decoded
// object": a network server with no codec configured sends none, and one whose codec produced
// nothing sends {}. A value that is not an object, or is over MaxDecodedBytes, is malformed.
func objectOf(raw json.RawMessage, what string) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if len(trimmed) > MaxDecodedBytes {
		return nil, malformed("%s is %d bytes, over the limit of %d", what, len(trimmed), MaxDecodedBytes)
	}
	var obj map[string]any
	if err := parseDocument(trimmed, &obj); err != nil {
		return nil, malformed("%s is not a JSON object", what)
	}
	if len(obj) == 0 {
		return nil, nil
	}
	return obj, nil
}

// eventTime resolves the envelope's time: the sender's when it gave one, else receipt. A sender
// time the platform cannot read, or that is older than the platform accepts, is malformed, as
// it is for a device's own events.
func eventTime(sent *string, what string, r *Request) (time.Time, bool, error) {
	if sent == nil || *sent == "" {
		return r.ReceivedAt.UTC(), false, nil
	}
	t, err := time.Parse(time.RFC3339Nano, *sent)
	if err != nil || t.IsZero() {
		return time.Time{}, false, malformed("%s is not an RFC 3339 timestamp: %q", what, clip(*sent))
	}
	if err := eventtime.CheckAge(t, r.ReceivedAt); err != nil {
		return time.Time{}, false, malformed("%s: %v", what, err)
	}
	return t.UTC(), true, nil
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// normalized canonicalises a sender's identifier under the scheme, with the one function the
// service that stores the alias uses. A value the scheme refuses is the sender's mistake.
func normalized(scheme, value string) (string, error) {
	v, err := devicealias.Normalize(scheme, value)
	if err != nil {
		return "", malformed("%v", err)
	}
	return v, nil
}

func isJSONContent(contentType string) bool {
	ct := strings.ToLower(contentType)
	return ct == "" || strings.Contains(ct, "json")
}

// numberText renders a JSON number in the shortest form that round-trips: an integer keeps its
// digits, anything with a fraction or exponent is parsed and re-rendered.
func numberText(n json.Number) (string, bool) {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if _, err := strconv.ParseInt(s, 10, 64); err == nil {
			return s, true
		}
		if _, err := strconv.ParseUint(s, 10, 64); err == nil {
			return s, true
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", false
	}
	return formatFloat(f)
}

func formatFloat(f float64) (string, bool) {
	if f != f || f > 1.7976931348623157e308 || f < -1.7976931348623157e308 {
		return "", false
	}
	abs := f
	if abs < 0 {
		abs = -abs
	}
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		return strconv.FormatFloat(f, 'g', -1, 64), true
	}
	return strconv.FormatFloat(f, 'f', -1, 64), true
}

// shaHex is the hex SHA-256 of the parts, separated so ("ab","c") and ("a","bc") differ.
func shaHex(parts ...[]byte) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
