// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"database/sql/driver"
	"fmt"
	"strings"
)

// Anchors is the Go type of an AnchorColumn: a PostgreSQL text[] of "kind:token"
// anchors (ADR-081) stamped on a device-derived row when it is written. Declare the
// column with it so the scope predicate (`column && ARRAY[...]::text[]`) has an array to
// compare:
//
//	ScopeAnchors rdb.Anchors `gorm:"type:text[]"`
//
// nil is stored as NULL and an empty list as '{}'; a scoped caller sees neither.
type Anchors []string

// GormDataType names the column type for AutoMigrate.
func (Anchors) GormDataType() string { return "text[]" }

// Value renders the list as a PostgreSQL array literal, every element quoted.
func (a Anchors) Value() (driver.Value, error) {
	if a == nil {
		return nil, nil
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range a {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range s {
			if r == '"' || r == '\\' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String(), nil
}

// Scan reads a one-dimensional PostgreSQL text array in its text form. A NULL scans as
// nil. A NULL element is refused: an anchor list has no use for one, and dropping it
// silently would change what the row says about its owners.
func (a *Anchors) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("rdb.Anchors: cannot scan %T", src)
	}
	out, err := parseTextArray(s)
	if err != nil {
		return err
	}
	*a = out
	return nil
}

// parseTextArray parses the text form of a one-dimensional text[]: {a,"b c","d\"e"}.
func parseTextArray(s string) (Anchors, error) {
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, fmt.Errorf("rdb.Anchors: %q is not an array literal", s)
	}
	body := s[1 : len(s)-1]
	out := Anchors{}
	if body == "" {
		return out, nil
	}
	for i := 0; i <= len(body); {
		var elem strings.Builder
		quoted := i < len(body) && body[i] == '"'
		if quoted {
			i++
			for ; i < len(body) && body[i] != '"'; i++ {
				if body[i] == '\\' && i+1 < len(body) {
					i++
				}
				elem.WriteByte(body[i])
			}
			if i >= len(body) {
				return nil, fmt.Errorf("rdb.Anchors: unterminated element in %q", s)
			}
			i++ // closing quote
		} else {
			for ; i < len(body) && body[i] != ','; i++ {
				if body[i] == '{' || body[i] == '"' {
					return nil, fmt.Errorf("rdb.Anchors: %q is not a one-dimensional text array", s)
				}
				elem.WriteByte(body[i])
			}
			if elem.String() == "NULL" {
				return nil, fmt.Errorf("rdb.Anchors: %q holds a NULL element", s)
			}
		}
		out = append(out, elem.String())
		if i < len(body) && body[i] != ',' {
			return nil, fmt.Errorf("rdb.Anchors: malformed array %q", s)
		}
		i++ // past the comma, or past the end
	}
	return out, nil
}
