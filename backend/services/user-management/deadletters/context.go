// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletters

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
)

// systemCtx marks a context as unscoped for the tenant-scope callbacks.
//
// It is one line in its own file because it is the sanctioned bypass, and every use of it
// wants to be findable. This table is genuinely instance-wide: it is written by a consumer
// draining every tenant's letters off one stream, and read by an operator asking across
// tenants. The rows carry their tenant as DATA — which is what the ADR-077 purge sweeps on
// — rather than as scope.
func systemCtx(ctx context.Context) context.Context { return core.WithSystemContext(ctx) }
