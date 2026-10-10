// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"reflect"

	"github.com/devicechain-io/dc-microservice/limit"
	"gorm.io/gorm"
)

// MaxLookupKeys is the most keys one request may name in a batch lookup. It is
// MaxPageSize on purpose: one ceiling for "how many rows a request may name", not a
// second number to drift from the first.
const MaxLookupKeys = MaxPageSize

// FindByKeys loads the rows whose column is in keys, refusing a list longer than
// MaxLookupKeys with a *limit.Error (served as LIMIT_EXCEEDED) instead of handing an
// unbounded caller-supplied list to the database's IN predicate.
//
// keys must be a slice (of strings, uints, ...). An empty slice returns no rows: it
// renders a match-nothing predicate, unlike gorm's inline-id form (see FindByIds).
// column is interpolated into SQL, so it must be a literal column name from the caller's
// source, never request data; an identifier that is not a plain column name is rejected.
//
// db is the caller's already-decorated handle, so Preload/Order chains compose:
//
//	rdb.FindByKeys(api.RDB.DB(ctx).Preload("DeviceType"), &found, "token", tokens)
func FindByKeys(db *gorm.DB, dest any, column string, keys any) error {
	rv := reflect.ValueOf(keys)
	if rv.Kind() != reflect.Slice {
		return fmt.Errorf("rdb.FindByKeys: keys must be a slice, got %T", keys)
	}
	if !validColumn(column) {
		return fmt.Errorf("rdb.FindByKeys: %q is not a plain column name", column)
	}
	if n := rv.Len(); n > MaxLookupKeys {
		return limit.Exceeded("lookup keys", n, MaxLookupKeys)
	}
	return db.Where(column+" IN ?", keys).Find(dest).Error
}

func validColumn(c string) bool {
	if c == "" {
		return false
	}
	for i := 0; i < len(c); i++ {
		b := c[i]
		if !(b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || i > 0 && b >= '0' && b <= '9') {
			return false
		}
	}
	return true
}
