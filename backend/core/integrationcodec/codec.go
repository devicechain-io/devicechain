// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integrationcodec

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
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
	// DownlinkReports says the route carries a network server's reports about downlinks (a ChirpStack
	// ack or txack). Without it those events are not read at all: they are Ignored, whatever their
	// bodies hold, exactly as they were before the reports existed.
	DownlinkReports bool
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
	// Frame is the raw frame when the sender supplied one and it decoded. The route samples an
	// undecoded uplink with it, and a payload decoder reads it.
	Frame []byte
	// FrameText, FrameEncoding and FrameErr are the frame as the sender wrote it. A codec decodes
	// the frame before the device or its decoder plan is known, so it cannot decide what a bad
	// encoding means: it keeps the text, the encoding it was read under and the error, and the
	// route applies strictness once it knows a decoder needs the frame. FrameErr is non-nil exactly
	// when FrameText is set and did not decode, and Frame is then nil. A frame that arrives raw
	// (a binary body) has no text, no encoding and no error.
	FrameText     string
	FrameEncoding string
	FrameErr      error
	// FPort and FCnt are the LoRaWAN application port and uplink frame counter, or NoLoRaWAN
	// when the envelope is not a LoRaWAN one. A LoRaWAN envelope that omits either carries the
	// protobuf-JSON default, 0.
	FPort int
	FCnt  int64
	// DepthErr is non-nil when Decoded nests deeper than MaxDepth (see CheckDepth). Like FrameErr
	// it is retained, not acted on: Cut A flattens such an object and counts the skipped leaves,
	// so only a route that carries the object whole to a decoder refuses it (400, too_deep).
	DepthErr error
	// MetaErr is non-nil when the envelope's fPort or fCnt was present and not a valid port or
	// counter (FPort / FCnt are then NoLoRaWAN). Like FrameErr it is retained, not acted on: the
	// endpoint has always accepted such a body, so only a route running a decoder that needs the
	// value may refuse it.
	MetaErr error
}

// NoLoRaWAN is the value of Uplink.FPort and Uplink.FCnt for an envelope that carries no LoRaWAN
// port or counter (a generic sender).
const NoLoRaWAN = -1

// The encodings a frame can be written in.
const (
	EncodingBase64 = "BASE64"
	EncodingHex    = "HEX"
)

// SetFrame records a frame written as text under the encoding, keeping the text and any error
// beside the decoded bytes. It does not refuse: see Uplink.FrameText.
func (u *Uplink) SetFrame(text, encoding string) {
	u.FrameText, u.FrameEncoding, u.Frame, u.FrameErr = text, encoding, nil, nil
	var err error
	switch encoding {
	case EncodingBase64:
		u.Frame, err = base64.StdEncoding.DecodeString(text)
	case EncodingHex:
		u.Frame, err = hex.DecodeString(text)
	default:
		err = fmt.Errorf("unknown frame encoding %q", encoding)
	}
	if err != nil {
		u.Frame, u.FrameErr = nil, err
	}
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
	// Downlinks are the network server's reports about downlinks it was asked to send (a ChirpStack
	// ack or txack). A result holds either uplinks or downlink reports, never both. A route that
	// does not carry downlink reports treats a result with only these as Ignored.
	Downlinks []DownlinkEvent
	// Untracked is set with Ignored on a downlink report that names no downlink the platform can
	// follow (a ChirpStack txack for a MAC-command-only downlink carries no queue item id). It is
	// counted by the route and answered 204, never refused.
	Untracked bool
}

// The kinds of DownlinkEvent.
const (
	// DownlinkAck is the network server's report on a CONFIRMED downlink: the device acknowledged
	// it, or the network server gave up waiting (Acknowledged says which).
	DownlinkAck = "ack"
	// DownlinkTxAck is the gateway accepting the downlink for transmission. It is not evidence the
	// device heard it.
	DownlinkTxAck = "txack"
	// DownlinkFailed is the network server reporting it could not send the downlink (The Things
	// Stack's downlink_failed). ChirpStack has no such event.
	DownlinkFailed = "failed"
)

// DownlinkCorrelationPrefix starts every correlation id the platform puts on a downlink it pushes
// to The Things Stack. A report is only the platform's when a correlation id carries it, so a
// downlink somebody queued by hand in the network server's console is not mistaken for one.
const DownlinkCorrelationPrefix = "dc:"

// DownlinkEvent is one network-server report about a downlink, identified by the queue item id the
// network server returned when the downlink was enqueued.
type DownlinkEvent struct {
	Kind string
	// Scheme and Identifier name the device, as an Uplink's do.
	Scheme, Identifier string
	// QueueItemID is the key the report is matched to its downlink by: ChirpStack's queue item id (a
	// UUID), or, for The Things Stack, the correlation id the platform put on the downlink when it
	// pushed it (DownlinkCorrelationPrefix + hex).
	QueueItemID string
	// DeviceRef names the device as the network server does, when DevEUI alone does not (The Things
	// Stack: "<application id>/<device id>"). Empty for ChirpStack.
	DeviceRef string
	// Attempt is a failed report's confirmed-downlink retry count (The Things Stack
	// confirmed_retry.attempt, absent meaning 0); ErrorNamespace and ErrorName are its error's.
	// They are read for Kind DownlinkFailed only.
	Attempt        int
	ErrorNamespace string
	ErrorName      string
	// Acknowledged is meaningful for DownlinkAck only: true when the device acknowledged the frame,
	// false when the network server timed the confirmation out.
	Acknowledged bool
	FCntDown     int64
}

