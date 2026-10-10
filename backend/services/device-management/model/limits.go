// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// Per-field size caps for free-form inputs, enforced before anything is stored. The request
// body is bounded (4 MiB) but a single field was not, so one request could write that much
// into every row it touched.
const (
	// MaxAttributeKeyBytes caps an entity attribute's key.
	MaxAttributeKeyBytes = 128
	// MaxAttributeValueBytes caps an entity attribute's value, whatever its declared type.
	MaxAttributeValueBytes = 64 << 10
	// MaxDetectionRuleBytes caps a detection rule's definition and its authoring graph,
	// each on its own.
	MaxDetectionRuleBytes = 256 << 10
)

// LimitExceededError is the typed refusal of an input over a documented cap. It carries
// extensions.code LIMIT_EXCEEDED and is returned unwrapped by the model so graphql-go
// serves the code.
type LimitExceededError struct {
	What string
	Max  int
}

func (e *LimitExceededError) Error() string {
	return fmt.Sprintf("%s exceeds the limit of %d bytes", e.What, e.Max)
}

// Extensions gives the refusal its wire code.
func (e *LimitExceededError) Extensions() map[string]any {
	return map[string]any{"code": "LIMIT_EXCEEDED"}
}

// checkBytes refuses a value longer than max bytes.
func checkBytes(what, value string, max int) error {
	if len(value) > max {
		return &LimitExceededError{What: what, Max: max}
	}
	return nil
}

// VersionListArgs are the optional paging arguments of a version-history read. Absent, a
// read returns the newest rdb.MaxPageSize versions; a limit above that is clamped to it.
type VersionListArgs struct {
	Limit  *int32
	Offset *int32
}

// window resolves the arguments to a (limit, offset) pair: limit in [1, rdb.MaxPageSize],
// offset >= 0. A non-positive limit means "the default", never "none".
func (a *VersionListArgs) window() (limit, offset int) {
	limit = rdb.MaxPageSize
	if a == nil {
		return limit, 0
	}
	if a.Limit != nil && *a.Limit > 0 && int(*a.Limit) < limit {
		limit = int(*a.Limit)
	}
	if a.Offset != nil && *a.Offset > 0 {
		offset = int(*a.Offset)
	}
	return limit, offset
}
