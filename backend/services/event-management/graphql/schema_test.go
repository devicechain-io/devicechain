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
	"testing"

	gql "github.com/graph-gophers/graphql-go"
)

// The schema is loaded with MustParseSchema at startup; this verifies it parses
// and every field has a matching resolver method (a mismatch would panic).
func TestSchemaParses(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	if schema == nil {
		t.Fatal("expected a parsed schema")
	}
}
