// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// destroyTranscript is a real `destroy` of an instance root, verbatim: what terraform
// printed when dcctl destroyed an HA instance (three broker servers, a synchronously
// replicating event store) that had been restored from a backup, so its backups live
// under dc-tsdb-up-4d2d498f-restored-<timestamp>/ rather than the default dc-tsdb/.
//
// The resource lines come from state and are the instance's own — restore_guard's input
// is its real backup server name. The "Changes to Outputs:" block is not: dcctl passes
// the destroy only kubeconfig_context and instance_namespace, so every output that
// depends on anything else is the configuration's default. One broker server, no
// synchronous replication, the default backup path.
//
// 🔴 This file must compile against a tree WITHOUT the filter (it references no symbol
// the fix adds), so the test below can be run there and seen to fail by value.
const destroyTranscript = `Terraform used the selected providers to generate the following execution
plan. Resource actions are indicated with the following symbols:
  - destroy

Terraform will perform the following actions:

  # terraform_data.backup_prerequisite_guard[0] will be destroyed
  - resource "terraform_data" "backup_prerequisite_guard" {
      - id     = "7213d586-fe95-3371-1e60-b57ef432ade7" -> null
      - input  = 1 -> null
      - output = 1 -> null
    }

  # terraform_data.cutover_guard["tsdb"] will be destroyed
  - resource "terraform_data" "cutover_guard" {
      - id     = "f1edaaba-87b6-9411-5778-2524f8b31afb" -> null
      - input  = 0 -> null
      - output = 0 -> null
    }

  # terraform_data.restore_guard[0] will be destroyed
  - resource "terraform_data" "restore_guard" {
      - id     = "b3fb4ef4-dcff-59ef-d696-0883c990763b" -> null
      - input  = "dc-tsdb-up-4d2d498f" -> null
      - output = "dc-tsdb-up-4d2d498f" -> null
    }

Plan: 0 to add, 0 to change, 3 to destroy.

` + destroyTranscriptBlock + `terraform_data.cutover_guard["tsdb"]: Destroying... [id=f1edaaba-87b6-9411-5778-2524f8b31afb]
terraform_data.restore_guard[0]: Destroying... [id=b3fb4ef4-dcff-59ef-d696-0883c990763b]
terraform_data.restore_guard[0]: Destruction complete after 0s
terraform_data.backup_prerequisite_guard[0]: Destroying... [id=7213d586-fe95-3371-1e60-b57ef432ade7]
terraform_data.cutover_guard["tsdb"]: Destruction complete after 0s
terraform_data.backup_prerequisite_guard[0]: Destruction complete after 0s

Destroy complete! Resources: 3 destroyed.
`

// destroyTranscriptBlock is the transcript's "Changes to Outputs:" block, header to last
// value, held apart so the expected output is derived from the transcript rather than
// retyped.
const destroyTranscriptBlock = `Changes to Outputs:
  - database_backup_destination      = "s3://devicechain-tsdb/dc-tsdb" -> null
  - database_backups_enabled         = true -> null
  - instance_namespace               = "dci-up" -> null
  - nats_ca                          = "" -> null
  - nats_client_url                  = "nats://dc-nats.dci-up:4222" -> null
  - nats_cluster_replicas            = 1 -> null
  - nats_mqtt_url                    = "tcp://dc-nats.dci-up:1883" -> null
  - nats_tls_enabled                 = true -> null
  - timescaledb_cluster_name         = "dc-tsdb" -> null
  - timescaledb_host                 = "dc-timescaledb-single.dci-up:5432" -> null
  - timescaledb_synchronous_enforced = false -> null
`

// fakeTofuPrinting is a fake tofu whose subcommand sub prints text exactly (its own
// trailing newline, or the lack of one, preserved), and which answers `version` so
// terraform-exec can run it. Any other subcommand fails, so a test cannot pass over a
// command it did not mean to run.
func fakeTofuPrinting(t *testing.T, sub, text string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sub+"-out"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1" in
  version) echo '{"terraform_version":"1.8.0","platform":"linux_amd64","provider_selections":{}}' ;;
  ` + sub + `) cat "$(dirname "$0")/` + sub + `-out" ;;
  *) echo "unexpected subcommand $1" >&2; exit 1 ;;
