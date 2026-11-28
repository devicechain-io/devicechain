// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/ledger"
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
	// ValueDigest is the stored value's ValueDigest, or its OversizeMarker when the stored value
	// is longer than MaxIntentValueBytes.
	ValueType   string
	ValueDigest string
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
// its type and its value digest equal the intended ones. An OVERSIZE row, once the key is
// declared with the intended type, is DRIFTED by construction and never IN_SYNC: its value is
// longer than any value an intent may hold.
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
	if row.Present && isOversize(row.ValueDigest) {
		return CodeDrifted
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
		if !validDigest(row.ValueDigest) && !isOversize(row.ValueDigest) {
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

// MaxChunkBytes bounds Chunk's maxBytes. It is the bulk-change planner's bound on a request's
// device list, ledger.MaxRequestBytes, so a chunk is never refused INPUT_TOO_LARGE.
const MaxChunkBytes = ledger.MaxRequestBytes

// Chunk returns the lines one reconcile would plan: the first drifted devices by ascending device
// id, stopping at whichever bound is reached first, maxLines lines or maxBytes bytes. Bytes are
// counted as the bulk-change planner counts them, Σ(len(token) + 1), and the planner refuses a
// list over ledger.MaxRequestBytes: 10,000 tokens of 128 bytes are 1,290,000 bytes, so the chunk
// for such a fleet is 8,128 lines (1,048,512 bytes).
//
// maxLines must be between 1 and MaxLinesPerReconcile, and maxBytes between MaxTokenLen + 1 (one
// line of the longest token) and MaxChunkBytes. Zero drift is the empty chunk, which the caller
// sends nowhere; a non-empty drift is never an empty chunk; a first line no bound can hold is an
// error.
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
	n, bytes := 0, 0
	for n < len(lines) && n < maxLines {
		cost := len(lines[n].Token) + 1
		if bytes+cost > maxBytes {
			break
		}
		bytes += cost
		n++
	}
	if n == 0 && len(lines) > 0 {
		return nil, fmt.Errorf("chunk: device %d's token is %d bytes, over maxBytes %d", lines[0].DeviceID, len(lines[0].Token), maxBytes)
	}
	return lines[:n], nil
}

// ObservationHasher computes the observation digest incrementally, so a caller can re-hash the
// pages it walks and compare with the digest device-management returned for the same target.
//
// The digest is "sha256:" + lowercase hex SHA-256 of the concatenation, in ascending device id
// order, of one line per member:
//
//	<device id in decimal> TAB <declared value type, or empty when undeclared> TAB
//	<"1" when present, else "0"> TAB <value type> TAB <value digest, or OVERSIZE:<n>> LF
//
// An oversize row hashes its marker (OversizeMarker) where a hashed row has its value digest, so
// a change of an oversize value's length moves the digest. The device token is not in it. The declared type is, so a profile publish that changes the
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
