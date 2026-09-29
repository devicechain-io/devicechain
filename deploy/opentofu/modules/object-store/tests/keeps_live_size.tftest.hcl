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
# failed, the next run would be passing for the wrong reason.
run "creates_the_volume_at_the_given_size" {
  variables {
    storage = "20Gi"
  }

  assert {
    condition     = kubernetes_persistent_volume_claim_v1.data.spec[0].resources[0].requests.storage == "20Gi"
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
    condition     = kubernetes_persistent_volume_claim_v1.data.spec[0].resources[0].requests.storage == "20Gi"
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
    condition     = kubernetes_persistent_volume_claim_v1.data.spec[0].resources[0].requests.storage == "20Gi"
    error_message = "a re-apply with a smaller size re-planned the live volume; every provisioner refuses a shrink"
  }
}
