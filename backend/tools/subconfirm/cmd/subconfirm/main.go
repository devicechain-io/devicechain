// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command subconfirm reports subscriptions the broker has never confirmed.
//
// Run it with a module as the working directory, the way every other Go gate in this
// repo runs:
//
//	cd backend/core && subconfirm ./...
//
// hack/check-subscribe-confirmed.sh does that across the whole workspace and is what
// CI runs. See the analyzer package comment for what is reported and how to suppress
// a report deliberately.
package main

import (
	"github.com/devicechain-io/dc-subconfirm/analyzer"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(analyzer.Analyzer)
}
