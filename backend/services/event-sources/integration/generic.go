// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtime"
)

// Generic senders: a webhook the tenant configured, whose body the platform reads by the
// integration's own settings (where the identifier is, where an array of items is, where the
// sender's time is).

// pointerGet resolves an RFC 6901 JSON pointer. The empty pointer is the document itself.
func pointerGet(doc any, pointer string) (any, bool) {
	if pointer == "" {
		return doc, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	cur := doc
	for _, seg := range strings.Split(pointer[1:], "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) || strconv.Itoa(i) != seg {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// pointerDelete removes the object member a pointer names, if it names one.
func pointerDelete(doc map[string]any, pointer string) {
	if !strings.HasPrefix(pointer, "/") {
		return
	}
	i := strings.LastIndex(pointer, "/")
	parent, ok := pointerGet(doc, pointer[:i])
	if !ok {
		return
	}
	if m, ok := parent.(map[string]any); ok {
		delete(m, strings.ReplaceAll(strings.ReplaceAll(pointer[i+1:], "~1", "/"), "~0", "~"))
	}
}

// scalarText renders an identifier value: a string, or a number in its written form.
func scalarText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	}
	return "", false
}

// idSource says where a generic integration's identifier is.
func idSource(s *GenericSettings) (kind, name string) {
	kind, name, _ = strings.Cut(s.IdFrom, ":")
	return kind, name
}

// fixedId reads an identifier named by a header or a query value, which is the same for every
// item of the request. ok=false means idFrom names a pointer into the body instead.
func fixedId(s *GenericSettings, r *Request) (value string, ok bool, err error) {
	kind, name := idSource(s)
	switch kind {
	case "header":
		v := strings.TrimSpace(r.Header.Get(name))
		if v == "" {
			return "", true, malformed("the request carries no %s header, which names the device", name)
		}
		return v, true, nil
	case "query":
		v := strings.TrimSpace(r.Query.Get(name))
		if v == "" {
			return "", true, malformed("the request carries no %q query value, which names the device", name)
		}
		return v, true, nil
	case "pointer":
		return "", false, nil
	}
	return "", false, fmt.Errorf("generic idFrom %q is not pointer:, header: or query:", s.IdFrom)
}

// senderTime reads the time an item carries at timePointer: an RFC 3339 string, or a number of
// seconds (or of milliseconds, from 1e11 on) since the epoch. An item that carries none is
// stamped at receipt and is not deduplicated across a retry.
func senderTime(item map[string]any, s *GenericSettings, r *Request) (time.Time, bool, error) {
	if s.TimePointer == "" {
		return r.ReceivedAt.UTC(), false, nil
	}
	v, ok := pointerGet(item, s.TimePointer)
	if !ok || v == nil {
		return r.ReceivedAt.UTC(), false, nil
	}
	var t time.Time
	switch x := v.(type) {
	case string:
		p, err := time.Parse(time.RFC3339Nano, x)
		if err != nil {
			return time.Time{}, false, malformed("the time at %s is not an RFC 3339 timestamp: %q", s.TimePointer, clip(x))
		}
		t = p
	case json.Number:
		f, err := x.Float64()
		if err != nil || f <= 0 {
			return time.Time{}, false, malformed("the time at %s is not a positive epoch number", s.TimePointer)
		}
		if f >= 1e11 {
			t = time.UnixMilli(int64(f))
		} else {
			t = time.Unix(int64(f), 0)
		}
	default:
		return time.Time{}, false, malformed("the time at %s is neither a string nor a number", s.TimePointer)
	}
	if err := eventtime.CheckAge(t, r.ReceivedAt); err != nil {
		return time.Time{}, false, malformed("the time at %s: %v", s.TimePointer, err)
	}
	return t.UTC(), true, nil
}

// frameOf reads the encoded raw frame an item carries at framePointer, only to sample it when
// the item turns out to hold nothing to read.
func frameOf(item map[string]any, s *GenericSettings) []byte {
	if s.FramePointer == "" {
		return nil
	}
	v, ok := pointerGet(item, s.FramePointer)
	if !ok {
		return nil
	}
	text, ok := v.(string)
	if !ok {
		return nil
	}
	switch strings.ToUpper(s.FrameEncoding) {
	case "HEX":
		b, _ := hex.DecodeString(text)
		return b
	case "BASE64":
		b, _ := base64.StdEncoding.DecodeString(text)
		return b
	}
	return nil
}

func decodeGenericJSON(d *Descriptor, r *Request) (*Result, error) {
	s := d.Settings()
	if s == nil {
		return nil, fmt.Errorf("integration %q has no generic settings", d.Token)
	}
	if !isJSONContent(r.ContentType) {
		return nil, malformed("the body is not JSON (content type %q)", clip(r.ContentType))
	}
	var root any
	if err := parseDocument(r.Body, &root); err != nil {
		return nil, err
	}

	var items []any
	switch {
	case s.ItemsPointer != "":
		at, ok := pointerGet(root, s.ItemsPointer)
		list, isList := at.([]any)
		if !ok || !isList {
			return nil, malformed("there is no array at %s", s.ItemsPointer)
		}
		items = list
	default:
		if list, ok := root.([]any); ok {
			items = list
		} else {
			items = []any{root}
		}
	}
	if len(items) == 0 {
		return nil, malformed("the request carries no items")
	}
	if len(items) > MaxItems {
		return nil, malformed("the request carries %d items, over the limit of %d; send them in smaller requests (nothing was accepted)", len(items), MaxItems)
	}

	shared, fromRequest, err := fixedId(s, r)
	if err != nil {
		return nil, err
	}
	_, idPointer := idSource(s)

	uplinks := make([]Uplink, 0, len(items))
	for i, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, malformed("item %d is not a JSON object", i)
		}
		canonical, err := json.Marshal(item)
		if err != nil {
			return nil, malformed("item %d cannot be read: %v", i, err)
		}
		if len(canonical) > MaxDecodedBytes {
			return nil, malformed("item %d is %d bytes, over the limit of %d", i, len(canonical), MaxDecodedBytes)
		}

		idText := shared
		if !fromRequest {
			v, ok := pointerGet(item, idPointer)
			if !ok {
				return nil, malformed("item %d has no device identifier at %s", i, idPointer)
			}
			if idText, ok = scalarText(v); !ok {
				return nil, malformed("item %d: the identifier at %s is neither a string nor a number", i, idPointer)
			}
		}
		scheme, err := d.Scheme()
		if err != nil {
			return nil, err
		}
		id, err := normalized(scheme, idText)
		if err != nil {
			return nil, malformed("item %d: %v", i, err)
		}
		at, sender, err := senderTime(item, s, r)
		if err != nil {
			return nil, malformed("item %d: %v", i, err)
		}

		u := Uplink{Identifier: id, Scheme: scheme, Time: at, SenderTime: sender, Frame: frameOf(item, s)}
		digest := shaHex([]byte(id), canonical)
		if sender {
			u.AltId = "gen:" + digest + ":" + at.Format(time.RFC3339Nano)
		} else {
			u.AltId = "gen:" + digest + "-" + strconv.FormatInt(at.UnixMilli(), 10)
		}
		// The identifier and the time are about the message, not readings: left in, a numeric
		// epoch would be stored as a metric.
		if !fromRequest {
			pointerDelete(item, idPointer)
		}
		if s.TimePointer != "" {
			pointerDelete(item, s.TimePointer)
		}
		if len(item) > 0 {
			u.Decoded = item
		}
		uplinks = append(uplinks, u)
	}
	return &Result{Uplinks: uplinks}, nil
}

func decodeGenericBinary(d *Descriptor, r *Request) (*Result, error) {
	s := d.Settings()
	if s == nil {
		return nil, fmt.Errorf("integration %q has no generic settings", d.Token)
	}
	idText, fromRequest, err := fixedId(s, r)
	if err != nil {
		return nil, err
	}
	if !fromRequest {
		return nil, fmt.Errorf("integration %q is binary and names its identifier by a pointer into the body", d.Token)
	}
	if len(bytes.TrimSpace(r.Body)) == 0 {
		return nil, malformed("the body is empty")
	}
	scheme, err := d.Scheme()
	if err != nil {
		return nil, err
	}
	id, err := normalized(scheme, idText)
	if err != nil {
		return nil, err
	}
	at := r.ReceivedAt.UTC()
	// A binary frame is decoded by nothing in this build: it is accepted, counted and sampled
	// on the integration (the route does that for any uplink with no decoded object).
	return &Result{Uplinks: []Uplink{{
		Identifier: id, Scheme: scheme, Time: at,
		AltId: "gen:" + shaHex([]byte(id), r.Body) + "-" + strconv.FormatInt(at.UnixMilli(), 10),
		Frame: r.Body,
	}}}, nil
}
