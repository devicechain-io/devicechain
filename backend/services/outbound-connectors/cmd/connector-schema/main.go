// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command connector-schema prints a connector config JSON Schema to stdout.
// Run from the outbound-connectors module: go run ./cmd/connector-schema mqtt
package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: connector-schema <connector-type>")
	}
	s, err := connectorspec.JSONSchema(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		log.Fatal(err)
	}
}
