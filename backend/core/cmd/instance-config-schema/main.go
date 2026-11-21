// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command instance-config-schema generates or checks the instance document schema.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/devicechain-io/dc-microservice/config"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("instance-config-schema", flag.ContinueOnError)
	output := flags.String("output", "", "Write schema to this file (default: stdout)")
	check := flags.String("check", "", "Check this file for drift without writing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*output != "" && *check != "") {
		return fmt.Errorf("usage: instance-config-schema [-output file | -check file]")
	}
	if *check != "" {
		raw, err := os.ReadFile(*check)
		if err != nil {
			return err
		}
		return config.CheckInstanceJSONSchema(raw)
	}
	raw, err := config.GenerateInstanceJSONSchema()
	if err != nil {
		return err
	}
	if *output != "" {
		return os.WriteFile(*output, raw, 0644)
	}
	_, err = out.Write(raw)
	return err
}
