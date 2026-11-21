// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"sigs.k8s.io/yaml"
)

func TestConfigSchemaCommand(t *testing.T) {
	command, _, err := rootCmd.Find([]string{"config-schema"})
	if err != nil || command.Name() != "config-schema" {
		t.Fatalf("missing config-schema command: %v", err)
	}
	if err := command.Args(command, []string{"another-release"}); err == nil {
		t.Fatal("must not imply support for a different release's schema")
	}
	var out bytes.Buffer
	command.SetOut(&out)
	t.Cleanup(func() { command.SetOut(nil) })
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), config.InstanceJSONSchema()) {
		t.Fatal("command must emit exactly the embedded schema, without prose")
	}
}

func TestConfigSchemaReleaseAsset(t *testing.T) {
	raw, err := os.ReadFile("../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Release struct {
			ExtraFiles []struct {
				Glob string `json:"glob"`
			} `json:"extra_files"`
		} `json:"release"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, file := range cfg.Release.ExtraFiles {
		if file.Glob == "../core/config/instance-config.schema.json" {
			asset, err := os.ReadFile(filepath.Join("..", file.Glob))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(asset, config.InstanceJSONSchema()) {
				t.Fatal("release asset differs from the embedded schema")
			}
			return
		}
	}
	t.Fatal("goreleaser must attach instance-config.schema.json to each release")
}
