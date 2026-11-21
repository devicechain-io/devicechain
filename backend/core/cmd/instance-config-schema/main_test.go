// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
)

func TestGenerateAndCheck(t *testing.T) {
	file := filepath.Join(t.TempDir(), "schema.json")
	if err := run([]string{"-output", file}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want, err := config.GenerateInstanceJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(raw, want) {
		t.Fatalf("generator output differs: %v", err)
	}
	var out bytes.Buffer
	if err := run(nil, &out); err != nil || !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("stdout differs: %v", err)
	}
	if err := run([]string{"-check", file}, &out); err != nil {
		t.Fatal(err)
	}
	corrupt := append(bytes.Clone(raw), ' ')
	if err := os.WriteFile(file, corrupt, 0644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-check", file}, &out); err == nil {
		t.Fatal("negative control: check accepted drift")
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(after, corrupt) {
		t.Fatalf("check mode rewrote the file: %v", err)
	}
	for _, args := range [][]string{{"-check", file, "-output", file}, {"unexpected"}, {"-check", file + ".missing"}} {
		if err := run(args, &out); err == nil {
			t.Errorf("accepted invalid invocation: %v", args)
		}
	}
}
