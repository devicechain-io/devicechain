// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command rule-schema prints the rule authoring JSON Schema to stdout.
// Run from the event-processing module: go run ./cmd/rule-schema
package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

func main() {
	s, err := rules.JSONSchema()
	if err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		log.Fatal(err)
	}
}