esac
`
	bin := filepath.Join(dir, "tofu")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestTheDestroyLeavesOutTheOutputsItWorkedOutWithoutTheInstancesSettings pins that
// dcctl destroy does not print, as this instance's, output values a destroy worked out
// from the configuration's defaults, and that it keeps every other line of the plan, in
// order.
func TestTheDestroyLeavesOutTheOutputsItWorkedOutWithoutTheInstancesSettings(t *testing.T) {
	root, bin := t.TempDir(), fakeTofuPrinting(t, "destroy", destroyTranscript)
	var destroyErr error
	out, _ := captureStdoutAndStderr(t, func() {
		tf, err := newTofuExec(root, bin)
		if err != nil {
			destroyErr = err
			return
		}
		destroyErr = tf.Destroy(context.Background())
	})
	if destroyErr != nil {
		t.Fatalf("destroy: %v", destroyErr)
	}
	// Without this the absences below would pass over a destroy that printed nothing.
	if !strings.Contains(out, "Destroy complete! Resources: 3 destroyed.") {
		t.Fatalf("the destroy's output never reached stdout, so this proves nothing; got:\n%s", out)
	}

	// The defect, by value: the instance has three broker servers, replicates its event
	// store synchronously and keeps its backups under its restored path.
	for _, wrong := range []string{
		`  - nats_cluster_replicas            = 1 -> null`,
		`  - database_backup_destination      = "s3://devicechain-tsdb/dc-tsdb" -> null`,
		`  - timescaledb_synchronous_enforced = false -> null`,
	} {
		if strings.Contains(out, wrong) {
			t.Errorf("destroy printed %q as this instance's value; it is the configuration's default "+
				"(one broker server, no synchronous replication, the default backup path), worked "+
				"out without the instance's settings", strings.TrimSpace(wrong))
		}
	}
	lines := strings.Split(out, "\n")
	notShown := -1
	for i, l := range lines {
		if l == "Changes to Outputs:" {
			t.Errorf("destroy printed the plan's output list (line %d), whose values are not this instance's", i+1)
		}
		if strings.HasPrefix(l, "Changes to Outputs: not shown") {
			if notShown >= 0 {
				t.Errorf("the line saying the outputs are not shown appears more than once (lines %d and %d)", notShown+1, i+1)
			}
			notShown = i
		}
	}

	// The counterweight, by value: everything that is not the block is kept, byte for
	// byte and in order, with the replacement line where the header was.
	want := strings.Replace(destroyTranscript, destroyTranscriptBlock, "\x00\n", 1)
	if notShown < 0 {
		t.Errorf("destroy did not say the output list was left out; an operator would read its absence as nothing to show")
	} else {
		lines[notShown] = "\x00"
	}
	if got := strings.Join(lines, "\n"); got != want {
		t.Errorf("destroy did not print the rest of the plan unchanged; got:\n%s\nwant (\\x00 marks the replacement line):\n%s", got, want)
	}
}

// TestApplyStillPrintsItsOutputs pins that the filter is Destroy's alone. Apply runs with
// the instance's own variables, so its output list is true; hiding it is the mistake a
// filter wired into every progress command would make.
func TestApplyStillPrintsItsOutputs(t *testing.T) {
	transcript := "Plan: 1 to add, 0 to change, 0 to destroy.\n\n" +
		"Changes to Outputs:\n" +
		"  + nats_cluster_replicas            = 3\n" +
		"  + timescaledb_synchronous_enforced = true\n" +
		"\nApply complete! Resources: 1 added, 0 changed, 0 destroyed.\n"
	root, bin := t.TempDir(), fakeTofuPrinting(t, "apply", transcript)
	var applyErr error
	out, _ := captureStdoutAndStderr(t, func() {
		tf, err := newTofuExec(root, bin)
		if err != nil {
			applyErr = err
			return
		}
		applyErr = tf.Apply(context.Background())
	})
	if applyErr != nil {
		t.Fatalf("apply: %v", applyErr)
	}
	if out != transcript {
		t.Errorf("apply's output was changed; its output values are the instance's own and must be printed as is.\ngot:\n%s\nwant:\n%s", out, transcript)
	}
}
