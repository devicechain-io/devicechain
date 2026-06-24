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

package graphql

import (
	"database/sql"
	_ "embed"
	"time"

	"gorm.io/datatypes"
)

// Converts a sql nullstring to a string pointer.
func NullStr(value sql.NullString) *string {
	if value.Valid {
		return &value.String
	}
	return nil
}

// Converts a sql nullbool to a bool pointer.
func NullBool(value sql.NullBool) *bool {
	if value.Valid {
		return &value.Bool
	}
	return nil
}

// Format time as a string.
func FormatTime(input time.Time) *string {
	if input.IsZero() {
		return nil
	}
	val := input.Format(time.RFC3339)
	return &val
}

// Converts a sql nullstring to a string pointer.
func MetadataStr(value *datatypes.JSON) *string {
	if value == nil {
		return nil
	}
	str := value.String()
	return &str
}
