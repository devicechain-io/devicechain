// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Code classifies one target device against an intent.
type Code string

// The diff codes, in order of precedence: the first that applies is the device's code.
const (
	// CodeKeyUndeclared: the device's published profile version does not declare the key, so it
	// is not configuration the device can see and a bulk change would refuse the line.
	CodeKeyUndeclared Code = "KEY_UNDECLARED"
	// CodeDeclaredTypeMismatch: the profile declares the key with a type other than the intended
	// value's, so the post would be refused CONFIGURATION_VALUE_TYPE_MISMATCH.
	CodeDeclaredTypeMismatch Code = "DECLARED_TYPE_MISMATCH"
	// CodeInSync: the device's desired value already equals the intended value.
	CodeInSync Code = "IN_SYNC"
	// CodeDrifted: a bulk change line would be created for the device.
	CodeDrifted Code = "DRIFTED"
)

// Row is one target device as device-management observes it, for one configuration key. It
// never carries the value itself, only its digest.
type Row struct {
	// DeviceID is the device's id; rows are strictly ascending by it.
	DeviceID int64
	// Token is the device's token, which a bulk change line addresses.
	Token string
	// DeclaredValueType is the type the device's active profile version declares the key with,
	// or nil when it does not declare it.
	DeclaredValueType *string
	// Present is true when the device has a live SHARED attribute row for the key with a non-null
	// value: the value a bulk change line would replace.
	Present bool
	// ValueType and ValueDigest describe that row; both are empty when Present is false.
	// ValueDigest is ValueDigest's "sha256:..." for a stored value of at most
	// MaxIntentValueBytes, and OversizeMarker(n) for a longer one, which device-management
	// reports without reading or hashing it.
	ValueType   string
	ValueDigest string
}

// oversizePrefix starts the marker that stands in for the digest of a stored value longer than
// MaxIntentValueBytes.
const oversizePrefix = "OVERSIZE:"

// OversizeMarker is the observation's stand-in for a stored value of n bytes, n greater than
// MaxIntentValueBytes: "OVERSIZE:" followed by n in decimal. Device-management writes it as
//
//	'OVERSIZE:' || octet_length(value)
//
// which reads the stored size and never the value. A row carrying it can never be IN_SYNC: no
// intended value is that long. What the marker gives up is a change from one oversize value to
// another of the same length, which does not move the observation digest; the device is DRIFTED
// before and after, so the plan does not move either.
//
// A length at or under MaxIntentValueBytes has no marker (such a value is hashed), and a length
// over math.MaxInt32 is not one a stored value can have; both are refused.
func OversizeMarker(n int64) (string, error) {
	if n <= MaxIntentValueBytes || n > math.MaxInt32 {
		return "", fmt.Errorf("oversize marker: %d bytes is not over %d or is beyond any stored value", n, MaxIntentValueBytes)
	}
	return oversizePrefix + strconv.FormatInt(n, 10), nil
}