// ErrTooDeep is the root of a refusal for a JSON value nested deeper than MaxDepth. It always
// arrives wrapped in ErrMalformed.
var ErrTooDeep = errors.New("nested too deeply")

// CheckDepth refuses a parsed JSON value that nests objects or arrays deeper than MaxDepth. A
// flatten only skips the leaves below the cutoff, so it bounds nothing about the value a decoder
// or a preview would be handed whole: this is the walk that does. It stops descending the moment
// the limit is passed, but a wide, shallow document is walked in full: its cost is bounded by the
// size of the document, which the 256 KiB body cap (MaxBodyBytes) bounds.
// The error wraps ErrMalformed (a 400) and ErrTooDeep (the counted reason).
func CheckDepth(v any) error {
	if nestingExceeds(v, 0) {
		return fmt.Errorf("%w: %w: the JSON is nested more than %d levels deep", ErrMalformed, ErrTooDeep, MaxDepth)
	}
	return nil
}

// nestingExceeds reports whether v holds more than MaxDepth levels of container; open is how
// many containers are already open above v.
func nestingExceeds(v any, open int) bool {
	switch n := v.(type) {
	case map[string]any:
		if open+1 > MaxDepth {
			return true
		}
		for _, c := range n {
			if nestingExceeds(c, open+1) {
				return true
			}
		}
	case []any:
		if open+1 > MaxDepth {
			return true
		}
		for _, c := range n {
			if nestingExceeds(c, open+1) {
				return true
			}
		}
	}
	return false
}

// loraPort reads a LoRaWAN application port from the raw protobuf-JSON value: a number, or a
// string holding one. Absent and null are 0, the default the encoding omits. A value that is not
// a port (outside 0-255, fractional, text) is an error for the caller to RECORD, not to refuse:
// this field was never read before, so a sender that sent junk in it was accepted and must keep
// being.
func loraPort(raw json.RawMessage, what string) (int, error) {
	text, ok := loraNumber(raw)
	if !ok {
		return 0, nil
	}
	v, err := strconv.ParseUint(text, 10, 8)
	if err != nil {
		return 0, malformed("%s is not a LoRaWAN port: %q", what, clip(text))
	}
	return int(v), nil
}

// loraCounter reads a LoRaWAN uplink frame counter (an unsigned 32-bit number) the same way.
func loraCounter(raw json.RawMessage, what string) (int64, error) {
	text, ok := loraNumber(raw)
	if !ok {
		return 0, nil
	}
	v, err := strconv.ParseUint(text, 10, 32)
	if err != nil {
		return 0, malformed("%s is not a frame counter: %q", what, clip(text))
	}
	return int64(v), nil
}

// loraNumber is the text of a raw JSON value read as a number, ok=false for absent and null. A
// JSON string is unquoted; anything else is returned as written.
func loraNumber(raw json.RawMessage) (string, bool) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return "", false
	}
	if t[0] == '"' {
		var s string
		if err := json.Unmarshal([]byte(t), &s); err == nil {
			t = s
		}
	}
	return t, true
}

// setMeta stores a port and counter on the uplink, or records why it could not. An invalid value
// is left unset (NoLoRaWAN) and kept in MetaErr; the route refuses it only when a decoder needs it.
func (u *Uplink) setMeta(port int, portErr error, cnt int64, cntErr error) {
	u.FPort, u.FCnt = port, cnt
	if portErr != nil {
		u.FPort, u.MetaErr = NoLoRaWAN, portErr
	}
	if cntErr != nil {
		u.FCnt = NoLoRaWAN
		if u.MetaErr == nil {
			u.MetaErr = cntErr
		}
	}
}

// MaxAltIdLen bounds a dedup key. It is the inbound message id too, held in the broker's memory for
// the whole duplicate window, and an indexed column: an unbounded one is memory in the broker and
// an index entry the database refuses.
const MaxAltIdLen = 192

// checkAltId refuses a dedup key that could harm either place it lands: too long, or holding a
// character the broker's header encoding rewrites (CR and LF become spaces, so two keys that differ
// only by them would be one on the wire) or a database cannot index sanely. Printable ASCII only.
func checkAltId(id string) error {
	if len(id) > MaxAltIdLen {
		return malformed("the dedup key is %d bytes, over the limit of %d", len(id), MaxAltIdLen)
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c <= 0x20 || c >= 0x7f {
			return malformed("the dedup key holds a character that is not printable ASCII")
		}
	}
	return nil
}

// Decode parses a request by the integration's registered type. Nothing is sniffed.
func Decode(d *Descriptor, r *Request) (*Result, error) {
	res, err := decodeBy(d, r)
	if err != nil {
		return nil, err
	}
	for i := range res.Uplinks {
		if err := checkAltId(res.Uplinks[i].AltId); err != nil {
			return nil, err
		}
	}
	for i := range res.Uplinks {
		res.Uplinks[i].DepthErr = CheckDepth(res.Uplinks[i].Decoded)
	}
	return res, nil
}

// decodeBy is the codec dispatch. A variable only so a test can stand a codec in its place; nothing
// in production reassigns it.
var decodeBy = decode

func decode(d *Descriptor, r *Request) (*Result, error) {
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
