/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package rdb

import (
	"database/sql"
	"encoding/json"
	"strings"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Entity that is referenced by a unique token which may change over time.
type TokenReference struct {
	Token string `gorm:"unique;not null;size:128"`
}

// Entity that has a name and description.
type NamedEntity struct {
	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`
}

// Entity that has branding information.
type BrandedEntity struct {
	ImageUrl        sql.NullString `gorm:"size:512"`
	Icon            sql.NullString `gorm:"size:128"`
	BackgroundColor sql.NullString `gorm:"size:32"`
	ForegroundColor sql.NullString `gorm:"size:32"`
	BorderColor     sql.NullString `gorm:"size:32"`
}

// Entity that has extra attached metadata.
type MetadataEntity struct {
	Metadata *datatypes.JSON
}

// Create JSON value from string input.
func MetadataStrOf(value *string) *datatypes.JSON {
	if value != nil {
		result := json.RawMessage{}
		err := result.UnmarshalJSON([]byte(*value))
		if err != nil {
			return nil
		}
		conv := datatypes.JSON(result)
		return &conv
	}
	return nil
}

// Creates a sql.NullString from a string constant.
func NullStrOf(value *string) sql.NullString {
	if value != nil {
		trimmed := strings.TrimSpace(*value)
		if len(trimmed) > 0 {
			return sql.NullString{
				String: trimmed,
				Valid:  true,
			}
		}
	}
	return sql.NullString{
		Valid: false,
	}
}

// Creates a sql.NullInt64 from a string constant.
func NullInt64Of(value *int64) sql.NullInt64 {
	if value != nil {
		return sql.NullInt64{
			Int64: *value,
			Valid: true,
		}
	} else {
		return sql.NullInt64{
			Valid: false,
		}
	}
}

// Creates a sql.NullFloat64 from a string constant.
func NullFloat64Of(value *float64) sql.NullFloat64 {
	if value != nil {
		return sql.NullFloat64{
			Float64: *value,
			Valid:   true,
		}
	} else {
		return sql.NullFloat64{
			Valid: false,
		}
	}
}

// Information for paged result sets
type Pagination struct {
	PageNumber int32
	PageSize   int32
}

// Scope function used to implement pagination.
func Paginate(pag Pagination) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		// Page size of less than 1 means return all.
		if pag.PageSize < 1 {
			return db
		}
		offset := (pag.PageNumber - 1) * pag.PageSize
		return db.Offset(int(offset)).Limit(int(pag.PageSize))
	}
}

// Pagination info included with search results.
type SearchResultsPagination struct {
	PageStart    int32
	PageEnd      int32
	TotalRecords int32
}