// oversizeLength returns n when d is a well-formed OversizeMarker(n).
func oversizeLength(d string) (int64, bool) {
	digits, ok := strings.CutPrefix(d, oversizePrefix)
	if !ok || digits == "" || digits[0] == '0' {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != digits || n <= MaxIntentValueBytes || n > math.MaxInt32 {
		return 0, false
	}
	return n, true
}

// Line is one device a bulk change would address.
type Line struct {
	DeviceID int64
	Token    string
}

// Counts is the number of target devices under each code. Its json tags are the codes, so its
// canonical form is the plan record's counts document.
type Counts struct {
	KeyUndeclared        int64 `json:"KEY_UNDECLARED"`
	DeclaredTypeMismatch int64 `json:"DECLARED_TYPE_MISMATCH"`
	InSync               int64 `json:"IN_SYNC"`
	Drifted              int64 `json:"DRIFTED"`
}

// Total is the number of devices counted.
func (c Counts) Total() int64 {
	return c.KeyUndeclared + c.DeclaredTypeMismatch + c.InSync + c.Drifted
}

func (c *Counts) add(code Code) {
	switch code {
	case CodeKeyUndeclared:
		c.KeyUndeclared++
	case CodeDeclaredTypeMismatch:
		c.DeclaredTypeMismatch++
	case CodeInSync:
		c.InSync++
	case CodeDrifted:
		c.Drifted++
	}
}

// Drift is the diff of one observation: the drifted devices in ascending id order, and the count
// of every code.
type Drift struct {
	Lines  []Line
	Counts Counts
}

// ErrCeilingExceeded is returned by Diff when the target has more members than the spec's
// ceiling. A target over its ceiling is refused, never truncated.
var ErrCeilingExceeded = errors.New("the target has more members than the intent's ceiling")

// Classify returns one device's code. The precedence is KEY_UNDECLARED, DECLARED_TYPE_MISMATCH,
// IN_SYNC, DRIFTED. An unset intent needs no type match: a declared key with no row is IN_SYNC
// and one with a row is DRIFTED. A set intent is IN_SYNC only when the row is present and both
// its type and its value digest equal the intended ones.
func Classify(s Spec, row Row, at time.Time) (Code, error) {
	want, err := IntendedValue(s, row.DeviceID, at)
	if err != nil {
		return "", err
	}
	if err := checkRow(row); err != nil {
		return "", err
	}
	return classify(want, row), nil
}

func classify(want Intended, row Row) Code {
	if row.DeclaredValueType == nil {
		return CodeKeyUndeclared
	}
	if want.Unset {
		if row.Present {
			return CodeDrifted
		}
		return CodeInSync
	}
	if *row.DeclaredValueType != want.ValueType {
		return CodeDeclaredTypeMismatch
	}
	if row.Present && row.ValueType == want.ValueType && row.ValueDigest == want.ValueDigest {
		return CodeInSync
	}
	return CodeDrifted
}

// checkRow refuses a row that cannot have come from a correct observation. A malformed row is an
// error, never a guess: classifying it would put a wrong code on evidence.
func checkRow(row Row) error {
	if row.DeviceID < 1 {
		return fmt.Errorf("observation row: device id %d is not positive", row.DeviceID)
	}
	if row.Token == "" || len(row.Token) > MaxTokenLen {
		return fmt.Errorf("observation row %d: device token must be 1 to %d bytes", row.DeviceID, MaxTokenLen)
	}
	if row.DeclaredValueType != nil && !ValidValueType(*row.DeclaredValueType) {
		return fmt.Errorf("observation row %d: declared value type %q is unknown", row.DeviceID, *row.DeclaredValueType)
	}
	if row.Present {
		if row.ValueType == "" || strings.ContainsAny(row.ValueType, "\t\n") {
			return fmt.Errorf("observation row %d: a present value needs a value type", row.DeviceID)
		}
		if _, oversize := oversizeLength(row.ValueDigest); !oversize && !validDigest(row.ValueDigest) {
			return fmt.Errorf("observation row %d: value digest %q is malformed", row.DeviceID, row.ValueDigest)
		}
	} else if row.ValueType != "" || row.ValueDigest != "" {
		return fmt.Errorf("observation row %d: an absent value carries no type or digest", row.DeviceID)
	}
	return nil
}

// Diff classifies every member of the target. rows must be the whole target, strictly ascending
// by device id; a target larger than the spec's ceiling is ErrCeilingExceeded.
func Diff(s Spec, rows []Row, at time.Time) (Drift, error) {
	if err := s.Validate(); err != nil {
		return Drift{}, err
	}
	if int64(len(rows)) > s.Ceiling.MaxDevices {
		return Drift{}, fmt.Errorf("%w: %d members, ceiling %d", ErrCeilingExceeded, len(rows), s.Ceiling.MaxDevices)
	}
	drift := Drift{Lines: []Line{}}
	var last int64
	for i, row := range rows {
		if err := checkRow(row); err != nil {
			return Drift{}, err
		}
		if i > 0 && row.DeviceID <= last {
			return Drift{}, fmt.Errorf("observation rows are not strictly ascending by device id at %d", row.DeviceID)
		}
		last = row.DeviceID
		want, err := intended(s, row.DeviceID, at)
		if err != nil {
			return Drift{}, err
		}
		code := classify(want, row)
		drift.Counts.add(code)
		if code == CodeDrifted {
			drift.Lines = append(drift.Lines, Line{DeviceID: row.DeviceID, Token: row.Token})
		}
	}
	return drift, nil
}

// Chunk returns the lines one reconcile would plan: the drifted devices by ascending device id,
// stopping at whichever bound is reached first, maxLines lines or maxBytes bytes. Bytes are
// counted as device-management's planner counts a request's device list, len(token)+1 per
// line, so a chunk under maxBytes = ledger.MaxRequestBytes is never refused INPUT_TOO_LARGE:
// 10,000 tokens of 128 bytes are 1,290,000 bytes, and their chunk is 8,128 lines.
//
// maxLines must be between 1 and MaxLinesPerReconcile, and maxBytes between one line of the
// longest token and MaxChunkBytes. A first line that does not fit is an error, never an empty
// chunk. The chunk of a drift with no lines is empty, and an empty chunk
// is never sent to the planner, which refuses an empty device list: "nothing to create" is a
// fact the caller records itself.
func Chunk(d Drift, maxLines, maxBytes int) ([]Line, error) {
	if maxLines < 1 || maxLines > MaxLinesPerReconcile {
		return nil, fmt.Errorf("chunk: maxLines must be between 1 and %d, got %d", MaxLinesPerReconcile, maxLines)
	}
	if maxBytes < MaxTokenLen+1 || maxBytes > MaxChunkBytes {
		return nil, fmt.Errorf("chunk: maxBytes must be between %d and %d, got %d", MaxTokenLen+1, MaxChunkBytes, maxBytes)
	}
	lines := slices.Clone(d.Lines)
	slices.SortFunc(lines, func(a, b Line) int {
		switch {
		case a.DeviceID < b.DeviceID:
			return -1
		case a.DeviceID > b.DeviceID:
			return 1
		}
		return 0
	})
	bytes := 0
	for i, l := range lines {
		if i == maxLines {
			return lines[:i], nil
		}
		if bytes+len(l.Token)+1 > maxBytes {
			if i == 0 {
				// A drifted fleet must never read as "nothing to create".
				return nil, fmt.Errorf("chunk: the first drifted line (%d bytes) does not fit maxBytes %d", len(l.Token)+1, maxBytes)
			}
			return lines[:i], nil
		}
		bytes += len(l.Token) + 1
	}
	return lines, nil
}

// PreviewLines decides what one evaluation sends to the bulk-change preview: the chunk of the
// drift under the spec's maxLinesPerReconcile and maxBytes, and whether there is anything to
// send at all. An empty target (memberCount 0) and a drift with no drifted line, a fleet in sync
// or one whose only non-matching members are KEY_UNDECLARED or DECLARED_TYPE_MISMATCH, are
// answered with send false and no lines: the planner refuses an empty device list, and "there is
// nothing to create" is a fact the caller records without asking. When send is true the lines
// are inside both of the planner's bounds by construction.
func PreviewLines(s Spec, memberCount int64, d Drift, maxBytes int) (lines []Line, send bool, err error) {
	if err := s.Validate(); err != nil {
		return nil, false, err
	}
	if memberCount < 0 {
		return nil, false, fmt.Errorf("preview lines: member count %d is negative", memberCount)
	}
	if memberCount == 0 {
		return []Line{}, false, nil
	}
	lines, err = Chunk(d, int(s.Reconcile.MaxLinesPerReconcile), maxBytes)
	if err != nil {
		return nil, false, err
	}
	return lines, len(lines) > 0, nil
}

// ObservationHasher computes the observation digest incrementally, so a caller can re-hash the
// pages it walks and compare with the digest device-management returned for the same target.
//
// The digest is "sha256:" + lowercase hex SHA-256 of the concatenation, in ascending device id
// order, of one line per member:
//
//	<device id in decimal> TAB <declared value type, or empty when undeclared> TAB
//	<"1" when present, else "0"> TAB <value type> TAB <value digest> LF
//
// The value digest is OversizeMarker(n) for a stored value longer than MaxIntentValueBytes.
// The device token is not in it. The declared type is, so a profile publish that changes the
// key's declared type moves the digest with no attribute write. An empty target hashes the empty
// string. testdata/vectors.json pins the format.
type ObservationHasher struct {
	h    hash.Hash
	last int64
	n    int64
}

// NewObservationHasher returns an empty hasher.
func NewObservationHasher() *ObservationHasher {
	return &ObservationHasher{h: sha256.New()}
}

// Add appends one row. Rows must arrive strictly ascending by device id.
func (o *ObservationHasher) Add(row Row) error {
	if err := checkRow(row); err != nil {
		return err
	}
	if o.n > 0 && row.DeviceID <= o.last {
		return fmt.Errorf("observation rows are not strictly ascending by device id at %d", row.DeviceID)
	}
	o.last = row.DeviceID
	o.n++
	declared := ""
	if row.DeclaredValueType != nil {
		declared = *row.DeclaredValueType
	}
	present := "0"
	if row.Present {
		present = "1"
	}
	line := strconv.FormatInt(row.DeviceID, 10) + "\t" + declared + "\t" + present + "\t" +
		row.ValueType + "\t" + row.ValueDigest + "\n"
	o.h.Write([]byte(line))
	return nil
}

// Count is the number of rows added.
func (o *ObservationHasher) Count() int64 { return o.n }

// Sum returns the digest of the rows added so far.
func (o *ObservationHasher) Sum() string {
	return "sha256:" + hex.EncodeToString(o.h.Sum(nil))
}

// ObservationDigest hashes a whole target's rows; see ObservationHasher.
func ObservationDigest(rows []Row) (string, error) {
	o := NewObservationHasher()
	for _, row := range rows {
		if err := o.Add(row); err != nil {
			return "", err
		}
	}
	return o.Sum(), nil
}
