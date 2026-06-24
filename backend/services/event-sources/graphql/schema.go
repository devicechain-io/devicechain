// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	_ "embed"
)

//go:embed schema.graphql
var SchemaContent string

type SchemaResolver struct{}
