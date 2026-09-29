# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The data volume's size is settled when the volume is created: a later apply
# with a different `storage` leaves the claim's request alone.
#
# Every `dcctl install` re-run -- the first step of every upgrade -- re-applies
# this module with whatever the release's default is. Without the lifecycle rule
# on the claim, a new default re-plans a live volume: a StorageClass without
# volume expansion refuses the growth, and every provisioner refuses a shrink back
# from a store grown by hand. Both fail the apply midway.
#
# Runs against a mocked provider, so no cluster is needed; what is under test is
# the plan OpenTofu computes from this configuration, which is where the rule
# lives. Run it with hack/check-tofu-module-tests.sh, which tests a COPY of the
# module: `tofu init` in place would leave a provider cache in a tree dcctl embeds.
#
# 🔴 THE RE-APPLY RUNS SKIP THE REFRESH, and they have to. A mocked provider's
# refresh does not return the object it stored: measured on OpenTofu 1.9.1, it
# hands back values built from the CONFIGURATION, so the prior state already reads
# the new size before `ignore_changes` is consulted, and a correct rule fails. A
# real refresh returns the live claim, which is what the stored state stands for
# here. (Terraform 1.13's mock refresh does return the stored object; the
# no-refresh form passes on both, and on both it fails without the rule.)

mock_provider "kubernetes" {}

variables {
  namespace = "dc-system"
  buckets   = ["dc-rdb", "dc-tsdb"]
}

# The negative control: on creation, the claim asks for what it is given. If this
# failed, the next runs would be passing for the wrong reason.
#
# 🔴 30Gi IS CHOSEN TO BE NO OTHER SIZE ANYWHERE: not the old default (20Gi), not
# the new one (160Gi), not `--compact`'s (20Gi), and neither size the re-apply
# runs pass. A creation size that equalled any of them could not tell
# `storage = var.storage` apart from that size written into the claim as a
# constant -- and a claim pinned to 20Gi is exactly the defect this suite exists
# after: every new cluster getting a 20Gi store.
run "creates_the_volume_at_the_given_size" {
  variables {
    storage = "30Gi"
  }

  assert {
    condition     = kubernetes_persistent_volume_claim_v1.data.spec[0].resources[0].requests.storage == "30Gi"
    error_message = "a new volume must be created at the size it is given"
  }
}

# A later apply with a LARGER size -- a release whose default grew -- keeps the
# live one.
run "a_larger_default_keeps_the_live_size" {
  plan_options {
    refresh = false
  }

  variables {
    storage = "160Gi"
  }

  assert {
    condition     = kubernetes_persistent_volume_claim_v1.data.spec[0].resources[0].requests.storage == "30Gi"
    error_message = "a re-apply with a larger size re-planned the live volume; a StorageClass without volume expansion refuses that, mid-apply"
  }
}

# And a SMALLER one -- `install --compact` over a full-size store, or the default
# over a store grown by hand -- keeps it too.
run "a_smaller_size_keeps_the_live_size" {
  plan_options {
    refresh = false
  }

  variables {
    storage = "8Gi"
  }

  assert {
    condition     = kubernetes_persistent_volume_claim_v1.data.spec[0].resources[0].requests.storage == "30Gi"
    error_message = "a re-apply with a smaller size re-planned the live volume; every provisioner refuses a shrink"
  }
}

# The rule ignores the SIZE, not the claim: anything else on it still follows the
# configuration. Without this run, `ignore_changes = all` passes every run above
# while silently freezing the storage class (and the labels) at whatever the
# first apply wrote. Last, because a new class replaces the claim.
run "a_new_storage_class_is_still_applied" {
  plan_options {
    refresh = false
  }

  variables {
    storage       = "8Gi"
    storage_class = "retained"
  }

  assert {
    condition     = kubernetes_persistent_volume_claim_v1.data.spec[0].storage_class_name == "retained"
    error_message = "a re-apply with a new storage_class did not reach the claim; the lifecycle rule is ignoring more than the size"
  }
}
